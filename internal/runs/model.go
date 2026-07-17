package runs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

const (
	SchemaVersion  = 5
	JournalVersion = 5
)

// runTransitionEventIDPrefix is the immutable legacy-compatible wire event ID
// prefix for a Run lifecycle transition. The complete event ID is this prefix
// followed by the deterministic transition ID.
const runTransitionEventIDPrefix = "factory:run-transition:"

// RunTransitionEventID returns the deterministic body-free wire event ID for a
// Run lifecycle transition. It is the sole owner of that identity so the store,
// validation, and the outbox collector cannot drift.
func RunTransitionEventID(transitionID string) string {
	return runTransitionEventIDPrefix + transitionID
}

type AdmissionOrigin string

const (
	AdmissionOriginEvent          AdmissionOrigin = "event"
	AdmissionOriginNative         AdmissionOrigin = "native"
	AdmissionOriginContinuation   AdmissionOrigin = "continuation"
	AdmissionOriginMigratedDirect AdmissionOrigin = "migrated_direct"
)

type AdmissionOutcomeKind string

const (
	AdmissionOutcomeRun        AdmissionOutcomeKind = "run"
	AdmissionOutcomeRejected   AdmissionOutcomeKind = "rejected"
	AdmissionOutcomeSuppressed AdmissionOutcomeKind = "suppressed"
)

type LifecycleState string

const (
	StateAdmitted           LifecycleState = "admitted"
	StateRouting            LifecycleState = "routing"
	StatePending            LifecycleState = "pending"
	StatePostMergePending   LifecycleState = "post_merge_pending"
	StateStarting           LifecycleState = "starting"
	StateRunning            LifecycleState = "running"
	StateAwaitingHumanMerge LifecycleState = "awaiting_human_merge"
	StateSucceeded          LifecycleState = "succeeded"
	StateBlocked            LifecycleState = "blocked"
	StateFailed             LifecycleState = "failed"
	StateRejected           LifecycleState = "rejected"
)

func (s LifecycleState) Nonterminal() bool {
	switch s {
	case StateAdmitted, StateRouting, StatePending, StatePostMergePending, StateStarting, StateRunning, StateAwaitingHumanMerge:
		return true
	default:
		return false
	}
}

func (s LifecycleState) Terminal() bool {
	return s == StateSucceeded || s == StateBlocked || s == StateFailed || s == StateRejected
}

// Model is the canonical journal projection. JournalSequence is durability
// metadata and is intentionally excluded from the semantic digest.
type Model struct {
	Schema              int                         `json:"schema"`
	JournalSequence     uint64                      `json:"journalSequence"`
	TotalBatches        uint64                      `json:"totalBatches"`
	TotalRuns           uint64                      `json:"totalRuns"`
	Migration           *MigrationSnapshotReceipt   `json:"migration,omitempty"`
	AdmissionOperations []AdmissionOperationReceipt `json:"admissionOperations"`
	AdmissionBatches    []AdmissionBatch            `json:"admissionBatches"`
	Runs                []Run                       `json:"runs"`
	RateBuckets         []RateBucket                `json:"rateBuckets"`
}

const MigrationSnapshotOrigin = "migration_snapshot"

// MigrationSnapshotReceipt is the immutable, body-free evidence for the one
// initial legacy conversion. OperationID is the NUL-separated SHA-256 of the
// domain "factory-runs-migration-snapshot-v1" followed by every other field
// in canonical order. This binds the migration and source-root identities to
// the complete receipt without inventing historical admission operations.
type MigrationSnapshotReceipt struct {
	Origin              string           `json:"origin"`
	OperationID         string           `json:"operationId"`
	MigrationID         string           `json:"migrationId"`
	SourceRootDigest    string           `json:"sourceRootDigest"`
	AdmissionBatches    []AdmissionBatch `json:"admissionBatches"`
	BatchIDs            []string         `json:"batchIds"`
	EventIDs            []string         `json:"eventIds"`
	EventSequences      []uint64         `json:"eventSequences"`
	RunIDs              []string         `json:"runIds"`
	AdmissionIDs        []string         `json:"admissionIds"`
	LifetimeRuns        uint64           `json:"lifetimeRuns"`
	RetainedBatches     uint64           `json:"retainedBatches"`
	RateBucketDigest    string           `json:"rateBucketDigest"`
	RateBucketCount     uint64           `json:"rateBucketCount"`
	CanonicalRunsDigest string           `json:"canonicalRunsDigest"`
}

