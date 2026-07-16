package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/workflow"
)

type testTaskController struct {
	create taskservice.CreateRequest
}

type testLinearTaskController struct {
	issue       taskservice.LinearIssue
	reads       int
	operation   string
	idempotency string
}

func (c *testLinearTaskController) Detail(context.Context, string) (taskservice.LinearIssue, error) {
	c.reads++
	return c.issue, nil
}
func (c *testLinearTaskController) Comment(_ context.Context, _, _, _, operation, idempotency string) (taskservice.LinearIssue, error) {
	c.operation, c.idempotency = operation, idempotency
	return c.issue, nil
}
func (c *testLinearTaskController) Link(context.Context, string, string, string) (taskservice.LinearIssue, error) {
	return c.issue, nil
}
func (c *testLinearTaskController) State(context.Context, string, string) (taskservice.LinearIssue, error) {
	return c.issue, nil
}
func (c *testLinearTaskController) Gate(context.Context, string, string, string, string, string) (taskservice.LinearIssue, error) {
	return c.issue, nil
}

func (c *testTaskController) Projects() []taskservice.ProjectChoice { return nil }
func (c *testTaskController) Control() taskcontrol.Snapshot {
	return taskcontrol.Snapshot{Version: 1}
}
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
	projects := authenticatedJSONRequest(t, handler, http.MethodGet, "/api/task-projects", nil, "")
	if projects.Code != http.StatusOK || !strings.Contains(projects.Body.String(), `"control":{"version":1,"revision":0`) {
		t.Fatalf("task projects = %d %s", projects.Code, projects.Body.String())
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

func TestManagedLinearDetailLoadsLiveWithoutAdmittingWorkspaceBacklog(t *testing.T) {
	linear := &testLinearTaskController{issue: taskservice.LinearIssue{
		Ref:   taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-46", Identifier: "ENG-46"},
		Title: "Live Linear title", Description: "Private live body", ProjectName: "Factory",
		State: "in_progress", StateName: "In Progress", UpdatedAt: testNow, ExternalURL: "https://linear.app/nags/issue/ENG-46/live",
	}}
	handler, runs := testTaskAPIHandlerWithLinear(t, &testTaskController{}, linear)
	if _, _, err := runs.Claim(agentrun.Trigger{DeliveryID: "linear-managed", IssueIdentifier: "ENG-46", Kind: agentrun.TriggerKindLabel}, testNow); err != nil {
		t.Fatal(err)
	}
	active := authenticatedJSONRequest(t, handler, http.MethodGet, "/api/tasks?provider=linear&activity=active", nil, "")
	if active.Code != http.StatusOK || !strings.Contains(active.Body.String(), "ENG-46") || strings.Contains(active.Body.String(), "nextCursor") {
		t.Fatalf("active Linear index = %d %s", active.Code, active.Body.String())
	}
	inactive := authenticatedJSONRequest(t, handler, http.MethodGet, "/api/tasks?provider=linear&activity=inactive", nil, "")
	if inactive.Code != http.StatusOK || strings.Contains(inactive.Body.String(), "ENG-46") {
		t.Fatalf("inactive Linear index = %d %s", inactive.Code, inactive.Body.String())
	}
	invalid := authenticatedJSONRequest(t, handler, http.MethodGet, "/api/tasks?activity=unknown", nil, "")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid activity filter = %d %s", invalid.Code, invalid.Body.String())
	}
	managed := authenticatedJSONRequest(t, handler, http.MethodGet, "/api/tasks/linear/ENG-46", nil, "")
	if managed.Code != http.StatusOK || !strings.Contains(managed.Body.String(), "Private live body") || !strings.Contains(managed.Body.String(), "latestRun") || linear.reads != 1 {
		t.Fatalf("managed detail = %d %s reads=%d", managed.Code, managed.Body.String(), linear.reads)
	}
	unmanaged := authenticatedJSONRequest(t, handler, http.MethodGet, "/api/tasks/linear/ENG-999", nil, "")
	if unmanaged.Code != http.StatusNotFound || linear.reads != 1 {
		t.Fatalf("unmanaged detail = %d %s reads=%d", unmanaged.Code, unmanaged.Body.String(), linear.reads)
	}
}

