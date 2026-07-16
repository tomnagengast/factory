package server

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/linearidentity"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskservice"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/viewerauth"
	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	browserFixtureProjectID = "project-factory"
	browserFixtureLinearID  = "ENG-47"
)

type browserFixtureTasks struct {
	store *taskstore.Store
	now   func() time.Time
}

func (c *browserFixtureTasks) Projects() []taskservice.ProjectChoice {
	return []taskservice.ProjectChoice{{
		Choice: projectsetup.Choice{
			ProjectID:   browserFixtureProjectID,
			ProjectName: "Factory",
			Repository:  "tomnagengast/factory",
		},
		Enabled: true,
	}}
}

func (c *browserFixtureTasks) Control() taskcontrol.Snapshot {
	return taskcontrol.Snapshot{
		Version:           1,
		Revision:          1,
		EnabledProjectIDs: []string{browserFixtureProjectID},
		UpdatedAt:         c.now(),
	}
}

func (c *browserFixtureTasks) SetProject(uint64, string, bool) (taskcontrol.Snapshot, error) {
	return c.Control(), nil
}

func (c *browserFixtureTasks) List(cursor string, limit int) (taskstore.TaskPage, error) {
	return c.store.List(cursor, limit)
}

func (c *browserFixtureTasks) Detail(taskID string, after uint64, limit int) (taskservice.Detail, error) {
	task, found := c.store.Find(taskID)
	if !found {
		task, found = c.store.FindIdentifier(taskID)
	}
	if !found {
		return taskservice.Detail{}, taskstore.ErrNotFound
	}
	messages, err := c.store.Messages(task.Ref.ProviderID, after, limit)
	if err != nil {
		return taskservice.Detail{}, err
	}
	links, err := c.store.Links(task.Ref.ProviderID)
	if err != nil {
		return taskservice.Detail{}, err
	}
	gates, err := c.store.Gates(task.Ref.ProviderID)
	if err != nil {
		return taskservice.Detail{}, err
	}
	return taskservice.Detail{Task: task, Messages: messages, Links: links, Gates: gates}, nil
}

func (c *browserFixtureTasks) Create(_ context.Context, request taskservice.CreateRequest) (taskstore.Result, error) {
	task, replayed, err := c.store.Create(taskstore.CreateCommand{
		Actor: request.Actor, Title: request.Title, Description: request.Description,
		ProjectID: request.ProjectID, ApprovalMode: request.ApprovalMode,
		IdempotencyKey: request.IdempotencyKey,
	}, c.now())
	return taskstore.Result{Task: task, Replayed: replayed}, err
}

func (c *browserFixtureTasks) Update(_ context.Context, command taskstore.UpdateCommand) (taskstore.Result, error) {
	task, replayed, err := c.store.Update(command, c.now())
	return taskstore.Result{Task: task, Replayed: replayed}, err
}

func (c *browserFixtureTasks) Message(_ context.Context, command taskstore.MessageCommand) (taskstore.Result, error) {
	task, message, replayed, err := c.store.AddMessage(command, c.now())
	return taskstore.Result{Task: task, Message: &message, Replayed: replayed}, err
}

func (c *browserFixtureTasks) Link(_ context.Context, command taskstore.LinkCommand) (taskstore.Result, error) {
	task, link, replayed, err := c.store.AddLink(command, c.now())
	return taskstore.Result{Task: task, Link: &link, Replayed: replayed}, err
}

func (c *browserFixtureTasks) Gate(_ context.Context, command taskstore.GateCommand) (taskstore.Result, error) {
	task, gate, replayed, err := c.store.OpenGate(command, c.now())
	return taskstore.Result{Task: task, Gate: &gate, Replayed: replayed}, err
}

func (c *browserFixtureTasks) Decide(_ context.Context, command taskstore.DecisionCommand) (taskstore.Result, error) {
	task, gate, replayed, err := c.store.DecideGate(command, c.now())
	return taskstore.Result{Task: task, Gate: &gate, Replayed: replayed}, err
}

func (c *browserFixtureTasks) State(_ context.Context, command taskstore.StateCommand) (taskstore.Result, error) {
	task, replayed, err := c.store.ChangeState(command, c.now())
	return taskstore.Result{Task: task, Replayed: replayed}, err
}

