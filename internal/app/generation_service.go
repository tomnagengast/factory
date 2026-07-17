package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tomnagengast/factory/internal/activation"
	"github.com/tomnagengast/factory/internal/migration"
)

type generationOperations struct {
	readSelection func(string) (activation.StateSelection, error)
	selectedPath  func(string, string) (string, error)
	build         func(string, string, migration.Options) (migration.Generation, error)
	finalize      func(context.Context, activation.FinalizerConfig) (*activation.Activation, error)
	resume        func(context.Context, activation.FinalizerConfig) (*activation.Activation, error)
	open          func(string) (*migration.SelectedGeneration, error)
}

type GenerationConfig struct {
	DataRoot        string
	GenerationsRoot string
	Migration       migration.Options
	Finalizer       activation.FinalizerConfig
	RetryInterval   time.Duration
	Logger          *slog.Logger
}

// GenerationService stages an unselected generation without opening mutable
// stores, or resumes an already selected generation. On first activation it
// waits for the exact provider receipt, finalizes under the state-transition
// lease, opens canonical stores only after the write boundary exists, and then
// transfers control to the selected runtime callback.
type GenerationService struct {
	config     GenerationConfig
	operations generationOperations
	path       string
	active     *activation.Activation
	selected   *migration.SelectedGeneration
	prepared   bool
}

func NewGenerationService(config GenerationConfig) (*GenerationService, error) {
	return newGenerationService(config, generationOperations{
		readSelection: activation.ReadSelection,
		selectedPath:  activation.SelectedGenerationPath,
		build:         migration.BuildGeneration,
		finalize:      activation.Finalize,
		resume:        activation.ResumeSelected,
		open:          migration.OpenSelectedGeneration,
	})
}

func newGenerationService(config GenerationConfig, operations generationOperations) (*GenerationService, error) {
	if config.DataRoot == "" || config.GenerationsRoot == "" || !filepath.IsAbs(config.DataRoot) || !filepath.IsAbs(config.GenerationsRoot) ||
		config.GenerationsRoot != filepath.Join(config.Finalizer.StateRoot, "generations") || config.DataRoot != config.Finalizer.DataRoot ||
		config.RetryInterval <= 0 || config.Logger == nil || operations.readSelection == nil || operations.selectedPath == nil ||
		operations.build == nil || operations.finalize == nil || operations.resume == nil || operations.open == nil {
		return nil, errors.New("app generation service: canonical paths, lifecycle operations, retry interval, and logger are required")
	}
	return &GenerationService{config: config, operations: operations}, nil
}

// Prepare performs every pre-listen operation. A fresh install stops at a
// validated staged generation; a restart reacquires the exact selected
// generation's continuous advancement lease before any HTTP socket is opened.
func (s *GenerationService) Prepare(ctx context.Context) error {
	if s == nil || s.prepared {
		return errors.New("app generation service: prepare must run exactly once")
	}
	_, err := s.operations.readSelection(s.config.DataRoot)
	switch {
	case err == nil:
		s.path, err = s.operations.selectedPath(s.config.Finalizer.StateRoot, s.config.DataRoot)
		if err != nil {
			return err
		}
		finalizer := s.config.Finalizer
		finalizer.GenerationPath = s.path
		s.active, err = s.operations.resume(ctx, finalizer)
		if err != nil {
			return err
		}
		s.selected, err = s.operations.open(s.path)
		if err != nil {
			_ = s.active.Close()
			s.active = nil
			return err
		}
	case errors.Is(err, os.ErrNotExist):
		generation, buildErr := s.operations.build(s.config.DataRoot, s.config.GenerationsRoot, s.config.Migration)
		if buildErr != nil {
			return buildErr
		}
		s.path = generation.Path
	default:
		return err
	}
	s.prepared = true
	return nil
}

func (s *GenerationService) Run(ctx context.Context, runtime func(context.Context, *migration.SelectedGeneration) error) error {
	if s == nil || !s.prepared || runtime == nil {
		return errors.New("app generation service: prepared service and selected runtime are required")
	}
	if s.selected == nil {
		if err := s.activate(ctx); err != nil {
			return err
		}
	}
	defer func() {
		_ = s.selected.Close()
		_ = s.active.Close()
		s.selected, s.active = nil, nil
	}()
	return runtime(ctx, s.selected)
}

func (s *GenerationService) activate(ctx context.Context) error {
	finalizer := s.config.Finalizer
	finalizer.GenerationPath = s.path
	for {
		active, err := s.operations.finalize(ctx, finalizer)
		if err == nil {
			s.active = active
			selected, openErr := s.operations.open(s.path)
			if openErr != nil {
				_ = s.active.Close()
				s.active = nil
				return openErr
			}
			s.selected = selected
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, activation.ErrReceiptPending) && !errors.Is(err, activation.ErrDeploymentLockUnavailable) && !errors.Is(err, activation.ErrLeaseUnavailable) {
			return err
		}
		s.config.Logger.Info("Factory canonical activation pending", "error", err, "retry_in", s.config.RetryInterval)
		timer := time.NewTimer(s.config.RetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *GenerationService) StagedPath() string {
	if s == nil {
		return ""
	}
	return s.path
}