func TestLinearAgentTaskRequiresExactActiveRunCapability(t *testing.T) {
	linear := &testLinearTaskController{issue: taskservice.LinearIssue{
		Ref:   taskmodel.TaskRef{Source: taskmodel.SourceLinear, ProviderID: "ENG-46", Identifier: "ENG-46"},
		Title: "Scoped Linear task", State: "in_progress", Revision: 9,
	}}
	handler, runs := testTaskAPIHandlerWithLinear(t, &testTaskController{}, linear)
	pinned := workflow.Pin(workflow.ProviderNeutralDefault(testNow))
	digest, err := pinned.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	run, _, err := runs.EnsureInvocationRun(agentrun.InvocationClaim{
		RunID: "run-0123456789abcdef", InvocationID: "invocation-linear", EventID: "event-linear",
		Task: linear.issue.Ref, RootEventID: "root-linear", Hop: 1, AncestorRuleIDs: []string{"rule-linear"},
		Workflow: pinned, WorkflowDigest: digest, PolicyRevision: 1,
		Repository: agentrun.RepositoryConfig{
			App: "factory", Repository: "tomnagengast/factory", RepoURL: "https://github.com/tomnagengast/factory.git",
			RepoPath: filepath.Join(root, "factory"), ManagedRoot: root, BaseBranch: "main",
		},
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	runDirectory := t.TempDir()
	if err := runs.MarkStarting(run.ID, "factory-linear-eng-46", runDirectory, testNow); err != nil {
		t.Fatal(err)
	}
	if err := runs.MarkRunning(run.ID, 1, testNow); err != nil {
		t.Fatal(err)
	}
	token, err := agentrun.WriteTaskCapability(runDirectory, run, strings.NewReader(strings.Repeat("a", 32)), testNow)
	if err != nil {
		t.Fatal(err)
	}
	request := func(authorization string) *httptest.ResponseRecorder {
		body := `{"operation":"comment","body":"Scoped reply","idempotencyKey":"comment-1"}`
		httpRequest := httptest.NewRequest(http.MethodPost, "/api/agent/task", strings.NewReader(body))
		httpRequest.RemoteAddr = "127.0.0.1:45712"
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("X-Factory-Run-ID", run.ID)
		httpRequest.Header.Set("Authorization", "Bearer "+authorization)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httpRequest)
		return recorder
	}
	if recorder := request("wrong-token"); recorder.Code != http.StatusUnauthorized || linear.reads != 0 {
		t.Fatalf("wrong capability = %d reads=%d", recorder.Code, linear.reads)
	}
	if recorder := request(token); recorder.Code != http.StatusOK || linear.operation != "comment" || linear.idempotency != "helper:"+run.ID+":comment-1" {
		t.Fatalf("scoped helper = %d body=%s operation=%q idempotency=%q", recorder.Code, recorder.Body.String(), linear.operation, linear.idempotency)
	}
	if _, err := os.Stat(filepath.Join(runDirectory, agentrun.TaskCapabilityTokenFileName)); err != nil {
		t.Fatalf("capability token file: %v", err)
	}
}

func testTaskAPIHandler(t *testing.T, controller TaskController) http.Handler {
	handler, _ := testTaskAPIHandlerWithLinear(t, controller, nil)
	return handler
}

func testTaskAPIHandlerWithLinear(t *testing.T, controller TaskController, linear LinearTaskController) (http.Handler, *agentrun.Store) {
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
		LinearTasks: linear,
		Now:         func() time.Time { return testNow }, Build: testBuildIdentity(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, runs
}
