package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/repositories"
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
	beforeDirectories, err := directoryModes(root)
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	compiledInput := slices.Clone(options.CompiledRepositories)
	report, err := DryRun(root, options)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(options.CompiledRepositories, compiledInput) {
		t.Fatal("dry run mutated compiled repository input")
	}
	after, err := hashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesEqual(before, after) {
		t.Fatal("dry run changed source artifacts")
	}
	afterDirectories, err := directoryModes(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(beforeDirectories, afterDirectories) {
		t.Fatal("dry run changed source directories")
	}
	if report.Schema != 3 || report.Activates || report.Manifest.Schema != 3 || report.Backup.Schema != 1 {
		t.Fatalf("activating or invalid report: %#v", report)
	}
	if report.Manifest.MigrationID != "migration-739285404f873b4621049f0e909b0432" {
		t.Fatalf("pre-audit migration ID = %s", report.Manifest.MigrationID)
	}
	if report.Manifest.TargetSchemas != (TargetSchemas{Policy: 1, Repositories: 1, Runs: 4}) || report.Audit.TargetSchemas != report.Manifest.TargetSchemas {
		t.Fatalf("target schemas = manifest %#v audit %#v", report.Manifest.TargetSchemas, report.Audit.TargetSchemas)
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
	policyAudit := report.Audit.CanonicalPolicy
	if policyAudit.Schema != 1 || policyAudit.Generation != 1 || !policyAudit.RegistrySourcePresent || !policyAudit.CompatibilityValidated ||
		policyAudit.SettingsRevision != 7 || policyAudit.RegistryRevision != 4 || policyAudit.TaskControlRevision != 1 ||
		policyAudit.Workflows != 2 || policyAudit.Rules != 1 || policyAudit.Schedules != 1 || policyAudit.EnabledProjects != 1 {
		t.Fatalf("canonical policy audit = %#v", policyAudit)
	}
	repositoryAudit := report.Audit.CanonicalRepositories
	if repositoryAudit.Schema != 1 || repositoryAudit.Generation != 1 || repositoryAudit.Compiled != 1 ||
		repositoryAudit.Admitted != 1 || repositoryAudit.Awaiting != 0 || repositoryAudit.Routable != 1 {
		t.Fatalf("canonical repository audit = %#v", repositoryAudit)
	}
	runsAudit := report.Audit.CanonicalRuns
	if runsAudit.Schema != 4 || runsAudit.SourceDecisions != 1 || runsAudit.SourceInvocations != 1 || runsAudit.SourceRunsRetained != 1 ||
		runsAudit.LinkedPairs != 1 || runsAudit.SynthesizedRuns != 0 || runsAudit.DirectRuns != 0 || runsAudit.TransitionReceipts != 4 ||
		runsAudit.CanonicalBatchesRetained != 1 || runsAudit.CanonicalBatchesLifetime != 1 || !runsAudit.BatchLifetimeMigrationBaseline ||
		runsAudit.CanonicalRunsRetained != 1 || runsAudit.CanonicalRunsLifetime != 1 || runsAudit.CanonicalRateBuckets != 1 {
		t.Fatalf("canonical Runs audit = %#v", runsAudit)
	}
	if report.Audit.CompiledRepositoryInputDigest != "520744fb78f49dc36b45cf4b8d38efeeb72049a7f775ab9e04177b29981ff8cf" ||
		policyAudit.Digest != "e3132827aa4041394ba294fd59d521263313f9e60120de968083dbdf86f97e20" ||
		repositoryAudit.Digest != "cf98d2b7b573d66a1b051dc9e81fc587262c7a10bd77abfdda648adf9b6c16eb" ||
		runsAudit.Digest != "efcc0a4205639b1a9e741b173db7b2873c7b76661a8bc7ce48544a55d46dee71" ||
		report.AuditDigest != "b0b7ff84842ab088fa7a996434babea87fe60d60502582e0c34e3ae6f694167d" {
		t.Fatalf("canonical digests = compiled %s policy %s repositories %s Runs %s audit %s", report.Audit.CompiledRepositoryInputDigest, policyAudit.Digest, repositoryAudit.Digest, runsAudit.Digest, report.AuditDigest)
	}
	auditJSON, err := json.Marshal(report.Audit)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/srv/factory", "git@github.com", "Sanitized fixture workflow"} {
		if strings.Contains(string(auditJSON), forbidden) {
			t.Fatalf("body-free canonical audit exposed %q: %s", forbidden, auditJSON)
		}
	}
	if err := VerifyDryRun(root, testOptions(), report); err != nil {
		t.Fatalf("verify report: %v", err)
	}
	assertNoCanonicalState(t, root)
}

