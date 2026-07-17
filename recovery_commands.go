package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/activation"
)

const defaultQuiescenceTimeout = 30 * time.Second

func runStateRollbackPreflight(args []string, output, errorOutput io.Writer) int {
	flags := flag.NewFlagSet("state-rollback-preflight", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	dataRoot := flags.String("data-root", "", "Factory data root")
	target := flags.String("to-deployment", "", "successful deployment ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !exactDataRoot(*dataRoot) || *target == "" {
		return 2
	}
	recovery, err := activation.AcquireLease(recoveryLeasePath(*dataRoot))
	if err == nil {
		defer recovery.Close()
	}
	var lease *activation.Lease
	if err == nil {
		lease, err = activation.AcquireLease(filepath.Join(*dataRoot, "state-transition.lock"))
	}
	if err == nil {
		defer lease.Close()
		err = activation.RollbackPreflight(*dataRoot, *target, lease)
	}
	if err != nil {
		fmt.Fprintf(errorOutput, "state rollback preflight failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(output, `{"status":"ready"}`)
	return 0
}

func runStateRollback(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	flags := flag.NewFlagSet("state-rollback", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	dataRoot := flags.String("data-root", "", "Factory data root")
	provider := flags.String("provider", "", "absolute nags provider path")
	timeout := flags.Duration("quiesce-timeout", defaultQuiescenceTimeout, "maximum quiescence wait")
	if flags.Parse(args) != nil || !exactDataRoot(*dataRoot) || !filepath.IsAbs(*provider) || *timeout <= 0 || flags.NArg() == 0 {
		return 2
	}
	providerArgs := flags.Args()
	target, err := rollbackTarget(providerArgs)
	if err != nil {
		fmt.Fprintf(errorOutput, "state rollback: %v\n", err)
		return 2
	}
	recovery, err := activation.AcquireLease(recoveryLeasePath(*dataRoot))
	if err != nil {
		fmt.Fprintf(errorOutput, "state rollback failed: another recovery operation owns authority: %v\n", err)
		return 1
	}
	defer recovery.Close()
	lease, err := activation.QuiesceAndAcquire(ctx, filepath.Join(*dataRoot, "state-transition.lock"), *timeout)
	if err != nil {
		fmt.Fprintf(errorOutput, "state rollback failed: %v\n", err)
		return 1
	}
	defer lease.Close()
	if err := activation.PrepareRollback(*dataRoot, lease); err != nil {
		fmt.Fprintf(errorOutput, "state rollback failed: %v\n", err)
		return 1
	}
	if err := activation.RollbackPreflight(*dataRoot, target, lease); err != nil {
		fmt.Fprintf(errorOutput, "state rollback failed: %v\n", err)
		return 1
	}
	command := exec.CommandContext(ctx, *provider, append([]string{"rollback", "factory"}, providerArgs...)...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, output, errorOutput
	if err := lease.ConfigureCommand(command); err != nil {
		fmt.Fprintf(errorOutput, "state rollback failed: %v\n", err)
		return 1
	}
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return exitError.ExitCode()
		}
		fmt.Fprintf(errorOutput, "state rollback failed: %v\n", err)
		return 1
	}
	return 0
}

func runStateRestore(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	flags := flag.NewFlagSet("state-restore", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	dataRoot := flags.String("data-root", "", "Factory data root")
	migrationReceipt := flags.String("migration-receipt", "", "absolute generation backup receipt")
	tmuxPath := flags.String("tmux", requiredCommand("tmux"), "tmux executable")
	tmuxSocket := flags.String("tmux-socket", envOr("FACTORY_TMUX_SOCKET", defaultTmuxSocket), "Factory tmux socket")
	timeout := flags.Duration("quiesce-timeout", defaultQuiescenceTimeout, "maximum quiescence wait")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !exactDataRoot(*dataRoot) ||
		!filepath.IsAbs(*migrationReceipt) || *tmuxPath == "" || strings.TrimSpace(*tmuxSocket) == "" || *timeout <= 0 {
		return 2
	}
	recovery, err := activation.AcquireLease(recoveryLeasePath(*dataRoot))
	if err != nil {
		fmt.Fprintf(errorOutput, "state restore failed: another recovery operation owns authority: %v\n", err)
		return 1
	}
	defer recovery.Close()
	lease, err := activation.QuiesceAndAcquire(ctx, filepath.Join(*dataRoot, "state-transition.lock"), *timeout)
	if err != nil {
		fmt.Fprintf(errorOutput, "state restore failed: %v\n", err)
		return 1
	}
	defer lease.Close()
	receipt, err := activation.RestoreState(activation.RestoreOptions{
		DataRoot: *dataRoot, MigrationReceipt: *migrationReceipt, Lease: lease, Now: time.Now().UTC(),
		LiveSessions: func() ([]string, error) { return liveTmuxSessions(ctx, *tmuxPath, *tmuxSocket) },
	})
	if err != nil {
		fmt.Fprintf(errorOutput, "state restore failed: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(output).Encode(receipt); err != nil {
		fmt.Fprintf(errorOutput, "state restore failed: encode receipt: %v\n", err)
		return 1
	}
	return 0
}

func rollbackTarget(args []string) (string, error) {
	var target string
	for index := 0; index < len(args); index++ {
		value := args[index]
		switch {
		case value == "--to":
			index++
			if index == len(args) || args[index] == "" || target != "" {
				return "", errors.New("provider arguments require exactly one --to deployment")
			}
			target = args[index]
		case strings.HasPrefix(value, "--to="):
			if target != "" || strings.TrimPrefix(value, "--to=") == "" {
				return "", errors.New("provider arguments require exactly one --to deployment")
			}
			target = strings.TrimPrefix(value, "--to=")
		}
	}
	if target == "" {
		return "", errors.New("provider arguments require exactly one --to deployment")
	}
	return target, nil
}

func liveTmuxSessions(ctx context.Context, tmuxPath, socket string) ([]string, error) {
	command := exec.CommandContext(ctx, tmuxPath, "-L", socket, "list-sessions", "-F", "#{session_name}")
	data, err := command.CombinedOutput()
	if err != nil {
		if strings.Contains(string(data), "no server running on") {
			return []string{}, nil
		}
		return nil, fmt.Errorf("tmux session inventory: %w: %s", err, strings.TrimSpace(string(data)))
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, nil
	}
	sessions := make([]string, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			return nil, errors.New("tmux session inventory is ambiguous")
		}
		seen[name] = true
		sessions = append(sessions, name)
	}
	return sessions, nil
}

func exactDataRoot(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.Base(path) == "data"
}

func recoveryLeasePath(dataRoot string) string {
	return filepath.Join(dataRoot, "state-recovery.lock")
}
