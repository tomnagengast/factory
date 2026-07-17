package migration

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/triggerscheduler"
	"github.com/tomnagengast/factory/internal/workflow"
)

var fixtureNow = time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	if os.Getenv("UPDATE_MIGRATION_GOLDEN") == "1" {
		root := filepath.Join("testdata", "current-shape")
		if err := generateGolden(root); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

func TestDryRunCharacterizesCurrentShapeWithoutActivation(t *testing.T) {
	t.Parallel()
	root := copyGolden(t)
	before, err := hashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := DryRun(root, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	after, err := hashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesEqual(before, after) {
		t.Fatal("dry run changed source artifacts")
	}
	if report.Activates || report.Manifest.Schema != 1 || report.Backup.Schema != 1 {
		t.Fatalf("activating or invalid report: %#v", report)
	}
	if report.Audit.Decisions != 1 || report.Audit.Invocations != 1 || report.Audit.Runs != 1 || report.Audit.ActiveRuns != 1 || report.Audit.WorkflowPins != 1 {
		t.Fatalf("routing/Run audit = %#v", report.Audit)
	}
	if report.Audit.LinearBindings != 1 || report.Audit.ActivityLifetime != 2 || report.Audit.ActivityRetained != 2 || report.Audit.PrivatePayloads != 1 {
		t.Fatalf("identity/activity audit = %#v", report.Audit)
	}
	if report.Audit.NativeTasks != 1 || report.Audit.NativeOutcomes == 0 || report.Audit.WorkflowDrafts != 1 || report.Audit.ScheduleCursors != 1 || report.Audit.AgentEventCursors != 1 {
		t.Fatalf("task/cursor audit = %#v", report.Audit)
	}
	if err := VerifyDryRun(root, testOptions(), report); err != nil {
		t.Fatalf("verify report: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "state-generation.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run created a generation selector: %v", err)
	}
}

func TestDryRunFailureInjection(t *testing.T) {
	for _, point := range []string{"before-hash", "hash:settings.json", "after-hash", "before-decode", "read:settings.json", "after-decode", "before-audit", "after-audit", "report", "after-report"} {
		point := point
		t.Run(point, func(t *testing.T) {
			root := copyGolden(t)
			options := testOptions()
			options.Inject = func(current string) error {
				if current == point {
					return errors.New("planned")
				}
				return nil
			}
			if _, err := DryRun(root, options); err == nil || !strings.Contains(err.Error(), point) {
				t.Fatalf("injected %s error = %v", point, err)
			}
		})
	}
}

func TestDryRunRejectsAmbiguousCrossArtifactState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{name: "unknown schema", mutate: func(t *testing.T, root string) {
			mutateJSON(t, root, "settings.json", func(value map[string]any) { value["schema"] = 99.0 })
		}, want: "schema"},
		{name: "custom reserved workflow", mutate: customReservedWorkflow, want: "reserved workflow"},
		{name: "duplicate active task", mutate: duplicateActiveRun, want: "duplicate active task"},
		{name: "duplicate identifier", mutate: func(t *testing.T, root string) { duplicateBinding(t, root, true, false) }, want: "duplicate Linear identifier"},
		{name: "duplicate UUID", mutate: func(t *testing.T, root string) { duplicateBinding(t, root, false, true) }, want: "duplicate Linear UUID"},
		{name: "changed mapping", mutate: changedBinding, want: "changed or missing Linear mapping"},
		{name: "orphan invocation", mutate: orphanInvocation, want: "orphan"},
		{name: "missing Run", mutate: missingRun, want: "missing Run"},
		{name: "conflicting route", mutate: conflictingRoute, want: "conflicting repository route"},
		{name: "incomplete stage", mutate: incompleteStage, want: "incomplete native task stage"},
		{name: "missing payload", mutate: missingPayload, want: "missing private payload"},
		{name: "orphan payload", mutate: orphanPayload, want: "orphan private payload"},
		{name: "altered payload", mutate: alteredPayload, want: "not valid JSON"},
		{name: "pending wire", mutate: pendingWire, want: "pending wire records"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := copyGolden(t)
			test.mutate(t, root)
			if _, err := DryRun(root, testOptions()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDryRunRejectsUnsafeModePathAndSymlink(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		root := copyGolden(t)
		if err := os.Chmod(filepath.Join(root, "settings.json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := DryRun(root, testOptions()); err == nil || !strings.Contains(err.Error(), "not private") {
			t.Fatalf("mode error = %v", err)
		}
	})
	t.Run("relative root", func(t *testing.T) {
		if _, err := DryRun("testdata/current-shape", testOptions()); err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("relative root error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := copyGolden(t)
		path := filepath.Join(root, "settings.json")
		target := filepath.Join(root, "settings-target.json")
		data, _ := os.ReadFile(path)
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := DryRun(root, testOptions()); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink error = %v", err)
		}
	})
}

func TestDryRunDetectsAlteredSourceAndAuditEvidence(t *testing.T) {
	t.Run("source during audit", func(t *testing.T) {
		root := copyGolden(t)
		options := testOptions()
		options.Inject = func(point string) error {
			if point == "after-audit" {
				path := filepath.Join(root, "agent-event-offsets.json")
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				return os.WriteFile(path, append(data, ' '), 0o600)
			}
			return nil
		}
		if _, err := DryRun(root, options); err == nil || !strings.Contains(err.Error(), "source changed") {
			t.Fatalf("altered source error = %v", err)
		}
	})
	t.Run("audit report", func(t *testing.T) {
		root := copyGolden(t)
		report, err := DryRun(root, testOptions())
		if err != nil {
			t.Fatal(err)
		}
		report.Audit.Runs++
		if err := VerifyDryRun(root, testOptions(), report); err == nil || !strings.Contains(err.Error(), "audit evidence changed") {
			t.Fatalf("altered audit error = %v", err)
		}
	})
}

func testOptions() Options {
	return Options{TriggerActorID: "actor-sanitized", Now: fixtureNow}
}

func copyGolden(t *testing.T) string {
	t.Helper()
	source := filepath.Join("testdata", "current-shape")
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "task-operations"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func mutateJSON(t *testing.T, root, name string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(root, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	writeJSON(t, path, value)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func customReservedWorkflow(t *testing.T, root string) {
	mutateJSON(t, root, "settings.json", func(value map[string]any) {
		workflows := value["workflows"].([]any)
		workflowValue := workflows[0].(map[string]any)
		workflowValue["id"] = workflow.DefaultID
		value["protectedWorkflows"].(map[string]any)["linearFeedback"].(map[string]any)["workflowId"] = workflow.DefaultID
		value["triggers"].(map[string]any)["linearLabel"].(map[string]any)["workflowId"] = workflow.DefaultID
		value["triggers"].(map[string]any)["linearComment"].(map[string]any)["workflowId"] = workflow.DefaultID
	})
	mutateJSON(t, root, "triggers.json", func(value map[string]any) {
		value["rules"].([]any)[0].(map[string]any)["workflowId"] = workflow.DefaultID
	})
}

func duplicateActiveRun(t *testing.T, root string) {
	mutateJSON(t, root, "agent-runs.json", func(value map[string]any) {
		runs := value["runs"].([]any)
		data, _ := json.Marshal(runs[0])
		var duplicate map[string]any
		_ = json.Unmarshal(data, &duplicate)
		duplicate["id"] = "run-fedcba9876543210"
		delete(duplicate, "invocationId")
		delete(duplicate, "transitions")
		delete(duplicate, "ready")
		runs = append(runs, duplicate)
		value["runs"] = runs
		value["total"] = value["total"].(float64) + 1
	})
}

func duplicateBinding(t *testing.T, root string, sameIdentifier, sameUUID bool) {
	mutateJSON(t, root, "linear-task-identities.json", func(value map[string]any) {
		bindings := value["bindings"].([]any)
		binding := bindings[0].(map[string]any)
		identifier, uuid := "ENG-48", "22222222-2222-4222-8222-222222222222"
		if sameIdentifier {
			identifier = binding["identifier"].(string)
		}
		if sameUUID {
			uuid = binding["uuid"].(string)
		}
		value["bindings"] = append(bindings, map[string]any{"identifier": identifier, "uuid": uuid})
	})
}

func changedBinding(t *testing.T, root string) {
	mutateJSON(t, root, "linear-task-identities.json", func(value map[string]any) {
		value["bindings"].([]any)[0].(map[string]any)["identifier"] = "ENG-48"
	})
}

func orphanInvocation(t *testing.T, root string) {
	path := filepath.Join(root, "trigger-routing.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	var operation map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &operation); err != nil {
		t.Fatal(err)
	}
	operation["decisions"].([]any)[0].(map[string]any)["outcomes"] = []any{}
	encoded, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	lines[1] = string(encoded)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func missingRun(t *testing.T, root string) {
	mutateJSON(t, root, "agent-runs.json", func(value map[string]any) { value["runs"] = []any{} })
}

func conflictingRoute(t *testing.T, root string) {
	mutateJSON(t, root, "agent-runs.json", func(value map[string]any) {
		value["runs"].([]any)[0].(map[string]any)["repositoryPath"] = "/srv/other/factory"
	})
}

func incompleteStage(t *testing.T, root string) {
	data, err := os.ReadFile(filepath.Join("testdata", "cases", "pending-stage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "task-operations", "op-0123456789abcdef.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func payloadPath(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "linear-activity-payloads"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("payload entries=%v err=%v", entries, err)
	}
	return filepath.Join(root, "linear-activity-payloads", entries[0].Name())
}

func missingPayload(t *testing.T, root string) { _ = os.Remove(payloadPath(t, root)) }

func orphanPayload(t *testing.T, root string) {
	if err := os.WriteFile(filepath.Join(root, "linear-activity-payloads", strings.Repeat("a", 64)+".json"), []byte(`{"sanitized":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func alteredPayload(t *testing.T, root string) {
	if err := os.WriteFile(payloadPath(t, root), []byte(`{"broken"`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pendingWire(t *testing.T, root string) {
	path := filepath.Join(root, "system-events.jsonl")
	journal, err := eventwire.Open(path, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = journal.Add(eventwire.Event{ID: "factory:pending", Source: eventwire.SourceFactory, Type: "task-mutation", Action: "create", ReceivedAt: fixtureNow.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
}

func slicesEqual(left, right []SourceHash) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func generateGolden(root string) error {
	if filepath.Clean(root) != filepath.Join("testdata", "current-shape") {
		return errors.New("refusing to replace an unexpected golden fixture root")
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	definition := workflow.Definition{ID: "custom-review", Revision: 3, Name: "Custom review", Enabled: true, Markdown: "# Custom review\n\nSanitized fixture workflow.\n", UpdatedAt: fixtureNow}
	digest, err := workflow.Digest(definition)
	if err != nil {
		return err
	}
	configuration := settings.Defaults(3)
	configuration.Revision = 7
	configuration.UpdatedAt = fixtureNow
	configuration.Triggers.LinearLabel.WorkflowID = definition.ID
	configuration.Triggers.LinearComment.WorkflowID = definition.ID
	configuration.ProtectedWorkflows.LinearFeedback.WorkflowID = definition.ID
	configuration.Workflows = []workflow.Definition{definition}
	if err := writeGoldenJSON(filepath.Join(root, "settings.json"), configuration); err != nil {
		return err
	}
	registry := triggerregistry.Snapshot{
		Schema: triggerregistry.SchemaVersion, Revision: 4, UpdatedAt: fixtureNow,
		Rules: []triggerregistry.Rule{{
			ID: "custom-visible", Revision: 2, Name: "Visible custom rule", Enabled: true,
			Filter:     triggerregistry.Filter{Source: eventwire.SourceLinear, Type: "Issue", Action: "update", Attributes: map[string]string{triggerregistry.AttributeActorID: "actor-sanitized"}},
			WorkflowID: definition.ID, Target: triggerregistry.TargetPolicy{Provider: taskmodel.SourceLinear, Kind: triggerregistry.TargetEventSubject},
			MaxHop: 4, MaxOutstanding: 10, AdmissionsHour: 120,
		}},
		Schedules: []triggerregistry.Schedule{{ID: "daily-audit", Revision: 2, Name: "Daily audit", Enabled: true, Cron: "0 8 * * *", Timezone: "UTC"}},
	}
	if err := writeGoldenJSON(filepath.Join(root, "triggers.json"), registry); err != nil {
		return err
	}

	wireJournal, err := eventwire.Open(filepath.Join(root, "system-events.jsonl"), 100, nil)
	if err != nil {
		return err
	}
	wireEvent := eventwire.Event{ID: "linear:delivery-sanitized", Source: eventwire.SourceLinear, Type: "Issue", Action: "update", Subject: "ENG-47", Attributes: map[string][]string{triggerregistry.AttributeActorID: {"actor-sanitized"}}, Channels: []string{"linear"}, ReceivedAt: fixtureNow}
	record, _, err := wireJournal.Add(wireEvent)
	if err != nil {
		return err
	}
	routing, err := triggerrouter.Open(filepath.Join(root, "trigger-routing.jsonl"))
	if err != nil {
		return err
	}
	decisions, err := routing.ApplyDecisionBatch([]eventwire.Record{record}, registry, configuration, fixtureNow)
	if err != nil {
		return err
	}
	invocationID := decisions[0].Outcomes[0].InvocationID
	invocation, _ := routing.Invocation(invocationID)
	runID := "run-0123456789abcdef"
	if _, err := routing.TransitionInvocation(invocationID, triggerrouter.StateClaiming, runID, "", nil, fixtureNow.Add(time.Second)); err != nil {
		return err
	}
	projectStore, err := projectsetup.Open(filepath.Join(root, "project-setups.json"), fixtureNow)
	if err != nil {
		return err
	}
	spec := projectsetup.Spec{ProjectID: "project-factory", ProjectName: "Factory", Repository: "tomnagengast/factory", RepoURL: "git@github.com:tomnagengast/factory.git", LocalPath: "/srv/factory/repos/factory", ManagedRoot: "/srv/factory/repos", BaseBranch: "main"}
	if _, err := projectStore.Upsert(spec, fixtureNow); err != nil {
		return err
	}
	runStore, err := agentrun.Open(filepath.Join(root, "agent-runs.json"), 100)
	if err != nil {
		return err
	}
	run, _, err := runStore.EnsureInvocationRun(agentrun.InvocationClaim{
		RunID: runID, InvocationID: invocation.ID, EventID: invocation.EventID, Task: invocation.Task, IssueIdentifier: invocation.IssueIdentifier,
		RootEventID: invocation.RootEventID, Hop: invocation.Hop, AncestorRuleIDs: invocation.AncestorRuleIDs,
		Workflow: invocation.Workflow, WorkflowDigest: invocation.WorkflowDigest, PolicyRevision: invocation.PolicyRevision,
		Repository: agentrun.RepositoryConfig{App: "factory", Repository: spec.Repository, RepoURL: spec.RepoURL, RepoPath: spec.LocalPath, ManagedRoot: spec.ManagedRoot, BaseBranch: spec.BaseBranch},
	}, fixtureNow.Add(2*time.Second))
	if err != nil {
		return err
	}
	if _, err := routing.TransitionInvocation(invocationID, triggerrouter.StateClaimed, runID, "", nil, fixtureNow.Add(3*time.Second)); err != nil {
		return err
	}
	if err := runStore.MarkStarting(run.ID, "factory-eng-47", "/srv/factory/runs/"+run.ID, fixtureNow.Add(4*time.Second)); err != nil {
		return err
	}
	if err := runStore.MarkRunning(run.ID, 1, fixtureNow.Add(5*time.Second)); err != nil {
		return err
	}
	ready := agentrun.ReadyCheckpoint{ContractVersion: 1, RunID: run.ID, Task: run.Task, Repository: spec.Repository, PullRequest: 18, BaseBranch: "main", HeadBranch: "eng-47-sanitized", VerifiedHeadOID: strings.Repeat("a", 40), CreatedAt: fixtureNow.Add(6 * time.Second)}
	if err := runStore.MarkAwaitingMerge(run.ID, ready, fixtureNow.Add(time.Hour), 1, fixtureNow.Add(7*time.Second)); err != nil {
		return err
	}

	tasks, err := taskstore.Open(filepath.Join(root, "native-tasks.jsonl"))
	if err != nil {
		return err
	}
	task, _, err := tasks.Create(taskstore.CreateCommand{Actor: taskstore.Actor{ID: "operator-sanitized", Kind: taskstore.AuthorHuman}, Title: "Sanitized native task", ProjectID: spec.ProjectID, ApprovalMode: taskstore.ApprovalGated, IdempotencyKey: "fixture-create"}, fixtureNow)
	if err != nil {
		return err
	}
	_, _, err = tasks.SetRouting(taskstore.RoutingCommand{Actor: taskstore.Actor{ID: run.ID, Kind: taskstore.AuthorAgent}, TaskID: task.Ref.ProviderID, ExpectedRevision: task.Revision, Routing: taskstore.RoutingSnapshot{ProjectID: spec.ProjectID, Repository: spec.Repository, RepositoryURL: spec.RepoURL, RepositoryPath: spec.LocalPath, ManagedRoot: spec.ManagedRoot, BaseBranch: spec.BaseBranch, WorkflowID: definition.ID, WorkflowDigest: digest, AdmittedAt: fixtureNow}, IdempotencyKey: "fixture-route"}, fixtureNow)
	if err != nil {
		return err
	}
	if err := tasks.Compact(); err != nil {
		return err
	}
	taskJournalPath := filepath.Join(root, "native-tasks.jsonl")
	taskJournal, err := os.ReadFile(taskJournalPath)
	if err != nil {
		return err
	}
	taskJournal = []byte(strings.ReplaceAll(string(taskJournal), task.Ref.ProviderID, "task-0123456789abcdef"))
	if err := os.WriteFile(taskJournalPath, taskJournal, 0o600); err != nil {
		return err
	}
	control, err := taskcontrol.Open(filepath.Join(root, "native-task-control.json"))
	if err != nil {
		return err
	}
	if _, err := control.SetProject(0, spec.ProjectID, true, fixtureNow); err != nil {
		return err
	}
	if err := writeGoldenJSON(filepath.Join(root, "linear-task-identities.json"), identityState{Version: 1, Bindings: []linearIdentityBinding{{Identifier: "ENG-47", UUID: "11111111-1111-4111-8111-111111111111"}}}); err != nil {
		return err
	}
	activityStore, err := activity.Open(filepath.Join(root, "linear-activity.json"), 100)
	if err != nil {
		return err
	}
	if _, err := activityStore.Add("delivery-historical", activity.Event{Type: "Issue", Action: "create", ReceivedAt: fixtureNow.Add(-time.Hour)}); err != nil {
		return err
	}
	if _, err := activityStore.AddWithPayload("delivery-sanitized", activity.Event{Type: "Issue", Action: "update", ReceivedAt: fixtureNow}, []byte(`{"type":"Issue","action":"update","data":{"identifier":"ENG-47","title":"Sanitized"}}`)); err != nil {
		return err
	}
	drafts, err := workflow.OpenDraftStore(filepath.Join(root, "workflow-drafts.json"))
	if err != nil {
		return err
	}
	if _, err := drafts.Create(workflow.Draft{WorkflowID: "draft-review", Revision: 1, Name: "Draft review", Enabled: true, Markdown: "# Draft review\n\nSanitized draft.\n", UpdatedAt: fixtureNow}); err != nil {
		return err
	}
	cursors, err := triggerscheduler.Open(filepath.Join(root, "trigger-cursors.json"))
	if err != nil {
		return err
	}
	if err := cursors.Advance(triggerscheduler.Cursor{ScheduleID: "daily-audit", ScheduleRevision: 2, LastScheduledAt: fixtureNow, Skipped: 1}); err != nil {
		return err
	}
	if err := writeGoldenJSON(filepath.Join(root, "agent-event-offsets.json"), agentCursorState{Version: 1, Offsets: map[string]int64{run.ID + "/attempt-1-events.jsonl": 128}, Prefixes: map[string]string{run.ID + "/attempt-1-events.jsonl": strings.Repeat("b", 64)}}); err != nil {
		return err
	}
	if err := writeGoldenJSON(filepath.Join(root, "github-events.json"), githubState{Version: 1, Events: []githubRecord{}}); err != nil {
		return err
	}
	if err := writeGoldenJSON(filepath.Join(root, "linear-comments.json"), linearState{Version: 1, Events: []linearRecord{}}); err != nil {
		return err
	}
	if err := wireJournal.Acknowledge(record.Sequence, map[string]uint64{"linear": 1}); err != nil {
		return err
	}
	marker := map[string]any{"version": 1, "boundary": "source-neutral-task-v1", "crossedAt": fixtureNow}
	if err := writeGoldenJSON(filepath.Join(root, "task-source-neutral.json"), marker); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join("testdata", "cases"), 0o700); err != nil {
		return err
	}
	return writeGoldenJSON(filepath.Join("testdata", "cases", "pending-stage.json"), map[string]any{"operationId": "op-0123456789abcdef", "kind": "create", "sanitized": true})
}

func writeGoldenJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