// AdmissionOperationReceipt retains the complete canonical input of one
// atomic admission operation. Receipts are never retention-evicted: they are
// the durable identity tombstones used to distinguish an exact retry from a
// subset, combination, or rewritten operation after its live projections are
// gone.
type AdmissionOperationReceipt struct {
	AdmissionBatches []AdmissionBatch `json:"admissionBatches"`
	Runs             []Run            `json:"runs"`
	RateIncrements   []RateBucket     `json:"rateIncrements"`
}

type AdmissionBatch struct {
	ID                string             `json:"id"`
	Origin            AdmissionOrigin    `json:"origin"`
	EventID           string             `json:"eventId"`
	EventSequence     uint64             `json:"eventSequence,omitempty"`
	EventSource       eventwire.Source   `json:"eventSource"`
	EventRecordDigest string             `json:"eventRecordDigest,omitempty"`
	RegistryRevision  uint64             `json:"registryRevision,omitempty"`
	SettingsRevision  uint64             `json:"settingsRevision,omitempty"`
	PolicyGeneration  uint64             `json:"policyGeneration,omitempty"`
	DecidedAt         time.Time          `json:"decidedAt"`
	Outcomes          []AdmissionOutcome `json:"outcomes"`
}

type AdmissionOutcome struct {
	Kind         AdmissionOutcomeKind `json:"kind"`
	RuleID       string               `json:"ruleId"`
	RuleRevision uint64               `json:"ruleRevision"`
	AdmissionID  string               `json:"admissionId,omitempty"`
	RunID        string               `json:"runId,omitempty"`
	Reason       string               `json:"reason,omitempty"`
}

// Causation is immutable after a Run is created. AdmissionID preserves the
// migrated Invocation identity; Run.ID separately preserves the legacy Run
// identity needed by parentRunId compatibility.
type Causation struct {
	AdmissionID       string            `json:"admissionId"`
	BatchID           string            `json:"batchId"`
	EventID           string            `json:"eventId"`
	EventSequence     uint64            `json:"eventSequence,omitempty"`
	EventSource       eventwire.Source  `json:"eventSource"`
	RuleID            string            `json:"ruleId"`
	RuleRevision      uint64            `json:"ruleRevision"`
	Workflow          *workflow.Pinned  `json:"workflow,omitempty"`
	WorkflowDigest    string            `json:"workflowDigest,omitempty"`
	PolicyRevision    uint64            `json:"policyRevision,omitempty"`
	PolicyGeneration  uint64            `json:"policyGeneration,omitempty"`
	Task              taskmodel.TaskRef `json:"task"`
	RootEventID       string            `json:"rootEventId"`
	ParentAdmissionID string            `json:"parentAdmissionId,omitempty"`
	ParentRunID       string            `json:"parentRunId,omitempty"`
	Hop               int               `json:"hop"`
	AncestorRuleIDs   []string          `json:"ancestorRuleIds"`
	AdmittedAt        time.Time         `json:"admittedAt"`
}

type Run struct {
	ID                   string                `json:"id"`
	Causation            Causation             `json:"causation"`
	MigratedBaseline     *MigratedBaseline     `json:"migratedBaseline,omitempty"`
	Repository           *repositories.Route   `json:"repository,omitempty"`
	RepositoryRejection  string                `json:"repositoryRejection,omitempty"`
	TriggerKind          string                `json:"triggerKind"`
	DeliveryIDs          []string              `json:"deliveryIds"`
	DuplicateDeliveries  uint64                `json:"duplicateDeliveries"`
	State                LifecycleState        `json:"state"`
	SessionName          string                `json:"sessionName,omitempty"`
	RunDirectory         string                `json:"runDirectory,omitempty"`
	Attempts             int                   `json:"attempts"`
	Detail               string                `json:"detail,omitempty"`
	CreatedAt            time.Time             `json:"createdAt"`
	UpdatedAt            time.Time             `json:"updatedAt"`
	StartedAt            *time.Time            `json:"startedAt,omitempty"`
	SegmentStartedAt     *time.Time            `json:"segmentStartedAt,omitempty"`
	SegmentAttempt       int                   `json:"segmentAttemptOffset,omitempty"`
	FinishedAt           *time.Time            `json:"finishedAt,omitempty"`
	Transitions          []LifecycleTransition `json:"transitions"`
	DeliveredThrough     int                   `json:"deliveredThrough,omitempty"`
	TransitionDeliveries []TransitionDelivery  `json:"transitionDeliveries,omitempty"`
	Ready                *ReadyCheckpoint      `json:"ready,omitempty"`
	MergeCommitOID       string                `json:"mergeCommitOid,omitempty"`
	GitHub               GitHubState           `json:"github"`
	ResumeCount          int                   `json:"resumeCount,omitempty"`
	TerminalIntent       string                `json:"terminalIntent,omitempty"`
	TerminalRejection    string                `json:"terminalRejection,omitempty"`
	Completion           *CompletionValidation `json:"completion,omitempty"`
}

