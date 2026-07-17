package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/workflow"
)

type repositoryRegistrar struct {
	staticConfigs []agentrun.RepositoryConfig
	catalog       *agentrun.RepositoryCatalog
	evidence      *agentrun.RepositoryCompletionEvidence
	readerOptions completionReaderOptions
}

func (r *repositoryRegistrar) SyncRepositories(specs []projectsetup.Spec) error {
	configs, err := repositoryConfigsWithSetups(r.staticConfigs, specs)
	if err != nil {
		return err
	}
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

func repositoryConfigsWithSetups(staticConfigs []agentrun.RepositoryConfig, specs []projectsetup.Spec) ([]agentrun.RepositoryConfig, error) {
	configs := append([]agentrun.RepositoryConfig(nil), staticConfigs...)
	for _, spec := range specs {
		if !spec.Managed {
			found := false
			for index := range configs {
				if !strings.EqualFold(configs[index].Repository, spec.Repository) {
					continue
				}
				if configs[index].ProjectPath != spec.LocalPath {
					return nil, fmt.Errorf("project setup: persisted path for %s does not match its compiled repository", spec.Repository)
				}
				configs[index].CloudURL = spec.CloudURL
				found = true
				break
			}
			if !found {
				return nil, fmt.Errorf("project setup: persisted existing repository %s is no longer compiled", spec.Repository)
			}
			continue
		}
		_, app, _ := strings.Cut(spec.Repository, "/")
		configs = append(configs, agentrun.RepositoryConfig{
			App:         app,
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
	return configs, nil
}

type completionReaderOptions struct {
	linearURL      string
	linearAPIKey   string
	gitPath        string
	worktrunkPath  string
	httpClient     *http.Client
	taskCompletion agentrun.TaskCompletionProvider
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
			TaskCompletion: options.taskCompletion,
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
	provider       *providerAgentStarter
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
	if spec.CloudURL != "" {
		if p.provider == nil {
			return fmt.Errorf("coordinate provider setup: provider starter is not configured")
		}
		if err := p.provider.Start(ctx, spec); err != nil {
			return fmt.Errorf("coordinate provider setup: %w", err)
		}
	}
	return nil
}

type agentRunNotifier interface {
	Notify()
}

type providerWorkflowSettings interface {
	Snapshot() settings.Snapshot
	MarkWorkflowRollbackIncompatible(time.Time) (settings.Snapshot, error)
}

type providerAgentStarter struct {
	coordinator projectsetup.ProviderCoordinator
	store       *agentrun.Store
	notifier    agentRunNotifier
	settings    providerWorkflowSettings
	repository  agentrun.RepositoryConfig
	now         func() time.Time
}

func newProviderAgentStarter(
	coordinator projectsetup.ProviderCoordinator,
	store *agentrun.Store,
	notifier agentRunNotifier,
	workflowSettings providerWorkflowSettings,
	repository agentrun.RepositoryConfig,
	now func() time.Time,
) (*providerAgentStarter, error) {
	if coordinator == nil || store == nil || notifier == nil || workflowSettings == nil || now == nil {
		return nil, fmt.Errorf("provider agent starter: coordinator, run store, notifier, workflow settings, and clock are required")
	}
	if repository.Repository == "" || repository.RepoURL == "" || !filepath.IsAbs(repository.RepoPath) || !filepath.IsAbs(repository.ManagedRoot) || repository.BaseBranch == "" {
		return nil, fmt.Errorf("provider agent starter: complete provider repository routing is required")
	}
	return &providerAgentStarter{
		coordinator: coordinator, store: store, notifier: notifier, settings: workflowSettings,
		repository: repository, now: now,
	}, nil
}

func (s *providerAgentStarter) Start(ctx context.Context, spec projectsetup.Spec) error {
	issue, err := s.coordinator.Ensure(ctx, spec)
	if err != nil {
		return err
	}
	if issue.ID == "" || !agentrun.ValidIssueIdentifier(issue.Identifier) {
		return fmt.Errorf("provider agent starter: coordinator returned an invalid Linear issue")
	}
	config := s.repository
	now := s.now()
	candidate := providerWorkflowCandidate(s.settings, now)
	_, created, err := s.store.Claim(agentrun.InitialClaim{Trigger: agentrun.Trigger{
		DeliveryID:      providerAgentDeliveryID(issue.ID, spec),
		IssueIdentifier: issue.Identifier,
		Kind:            agentrun.TriggerKindLabel,
		Repository:      config.Repository,
		RepositoryURL:   config.RepoURL,
		RepositoryPath:  config.RepoPath,
		ManagedRoot:     config.ManagedRoot,
		BaseBranch:      config.BaseBranch,
		Bootstrap:       config.Bootstrap,
		CloudURL:        config.CloudURL,
	}, Workflow: candidate}, now)
	if err != nil {
		return fmt.Errorf("provider agent starter: claim run: %w", err)
	}
	if created {
		s.notifier.Notify()
	}
	return nil
}

func providerWorkflowCandidate(store providerWorkflowSettings, now time.Time) agentrun.WorkflowCandidate {
	configuration := store.Snapshot()
	definition, err := configuration.WorkflowForTrigger(agentrun.TriggerKindLabel)
	if err != nil {
		return agentrun.FailedWorkflowCandidate(fmt.Errorf("select provider workflow: %w", err))
	}
	pinned := workflow.Pin(definition)
	digest, err := pinned.Digest()
	if err != nil {
		return agentrun.FailedWorkflowCandidate(fmt.Errorf("digest provider workflow: %w", err))
	}
	configuration, err = store.MarkWorkflowRollbackIncompatible(now)
	if err != nil {
		return agentrun.FailedWorkflowCandidate(fmt.Errorf("mark workflow rollback boundary: %w", err))
	}
	return agentrun.ResolvedWorkflowCandidate(pinned, digest, configuration.Revision)
}

func providerAgentDeliveryID(issueID string, spec projectsetup.Spec) string {
	digest := sha256.Sum256([]byte(spec.ProjectID + "\x00" + spec.Repository + "\x00" + spec.CloudURL))
	return "project-provider:" + issueID + ":" + hex.EncodeToString(digest[:8])
}

var _ projectsetup.Registrar = (*repositoryRegistrar)(nil)
var _ projectsetup.Provisioner = (*repositoryProvisioner)(nil)
