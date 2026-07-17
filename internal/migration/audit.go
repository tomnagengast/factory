package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/workflow"
)

func DryRun(root string, options Options) (DryRunReport, error) {
	state, err := readSources(root, options)
	if err != nil {
		return DryRunReport{}, err
	}
	if err := inject(options, "before-audit"); err != nil {
		return DryRunReport{}, err
	}
	audit, totals, err := auditSources(state)
	if err != nil {
		return DryRunReport{}, err
	}
	auditDigest, err := digestJSON(audit)
	if err != nil {
		return DryRunReport{}, err
	}
	if err := inject(options, "after-audit"); err != nil {
		return DryRunReport{}, err
	}
	secondHashes, err := hashTree(filepath.Clean(root))
	if err != nil {
		return DryRunReport{}, err
	}
	secondDirectories, err := directoryModes(filepath.Clean(root))
	if err != nil {
		return DryRunReport{}, err
	}
	if !slices.Equal(state.hashes, secondHashes) || !slices.Equal(state.directories, secondDirectories) {
		return DryRunReport{}, errors.New("migration: source changed during dry run")
	}
	rootDigest, err := digestJSON(struct {
		Files       []SourceHash      `json:"files"`
		Directories []SourceDirectory `json:"directories"`
	}{Files: state.hashes, Directories: state.directories})
	if err != nil {
		return DryRunReport{}, err
	}
	migrationID := "migration-" + rootDigest[:16] + "-" + auditDigest[:16]
	manifest := MigrationManifest{
		Schema: manifestSchema, MigrationID: migrationID, ObservedAt: options.Now.UTC(),
		SourceRootDigest: rootDigest, Sources: slices.Clone(state.hashes), Directories: slices.Clone(state.directories), AuditDigest: auditDigest,
		SourceSchemas: map[string]int{
			"settings": state.settings.Schema, "registry": state.registry.Schema, "routing": state.routing.Schema,
			"projects": state.projects.Version, "runs": state.runs.Version, "tasks": state.tasks.Schema,
			"taskControl": state.taskControl.Version, "linearIdentities": state.identities.Version,
			"workflowDrafts": state.drafts.Schema, "triggerCursors": state.cursors.Schema,
			"agentEventCursors": state.agentCursors.Version, "githubEvents": state.githubEvents.Version,
			"linearComments": state.linearComments.Version,
		},
		RetainedTotals: totals,
	}
	report := DryRunReport{
		Schema: dryRunReportSchema, Manifest: manifest,
		Backup: BackupReceipt{Schema: backupReceiptSchema, MigrationID: migrationID, ObservedAt: options.Now.UTC(), SourceRootDigest: rootDigest, Files: slices.Clone(state.hashes), Directories: slices.Clone(state.directories)},
		Audit:  audit, AuditDigest: auditDigest,
	}
	if err := inject(options, "report"); err != nil {
		return DryRunReport{}, err
	}
	if err := report.validate(); err != nil {
		return DryRunReport{}, err
	}
	if err := inject(options, "after-report"); err != nil {
		return DryRunReport{}, err
	}
	return report, nil
}

// VerifyDryRun detects both altered source artifacts and altered audit/report
// evidence. It performs another non-activating characterization.
func VerifyDryRun(root string, options Options, report DryRunReport) error {
	if err := report.validate(); err != nil {
		return err
	}
	auditDigest, err := digestJSON(report.Audit)
	if err != nil || auditDigest != report.AuditDigest {
		return errors.New("migration: audit evidence changed")
	}
	observed, err := DryRun(root, options)
	if err != nil {
		return err
	}
	if observed.Manifest.SourceRootDigest != report.Manifest.SourceRootDigest || observed.AuditDigest != report.AuditDigest || !slices.Equal(observed.Manifest.Sources, report.Manifest.Sources) || !slices.Equal(observed.Manifest.Directories, report.Manifest.Directories) {
		return errors.New("migration: source or audit evidence changed")
	}
	return nil
}