func TestDryRunAcceptsImplicitRegistryAndAbsentScheduleCursors(t *testing.T) {
	root := copyGolden(t)
	if err := os.Remove(filepath.Join(root, "triggers.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "trigger-cursors.json")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "trigger-routing.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), `"registryRevision":4`, `"registryRevision":0`))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := DryRun(root, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if report.Audit.ScheduleCursors != 0 || report.Manifest.SourceSchemas["registry"] != 0 || report.Manifest.SourceSchemas["triggerCursors"] != 1 ||
		report.Audit.CanonicalPolicy.RegistrySourcePresent || report.Audit.CanonicalPolicy.RegistryRevision != 0 ||
		report.Audit.CanonicalPolicy.Rules != 1 || report.Audit.CanonicalPolicy.Schedules != 0 {
		t.Fatalf("implicit policy audit = %#v schemas=%#v", report.Audit, report.Manifest.SourceSchemas)
	}
}

func TestDryRunRejectsActorOnlyReservedRuleAmbiguity(t *testing.T) {
	root := copyGolden(t)
	var configuration settings.Snapshot
	if err := decodeFile(root, "settings.json", &configuration); err != nil {
		t.Fatal(err)
	}
	registry := triggerregistry.Defaults(configuration, "actor-from-stale-source")
	registry.Revision = 4
	registry.UpdatedAt = fixtureNow
	writeJSON(t, filepath.Join(root, "triggers.json"), registry)
	if _, err := DryRun(root, testOptions()); err == nil || !strings.Contains(err.Error(), "differs from the compiled default only by actor") {
		t.Fatalf("actor-only reserved rule error = %v", err)
	}
}

