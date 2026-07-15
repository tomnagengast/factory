package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/taskstore"
)

type testTaskController struct {
	create taskservice.CreateRequest
}

func (c *testTaskController) Projects() []taskservice.ProjectChoice { return nil }
func (c *testTaskController) SetProject(uint64, string, bool) (taskcontrol.Snapshot, error) {
	return taskcontrol.Snapshot{Version: 1}, nil
}
func (c *testTaskController) List(string, int) (taskstore.TaskPage, error) {
	return taskstore.TaskPage{}, nil
}
func (c *testTaskController) Detail(string, uint64, int) (taskservice.Detail, error) {
	return taskservice.Detail{}, taskstore.ErrNotFound
}
func (c *testTaskController) Create(_ context.Context, request taskservice.CreateRequest) (taskstore.Result, error) {
	c.create = request
	return taskstore.Result{Task: taskstore.Task{Title: request.Title}}, nil
}
func (c *testTaskController) Update(context.Context, taskstore.UpdateCommand) (taskstore.Result, error) {
	return taskstore.Result{}, nil
}
func (c *testTaskController) Message(context.Context, taskstore.MessageCommand) (taskstore.Result, error) {
	return taskstore.Result{}, nil
}
func (c *testTaskController) Link(context.Context, taskstore.LinkCommand) (taskstore.Result, error) {
	return taskstore.Result{}, nil
}
func (c *testTaskController) Gate(context.Context, taskstore.GateCommand) (taskstore.Result, error) {
	return taskstore.Result{}, nil
}
func (c *testTaskController) Decide(context.Context, taskstore.DecisionCommand) (taskstore.Result, error) {
	return taskstore.Result{}, nil
}
func (c *testTaskController) State(context.Context, taskstore.StateCommand) (taskstore.Result, error) {
	return taskstore.Result{}, nil
}
func (c *testTaskController) Start(context.Context, taskservice.StartRequest) (taskservice.StartResult, error) {
	return taskservice.StartResult{}, nil
}

func TestTaskAPIsRequireAuthenticationAndDeriveActor(t *testing.T) {
	controller := &testTaskController{}
	handler := testTaskAPIHandler(t, controller)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthorized tasks = %d headers=%v", unauthorized.Code, unauthorized.Header())
	}

	body := `{"title":"Native","description":"Private","projectId":"project-factory","approvalMode":"gated"}`
	crossOrigin := authenticatedJSONRequest(t, handler, http.MethodPost, "/api/tasks", json.RawMessage(body), "https://attacker.example")
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin create = %d %s", crossOrigin.Code, crossOrigin.Body.String())
	}

	created := authenticatedJSONRequest(t, handler, http.MethodPost, "/api/tasks", json.RawMessage(body), "")
	if created.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency create = %d %s", created.Code, created.Body.String())
	}
	requestWithKey := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	requestWithKey.AddCookie(viewerSessionCookie(t, handler))
	requestWithKey.Header.Set("Content-Type", "application/json")
	requestWithKey.Header.Set("Idempotency-Key", "create-native")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, requestWithKey)
	if recorder.Code != http.StatusCreated || controller.create.Actor.Kind != taskstore.AuthorHuman || controller.create.Actor.ID != "google:google-subject:tom@example.com" {
		t.Fatalf("created task = %d actor=%#v body=%s", recorder.Code, controller.create.Actor, recorder.Body.String())
	}

	readOnly := authenticatedJSONRequest(t, handler, http.MethodPost, "/api/tasks/linear/ENG-46/messages", map[string]any{}, "")
	if readOnly.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Linear mutation = %d %s", readOnly.Code, readOnly.Body.String())
	}
}

func testTaskAPIHandler(t *testing.T, controller TaskController) http.Handler {
	t.Helper()
	directory := t.TempDir()
	activityStore, err := activity.Open(filepath.Join(directory, "activity.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := agentrun.Open(filepath.Join(directory, "runs.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	githubEvents, err := githubhook.Open(filepath.Join(directory, "github.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	linearComments, err := linearhook.Open(filepath.Join(directory, "linear.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{
		Web: testWeb(), ActivityStore: activityStore, RunStore: runs, RunNotifier: &testNotifier{},
		AgentObserver: &testObserver{err: agentrun.ErrRunNotFound}, Settings: testSettingsStore(t),
		ViewerAuth: testViewerAuth(t), LinearSecret: testSecret, GitHubSecret: testGitHubSecret,
		Events: testEventWire(t, 0, 0), GitHubEvents: githubEvents, LinearComments: linearComments,
		ProjectSetups: &testProjectSetups{}, TriggerActor: testActorID, Tasks: controller,
		Now: func() time.Time { return testNow }, Build: testBuildIdentity(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