func auditSources(state sourceState) (Audit, map[string]uint64, error) {
	if err := auditReservedWorkflows(state); err != nil {
		return Audit{}, nil, err
	}
	wireByID := make(map[string]uint64, len(state.wireRecords))
	wireBySequence := make(map[uint64]string, len(state.wireRecords))
	for _, record := range state.wireRecords {
		if record.Sequence == 0 || wireByID[record.Event.ID] != 0 || wireBySequence[record.Sequence] != "" {
			return Audit{}, nil, errors.New("migration: duplicate wire identity or sequence")
		}
		wireByID[record.Event.ID] = record.Sequence
		wireBySequence[record.Sequence] = record.Event.ID
	}

	invocations := make(map[string]triggerrouter.Invocation, len(state.routing.Invocations))
	referencedInvocations := make(map[string]bool, len(state.routing.Invocations))
	for _, invocation := range state.routing.Invocations {
		if invocation.ID == "" || invocations[invocation.ID].ID != "" {
			return Audit{}, nil, errors.New("migration: duplicate invocation")
		}
		if wireByID[invocation.EventID] != invocation.EventSequence {
			return Audit{}, nil, fmt.Errorf("migration: invocation %s has an invalid event sequence", invocation.ID)
		}
		if invocation.PolicyRevision == 0 || invocation.PolicyRevision > state.settings.Revision {
			return Audit{}, nil, fmt.Errorf("migration: invocation %s has an invalid policy revision", invocation.ID)
		}
		if err := auditPinned(invocation.Workflow, invocation.WorkflowDigest); err != nil {
			return Audit{}, nil, fmt.Errorf("migration: invocation %s: %w", invocation.ID, err)
		}
		invocations[invocation.ID] = invocation
	}
	for _, decision := range state.routing.Decisions {
		if wireByID[decision.EventID] != decision.EventSequence || decision.SettingsRevision == 0 || decision.SettingsRevision > state.settings.Revision || decision.RegistryRevision > state.registry.Revision {
			return Audit{}, nil, fmt.Errorf("migration: decision %s has invalid event or policy causation", decision.EventID)
		}
		for _, outcome := range decision.Outcomes {
			if outcome.Kind != triggerrouter.OutcomeInvocation {
				continue
			}
			invocation, found := invocations[outcome.InvocationID]
			if !found || referencedInvocations[outcome.InvocationID] || invocation.EventID != decision.EventID || invocation.Rule.ID != outcome.RuleID || invocation.Rule.Revision != outcome.RuleRevision {
				return Audit{}, nil, errors.New("migration: orphan or multiply-owned invocation")
			}
			referencedInvocations[outcome.InvocationID] = true
		}
	}
	if len(referencedInvocations) != len(invocations) {
		return Audit{}, nil, errors.New("migration: orphan invocation")
	}

	runs := make(map[string]agentrun.Run, len(state.runs.Runs))
	activeTasks := make(map[string]string)
	linearIdentifiers := make(map[string]bool)
	workflowPins := uint64(0)
	repositoryRoutes := uint64(0)
	active := uint64(0)
	for _, run := range state.runs.Runs {
		if run.ID == "" || runs[run.ID].ID != "" || run.Task.Validate() != nil {
			return Audit{}, nil, errors.New("migration: duplicate or invalid Run")
		}
		if err := validateRetainedRun(run); err != nil {
			return Audit{}, nil, fmt.Errorf("migration: Run %s: %w", run.ID, err)
		}
		if run.State.Active() {
			key := run.Task.OwnershipKey()
			if owner := activeTasks[key]; owner != "" {
				return Audit{}, nil, fmt.Errorf("migration: duplicate active task ownership by %s and %s", owner, run.ID)
			}
			activeTasks[key] = run.ID
			active++
		}
		if run.Task.Source == taskmodel.SourceLinear {
			linearIdentifiers[run.Task.Identifier] = true
		}
		if run.PinnedWorkflow != nil {
			if err := auditPinned(*run.PinnedWorkflow, run.PinnedWorkflowDigest); err != nil {
				return Audit{}, nil, fmt.Errorf("migration: Run %s: %w", run.ID, err)
			}
			workflowPins++
		}
		if run.Repository != "" {
			if err := validateRunRoute(run, state.projects.Entries); err != nil {
				return Audit{}, nil, err
			}
			repositoryRoutes++
		}
		runs[run.ID] = run
	}
	for _, invocation := range invocations {
		if invocation.RunID == "" {
			if invocation.State != triggerrouter.StateQueued {
				return Audit{}, nil, fmt.Errorf("migration: invocation %s is missing a Run", invocation.ID)
			}
			continue
		}
		run, found := runs[invocation.RunID]
		if !found {
			return Audit{}, nil, fmt.Errorf("migration: invocation %s references a missing Run", invocation.ID)
		}
		if run.InvocationID != invocation.ID || !run.Task.Equal(invocation.Task) || run.PinnedWorkflowDigest != invocation.WorkflowDigest || run.PinnedPolicyRevision != invocation.PolicyRevision {
			return Audit{}, nil, fmt.Errorf("migration: invocation %s and Run %s disagree", invocation.ID, run.ID)
		}
	}
	for _, run := range runs {
		if run.InvocationID != "" {
			invocation, found := invocations[run.InvocationID]
			if !found || invocation.RunID != run.ID {
				return Audit{}, nil, fmt.Errorf("migration: Run %s has no matching invocation", run.ID)
			}
		}
	}

	bindingsByIdentifier := make(map[string]string, len(state.identities.Bindings))
	bindingsByUUID := make(map[string]string, len(state.identities.Bindings))
	for _, binding := range state.identities.Bindings {
		identifier := strings.ToUpper(strings.TrimSpace(binding.Identifier))
		uuid := strings.ToLower(strings.TrimSpace(binding.UUID))
		if !taskmodel.ValidLinearIdentifier(identifier) || !validUUID(uuid) {
			return Audit{}, nil, errors.New("migration: invalid Linear identity binding")
		}
		if bindingsByIdentifier[identifier] != "" {
			return Audit{}, nil, fmt.Errorf("migration: duplicate Linear identifier %s", identifier)
		}
		if bindingsByUUID[uuid] != "" {
			return Audit{}, nil, fmt.Errorf("migration: duplicate Linear UUID %s", uuid)
		}
		bindingsByIdentifier[identifier] = uuid
		bindingsByUUID[uuid] = identifier
	}
	for identifier := range linearIdentifiers {
		if bindingsByIdentifier[identifier] == "" {
			return Audit{}, nil, fmt.Errorf("migration: changed or missing Linear mapping for %s", identifier)
		}
	}

	for _, task := range state.tasks.Tasks {
		if task.Routing != nil {
			if err := validateTaskRoute(task, state.projects.Entries); err != nil {
				return Audit{}, nil, err
			}
			repositoryRoutes++
		}
		if task.Completion != nil {
			if _, found := runs[task.Completion.RunID]; !found {
				return Audit{}, nil, fmt.Errorf("migration: task %s completion references a missing Run", task.Ref.ProviderID)
			}
		}
	}

	seenDeliveries := make(map[string]bool, len(state.activity.Events))
	var previous time.Time
	for _, record := range state.activity.Events {
		if record.DeliveryID == "" || seenDeliveries[record.DeliveryID] || record.ReceivedAt.IsZero() || (!previous.IsZero() && record.ReceivedAt.After(previous)) {
			return Audit{}, nil, errors.New("migration: activity history is invalid")
		}
		seenDeliveries[record.DeliveryID] = true
		previous = record.ReceivedAt
		if record.PayloadAvailable && state.payloadHashes[record.DeliveryID] == "" {
			return Audit{}, nil, fmt.Errorf("migration: activity payload %s is missing or altered", record.DeliveryID)
		}
	}
	if len(state.payloadHashes) != countPayloadRecords(state.activity.Events) {
		return Audit{}, nil, errors.New("migration: orphan activity payload")
	}

	scheduleRevisions := make(map[string]uint64, len(state.registry.Schedules))
	for _, schedule := range state.registry.Schedules {
		scheduleRevisions[schedule.ID] = schedule.Revision
	}
	seenCursors := make(map[string]bool)
	for _, cursor := range state.cursors.Cursors {
		if cursor.ScheduleID == "" || seenCursors[cursor.ScheduleID] || cursor.LastScheduledAt.IsZero() || scheduleRevisions[cursor.ScheduleID] != cursor.ScheduleRevision {
			return Audit{}, nil, errors.New("migration: event schedule cursor conflicts with policy")
		}
		seenCursors[cursor.ScheduleID] = true
	}
	for key, offset := range state.agentCursors.Offsets {
		if offset < 0 || key == "" || filepath.IsAbs(key) || strings.Contains(key, "..") {
			return Audit{}, nil, errors.New("migration: unsafe agent event cursor")
		}
		if prefix := state.agentCursors.Prefixes[key]; prefix != "" && len(prefix) != 64 {
			return Audit{}, nil, errors.New("migration: invalid agent event cursor prefix")
		}
	}

	audit := Audit{
		TaskIdentities: uint64(len(activeTasks)), WorkflowPins: workflowPins, RepositoryRoutes: repositoryRoutes,
		Decisions: uint64(len(state.routing.Decisions)), Invocations: uint64(len(state.routing.Invocations)),
		Runs: uint64(len(state.runs.Runs)), ActiveRuns: active,
		NativeTasks: uint64(len(state.tasks.Tasks)), NativeOutcomes: uint64(len(state.tasks.Outcomes)),
		LinearBindings: uint64(len(state.identities.Bindings)), ActivityLifetime: state.activity.Total,
		ActivityRetained: uint64(len(state.activity.Events)), PrivatePayloads: uint64(len(state.payloadHashes)),
		WireTotal: state.wireTotal, WireDispatched: state.wireDispatched,
		WorkflowDrafts: uint64(len(state.drafts.Drafts)), ScheduleCursors: uint64(len(state.cursors.Cursors)),
		AgentEventCursors: uint64(len(state.agentCursors.Offsets)),
	}
	totals := map[string]uint64{
		"activityLifetime": audit.ActivityLifetime, "activityRetained": audit.ActivityRetained,
		"decisions": audit.Decisions, "invocations": audit.Invocations, "runsLifetime": state.runs.Total,
		"runsRetained": audit.Runs, "nativeTasks": audit.NativeTasks, "nativeOutcomes": audit.NativeOutcomes,
		"linearBindings": audit.LinearBindings, "privatePayloads": audit.PrivatePayloads,
		"wireTotal": audit.WireTotal, "wireRetained": uint64(len(state.wireRecords)),
		"githubEventsLifetime": state.githubEvents.Total, "githubEventsRetained": uint64(len(state.githubEvents.Events)),
		"linearCommentsLifetime": state.linearComments.Total, "linearCommentsRetained": uint64(len(state.linearComments.Events)),
	}
	digests, err := characterizationDigests(state, totals)
	if err != nil {
		return Audit{}, nil, err
	}
	audit.TaskIdentityDigest = digests["tasks"]
	audit.WorkflowPinDigest = digests["workflows"]
	audit.RepositoryRouteDigest = digests["routes"]
	audit.InvocationRunDigest = digests["invocationRuns"]
	audit.ActiveOwnershipDigest = digests["active"]
	audit.EventSequenceDigest = digests["events"]
	audit.LinearBijectionDigest = digests["linear"]
	audit.ActivityHistoryDigest = digests["activity"]
	audit.PayloadCorpusDigest = digests["payloads"]
	audit.RetainedTotalsDigest = digests["totals"]
	return audit, totals, nil
}

