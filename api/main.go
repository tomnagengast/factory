package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tomnagengast/factory/api/internal/agent"
	"github.com/tomnagengast/factory/api/internal/credential"
	"github.com/tomnagengast/factory/api/internal/deployment"
	"github.com/tomnagengast/factory/api/internal/objectstore"
	"github.com/tomnagengast/factory/api/internal/quiescence"
	"github.com/tomnagengast/factory/api/internal/server"
	"github.com/tomnagengast/factory/api/internal/store"
	"github.com/tomnagengast/factory/api/internal/workflow"
)

//go:embed all:dist
var frontend embed.FS

type config struct {
	Address           string
	DatabaseURL       string
	CredentialsKey    string
	S3Bucket          string
	S3Prefix          string
	S3Region          string
	WorkflowWorkspace string
	CodexCommand      string
	ClaudeCommand     string
	FactoryCommand    string
	WorkflowCommand   string
	Objects           objectstore.Store
}

type componentResult struct {
	name string
	err  error
}

const httpShutdownTimeout = 3 * time.Second

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
	port := os.Getenv("PORT")
	if port == "" {
		port = "8092"
	}
	flags := flag.NewFlagSet("factory-api", flag.ContinueOnError)
	flags.SetOutput(output)
	var configuration config
	configuration.DatabaseURL = os.Getenv("DATABASE_URL")
	configuration.CredentialsKey = os.Getenv("FACTORY_CREDENTIALS_KEY")
	configuration.S3Bucket = os.Getenv("S3_BUCKET")
	configuration.S3Prefix = os.Getenv("S3_PREFIX")
	configuration.S3Region = os.Getenv("S3_REGION")
	flags.StringVar(&configuration.Address, "addr", "127.0.0.1:"+port, "HTTP listen address")
	flags.StringVar(
		&configuration.WorkflowWorkspace,
		"workflow-workspace",
		filepath.Join(home, ".local", "share", "factory", "workflow-workspace"),
		"untracked dynamic workflow workspace",
	)
	flags.StringVar(&configuration.CodexCommand, "codex", "codex", "Codex executable")
	flags.StringVar(&configuration.ClaudeCommand, "claude", "claude", "Claude Code executable")
	flags.StringVar(&configuration.FactoryCommand, "factory", "./factory", "Factory CLI exposed to workflows")
	flags.StringVar(&configuration.WorkflowCommand, "workflow", "workflow", "workflow CLI executable")
	flags.Usage = func() {
		fmt.Fprintln(output, "Factory serves the event wire, resource API, Solid UI, and workflow coordinator.")
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Usage: factory-api [options]")
		fmt.Fprintln(output)
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("factory-api accepts options only")
	}
	if configuration.Address == "" || configuration.DatabaseURL == "" || configuration.CredentialsKey == "" ||
		configuration.S3Bucket == "" || configuration.S3Prefix == "" || configuration.S3Region == "" || configuration.WorkflowWorkspace == "" || configuration.CodexCommand == "" ||
		configuration.ClaudeCommand == "" ||
		configuration.FactoryCommand == "" || configuration.WorkflowCommand == "" {
		return config{}, errors.New("DATABASE_URL, FACTORY_CREDENTIALS_KEY, S3_BUCKET, S3_PREFIX, S3_REGION, and all serve options require values")
	}
	return configuration, nil
}

func run(ctx context.Context, configuration config) error {
	factoryURL, err := localServerURL(configuration.Address)
	if err != nil {
		return err
	}
	eventStore, err := store.Open(configuration.DatabaseURL)
	if err != nil {
		return err
	}
	defer eventStore.Close()
	credentials, err := credential.Open(eventStore, configuration.CredentialsKey)
	if err != nil {
		return err
	}
	objects := configuration.Objects
	if objects == nil {
		objects, err = objectstore.OpenS3(ctx, configuration.S3Bucket, configuration.S3Prefix, configuration.S3Region)
		if err != nil {
			return err
		}
	}
	release := deployment.FromEnvironment()
	deployments := deployment.NewRecorder(eventStore, release)
	if err := deployments.Started(); err != nil {
		return fmt.Errorf("record deployment start: %w", err)
	}

	workflowCLI := workflow.CLI{
		Command: configuration.WorkflowCommand, Workspace: configuration.WorkflowWorkspace,
		CodexCommand: configuration.CodexCommand, ClaudeCommand: configuration.ClaudeCommand,
		FactoryCommand: configuration.FactoryCommand, FactoryURL: factoryURL,
		Environment: credentials.Environment,
		Objects:     objects,
	}
	if err := workflowCLI.Prepare(ctx); err != nil {
		return err
	}
	admission := quiescence.New(quiescence.Hooks{
		Quiescing: deployments.Quiescing,
		Quiesced:  deployments.Quiesced,
		Resuming:  deployments.Resumed,
	})
	loop, err := agent.NewLoop(eventStore, agent.CommandRunner{
		CodexCommand: configuration.CodexCommand, ClaudeCommand: configuration.ClaudeCommand,
		Workspace:      configuration.WorkflowWorkspace,
		FactoryCommand: configuration.FactoryCommand, FactoryURL: factoryURL,
		Environment: credentials.Environment,
	}, workflowCLI, admission)
	if err != nil {
		return err
	}
	assets, err := fs.Sub(frontend, "dist")
	if err != nil {
		return fmt.Errorf("open embedded web bundle: %w", err)
	}
	app, err := server.New(eventStore, credentials, assets, objects, admission, release)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", configuration.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", configuration.Address, err)
	}
	defer listener.Close()
	if err := loop.Initialize(ctx); err != nil {
		return fmt.Errorf("initialize workflow coordinator: %w", err)
	}
	if err := deployments.Resumed("startup", 0); err != nil {
		return fmt.Errorf("record deployment resumption: %w", err)
	}

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	httpServer := newHTTPServer(app.Handler(), runContext)
	results := make(chan componentResult, 2)
	go func() { results <- componentResult{name: "workflow coordinator", err: loop.Run(runContext)} }()
	go func() { results <- componentResult{name: "HTTP server", err: httpServer.Serve(listener)} }()

	slog.Info(
		"factory listening",
		"address", "http://"+listener.Addr().String(),
		"store", "postgres",
		"media", "s3",
		"workflowWorkspace", configuration.WorkflowWorkspace,
	)

	first := <-results
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
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

func localServerURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("resolve local server URL from %s: %w", address, err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func newHTTPServer(handler http.Handler, baseContext context.Context) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return baseContext
		},
	}
}

func operationalError(result componentResult) error {
	if result.err == nil || errors.Is(result.err, context.Canceled) || errors.Is(result.err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s: %w", result.name, result.err)
}
