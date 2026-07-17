package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/activation"
	"github.com/tomnagengast/factory/internal/migration"
)

func TestGenerationServiceStagesThenRetriesReceiptBeforeRuntime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := testGenerationConfig(root)
	staged := filepath.Join(config.GenerationsRoot, "migration-1")
	attempts := 0
	selected := &migration.SelectedGeneration{}
	operations := generationOperations{
		readSelection: func(string) (activation.StateSelection, error) { return activation.StateSelection{}, os.ErrNotExist },
		selectedPath: func(string, string) (string, error) {
			t.Fatal("selected path called for fresh install")
			return "", nil
		},
		build: func(string, string, migration.Options) (migration.Generation, error) {
			return migration.Generation{Path: staged}, nil
		},
		finalize: func(_ context.Context, candidate activation.FinalizerConfig) (*activation.Activation, error) {
			attempts++
			if candidate.GenerationPath != staged {
				t.Fatalf("generation path = %q", candidate.GenerationPath)
			}
			if attempts == 1 {
				return nil, activation.ErrReceiptPending
			}
			return &activation.Activation{}, nil
		},
		resume: func(context.Context, activation.FinalizerConfig) (*activation.Activation, error) {
			t.Fatal("resume called for fresh install")
			return nil, nil
		},
		open: func(path string) (*migration.SelectedGeneration, error) {
			if path != staged {
				t.Fatalf("open path = %q", path)
			}
			return selected, nil
		},
	}
	service, err := newGenerationService(config, operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := service.Run(t.Context(), func(_ context.Context, got *migration.SelectedGeneration) error {
		called = true
		if got != selected {
			t.Fatal("runtime received wrong generation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called || attempts != 2 {
		t.Fatalf("called=%v attempts=%d", called, attempts)
	}
}

func TestGenerationServiceResumesSelectionBeforeRuntime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := testGenerationConfig(root)
	selectedPath := filepath.Join(config.GenerationsRoot, "migration-2")
	selected := &migration.SelectedGeneration{}
	operations := generationOperations{
		readSelection: func(string) (activation.StateSelection, error) {
			return activation.StateSelection{StateGeneration: 1}, nil
		},
		selectedPath: func(string, string) (string, error) { return selectedPath, nil },
		build: func(string, string, migration.Options) (migration.Generation, error) {
			t.Fatal("build called for selected generation")
			return migration.Generation{}, nil
		},
		finalize: func(context.Context, activation.FinalizerConfig) (*activation.Activation, error) {
			t.Fatal("finalize called for selected generation")
			return nil, nil
		},
		resume: func(_ context.Context, candidate activation.FinalizerConfig) (*activation.Activation, error) {
			if candidate.GenerationPath != selectedPath {
				t.Fatalf("resume path = %q", candidate.GenerationPath)
			}
			return &activation.Activation{}, nil
		},
		open: func(string) (*migration.SelectedGeneration, error) { return selected, nil },
	}
	service, err := newGenerationService(config, operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Run(t.Context(), func(context.Context, *migration.SelectedGeneration) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationServiceFailsClosedOnSelectionAndActivationErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := testGenerationConfig(root)
	sentinel := errors.New("corrupt selection")
	operations := generationOperations{
		readSelection: func(string) (activation.StateSelection, error) { return activation.StateSelection{}, sentinel },
		selectedPath:  func(string, string) (string, error) { return "", nil },
		build: func(string, string, migration.Options) (migration.Generation, error) {
			return migration.Generation{}, nil
		},
		finalize: func(context.Context, activation.FinalizerConfig) (*activation.Activation, error) { return nil, nil },
		resume:   func(context.Context, activation.FinalizerConfig) (*activation.Activation, error) { return nil, nil },
		open:     func(string) (*migration.SelectedGeneration, error) { return nil, nil },
	}
	service, err := newGenerationService(config, operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Prepare(t.Context()); !errors.Is(err, sentinel) {
		t.Fatalf("Prepare error = %v", err)
	}
	if err := service.Run(t.Context(), func(context.Context, *migration.SelectedGeneration) error { return nil }); err == nil {
		t.Fatal("unprepared service ran")
	}
}

func testGenerationConfig(root string) GenerationConfig {
	stateRoot := filepath.Join(root, ".local", "share", "factory")
	dataRoot := filepath.Join(stateRoot, "data")
	return GenerationConfig{
		DataRoot: dataRoot, GenerationsRoot: filepath.Join(stateRoot, "generations"),
		Migration:     migration.Options{Now: time.Now()},
		Finalizer:     activation.FinalizerConfig{StateRoot: stateRoot, DataRoot: dataRoot},
		RetryInterval: time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}