// MigratedBaseline is immutable source evidence for a retained legacy Run.
// Legacy Transitions was an acknowledged outbox, so State and ObservedAt are
// the canonical journal's starting point rather than an invented lifecycle
// history. The compatibility flags distinguish unavailable source evidence
// from ordinary canonical Runs, which must retain complete initialization.
type MigratedBaseline struct {
	State                        LifecycleState        `json:"state"`
	ObservedAt                   time.Time             `json:"observedAt"`
	PriorTransitionsAcknowledged bool                  `json:"priorTransitionsAcknowledged"`
	WorkflowPinUnavailable       bool                  `json:"workflowPinUnavailable,omitempty"`
	WorkflowDigestUnavailable    bool                  `json:"workflowDigestUnavailable,omitempty"`
	RepositoryRouteUnavailable   bool                  `json:"repositoryRouteUnavailable,omitempty"`
	HistoricalRepository         *HistoricalRepository `json:"historicalRepository,omitempty"`
}

// HistoricalRepository preserves body-free legacy route evidence that may no
// longer correspond to an admitted repository record.
type HistoricalRepository struct {
	Repository    string `json:"repository"`
	Origin        string `json:"origin,omitempty"`
	ManagedPath   string `json:"managedPath,omitempty"`
	ManagedRoot   string `json:"managedRoot,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Bootstrap     bool   `json:"bootstrap,omitempty"`
	CloudURL      string `json:"cloudUrl,omitempty"`
}

type LifecycleTransition struct {
	ID       string         `json:"id"`
	State    LifecycleState `json:"state"`
	Attempts int            `json:"attempts"`
	At       time.Time      `json:"at"`
}

type DeliveryState string

const (
	DeliveryPending   DeliveryState = "pending"
	DeliveryPublished DeliveryState = "published"
)

// TransitionDelivery is the outbox obligation for exactly one LifecycleTransition
// whose event is not yet globally acknowledged. Deliveries correspond one-for-one,
// in order, to Transitions[DeliveredThrough:]. A pending delivery carries a zero
// Sequence; a published delivery carries the authoritative positive wire Sequence.
type TransitionDelivery struct {
	TransitionID string        `json:"transitionId"`
	EventID      string        `json:"eventId"`
	State        DeliveryState `json:"state"`
	Sequence     uint64        `json:"sequence,omitempty"`
}

type GitHubState struct {
	LastCursor                 uint64     `json:"lastCursor,omitempty"`
	LastAuthoritativeRefreshAt *time.Time `json:"lastAuthoritativeRefreshAt,omitempty"`
	NextReconcileAt            *time.Time `json:"nextReconcileAt,omitempty"`
	ReconcileFailures          int        `json:"reconcileFailures,omitempty"`
	RemediationRequested       bool       `json:"remediationRequested,omitempty"`
}

type ReadyCheckpoint struct {
	ContractVersion      int               `json:"contractVersion"`
	RunID                string            `json:"runId"`
	Task                 taskmodel.TaskRef `json:"task,omitzero"`
	Repository           string            `json:"repository"`
	PullRequest          int               `json:"pullRequest"`
	BaseBranch           string            `json:"baseBranch"`
	HeadBranch           string            `json:"headBranch"`
	VerifiedHeadOID      string            `json:"verifiedHeadOid"`
	PullRequestUpdatedAt time.Time         `json:"pullRequestUpdatedAt,omitempty"`
	CreatedAt            time.Time         `json:"createdAt"`
	ValidatedAt          time.Time         `json:"validatedAt,omitempty"`
}

type CompletionValidation struct {
	Accepted         bool           `json:"accepted"`
	Intent           string         `json:"intent"`
	Blocker          string         `json:"blocker,omitempty"`
	State            LifecycleState `json:"state"`
	Reason           string         `json:"reason"`
	ValidatedAt      time.Time      `json:"validatedAt"`
	PullRequestState string         `json:"pullRequestState,omitempty"`
	PullRequestHead  string         `json:"pullRequestHead,omitempty"`
	MergeCommitOID   string         `json:"mergeCommitOid,omitempty"`
	DeploymentID     string         `json:"deploymentId,omitempty"`
	DeploymentCommit string         `json:"deploymentCommit,omitempty"`
}

type RateBucket struct {
	RuleID string    `json:"ruleId"`
	Minute time.Time `json:"minute"`
	Count  int       `json:"count"`
}

// Snapshot is immutable. Model returns a complete deep clone.
type Snapshot struct {
	model Model
}

func NewSnapshot(model Model) (Snapshot, error) {
	canonicalizeModel(&model)
	if err := validateModel(model); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{model: cloneModel(model)}, nil
}

func EmptyModel() Model {
	return Model{
		Schema: SchemaVersion, AdmissionOperations: []AdmissionOperationReceipt{}, AdmissionBatches: []AdmissionBatch{},
		Runs: []Run{}, RateBuckets: []RateBucket{},
	}
}

func (s Snapshot) Model() Model { return cloneModel(s.model) }

func (s Snapshot) Validate() error { return validateModel(s.model) }

func (s Snapshot) Digest() (string, error) {
	semantic := struct {
		Schema              int                         `json:"schema"`
		TotalBatches        uint64                      `json:"totalBatches"`
		TotalRuns           uint64                      `json:"totalRuns"`
		Migration           *MigrationSnapshotReceipt   `json:"migration,omitempty"`
		AdmissionOperations []AdmissionOperationReceipt `json:"admissionOperations"`
		AdmissionBatches    []AdmissionBatch            `json:"admissionBatches"`
		Runs                []Run                       `json:"runs"`
		RateBuckets         []RateBucket                `json:"rateBuckets"`
	}{
		Schema: s.model.Schema, TotalBatches: s.model.TotalBatches, TotalRuns: s.model.TotalRuns,
		Migration:           cloneMigrationSnapshotReceipt(s.model.Migration),
		AdmissionOperations: cloneAdmissionOperations(s.model.AdmissionOperations),
		AdmissionBatches:    cloneAdmissionBatches(s.model.AdmissionBatches),
		Runs:                cloneRuns(s.model.Runs), RateBuckets: slices.Clone(s.model.RateBuckets),
	}
	data, err := json.Marshal(semantic)
	if err != nil {
		return "", fmt.Errorf("runs: encode digest: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (r Run) Clone() Run { return cloneRun(r) }

func (r Run) Validate() error {
	r = cloneRun(r)
	canonicalizeRun(&r)
	return validateRun(r, false)
}

func canonicalizeModel(model *Model) {
	canonicalizeMigrationSnapshotReceipt(model.Migration)
	for index := range model.AdmissionOperations {
		canonicalizeAdmissionOperation(&model.AdmissionOperations[index])
	}
	for index := range model.AdmissionBatches {
		canonicalizeAdmissionBatch(&model.AdmissionBatches[index])
	}
	for index := range model.Runs {
		canonicalizeRun(&model.Runs[index])
	}
	for index := range model.RateBuckets {
		model.RateBuckets[index].Minute = model.RateBuckets[index].Minute.UTC().Truncate(time.Minute)
	}
	sort.Slice(model.AdmissionBatches, func(i, j int) bool {
		left, right := model.AdmissionBatches[i], model.AdmissionBatches[j]
		if !left.DecidedAt.Equal(right.DecidedAt) {
			return left.DecidedAt.Before(right.DecidedAt)
		}
		return left.ID < right.ID
	})
	slices.SortFunc(model.AdmissionOperations, compareAdmissionOperationReceipts)
	sort.Slice(model.Runs, func(i, j int) bool {
		left, right := model.Runs[i], model.Runs[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.ID < right.ID
	})
	sort.Slice(model.RateBuckets, func(i, j int) bool {
		if model.RateBuckets[i].RuleID != model.RateBuckets[j].RuleID {
			return model.RateBuckets[i].RuleID < model.RateBuckets[j].RuleID
		}
		return model.RateBuckets[i].Minute.Before(model.RateBuckets[j].Minute)
	})
}

func canonicalizeMigrationSnapshotReceipt(receipt *MigrationSnapshotReceipt) {
	if receipt == nil {
		return
	}
	for index := range receipt.AdmissionBatches {
		canonicalizeAdmissionBatch(&receipt.AdmissionBatches[index])
	}
	slices.SortFunc(receipt.AdmissionBatches, compareAdmissionBatches)
}

func canonicalizeAdmissionOperation(operation *AdmissionOperationReceipt) {
	for index := range operation.AdmissionBatches {
		canonicalizeAdmissionBatch(&operation.AdmissionBatches[index])
	}
	slices.SortFunc(operation.AdmissionBatches, compareAdmissionBatches)
	for index := range operation.Runs {
		canonicalizeRun(&operation.Runs[index])
	}
	slices.SortFunc(operation.Runs, compareRuns)
	if len(operation.Runs) == 0 {
		operation.Runs = nil
	}
	for index := range operation.RateIncrements {
		operation.RateIncrements[index].Minute = operation.RateIncrements[index].Minute.UTC().Truncate(time.Minute)
	}
	slices.SortFunc(operation.RateIncrements, compareRateBuckets)
	if len(operation.RateIncrements) == 0 {
		operation.RateIncrements = nil
	}
}

func compareAdmissionOperationReceipts(left, right AdmissionOperationReceipt) int {
	if len(left.AdmissionBatches) == 0 || len(right.AdmissionBatches) == 0 {
		return len(left.AdmissionBatches) - len(right.AdmissionBatches)
	}
	if comparison := compareAdmissionBatches(left.AdmissionBatches[0], right.AdmissionBatches[0]); comparison != 0 {
		return comparison
	}
	return len(left.AdmissionBatches) - len(right.AdmissionBatches)
}

func compareRateBuckets(left, right RateBucket) int {
	if left.RuleID < right.RuleID {
		return -1
	}
	if left.RuleID > right.RuleID {
		return 1
	}
	return left.Minute.Compare(right.Minute)
}

func canonicalizeAdmissionBatch(batch *AdmissionBatch) {
	batch.DecidedAt = batch.DecidedAt.UTC()
	sort.Slice(batch.Outcomes, func(i, j int) bool {
		left, right := batch.Outcomes[i], batch.Outcomes[j]
		if left.RuleID != right.RuleID {
			return left.RuleID < right.RuleID
		}
		if left.RuleRevision != right.RuleRevision {
			return left.RuleRevision < right.RuleRevision
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.AdmissionID < right.AdmissionID
	})
}

func canonicalizeRun(run *Run) {
	run.Causation.AdmittedAt = run.Causation.AdmittedAt.UTC()
	if run.MigratedBaseline != nil {
		baseline := *run.MigratedBaseline
		baseline.ObservedAt = baseline.ObservedAt.UTC()
		if baseline.HistoricalRepository != nil {
			repository := *baseline.HistoricalRepository
			baseline.HistoricalRepository = &repository
		}
		run.MigratedBaseline = &baseline
	}
	if run.Causation.Workflow != nil {
		pin := run.Causation.Workflow.Clone()
		if pin.UpdatedAt != nil {
			updated := pin.UpdatedAt.UTC()
			pin.UpdatedAt = &updated
		}
		run.Causation.Workflow = &pin
	}
	sort.Strings(run.DeliveryIDs)
	run.CreatedAt = run.CreatedAt.UTC()
	run.UpdatedAt = run.UpdatedAt.UTC()
	run.StartedAt = canonicalTime(run.StartedAt)
	run.SegmentStartedAt = canonicalTime(run.SegmentStartedAt)
	run.FinishedAt = canonicalTime(run.FinishedAt)
	for index := range run.Transitions {
		run.Transitions[index].At = run.Transitions[index].At.UTC()
	}
	if len(run.TransitionDeliveries) == 0 {
		run.TransitionDeliveries = nil
	}
	if run.Ready != nil {
		ready := *run.Ready
		ready.PullRequestUpdatedAt = ready.PullRequestUpdatedAt.UTC()
		ready.CreatedAt = ready.CreatedAt.UTC()
		ready.ValidatedAt = ready.ValidatedAt.UTC()
		run.Ready = &ready
	}
	run.GitHub.LastAuthoritativeRefreshAt = canonicalTime(run.GitHub.LastAuthoritativeRefreshAt)
	run.GitHub.NextReconcileAt = canonicalTime(run.GitHub.NextReconcileAt)
	if run.Completion != nil {
		completion := *run.Completion
		completion.ValidatedAt = completion.ValidatedAt.UTC()
		run.Completion = &completion
	}
}

func cloneModel(model Model) Model {
	clone := model
	clone.Migration = cloneMigrationSnapshotReceipt(model.Migration)
	clone.AdmissionOperations = cloneAdmissionOperations(model.AdmissionOperations)
	clone.AdmissionBatches = cloneAdmissionBatches(model.AdmissionBatches)
	clone.Runs = cloneRuns(model.Runs)
	clone.RateBuckets = slices.Clone(model.RateBuckets)
	return clone
}

func cloneMigrationSnapshotReceipt(receipt *MigrationSnapshotReceipt) *MigrationSnapshotReceipt {
	if receipt == nil {
		return nil
	}
	clone := *receipt
	clone.AdmissionBatches = cloneAdmissionBatches(receipt.AdmissionBatches)
	clone.BatchIDs = slices.Clone(receipt.BatchIDs)
	clone.EventIDs = slices.Clone(receipt.EventIDs)
	clone.EventSequences = slices.Clone(receipt.EventSequences)
	clone.RunIDs = slices.Clone(receipt.RunIDs)
	clone.AdmissionIDs = slices.Clone(receipt.AdmissionIDs)
	return &clone
}

func cloneAdmissionOperations(values []AdmissionOperationReceipt) []AdmissionOperationReceipt {
	cloned := make([]AdmissionOperationReceipt, len(values))
	for index, value := range values {
		cloned[index] = AdmissionOperationReceipt{
			AdmissionBatches: cloneAdmissionBatches(value.AdmissionBatches),
			Runs:             cloneRuns(value.Runs),
			RateIncrements:   slices.Clone(value.RateIncrements),
		}
	}
	return cloned
}

func cloneAdmissionBatches(values []AdmissionBatch) []AdmissionBatch {
	cloned := make([]AdmissionBatch, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Outcomes = slices.Clone(value.Outcomes)
	}
	return cloned
}

func cloneRuns(values []Run) []Run {
	cloned := make([]Run, len(values))
	for index, value := range values {
		cloned[index] = cloneRun(value)
	}
	return cloned
}

func cloneRun(run Run) Run {
	run.Causation.AncestorRuleIDs = slices.Clone(run.Causation.AncestorRuleIDs)
	if run.Causation.Workflow != nil {
		pin := run.Causation.Workflow.Clone()
		run.Causation.Workflow = &pin
	}
	if run.MigratedBaseline != nil {
		baseline := *run.MigratedBaseline
		if baseline.HistoricalRepository != nil {
			repository := *baseline.HistoricalRepository
			baseline.HistoricalRepository = &repository
		}
		run.MigratedBaseline = &baseline
	}
	if run.Repository != nil {
		route := *run.Repository
		run.Repository = &route
	}
	run.DeliveryIDs = slices.Clone(run.DeliveryIDs)
	run.StartedAt = cloneTime(run.StartedAt)
	run.SegmentStartedAt = cloneTime(run.SegmentStartedAt)
	run.FinishedAt = cloneTime(run.FinishedAt)
	run.Transitions = slices.Clone(run.Transitions)
	run.TransitionDeliveries = slices.Clone(run.TransitionDeliveries)
	if run.Ready != nil {
		ready := *run.Ready
		run.Ready = &ready
	}
	run.GitHub.LastAuthoritativeRefreshAt = cloneTime(run.GitHub.LastAuthoritativeRefreshAt)
	run.GitHub.NextReconcileAt = cloneTime(run.GitHub.NextReconcileAt)
	if run.Completion != nil {
		completion := *run.Completion
		run.Completion = &completion
	}
	return run
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func canonicalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	canonical := value.UTC()
	return &canonical
}