func (c *browserFixtureTasks) Start(_ context.Context, request taskservice.StartRequest) (taskservice.StartResult, error) {
	task, found := c.store.Find(request.TaskID)
	if !found {
		return taskservice.StartResult{}, taskstore.ErrNotFound
	}
	return taskservice.StartResult{Task: task, Admitted: true}, nil
}

func (c *browserFixtureTasks) advance(taskID string) (taskstore.Task, error) {
	current, found := c.store.Find(taskID)
	if !found {
		return taskstore.Task{}, taskstore.ErrNotFound
	}
	task, _, err := c.store.Update(taskstore.UpdateCommand{
		Actor:  taskstore.Actor{ID: "fixture", Kind: taskstore.AuthorSystem},
		TaskID: current.Ref.ProviderID, ExpectedRevision: current.Revision,
		Title: current.Title, Description: current.Description, ApprovalMode: current.ApprovalMode,
		IdempotencyKey: fmt.Sprintf("fixture-advance-%d", current.Revision),
	}, c.now())
	return task, err
}

func TestCandidateBrowserFixture(t *testing.T) {
	if os.Getenv("FACTORY_BROWSER_FIXTURE") != "1" {
		t.Skip("candidate browser fixture is explicitly gated")
	}

	address := os.Getenv("FACTORY_BROWSER_FIXTURE_ADDR")
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parse fixture address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse fixture port: %v", err)
	}
	viewer, err := viewerauth.NewLocal(host, port)
	if err != nil {
		t.Fatalf("create local viewer auth: %v", err)
	}

	root := filepath.Clean(os.Getenv("FACTORY_BROWSER_FIXTURE_ROOT"))
	if root == "." || !filepath.IsAbs(root) {
		t.Fatal("FACTORY_BROWSER_FIXTURE_ROOT must be an absolute disposable path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create fixture root: %v", err)
	}

	webRoot, err := filepath.Abs(filepath.Join("..", "..", "frontend", "dist"))
	if err != nil {
		t.Fatalf("resolve candidate frontend: %v", err)
	}
	web := os.DirFS(webRoot)
	if _, err := fs.Stat(web, "index.html"); err != nil {
		t.Fatalf("candidate frontend is not built: %v", err)
	}

	now := func() time.Time { return time.Now().UTC() }
	activityStore, err := activity.Open(filepath.Join(root, "activity.json"), 100)
	if err != nil {
		t.Fatalf("open activity store: %v", err)
	}
	runs, err := agentrun.Open(filepath.Join(root, "runs.json"), 100)
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	configuration, err := settings.Open(filepath.Join(root, "settings.json"), settings.Defaults(3))
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	drafts, err := workflow.OpenDraftStore(filepath.Join(root, "workflow-drafts.json"))
	if err != nil {
		t.Fatalf("open workflow drafts: %v", err)
	}
	registry, err := triggerregistry.Open(
		filepath.Join(root, "triggers.json"),
		triggerregistry.Defaults(configuration.Snapshot(), testActorID),
		configuration.Snapshot(),
	)
	if err != nil {
		t.Fatalf("open trigger registry: %v", err)
	}
	routing, err := triggerrouter.Open(filepath.Join(root, "routing.jsonl"))
	if err != nil {
		t.Fatalf("open trigger routing: %v", err)
	}
	githubEvents, err := githubhook.Open(filepath.Join(root, "github.json"), 100)
	if err != nil {
		t.Fatalf("open GitHub events: %v", err)
	}
	linearComments, err := linearhook.Open(filepath.Join(root, "linear.json"), 100)
	if err != nil {
		t.Fatalf("open Linear comments: %v", err)
	}
	journal, err := eventwire.Open(filepath.Join(root, "wire.jsonl"), 100, map[string]uint64{
		githubhook.WireChannel: 0,
		linearhook.WireChannel: 0,
	})
	if err != nil {
		t.Fatalf("open event journal: %v", err)
	}
	rawWire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("create event wire: %v", err)
	}
	policy, err := triggerrouter.NewCoordinatedWire(rawWire, registry, configuration, routing, now)
	if err != nil {
		t.Fatalf("create trigger policy: %v", err)
	}
	identities, err := linearidentity.Open(filepath.Join(root, "linear-task-identities.json"))
	if err != nil {
		t.Fatalf("open Linear identities: %v", err)
	}
	nativeStore, err := taskstore.Open(filepath.Join(root, "native-tasks.jsonl"))
	if err != nil {
		t.Fatalf("open native task store: %v", err)
	}
	tasks := &browserFixtureTasks{store: nativeStore, now: now}
	seed, err := tasks.Create(context.Background(), taskservice.CreateRequest{
		Actor: taskstore.Actor{ID: "local-operator", Kind: taskstore.AuthorHuman},
		Title: "Candidate browser verification", Description: "Disposable native task state for ENG-47 browser verification.",
		ProjectID: browserFixtureProjectID, ApprovalMode: taskstore.ApprovalGated,
		IdempotencyKey: "fixture-seed-native",
	})
	if err != nil {
		t.Fatalf("seed native task: %v", err)
	}

	run, _, err := runs.Claim(agentrun.Trigger{
		DeliveryID: "fixture-linear-run", IssueIdentifier: browserFixtureLinearID,
		Kind: agentrun.TriggerKindLabel,
	}, now())
	if err != nil {
		t.Fatalf("seed managed Linear run: %v", err)
	}
	runDirectory := filepath.Join(root, "run")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatalf("create fixture run directory: %v", err)
	}
	if err := runs.MarkStarting(run.ID, "fixture-agent", runDirectory, now()); err != nil {
		t.Fatalf("mark fixture run starting: %v", err)
	}
	if err := runs.MarkRunning(run.ID, 1, now()); err != nil {
		t.Fatalf("mark fixture run running: %v", err)
	}
	run, _ = runs.Find(run.ID)
	observer := &testObserver{view: agentrun.AgentView{
		ID: run.ID, Task: run.Task, IssueIdentifier: run.IssueIdentifier, State: run.State,
		Attempts: run.Attempts, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		StartedAt: run.StartedAt, ObservedAt: now(), Live: true, Windows: []agentrun.WindowView{},
	}}
	linear := &testLinearTaskController{issue: taskservice.LinearIssue{
		Ref: taskmodel.TaskRef{
			Source: taskmodel.SourceLinear, ProviderID: browserFixtureLinearID,
			Identifier: browserFixtureLinearID,
		},
		Title: "Simplify simplify simplify", Description: "Read-only managed Linear detail from disposable fixture state.",
		ProjectID: "factory", ProjectName: "Factory", State: "in_progress", StateName: "In Progress",
		UpdatedAt: now(), Revision: 1, ExternalURL: "https://linear.app/nags-cloud/issue/ENG-47/simplify-simplify-simplify",
		Messages: []taskservice.LinearMessage{{
			ID: "fixture-linear-message", Ordinal: 1, Body: "Fixture discussion remains read-only.",
			Author:    taskservice.LinearActor{ID: "fixture-human", Name: "Fixture operator", Kind: "human"},
			CreatedAt: now(),
		}},
	}}

	handler, err := New(Config{
		Web: web, ActivityStore: activityStore, RunStore: runs, RunNotifier: &testNotifier{},
		AgentObserver: observer, Settings: configuration, WorkflowDrafts: drafts,
		ViewerAuth: viewer, LinearSecret: testSecret, GitHubSecret: testGitHubSecret,
		Events: policy, GitHubEvents: githubEvents, LinearComments: linearComments,
		LinearIdentities: identities, ProjectSetups: &testProjectSetups{}, TriggerActor: testActorID,
		Now: now, Build: testBuildIdentity(), GenericTriggers: true,
		TriggerPolicy: policy, ScheduleStatus: scheduleStatusStub{},
		Tasks: tasks, LinearTasks: linear, TaskStatus: nativeStore.Status,
	})
	if err != nil {
		t.Fatalf("create candidate server: %v", err)
	}

	fixtureMux := http.NewServeMux()
	fixtureMux.Handle("POST /__fixture/advance", viewer.API(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		task, err := tasks.advance(seed.Task.Ref.ProviderID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, task)
	})))
	fixtureMux.Handle("/", handler)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listen on fixture address: %v", err)
	}
	server := &http.Server{Handler: fixtureMux, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() {
		_ = server.Close()
	})
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatalf("serve candidate browser fixture: %v", err)
	}
}
