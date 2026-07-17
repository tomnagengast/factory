package repositories

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskstore"
)

type equivalenceRoundTripper func(*http.Request) (*http.Response, error)

func (f equivalenceRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type equivalenceCompletionReader struct{ repository string }

func (r equivalenceCompletionReader) ReadCompletionEvidence(context.Context, agentrun.Run, agentrun.PullRequestSnapshot) (agentrun.CompletionEvidence, error) {
	return agentrun.CompletionEvidence{Deployment: agentrun.DeploymentReceipt{SourceRepository: r.repository}}, nil
}

func TestCanonicalRepositoryLookupsMatchLegacyOwners(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 23, 0, 0, 0, time.UTC)
	compiled := compiledSource(root, "factory")
	setup := succeededSetup(root, "factory", now)
	state, err := ConvertSources([]CompiledSource{compiled}, []SetupSource{setup})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	record, found := canonical.Record(setup.Repository)
	if !found {
		t.Fatal("canonical repository record is missing")
	}

	legacyConfig := legacyRepositoryConfig(compiled)
	legacyCatalog, err := agentrun.NewRepositoryCatalog([]agentrun.RepositoryConfig{legacyConfig})
	if err != nil {
		t.Fatal(err)
	}
	legacySetups, err := projectsetup.Open(filepath.Join(root, "project-setups.json"), now)
	if err != nil {
		t.Fatal(err)
	}
	legacySpec := projectsetup.Spec{
		ProjectID: setup.ProjectID, ProjectName: setup.ProjectName, Repository: setup.Repository,
		RepoURL: setup.RepoURL, LocalPath: setup.LocalPath, ManagedRoot: setup.ManagedRoot,
		BaseBranch: setup.BaseBranch,
	}
	if needsProvision, err := legacySetups.Upsert(legacySpec, now); err != nil || needsProvision {
		t.Fatalf("legacy setup upsert = %t, %v", needsProvision, err)
	}
	legacyChoices := legacySetups.Choices()
	canonicalChoices := canonical.Choices()
	if len(legacyChoices) != 1 || len(canonicalChoices) != 1 ||
		legacyChoices[0].ProjectID != canonicalChoices[0].ProjectID ||
		legacyChoices[0].ProjectName != canonicalChoices[0].ProjectName ||
		legacyChoices[0].Repository != canonicalChoices[0].Repository {
		t.Fatalf("setup choices = legacy %#v canonical %#v", legacyChoices, canonicalChoices)
	}
	legacyResolvedSetup, err := legacySetups.ResolveSucceeded(setup.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	canonicalResolvedSetup, err := canonical.ResolveProjectID(setup.ProjectID)
	if err != nil || legacyResolvedSetup.Repository != canonicalResolvedSetup.Repository ||
		legacyResolvedSetup.LocalPath != canonicalResolvedSetup.LocalPath {
		t.Fatalf("setup lookup = legacy %#v canonical %#v err=%v", legacyResolvedSetup, canonicalResolvedSetup, err)
	}

	description := "GitHub Repo: " + setup.Repository + "\nLocal Path: " + setup.LocalPath + "\n"
	linearResolver := legacyLinearResolver(t, legacyCatalog, description)
	linearRef := taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-47", Identifier: "ENG-47"}
	legacyLinear, err := linearResolver.ResolveTask(context.Background(), linearRef)
	if err != nil {
		t.Fatal(err)
	}
	metadata := ProjectMetadata{
		ProjectID: setup.ProjectID, ProjectName: setup.ProjectName,
		Repository: setup.Repository, LocalPath: setup.LocalPath,
	}
	canonicalLinear, err := canonical.ResolveTask(TaskLookup{Ref: linearRef, Project: &metadata})
	if err != nil {
		t.Fatal(err)
	}
	assertRepositoryIdentityEquivalent(t, legacyLinear, canonicalLinear)

	tasks, err := taskstore.Open(filepath.Join(root, "native-tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	actor := taskstore.Actor{ID: "operator", Kind: taskstore.AuthorHuman}
	task, _, err := tasks.Create(taskstore.CreateCommand{
		Actor: actor, Title: "Native", ProjectID: setup.ProjectID,
		ApprovalMode: taskstore.ApprovalGated, IdempotencyKey: "create",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = tasks.SetRouting(taskstore.RoutingCommand{
		Actor: actor, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, IdempotencyKey: "route",
		Routing: taskstore.RoutingSnapshot{
			ProjectID: setup.ProjectID, Repository: legacyConfig.Repository, RepositoryURL: legacyConfig.RepoURL,
			RepositoryPath: legacyConfig.RepoPath, ManagedRoot: legacyConfig.ManagedRoot,
			BaseBranch: legacyConfig.BaseBranch, Bootstrap: legacyConfig.Bootstrap,
			WorkflowID: "custom-review", WorkflowDigest: "digest", AdmittedAt: now,
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	factoryResolver, err := agentrun.NewFactoryRepositoryResolver(tasks, legacyCatalog)
	if err != nil {
		t.Fatal(err)
	}
	legacyFactory, err := factoryResolver.ResolveTask(context.Background(), task.Ref)
	if err != nil {
		t.Fatal(err)
	}
	route := record.Route()
	canonicalFactory, err := canonical.ResolveTask(TaskLookup{Ref: task.Ref, ProjectID: setup.ProjectID, Route: &route})
	if err != nil {
		t.Fatal(err)
	}
	assertRepositoryIdentityEquivalent(t, legacyFactory, canonicalFactory)

	launch, err := canonical.LaunchConfig(setup.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Repository != legacyConfig.Repository || launch.Origin != legacyConfig.RepoURL ||
		launch.Path != legacyConfig.RepoPath || launch.ManagedRoot != legacyConfig.ManagedRoot ||
		launch.DefaultBranch != legacyConfig.BaseBranch || launch.Bootstrap != legacyConfig.Bootstrap ||
		launch.CloudURL != legacyConfig.CloudURL {
		t.Fatalf("launch identity = canonical %#v legacy %#v", launch, legacyConfig)
	}

	completion, err := canonical.CompletionIdentity(setup.Repository)
	if err != nil {
		t.Fatal(err)
	}
	wantRemotes := []string{
		"git@github.com:" + legacyConfig.Repository + ".git",
		"https://github.com/" + legacyConfig.Repository,
		"https://github.com/" + legacyConfig.Repository + ".git",
	}
	if completion.App != legacyConfig.App || completion.Repository != legacyConfig.Repository ||
		completion.Path != legacyConfig.RepoPath || completion.DefaultBranch != legacyConfig.BaseBranch ||
		!reflect.DeepEqual(completion.RemoteURLs, wantRemotes) ||
		completion.Deployment.ReceiptPath != legacyConfig.ReceiptPath ||
		completion.Deployment.PendingReceiptPath != legacyConfig.PendingReceipt ||
		completion.Deployment.HealthURL != legacyConfig.HealthURL ||
		completion.Deployment.SourcePath != legacyConfig.SourcePath {
		t.Fatalf("completion identity = canonical %#v legacy %#v", completion, legacyConfig)
	}
	legacyCompletion, err := agentrun.NewRepositoryCompletionEvidence(map[string]agentrun.CompletionEvidenceReader{
		legacyConfig.Repository: equivalenceCompletionReader{repository: legacyConfig.Repository},
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := legacyCompletion.ReadCompletionEvidence(context.Background(), agentrun.Run{Repository: setup.Repository}, agentrun.PullRequestSnapshot{})
	if err != nil || evidence.Deployment.SourceRepository != completion.Repository {
		t.Fatalf("completion selection = %#v, %v", evidence, err)
	}

	if _, err := legacyCatalog.ResolveRepository("tomnagengast/missing"); err == nil {
		t.Fatal("legacy launch lookup accepted an unknown repository")
	}
	if _, err := canonical.LaunchConfig("tomnagengast/missing"); err == nil {
		t.Fatal("canonical launch lookup accepted an unknown repository")
	}
	if _, err := legacyCompletion.ReadCompletionEvidence(context.Background(), agentrun.Run{Repository: "tomnagengast/missing"}, agentrun.PullRequestSnapshot{}); err == nil {
		t.Fatal("legacy completion lookup accepted an unknown repository")
	}
	if _, err := canonical.CompletionIdentity("tomnagengast/missing"); err == nil {
		t.Fatal("canonical completion lookup accepted an unknown repository")
	}

	staleRoute := route
	staleRoute.Origin = "git@github.com:tomnagengast/other.git"
	if _, err := canonical.ResolveTask(TaskLookup{Ref: task.Ref, ProjectID: setup.ProjectID, Route: &staleRoute}); err == nil {
		t.Fatal("canonical task lookup accepted a stale pinned route")
	}
	changedLegacy := legacyConfig
	changedLegacy.RepoURL = "git@github.com:tomnagengast/other.git"
	changedLegacy.Repository = "tomnagengast/other"
	if err := legacyCatalog.Replace([]agentrun.RepositoryConfig{changedLegacy}); err != nil {
		t.Fatal(err)
	}
	if _, err := factoryResolver.ResolveTask(context.Background(), task.Ref); err == nil {
		t.Fatal("legacy task lookup accepted a stale pinned route")
	}
}

func TestCanonicalRepositoryTaskLookupIntentionallyRejectsNoncanonicalRefAcceptedByLegacyResolver(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, time.July, 16, 23, 0, 0, 0, time.UTC)
	compiled := compiledSource(root, "factory")
	setup := succeededSetup(root, "factory", now)
	state, err := ConvertSources([]CompiledSource{compiled}, []SetupSource{setup})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := NewCatalog(state)
	if err != nil {
		t.Fatal(err)
	}
	legacyCatalog, err := agentrun.NewRepositoryCatalog([]agentrun.RepositoryConfig{legacyRepositoryConfig(compiled)})
	if err != nil {
		t.Fatal(err)
	}
	description := "GitHub Repo: " + setup.Repository + "\nLocal Path: " + setup.LocalPath + "\n"
	legacy := legacyLinearResolver(t, legacyCatalog, description)
	noncanonical := taskmodel.TaskRef{Source: " LINEAR ", ProviderID: " eng-47 ", Identifier: " eng-47 "}
	if resolved, err := legacy.ResolveTask(context.Background(), noncanonical); err != nil || resolved.Repository != setup.Repository {
		t.Fatalf("legacy normalization boundary = %#v, %v", resolved, err)
	}
	metadata := ProjectMetadata{
		ProjectID: setup.ProjectID, ProjectName: setup.ProjectName,
		Repository: setup.Repository, LocalPath: setup.LocalPath,
	}
	_, err = canonical.ResolveTask(TaskLookup{Ref: noncanonical, Project: &metadata})
	if err == nil {
		t.Fatal("canonical lookup accepted a noncanonical task reference")
	}
	var permanent interface{ Permanent() bool }
	if !errors.As(err, &permanent) || !permanent.Permanent() {
		t.Fatalf("canonical rejection is not permanent: %v", err)
	}
}

func legacyRepositoryConfig(source CompiledSource) agentrun.RepositoryConfig {
	return agentrun.RepositoryConfig{
		App: source.App, Repository: source.Repository, RepoURL: source.RepoURL,
		RepoPath: source.RepoPath, ManagedRoot: source.ManagedRoot, ProjectPath: source.ProjectPath,
		BaseBranch: source.BaseBranch, Bootstrap: source.Bootstrap, CloudURL: source.CloudURL,
		ReceiptPath: source.ReceiptPath, PendingReceipt: source.PendingReceipt,
		HealthURL: source.HealthURL, SourcePath: source.SourcePath,
	}
}

func legacyLinearResolver(t *testing.T, catalog *agentrun.RepositoryCatalog, description string) *agentrun.LinearRepositoryResolver {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{"issue": map[string]any{"project": map[string]string{"description": description}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: equivalenceRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(body)),
		}, nil
	})}
	resolver, err := agentrun.NewLinearRepositoryResolver("https://api.linear.app/graphql", "test-key", client, catalog)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func assertRepositoryIdentityEquivalent(t *testing.T, legacy agentrun.RepositoryConfig, canonical Record) {
	t.Helper()
	if legacy.App != canonical.App || legacy.Repository != canonical.Repository || legacy.RepoURL != canonical.Origin ||
		legacy.RepoPath != canonical.ManagedPath || legacy.ManagedRoot != canonical.ManagedRoot ||
		legacy.ProjectPath != canonical.LocalPath || legacy.BaseBranch != canonical.DefaultBranch ||
		legacy.Bootstrap != canonical.Bootstrap || legacy.CloudURL != canonical.CloudURL {
		t.Fatalf("repository identity = legacy %#v canonical %#v", legacy, canonical)
	}
}