func TestDryRunAcceptsLegacyNativeSyntheticAdmissions(t *testing.T) {
	root := copyGolden(t)
	store, err := triggerrouter.Open(filepath.Join(root, "trigger-routing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	definition := workflow.Definition{ID: "custom-review", Revision: 3, Name: "Custom review", Enabled: true, Markdown: "# Custom review\n\nSanitized fixture workflow.\n", UpdatedAt: fixtureNow}
	pinned := workflow.Pin(definition)
	digest, err := pinned.Digest()
	if err != nil {
		t.Fatal(err)
	}
	admission := triggerrouter.NativeAdmission{
		Task:     taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"},
		Workflow: pinned, WorkflowDigest: digest, PolicyRevision: 7, RegistryRevision: 4, AdmittedAt: fixtureNow.Add(10 * time.Minute),
	}
	if _, created, err := store.AdmitNative(admission); err != nil || !created {
		t.Fatalf("native admission created=%t err=%v", created, err)
	}
	if _, created, err := store.AdmitNativeContinuation(admission, "message:msg-0123456789abcdef"); err != nil || !created {
		t.Fatalf("native continuation created=%t err=%v", created, err)
	}
	report, err := DryRun(root, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if report.Audit.Decisions != 3 || report.Audit.Invocations != 3 {
		t.Fatalf("native audit = %#v", report.Audit)
	}
}

func TestProviderJournalSequencesAreNewestFirst(t *testing.T) {
	for _, test := range []struct {
		name      string
		total     uint64
		sequences []uint64
		wantError bool
	}{
		{name: "complete", total: 4, sequences: []uint64{4, 3, 2, 1}},
		{name: "retained with gaps", total: 8, sequences: []uint64{8, 6, 2}},
		{name: "ascending", total: 4, sequences: []uint64{1, 2, 3, 4}, wantError: true},
		{name: "duplicate", total: 4, sequences: []uint64{4, 4}, wantError: true},
		{name: "past total", total: 4, sequences: []uint64{5}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateProviderJournal(1, test.total, test.sequences)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError=%t", err, test.wantError)
			}
		})
	}
}

func TestCompactedWorkflowPinsAreTerminalOnly(t *testing.T) {
	legacy := workflow.Pinned{ID: workflow.DefaultID}
	if err := auditPinned(legacy, "", false); err != nil {
		t.Fatalf("terminal legacy pin: %v", err)
	}
	if err := auditPinned(legacy, strings.Repeat("a", 64), true); err == nil {
		t.Fatal("active compacted pin passed")
	}
}

func TestHistoricalRunRouteDoesNotRequireCurrentAdmission(t *testing.T) {
	run := agentrun.Run{ID: "run-historical", Repository: "tomnagengast/retired"}
	if err := validateRunRoute(run, nil, false); err != nil {
		t.Fatalf("historical route: %v", err)
	}
	if err := validateRunRoute(run, nil, true); err == nil {
		t.Fatal("active unadmitted route passed")
	}
}

func TestHistoricalRunMayOutliveCompactedInvocation(t *testing.T) {
	run := agentrun.Run{ID: "run-historical", InvocationID: "invocation-pruned", State: agentrun.StateFailed}
	if err := validateRunInvocation(run, nil); err != nil {
		t.Fatalf("historical invocation: %v", err)
	}
	run.State = agentrun.StateRunning
	if err := validateRunInvocation(run, nil); err == nil {
		t.Fatal("active Run without invocation passed")
	}
}

func TestActivityAuditPreservesInsertionOrderWithoutChronologyAssumption(t *testing.T) {
	root := copyGolden(t)
	mutateJSON(t, root, "linear-activity.json", func(value map[string]any) {
		events := value["events"].([]any)
		events[0].(map[string]any)["receivedAt"] = "2026-07-16T16:00:00Z"
		events[1].(map[string]any)["receivedAt"] = "2026-07-16T17:00:00Z"
	})
	if _, err := DryRun(root, testOptions()); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunFailureInjection(t *testing.T) {
	for _, point := range []string{
		"before-hash", "hash:settings.json", "after-hash", "before-decode", "read:settings.json", "after-decode",
		"before-policy-conversion", "after-policy-conversion", "before-repository-conversion", "after-repository-conversion",
		"before-canonical-evidence", "after-canonical-evidence", "before-runs-conversion", "after-runs-conversion",
		"before-runs-evidence", "after-runs-evidence", "before-audit", "after-audit", "report", "after-report",
	} {
		point := point
		t.Run(point, func(t *testing.T) {
			root := copyGolden(t)
			before, err := hashTree(root)
			if err != nil {
				t.Fatal(err)
			}
			beforeDirectories, err := directoryModes(root)
			if err != nil {
				t.Fatal(err)
			}
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
			after, err := hashTree(root)
			if err != nil {
				t.Fatal(err)
			}
			afterDirectories, err := directoryModes(root)
			if err != nil {
				t.Fatal(err)
			}
			if !slicesEqual(before, after) || !slices.Equal(beforeDirectories, afterDirectories) {
				t.Fatal("injected dry run changed source tree")
			}
			assertNoCanonicalState(t, root)
		})
	}
}

func TestDryRunRejectsCanonicalRepositoryInputConflicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, *Options)
		want   string
	}{
		{name: "missing compiled input", mutate: func(_ *testing.T, _ string, options *Options) {
			options.CompiledRepositories = nil
		}, want: "no longer compiled"},
		{name: "compiled setup origin conflict", mutate: func(_ *testing.T, _ string, options *Options) {
			options.CompiledRepositories[0].Repository = "tomnagengast/other"
			options.CompiledRepositories[0].RepoURL = "git@github.com:tomnagengast/other.git"
		}, want: "no longer compiled"},
		{name: "compiled setup path conflict", mutate: func(_ *testing.T, _ string, options *Options) {
			options.CompiledRepositories[0].ProjectPath = "/srv/factory/repos/other"
		}, want: "conflicts with compiled"},
		{name: "compiled setup branch conflict", mutate: func(_ *testing.T, _ string, options *Options) {
			options.CompiledRepositories[0].BaseBranch = "release"
		}, want: "conflicts with compiled"},
		{name: "compiled repository origin conflict", mutate: func(_ *testing.T, _ string, options *Options) {
			options.CompiledRepositories[0].RepoURL = "git@github.com:tomnagengast/other.git"
		}, want: "compiled repository conflicts with origin"},
		{name: "duplicate app", mutate: func(_ *testing.T, _ string, options *Options) {
			other := options.CompiledRepositories[0]
			other.Repository, other.RepoURL = "tomnagengast/other", "git@github.com:tomnagengast/other.git"
			other.RepoPath, other.ProjectPath = "/srv/factory/repos/other", "/srv/factory/projects/other"
			options.CompiledRepositories = append(options.CompiledRepositories, other)
		}, want: "share app"},
		{name: "overlapping compiled paths", mutate: func(_ *testing.T, _ string, options *Options) {
			other := options.CompiledRepositories[0]
			other.App, other.Repository, other.RepoURL = "other", "tomnagengast/other", "git@github.com:tomnagengast/other.git"
			other.RepoPath, other.ProjectPath = options.CompiledRepositories[0].RepoPath+"/nested", "/srv/factory/projects/other"
			options.CompiledRepositories = append(options.CompiledRepositories, other)
		}, want: "managed paths"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := copyGolden(t)
			options := testOptions()
			test.mutate(t, root, &options)
			if _, err := DryRun(root, options); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRepositorySetupStateMappingFailsClosed(t *testing.T) {
	for _, state := range []projectsetup.State{
		projectsetup.StateAwaitingMetadata, projectsetup.StatePending, projectsetup.StateRunning,
		projectsetup.StateSucceeded, projectsetup.StateFailed,
	} {
		if _, err := repositorySetupState(state); err != nil {
			t.Fatalf("known state %q: %v", state, err)
		}
	}
	if _, err := repositorySetupState(projectsetup.State("unknown")); err == nil {
		t.Fatal("unknown setup state was accepted")
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
		{name: "duplicate active task", mutate: duplicateActiveRun, want: "active Run"},
		{name: "duplicate identifier", mutate: func(t *testing.T, root string) { duplicateBinding(t, root, true, false) }, want: "duplicate Linear identifier"},
		{name: "duplicate UUID", mutate: func(t *testing.T, root string) { duplicateBinding(t, root, false, true) }, want: "duplicate Linear UUID"},
		{name: "orphan invocation", mutate: orphanInvocation, want: "orphan"},
		{name: "missing Run", mutate: missingRun, want: "missing its Run"},
		{name: "conflicting route", mutate: conflictingRoute, want: "not exactly admitted"},
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
	t.Run("pre-audit migration identity", func(t *testing.T) {
		root := copyGolden(t)
		report, err := DryRun(root, testOptions())
		if err != nil {
			t.Fatal(err)
		}
		report.Manifest.MigrationID = "migration-altered"
		report.Backup.MigrationID = report.Manifest.MigrationID
		if err := VerifyDryRun(root, testOptions(), report); err == nil || !strings.Contains(err.Error(), "source root") {
			t.Fatalf("altered migration identity error = %v", err)
		}
	})
	t.Run("Linear mapping after audit", func(t *testing.T) {
		root := copyGolden(t)
		report, err := DryRun(root, testOptions())
		if err != nil {
			t.Fatal(err)
		}
		changedBinding(t, root)
		if err := VerifyDryRun(root, testOptions(), report); err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("changed mapping error = %v", err)
		}
	})
	t.Run("compiled input after audit", func(t *testing.T) {
		root := copyGolden(t)
		report, err := DryRun(root, testOptions())
		if err != nil {
			t.Fatal(err)
		}
		changed := testOptions()
		changed.CompiledRepositories[0].App = "FACTORY"
		changedReport, err := DryRun(root, changed)
		if err != nil {
			t.Fatal(err)
		}
		if changedReport.Audit.CanonicalRepositories.Digest != report.Audit.CanonicalRepositories.Digest ||
			changedReport.Audit.CompiledRepositoryInputDigest == report.Audit.CompiledRepositoryInputDigest {
			t.Fatalf("compiled provenance was not independently bound: original=%#v changed=%#v", report.Audit, changedReport.Audit)
		}
		if err := VerifyDryRun(root, changed, report); err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("changed compiled input error = %v", err)
		}
	})
}

func assertNoCanonicalState(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{
		"policy.json", "repositories.json", "runs.jsonl", "generation-manifest.json", "state-generation.json",
		"canonicalWritesStarted", "canonical-writes-started", "generations",
	} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dry run created canonical state %s: %v", name, err)
		}
	}
}

