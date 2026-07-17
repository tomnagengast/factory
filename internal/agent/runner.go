package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

type Output struct {
	Stream string
	Text   string
}

type Runner interface {
	Run(context.Context, string, func(Output) error) error
}

// CommandRunner invokes one unrestricted, ephemeral Codex process. The prompt
// is sent on stdin and both output streams are returned line by line.
type CommandRunner struct {
	Command   string
	Workspace string
}

func (r CommandRunner) Run(ctx context.Context, prompt string, emit func(Output) error) error {
	if r.Command == "" || r.Workspace == "" || emit == nil {
		return errors.New("agent command, workspace, and output handler are required")
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()

	command := exec.CommandContext(
		runContext,
		r.Command,
		"exec",
		"--json",
		"--ephemeral",
		"--dangerously-bypass-approvals-and-sandbox",
		"--dangerously-bypass-hook-trust",
		"--ignore-rules",
		"--skip-git-repo-check",
		"-C",
		r.Workspace,
		"-",
	)
	command.Dir = r.Workspace
	command.Stdin = strings.NewReader(prompt)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open agent stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return fmt.Errorf("open agent stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	outputs := make(chan Output)
	readErrors := make(chan error, 2)
	var readers sync.WaitGroup
	readers.Add(2)
	go readOutput(runContext, &readers, stdout, "stdout", outputs, readErrors)
	go readOutput(runContext, &readers, stderr, "stderr", outputs, readErrors)
	go func() {
		readers.Wait()
		close(outputs)
		close(readErrors)
	}()

	var outputErr error
	for output := range outputs {
		if outputErr != nil {
			continue
		}
		if err := emit(output); err != nil {
			outputErr = err
			cancel()
		}
	}
	var readErr error
	for err := range readErrors {
		if err != nil && !errors.Is(err, context.Canceled) && readErr == nil {
			readErr = err
		}
	}
	waitErr := command.Wait()
	switch {
	case outputErr != nil:
		return outputErr
	case readErr != nil:
		return fmt.Errorf("read agent output: %w", readErr)
	case waitErr != nil:
		return fmt.Errorf("agent process: %w", waitErr)
	default:
		return nil
	}
}

func readOutput(
	ctx context.Context,
	readers *sync.WaitGroup,
	reader io.Reader,
	stream string,
	outputs chan<- Output,
	readErrors chan<- error,
) {
	defer readers.Done()
	buffer := bufio.NewReader(reader)
	for {
		line, err := buffer.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		if line != "" {
			select {
			case <-ctx.Done():
				readErrors <- ctx.Err()
				return
			case outputs <- Output{Stream: stream, Text: line}:
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErrors <- err
			}
			return
		}
	}
}
