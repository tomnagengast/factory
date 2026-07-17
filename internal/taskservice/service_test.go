package taskservice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
)

var serviceNow = time.Date(2026, 7, 15, 16, 0, 0, 0, time.UTC)

type enabledControl bool

func (c enabledControl) Enabled(string) bool { return bool(c) }
func (c enabledControl) Snapshot() taskcontrol.Snapshot {
	return taskcontrol.Snapshot{Version: 1}
}
func (c enabledControl) SetProject(_ uint64, _ string, _ bool, _ time.Time) (taskcontrol.Snapshot, error) {
	return c.Snapshot(), nil
}

type projectAuthority struct{ spec projectsetup.Spec }

func (p projectAuthority) ResolveSucceeded(string) (projectsetup.Spec, error) { return p.spec, nil }
func (p projectAuthority) Choices() []projectsetup.Choice {
	return []projectsetup.Choice{{ProjectID: p.spec.ProjectID, Repository: p.spec.Repository}}
}

type fakeMutator struct{ store *taskstore.Store }

func (m fakeMutator) Execute(_ context.Context, command taskstore.CommandEnvelope, now time.Time) (taskstore.Result, error) {
	return m.store.Execute(command, now)
}

type policyAuthority struct {
	settings settings.Snapshot
	registry triggerregistry.Snapshot
}

func (p policyAuthority) SettingsSnapshot() settings.Snapshot        { return p.settings }
func (p policyAuthority) RegistrySnapshot() triggerregistry.Snapshot { return p.registry }

type nativeAdmitter struct {
	store *triggerrouter.Store
}

func (a nativeAdmitter) AdmitNative(value triggerrouter.NativeAdmission) (triggerrouter.Invocation, bool, error) {
	return a.store.AdmitNative(value)
}
func (a nativeAdmitter) AdmitNativeContinuation(value triggerrouter.NativeAdmission, eventKey string) (triggerrouter.Invocation, bool, error) {
	return a.store.AdmitNativeContinuation(value, eventKey)
}

type reconcileCounter struct{ calls int }

func (r *reconcileCounter) Reconcile(context.Context) error { r.calls++; return nil }

func TestCreateRequiresScopedEnablementAndSucceededProject(t *testing.T) {
	service, _, _ := newService(t, false)
	_, err := service.Create(context.Background(), CreateRequest{
		Actor: taskstore.Actor{ID: "human", Kind: taskstore.AuthorHuman}, Title: "Native task",
		ProjectID: "project-factory", ApprovalMode: taskstore.ApprovalGated, IdempotencyKey: "create-1",
	})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Create error = %v", err)
	}
}

func TestStartPinsRouteAndRepairsAdmissionIdempotently(t *testing.T) {
	service, store, reconciler := newService(t, true)
	created, err := service.Create(context.Background(), CreateRequest{
		Actor: taskstore.Actor{ID: "human", Kind: taskstore.AuthorHuman}, Title: "Native task",
		ProjectID: "project-factory", ApprovalMode: taskstore.ApprovalGated, IdempotencyKey: "create-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{Actor: taskstore.Actor{ID: "human", Kind: taskstore.AuthorHuman}, TaskID: created.Task.Ref.ProviderID, IdempotencyKey: "start-1"}
	first, err := service.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Admitted || first.Task.State != taskstore.StateInProgress || first.Task.Routing == nil || first.Invocation.Task.Source != taskmodel.SourceFactory {
		t.Fatalf("first start = %#v", first)
	}
	second, err := service.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Admitted || second.Invocation.ID != first.Invocation.ID || reconciler.calls != 2 {
		t.Fatalf("second start = %#v calls=%d", second, reconciler.calls)
	}
	stored, found := store.Find(created.Task.Ref.ProviderID)
	if !found || stored.Revision != first.Task.Revision {
		t.Fatalf("stored task = %#v found=%v", stored, found)
	}
}

func TestStartScopesRoutingIdempotencyToEachTask(t *testing.T) {
	service, store, _ := newService(t, true)
	actor := taskstore.Actor{ID: "human", Kind: taskstore.AuthorHuman}
	var tasks []taskstore.Task
	for _, key := range []string{"create-one", "create-two"} {
		created, err := service.Create(context.Background(), CreateRequest{
			Actor: actor, Title: key, ProjectID: "project-factory", ApprovalMode: taskstore.ApprovalGated, IdempotencyKey: key,
		})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, created.Task)
	}
	invocations := make(map[string]string)
	for _, task := range tasks {
		request := StartRequest{Actor: actor, TaskID: task.Ref.ProviderID, IdempotencyKey: "same-browser-key"}
		first, err := service.Start(context.Background(), request)
		if err != nil || !first.Admitted {
			t.Fatalf("start %s: result=%#v err=%v", task.Ref.ProviderID, first, err)
		}
		retry, err := service.Start(context.Background(), request)
		if err != nil || retry.Admitted || retry.Invocation.ID != first.Invocation.ID {
			t.Fatalf("retry %s: result=%#v err=%v", task.Ref.ProviderID, retry, err)
		}
		stored, _ := store.Find(task.Ref.ProviderID)
		if stored.Revision != 2 {
			t.Fatalf("task %s revision = %d, want 2", task.Ref.ProviderID, stored.Revision)
		}
		invocations[task.Ref.ProviderID] = first.Invocation.ID
	}
	if invocations[tasks[0].Ref.ProviderID] == invocations[tasks[1].Ref.ProviderID] {
		t.Fatal("different tasks shared a native admission invocation")
	}
}

func TestDetailSerializesEmptyCollectionsAsArrays(t *testing.T) {
	service, _, _ := newService(t, true)
	created, err := service.Create(context.Background(), CreateRequest{
		Actor: taskstore.Actor{ID: "human", Kind: taskstore.AuthorHuman}, Title: "Native task",
		ProjectID: "project-factory", ApprovalMode: taskstore.ApprovalGated, IdempotencyKey: "create-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(created.Task.Ref.ProviderID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Messages.Messages == nil || detail.Links == nil || detail.Gates == nil {
		t.Fatalf("detail collections must not be nil: %#v", detail)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"messages":[]`, `"links":[]`, `"gates":[]`} {
		if !strings.Contains(string(encoded), fragment) {
			t.Fatalf("detail JSON is missing %s: %s", fragment, encoded)
		}
	}
}

func newService(t *testing.T, enabled bool) (*Service, *taskstore.Store, *reconcileCounter) {
	t.Helper()
	directory := t.TempDir()
	tasks, err := taskstore.Open(filepath.Join(directory, "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	routing, err := triggerrouter.Open(filepath.Join(directory, "routing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	repository := agentrun.RepositoryConfig{
		App: "factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git",
		RepoPath: "/tmp/repos/factory", ManagedRoot: "/tmp/repos", ProjectPath: "/tmp/repos/factory", BaseBranch: "main",
	}
	catalog, err := agentrun.NewRepositoryCatalog([]agentrun.RepositoryConfig{repository})
	if err != nil {
		t.Fatal(err)
	}
	configuration := settings.Defaults(3)
	reconciler := &reconcileCounter{}
	service, err := New(
		enabledControl(enabled), projectAuthority{projectsetup.Spec{ProjectID: "project-factory", Repository: repository.Repository}},
		catalog, tasks, fakeMutator{tasks}, policyAuthority{settings: configuration, registry: triggerregistry.Snapshot{Schema: triggerregistry.SchemaVersion}},
		nativeAdmitter{routing}, reconciler, func() time.Time { return serviceNow },
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, tasks, reconciler
}