func validateRetainedRun(run agentrun.Run) error {
	switch run.State {
	case agentrun.StatePending, agentrun.StatePostMergePending, agentrun.StateStarting, agentrun.StateRunning,
		agentrun.StateAwaitingMerge, agentrun.StateSucceeded, agentrun.StateBlocked, agentrun.StateFailed:
	default:
		return fmt.Errorf("unknown state %q", run.State)
	}
	if run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() || run.UpdatedAt.Before(run.CreatedAt) {
		return errors.New("lifecycle timestamps are invalid")
	}
	if run.Ready != nil {
		if err := run.Ready.Validate(); err != nil {
			return err
		}
		if run.Ready.RunID != run.ID || (!run.Ready.Task.IsZero() && !run.Ready.Task.Equal(run.Task)) || run.Ready.Repository != run.Repository {
			return errors.New("ready checkpoint conflicts with Run identity")
		}
	}
	seenTransitions := make(map[string]bool, len(run.Transitions))
	for _, transition := range run.Transitions {
		if transition.ID == "" || seenTransitions[transition.ID] || transition.At.IsZero() {
			return errors.New("transition outbox is invalid")
		}
		seenTransitions[transition.ID] = true
	}
	return nil
}

func characterizationDigests(state sourceState, totals map[string]uint64) (map[string]string, error) {
	values := map[string][]string{
		"tasks": {}, "workflows": {}, "routes": {}, "invocationRuns": {}, "active": {},
		"events": {}, "linear": {}, "activity": {}, "payloads": {},
	}
	for _, run := range state.runs.Runs {
		values["tasks"] = append(values["tasks"], fmt.Sprintf("run|%s|%s|%s|%s", run.ID, run.Task.Source, run.Task.ProviderID, run.Task.Identifier))
		if run.PinnedWorkflow != nil {
			values["workflows"] = append(values["workflows"], fmt.Sprintf("run|%s|%s|%d", run.ID, run.PinnedWorkflowDigest, run.PinnedPolicyRevision))
		}
		if run.Repository != "" {
			values["routes"] = append(values["routes"], fmt.Sprintf("run|%s|%s|%s|%s|%s|%t|%s", run.ID, run.Repository, run.RepositoryURL, run.RepositoryPath, run.ManagedRoot, run.Bootstrap, run.BaseBranch))
		}
		if run.InvocationID != "" {
			values["invocationRuns"] = append(values["invocationRuns"], run.InvocationID+"|"+run.ID)
		}
		if run.State.Active() {
			values["active"] = append(values["active"], run.Task.OwnershipKey()+"|"+run.ID+"|"+string(run.State))
		}
	}
	for _, task := range state.tasks.Tasks {
		values["tasks"] = append(values["tasks"], fmt.Sprintf("native|%s|%s|%s", task.Ref.Source, task.Ref.ProviderID, task.Ref.Identifier))
		if task.Routing != nil {
			values["routes"] = append(values["routes"], fmt.Sprintf("native|%s|%s|%s|%s|%s|%t|%s", task.Ref.ProviderID, task.Routing.Repository, task.Routing.RepositoryURL, task.Routing.RepositoryPath, task.Routing.ManagedRoot, task.Routing.Bootstrap, task.Routing.BaseBranch))
			values["workflows"] = append(values["workflows"], fmt.Sprintf("native|%s|%s|%s", task.Ref.ProviderID, task.Routing.WorkflowID, task.Routing.WorkflowDigest))
		}
	}
	for _, invocation := range state.routing.Invocations {
		values["workflows"] = append(values["workflows"], fmt.Sprintf("invocation|%s|%s|%d", invocation.ID, invocation.WorkflowDigest, invocation.PolicyRevision))
		values["invocationRuns"] = append(values["invocationRuns"], invocation.ID+"|"+invocation.RunID)
		values["events"] = append(values["events"], fmt.Sprintf("invocation|%020d|%s|%s", invocation.EventSequence, invocation.EventID, invocation.ID))
	}
	for _, decision := range state.routing.Decisions {
		values["events"] = append(values["events"], fmt.Sprintf("decision|%020d|%s|%d|%d", decision.EventSequence, decision.EventID, decision.RegistryRevision, decision.SettingsRevision))
	}
	for _, record := range state.wireRecords {
		values["events"] = append(values["events"], fmt.Sprintf("wire|%020d|%s", record.Sequence, record.Event.ID))
	}
	for _, binding := range state.identities.Bindings {
		values["linear"] = append(values["linear"], strings.ToUpper(binding.Identifier)+"|"+strings.ToLower(binding.UUID))
	}
	values["activity"] = append(values["activity"], fmt.Sprintf("lifetime|%020d", state.activity.Total))
	for index, record := range state.activity.Events {
		values["activity"] = append(values["activity"], fmt.Sprintf("%06d|%s|%s|%s|%s|%t", index, record.DeliveryID, record.Type, record.Action, record.ReceivedAt.UTC().Format(time.RFC3339Nano), record.PayloadAvailable))
	}
	for deliveryID, hash := range state.payloadHashes {
		values["payloads"] = append(values["payloads"], deliveryID+"|"+hash)
	}
	for _, source := range state.hashes {
		if strings.HasPrefix(source.Path, "linear-activity-payloads/") {
			values["payloads"] = append(values["payloads"], fmt.Sprintf("%s|%s|%04o|%d", source.Path, source.SHA256, source.Mode, source.Size))
		}
	}
	digests := make(map[string]string, len(values)+1)
	for name, entries := range values {
		slices.Sort(entries)
		digest, err := digestJSON(entries)
		if err != nil {
			return nil, err
		}
		digests[name] = digest
	}
	totalDigest, err := digestJSON(totals)
	if err != nil {
		return nil, err
	}
	digests["totals"] = totalDigest
	return digests, nil
}

