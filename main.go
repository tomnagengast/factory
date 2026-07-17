package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tomnagengast/factory/internal/agent"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/server"
)

//go:embed frontend/index.html frontend/src/index.js frontend/src/styles.css
var frontend embed.FS

type config struct {
	Address      string
	DataPath     string
	Workspace    string
	AgentCommand string
}

type componentResult struct {
	name string
	err  error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	configuration, err := parseConfig(os.Args[1:], os.Stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			slog.Error("factory configuration", "error", err)
			os.Exit(2)
		}
		return
	}
	if err := run(ctx, configuration); err != nil {
		slog.Error("factory stopped", "error", err)
		os.Exit(1)
	}
}

func parseConfig(arguments []string, output io.Writer) (config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config{}, fmt.Errorf("resolve home directory: %w", err)
	}
	workspace, err := os.Getwd()
	if err != nil {
		return config{}, fmt.Errorf("resolve workspace: %w", err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8092"
	}

	flags := flag.NewFlagSet("factory", flag.ContinueOnError)
	flags.SetOutput(output)
	var configuration config
	flags.StringVar(&configuration.Address, "addr", "127.0.0.1:"+port, "HTTP listen address")
	flags.StringVar(
		&configuration.DataPath,
		"data",
		filepath.Join(home, ".local", "share", "factory", "events.jsonl"),
		"append-only event wire path",
	)
	flags.StringVar(&configuration.Workspace, "workspace", workspace, "agent working directory")
	flags.StringVar(&configuration.AgentCommand, "agent", "codex", "Codex executable")
	flags.Usage = func() {
		fmt.Fprintln(output, "Factory turns prompts into observable agent runs on one event wire.")
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Usage: factory [options]")
		fmt.Fprintln(output)
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("factory accepts options only")
	}
	configuration.Workspace, err = filepath.Abs(configuration.Workspace)
	if err != nil {
		return config{}, fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(configuration.Workspace)
	if err != nil {
		return config{}, fmt.Errorf("inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return config{}, errors.New("workspace must be a directory")
	}
	if configuration.Address == "" || configuration.DataPath == "" || configuration.AgentCommand == "" {
		return config{}, errors.New("address, data path, and agent command are required")
	}
	return configuration, nil
}

func run(ctx context.Context, configuration config) error {
	wire, err := eventwire.Open(configuration.DataPath)
	if err != nil {
		return err
	}
	defer wire.Close()

	loop, err := agent.NewLoop(wire, agent.CommandRunner{
		Command: configuration.AgentCommand, Workspace: configuration.Workspace,
	})
	if err != nil {
		return err
	}
	app, err := server.New(wire, frontend, filepath.Base(configuration.AgentCommand))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", configuration.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", configuration.Address, err)
	}
	defer listener.Close()

	httpServer := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan componentResult, 2)
	go func() { results <- componentResult{name: "agent loop", err: loop.Run(runContext)} }()
	go func() { results <- componentResult{name: "http server", err: httpServer.Serve(listener)} }()

	slog.Info(
		"factory listening",
		"address", "http://"+listener.Addr().String(),
		"workspace", configuration.Workspace,
		"wire", configuration.DataPath,
	)

	first := <-results
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := httpServer.Shutdown(shutdownContext)
	shutdownCancel()
	second := <-results

	if err := operationalError(first); err != nil {
		return err
	}
	if err := operationalError(second); err != nil {
		return err
	}
	if shutdownErr != nil {
		return fmt.Errorf("stop HTTP server: %w", shutdownErr)
	}
	return nil
}

func operationalError(result componentResult) error {
	if result.err == nil || errors.Is(result.err, context.Canceled) || errors.Is(result.err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s: %w", result.name, result.err)
}
