package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
)

type providerSetupStarter interface {
	Start(context.Context, projectsetup.Spec) error
}

// RepositoryProvisioner retains the hardened workspace/bootstrap and provider
// webhook mechanics while canonical RepositoryOnboarding owns durable state.
type RepositoryProvisioner struct {
	launcherConfig agentrun.LauncherConfig
	nagsPath       string
	provider       providerSetupStarter
}

func NewRepositoryProvisioner(config agentrun.LauncherConfig, nagsPath string, provider providerSetupStarter) (*RepositoryProvisioner, error) {
	if config.BinaryPath == "" || config.GitPath == "" || config.WorktrunkPath == "" || config.TmuxPath == "" || config.TmuxSocket == "" || nagsPath == "" || provider == nil {
		return nil, errors.New("app repository provisioner: launcher, nags, and provider authorities are required")
	}
	return &RepositoryProvisioner{launcherConfig: config, nagsPath: nagsPath, provider: provider}, nil
}

func (p *RepositoryProvisioner) Provision(ctx context.Context, spec projectsetup.Spec) error {
	config := p.launcherConfig
	config.Repository = spec.Repository
	config.RepoURL = spec.RepoURL
	config.RepoPath = spec.LocalPath
	config.ManagedRoot = spec.ManagedRoot
	config.BaseBranch = spec.BaseBranch
	config.Bootstrap = spec.Bootstrap
	launcher, err := agentrun.NewTmuxLauncher(config)
	if err != nil {
		return err
	}
	if err := launcher.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare repository: %w", err)
	}
	command := exec.CommandContext(ctx, p.nagsPath, "github-hook", spec.Repository)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("register GitHub webhook: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if spec.CloudURL != "" {
		if err := p.provider.Start(ctx, spec); err != nil {
			return fmt.Errorf("coordinate provider setup: %w", err)
		}
	}
	return nil
}