func auditReservedWorkflows(state sourceState) error {
	compiled := map[string]workflow.Definition{
		workflow.DefaultID:         workflow.Default(time.Time{}),
		workflow.ProviderNeutralID: workflow.ProviderNeutralDefault(time.Time{}),
	}
	for _, definition := range state.settings.Workflows {
		expected, reserved := compiled[definition.ID]
		if !reserved {
			continue
		}
		actualDigest, err := workflow.Digest(definition)
		if err != nil {
			return err
		}
		expectedDigest, err := workflow.Digest(expected)
		if err != nil || actualDigest != expectedDigest {
			return fmt.Errorf("migration: customized reserved workflow %s conflicts with compiled policy", definition.ID)
		}
	}
	return nil
}

func auditPinned(pin workflow.Pinned, expected string) error {
	if expected == "" {
		return errors.New("workflow digest is missing")
	}
	if pin.Complete() {
		if err := pin.Validate(); err != nil {
			return err
		}
		digest, err := pin.Digest()
		if err != nil || digest != expected {
			return errors.New("workflow digest mismatch")
		}
		return nil
	}
	if pin.ID == "" || pin.Revision == 0 {
		return errors.New("compacted workflow pin is incomplete")
	}
	return nil
}

func validateRunRoute(run agentrun.Run, projects []projectsetup.Entry) error {
	for _, entry := range projects {
		if entry.Repository != run.Repository {
			continue
		}
		if entry.RepoURL != run.RepositoryURL || entry.LocalPath != run.RepositoryPath || entry.ManagedRoot != run.ManagedRoot || entry.BaseBranch != run.BaseBranch || entry.Bootstrap != run.Bootstrap || entry.CloudURL != run.CloudURL {
			return fmt.Errorf("migration: Run %s has a conflicting repository route", run.ID)
		}
		return nil
	}
	return fmt.Errorf("migration: Run %s repository route is not admitted", run.ID)
}

func validateTaskRoute(task taskstore.Task, projects []projectsetup.Entry) error {
	for _, entry := range projects {
		if entry.ProjectID != task.Routing.ProjectID {
			continue
		}
		if entry.Repository != task.Routing.Repository || entry.RepoURL != task.Routing.RepositoryURL || entry.LocalPath != task.Routing.RepositoryPath || entry.ManagedRoot != task.Routing.ManagedRoot || entry.BaseBranch != task.Routing.BaseBranch || entry.Bootstrap != task.Routing.Bootstrap || entry.CloudURL != task.Routing.CloudURL {
			return fmt.Errorf("migration: native task %s has a conflicting repository route", task.Ref.ProviderID)
		}
		return nil
	}
	return fmt.Errorf("migration: native task %s repository route is not admitted", task.Ref.ProviderID)
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, character := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func countPayloadRecords(records []activityRecord) int {
	count := 0
	for _, record := range records {
		if record.PayloadAvailable {
			count++
		}
	}
	return count
}

func digestJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
