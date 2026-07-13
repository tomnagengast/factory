package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
)

type repositoryRegistrar struct {
	staticConfigs []agentrun.RepositoryConfig
	catalog       *agentrun.RepositoryCatalog
	evidence      *agentrun.RepositoryCompletionEvidence
	readerOptions completionReaderOptions
}

func (r *repositoryRegistrar) SyncRepositories(specs []projectsetup.Spec) error {
	configs := repositoryConfigsWithSetups(r.staticConfigs, specs)
	if _, err := agentrun.NewRepositoryCatalog(configs); err != nil {
		return err
	}
	readers, err := buildCompletionReaders(configs, r.readerOptions)
	if err != nil {
		return err
	}
	if err := r.catalog.Replace(configs); err != nil {
		return err
	}
	return r.evidence.Replace(readers)
}

func repositoryConfigsWithSetups(staticConfigs []agentrun.RepositoryConfig, specs []projectsetup.Spec) []agentrun.RepositoryConfig {
	configs := append([]agentrun.RepositoryConfig(nil), staticConfigs...)
	for _, spec := range specs {
		configs = append(configs, agentrun.RepositoryConfig{
			App:         strings.TrimPrefix(spec.Repository, "tomnagengast/"),
			Repository:  spec.Repository,
			RepoURL:     spec.RepoURL,
			RepoPath:    spec.LocalPath,
			ManagedRoot: spec.ManagedRoot,
			ProjectPath: spec.LocalPath,
			BaseBranch:  spec.BaseBranch,
			Bootstrap:   spec.Bootstrap,
			CloudURL:    spec.CloudURL,
		})
	}
	return configs
}

type completionReaderOptions struct {
	linearURL     string
	linearAPIKey  string
	gitPath       string
	worktrunkPath string
	httpClient    *http.Client
}

func buildCompletionReaders(configs []agentrun.RepositoryConfig, options completionReaderOptions) (map[string]agentrun.CompletionEvidenceReader, error) {
	readers := make(map[string]agentrun.CompletionEvidenceReader, len(configs))
	for _, config := range configs {
		completionConfig := agentrun.SystemCompletionConfig{
			App:            config.App,
			Repository:     config.Repository,
			RemoteURLs:     repositoryRemoteURLs(config.Repository),
			RepoPath:       config.RepoPath,
			BaseBranch:     config.BaseBranch,
			ReceiptPath:    config.ReceiptPath,
			PendingReceipt: config.PendingReceipt,
			HealthURL:      config.HealthURL,
			SourcePath:     config.SourcePath,
			LinearURL:      options.linearURL,
			GitPath:        options.gitPath,
			WorktrunkPath:  options.worktrunkPath,
			LinearAPIKey:   options.linearAPIKey,
			HTTPClient:     options.httpClient,
		}
		var reader agentrun.CompletionEvidenceReader
		var err error
		if config.DeploymentRequired() {
			reader, err = agentrun.NewSystemCompletionEvidence(completionConfig)
		} else {
			reader, err = agentrun.NewRepositoryOnlyCompletionEvidence(completionConfig)
		}
		if err != nil {
			return nil, err
		}
		readers[config.Repository] = reader
	}
	return readers, nil
}

type repositoryProvisioner struct {
	launcherConfig agentrun.LauncherConfig
	nagsPath       string
}

func (p *repositoryProvisioner) Provision(ctx context.Context, spec projectsetup.Spec) error {
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
	return nil
}

var _ projectsetup.Registrar = (*repositoryRegistrar)(nil)
var _ projectsetup.Provisioner = (*repositoryProvisioner)(nil)

func projectSetupHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}
