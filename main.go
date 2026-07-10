package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/activity"
	"github.com/tomnagengast/network/apps/factory/internal/server"
)

const (
	defaultPort        = "8092"
	activityEventLimit = 250
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("factory stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	web := os.DirFS("frontend/dist")
	if _, err := fs.Stat(web, "index.html"); err != nil {
		return fmt.Errorf("frontend is not built (run bun run build in frontend): %w", err)
	}
	secret := os.Getenv("LINEAR_WEBHOOK_SECRET")
	if secret == "" {
		return errors.New("LINEAR_WEBHOOK_SECRET is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	activityStore, err := activity.Open(
		filepath.Join(home, ".local", "share", "factory", "data", "linear-activity.json"),
		activityEventLimit,
	)
	if err != nil {
		return err
	}
	handler, err := server.New(web, activityStore, []byte(secret), time.Now)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              "127.0.0.1:" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("factory listening", "address", httpServer.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}