func testOptions() Options {
	return Options{
		TriggerActorID: "actor-sanitized",
		CompiledRepositories: []repositories.CompiledSource{{
			App: "factory", Repository: "tomnagengast/factory",
			RepoURL:  "git@github.com:tomnagengast/factory.git",
			RepoPath: "/srv/factory/repos/factory", ManagedRoot: "/srv/factory/repos",
			ProjectPath: "/srv/factory/repos/factory", BaseBranch: "main",
		}},
		Now: fixtureNow,
	}
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
	data, err := os.ReadFile(filepath.Join("testdata", "cases", "pending-system-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system-events.jsonl"), data, 0o600); err != nil {
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
	retainedRuns := runStore.Snapshot().Runs
	if len(retainedRuns) != 1 {
		return fmt.Errorf("generated Run fixture retained %d Runs", len(retainedRuns))
	}
	lastWireSequence := record.Sequence
	for _, transition := range retainedRuns[0].Transitions {
		transitionRecord, _, addErr := wireJournal.Add(eventwire.Event{
			ID:      "factory:run-transition:" + transition.ID,
			Source:  eventwire.SourceFactory,
			Type:    "agent-run",
			Action:  string(transition.State),
			Subject: retainedRuns[0].IssueIdentifier,
			Attributes: map[string][]string{
				"runId":                       {retainedRuns[0].ID},
				"attempts":                    {strconv.Itoa(transition.Attempts)},
				"taskSource":                  {string(retainedRuns[0].Task.Source)},
				"taskProviderId":              {retainedRuns[0].Task.ProviderID},
				"taskIdentifier":              {retainedRuns[0].Task.Identifier},
				eventwire.AttributeProducer:   {"agent-collector"},
				eventwire.AttributeProvenance: {"factory"},
			},
			RootEventID:        retainedRuns[0].InvocationRootEventID,
			ParentInvocationID: retainedRuns[0].InvocationID,
			ParentRunID:        retainedRuns[0].ID,
			Hop:                retainedRuns[0].InvocationHop,
			AncestorRuleIDs:    slices.Clone(retainedRuns[0].InvocationAncestorRuleIDs),
			ReceivedAt:         transition.At,
		})
		if addErr != nil {
			return addErr
		}
		lastWireSequence = transitionRecord.Sequence
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
	var taskCheckpoint map[string]any
	if err := json.Unmarshal(taskJournal, &taskCheckpoint); err != nil {
		return err
	}
	outcomes := taskCheckpoint["checkpoint"].(map[string]any)["outcomes"].([]any)
	outcomes[1].(map[string]any)["commandHash"] = strings.Repeat("c", 64)
	taskJournal, err = json.Marshal(taskCheckpoint)
	if err != nil {
		return err
	}
	if err := os.WriteFile(taskJournalPath, append(taskJournal, '\n'), 0o600); err != nil {
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
	if err := wireJournal.Acknowledge(lastWireSequence, map[string]uint64{"linear": 1}); err != nil {
		return err
	}
	marker := map[string]any{"version": 1, "boundary": "source-neutral-task-v1", "crossedAt": fixtureNow}
	if err := writeGoldenJSON(filepath.Join(root, "task-source-neutral.json"), marker); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join("testdata", "cases"), 0o700); err != nil {
		return err
	}
	if err := writeGoldenJSON(filepath.Join("testdata", "cases", "pending-stage.json"), map[string]any{"operationId": "op-0123456789abcdef", "kind": "create", "sanitized": true}); err != nil {
		return err
	}
	wireData, err := os.ReadFile(filepath.Join(root, "system-events.jsonl"))
	if err != nil {
		return err
	}
	wireLines := strings.Split(string(wireData), "\n")
	if len(wireLines) < 4 {
		return errors.New("generated wire fixture is incomplete")
	}
	return os.WriteFile(filepath.Join("testdata", "cases", "pending-system-events.jsonl"), []byte(strings.Join(wireLines[:2], "\n")+"\n"), 0o600)
}

func writeGoldenJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
