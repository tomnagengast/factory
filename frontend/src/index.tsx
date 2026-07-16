import {
  createMemo,
  createEffect,
  createResource,
  createSignal,
  For,
  onCleanup,
  onMount,
  Show,
  type JSX,
  type Resource,
} from "solid-js";
import { render } from "solid-js/web";
import {
  ActivityHeader,
  formatTime,
  InlineError,
  LoadingRows,
  resourceState,
  runStateLabel,
} from "./activity";
import { agentRunHref, type AgentActivityRun, type AgentRun } from "./agent";
import { getJSON } from "./http";
import "./styles.css";

type Health = {
  status: string;
  app: string;
  commit: string;
  tree: string;
  buildId: string;
  deploymentId: string;
  contractVersion: string;
  startedAt: string;
};

type ActivityEvent = {
  type: string;
  action: string;
  receivedAt: string;
};

type AgentRunSnapshot = {
  total: number;
  active: number;
  runs: AgentRun[];
};

type ActivitySnapshot = {
  status: string;
  total: number;
  lastReceivedAt: string | null;
  events: ActivityEvent[];
  agentRuns: AgentRunSnapshot;
};

type TaskRef = {
  source: "factory" | "linear";
  providerId: string;
  identifier: string;
};

type TaskActor = {
  id: string;
  kind: "human" | "agent" | "system";
};

type TaskSummary = {
  ref: TaskRef;
  title: string;
  projectId?: string;
  approvalMode?: "gated" | "automatic";
  state: string;
  revision?: number;
  updatedAt?: string;
  readOnly: boolean;
  externalUrl?: string;
  description?: string;
  projectName?: string;
  stateName?: string;
  messages?: TaskMessage[];
  latestRun?: AgentActivityRun;
};

type TaskRecord = {
  ref: TaskRef;
  title: string;
  sequence: number;
  description?: string;
  projectId: string;
  approvalMode: "gated" | "automatic";
  state: string;
  revision: number;
  createdBy: TaskActor;
  createdAt: string;
  updatedAt: string;
  messageCount: number;
  linkCount: number;
  gateCount: number;
  completedAt?: string;
  routing?: {
    projectId: string;
    repository: string;
    baseBranch: string;
    workflowId: string;
    workflowDigest: string;
    admittedAt: string;
  };
  completion?: {
    runId: string;
    evidenceRef: string;
    completedAt: string;
  };
};

type TaskMessage = {
  id: string;
  ordinal: number;
  parentId?: string;
  body: string;
  author: TaskActor;
  createdAt: string;
};

type TaskLink = {
  id: string;
  label: string;
  url: string;
  actor: TaskActor;
  createdAt: string;
};

type TaskGate = {
  id: string;
  kind: string;
  mode: "gated" | "automatic";
  status: "open" | "approved" | "revision_requested";
  artifactUrl?: string;
  openedBy: TaskActor;
  openedAt: string;
  decision?: {
    action: string;
    reason?: string;
    actor: TaskActor;
    decidedAt: string;
  };
};

type NativeTaskDetail = {
  task: TaskRecord;
  messages: { messages: TaskMessage[]; nextAfter?: number };
  links: TaskLink[];
  gates: TaskGate[];
  latestRun?: AgentActivityRun;
};

type TasksResponse = {
  tasks: TaskSummary[];
  nextCursor?: string;
};

type TaskProject = {
  projectId: string;
  projectName: string;
  repository: string;
  enabled: boolean;
};

type TaskProjectsResponse = {
  projects: TaskProject[];
  control: { version: number; revision: number; enabledProjectIds: string[]; updatedAt?: string };
};
type TaskMutationResult = { task: TaskRecord };

type ActivityCount = {
  label: string;
  count: number;
};

type WireEvent = {
  id: string;
  source: string;
  type: string;
  action: string;
  subject?: string;
  attributes?: Record<string, string[]>;
  channels?: string[];
  receivedAt: string;
};

type WireRecord = {
  sequence: number;
  channelSequences?: Record<string, number>;
  event: WireEvent;
};

type WireStatus = {
  total: number;
  dispatched: number;
  pending: number;
  rejectedTotal: number;
};

type WireSnapshot = {
  status: WireStatus;
  retained: number;
  matching: number;
  page: number;
  pageSize: number;
  pageCount: number;
  records: WireRecord[];
  sourceCounts: ActivityCount[];
  typeCounts: ActivityCount[];
  hourCounts: ActivityCount[];
};

type WireEventDetail = {
  record: WireRecord;
  payloadAvailable: boolean;
  payload?: unknown;
};

type AgentActivitySnapshot = {
  total: number;
  active: number;
  runs: AgentActivityRun[];
};

type AgentWindow = {
  id: string;
  name: string;
  command: string;
  output: string;
  steps: AgentStep[];
};

type AgentStep = {
  id: string;
  type: string;
  status?: string;
  action: string;
  summary: string;
  detail?: string;
  output?: string;
  error?: string;
  payload: string;
};

type AgentView = {
  id: string;
  issueIdentifier: string;
  state: string;
  attempts: number;
  duplicateTriggers: number;
  detail?: string;
  createdAt: string;
  updatedAt: string;
  startedAt?: string;
  finishedAt?: string;
  observedAt: string;
  live: boolean;
  attachCommand?: string;
  windows: AgentWindow[];
};

type WorkflowSummary = {
  id: string;
  revision: number;
  name: string;
  enabled: boolean;
};

type WorkflowDefinition = WorkflowSummary & {
  markdown: string;
  updatedAt?: string;
};

type WorkflowDraft = {
  workflowId: string;
  revision: number;
  baseWorkflowRevision: number;
  name: string;
  enabled: boolean;
  markdown: string;
  updatedAt?: string;
};

type WorkflowReference = {
  kind: "protected" | "rule";
  id: string;
  name: string;
  enabled: boolean;
};

type WorkflowDocument = {
  workflowId: string;
  published?: WorkflowDefinition;
  draft: WorkflowDraft;
  savedDraft: boolean;
  draftConflict?: boolean;
  references: WorkflowReference[];
};

type WorkflowsResponse = {
  policyRevision: number;
  draftAvailable: boolean;
  draftError?: string;
  workflows: WorkflowDocument[];
};

type ProviderSettings = {
  model: string;
  effort: string;
};

type FactorySettings = {
  revision: number;
  updatedAt?: string;
  agents: {
    principal: ProviderSettings & { maxAttempts: number };
    codexChild: ProviderSettings;
    claudeChild: ProviderSettings;
  };
  runtime: {
    maxConcurrentRuns: number;
  };
};

type SettingsSaveResult = {
  snapshot: FactorySettings;
  conflict: boolean;
};

type SettingsSaveState =
  | "idle"
  | "dirty"
  | "saving"
  | "saved"
  | "conflict"
  | "failed";

type TriggerFilter = {
  source?: string;
  type?: string;
  action?: string;
  subject?: string;
  attributes?: Record<string, string>;
};

type TriggerRule = {
  id: string;
  revision: number;
  name: string;
  enabled: boolean;
  filter: TriggerFilter;
  workflowId: string;
  target: { kind: "fixed" | "subject" | "attribute"; value?: string };
  maxHop: number;
  maxOutstanding: number;
  admissionsPerHour: number;
};

type TriggerSchedule = {
  id: string;
  revision: number;
  name: string;
  enabled: boolean;
  cron: string;
  timezone: string;
  subject?: string;
  attributes?: Record<string, string[]>;
};

type TriggerRegistry = {
  schema: number;
  revision: number;
  updatedAt?: string;
  legacyRollbackIncompatible?: boolean;
  rules: TriggerRule[];
  schedules: TriggerSchedule[];
};

type TriggerRuleStatus = {
  ruleId: string;
  outstanding: number;
  admissionsLastHour: number;
};

type TriggerScheduleStatus = {
  scheduleId: string;
  last?: string;
  next?: string;
  skipped: number;
};

type TriggerInvocation = {
  id: string;
  eventId: string;
  ruleId: string;
  ruleRevision: number;
  workflowId: string;
  issueIdentifier: string;
  state: string;
  runId?: string;
  reason?: string;
  updatedAt: string;
};

type TriggerResponse = {
  registry: TriggerRegistry;
  settingsRevision: number;
  workflows: WorkflowSummary[];
  observedSources: string[];
  ruleStatus: TriggerRuleStatus[];
  scheduleStatus: TriggerScheduleStatus[];
  recentInvocations: TriggerInvocation[];
  protectedRoutes: { id: string; name: string; description: string; workflowId?: string; enabled: boolean; protected: boolean }[];
};

type TriggerSaveResult = { snapshot: TriggerResponse; conflict: boolean };
type SubjectFilterMode = "wildcard" | "absent" | "exact";

const refreshIntervalMs = 2000;

const activityPageSize = 25;

const observationTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "medium",
});

async function getHealth(): Promise<Health> {
  return getJSON<Health>("/api/healthz", "Health check");
}

async function getActivity(): Promise<ActivitySnapshot> {
  return getJSON<ActivitySnapshot>("/api/home", "Home request");
}

async function getWire(request: string): Promise<WireSnapshot> {
  return getJSON<WireSnapshot>(request, "Wire request");
}

async function getWireEvent(sequence: number): Promise<WireEventDetail> {
  return getJSON<WireEventDetail>(
    `/api/wire/${sequence}`,
    "Wire event request",
  );
}

async function getAgentActivity(): Promise<AgentActivitySnapshot> {
  return getJSON<AgentActivitySnapshot>(
    "/api/agents",
    "Agent activity request",
  );
}

async function getTasks(request: string): Promise<TasksResponse> {
  return getJSON<TasksResponse>(request, "Task index request");
}

async function getTaskProjects(): Promise<TaskProjectsResponse> {
  return getJSON<TaskProjectsResponse>("/api/task-projects", "Task project request");
}

async function getTaskDetail(provider: string, id: string): Promise<NativeTaskDetail | TaskSummary> {
  return getJSON<NativeTaskDetail | TaskSummary>(
    `/api/tasks/${encodeURIComponent(provider)}/${encodeURIComponent(id)}`,
    "Task detail request",
  );
}

class TaskConflict extends Error {
  constructor(readonly current: unknown) {
    super("A newer task revision is available");
  }
}

async function taskRequest<T>(url: string, method: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method,
    cache: "no-store",
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": crypto.randomUUID(),
    },
    body: JSON.stringify(body),
  });
  if (response.status === 409) {
    const text = await response.text();
    let current: unknown = text;
    try {
      current = text ? JSON.parse(text) : undefined;
    } catch {
      // Preserve a plain-text conflict reason.
    }
    throw new TaskConflict(current);
  }
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(detail || `Task request failed with ${response.status}`);
  }
  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

async function getAgentByReference(
  taskIdentifier: string,
  startedAt: string,
  source?: TaskRef["source"],
): Promise<AgentView> {
  const query = source ? `?source=${source}` : "";
  return getJSON<AgentView>(
    `/api/agents/${encodeURIComponent(taskIdentifier)}/${encodeURIComponent(startedAt)}/run${query}`,
    "Agent request",
  );
}

async function getSettings(): Promise<FactorySettings> {
  return getJSON<FactorySettings>("/api/settings", "Settings request");
}

async function getTriggers(): Promise<TriggerResponse> {
  return getJSON<TriggerResponse>("/api/triggers", "Triggers request");
}

async function getWorkflows(): Promise<WorkflowsResponse> {
  return getJSON<WorkflowsResponse>("/api/workflows", "Workflows request");
}

async function createWorkflowDraft(): Promise<WorkflowDraft> {
  return workflowRequest<WorkflowDraft>("/api/workflow-drafts", "POST");
}

async function saveWorkflowDraft(draft: WorkflowDraft): Promise<WorkflowDraft> {
  return workflowRequest<WorkflowDraft>(`/api/workflow-drafts/${encodeURIComponent(draft.workflowId)}`, "PUT", {
    expectedDraftRevision: draft.revision,
    expectedWorkflowRevision: draft.baseWorkflowRevision,
    name: draft.name,
    enabled: draft.enabled,
    markdown: draft.markdown,
  });
}

async function discardWorkflowDraft(draft: WorkflowDraft): Promise<void> {
  await workflowRequest<void>(`/api/workflow-drafts/${encodeURIComponent(draft.workflowId)}`, "DELETE", {
    expectedDraftRevision: draft.revision,
    expectedWorkflowRevision: draft.baseWorkflowRevision,
  });
}

async function publishWorkflowDraft(draft: WorkflowDraft, policyRevision: number): Promise<void> {
  await workflowRequest<void>(`/api/workflow-drafts/${encodeURIComponent(draft.workflowId)}/publish`, "POST", {
    expectedDraftRevision: draft.revision,
    expectedWorkflowRevision: draft.baseWorkflowRevision,
    expectedPolicyRevision: policyRevision,
  });
}

async function deletePublishedWorkflow(document: WorkflowDocument, policyRevision: number): Promise<void> {
  if (!document.published) return;
  await workflowRequest<void>(`/api/workflows/${encodeURIComponent(document.workflowId)}`, "DELETE", {
    expectedWorkflowRevision: document.published.revision,
    expectedPolicyRevision: policyRevision,
  });
}

async function saveProtectedFeedback(snapshot: TriggerResponse, workflowId: string): Promise<TriggerResponse> {
  return workflowRequest<TriggerResponse>("/api/triggers/protected/linear-feedback", "PUT", {
    expectedPolicyRevision: snapshot.settingsRevision,
    workflowId,
  });
}

class WorkflowConflict extends Error {
  constructor(readonly snapshot: WorkflowsResponse) {
    super("A newer workflow revision is available");
  }
}

async function workflowRequest<T>(url: string, method: string, body?: unknown): Promise<T> {
  const response = await fetch(url, {
    method,
    cache: "no-store",
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (response.status === 409) {
    throw new WorkflowConflict((await response.json()) as WorkflowsResponse);
  }
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(detail || `Workflow request failed with ${response.status}`);
  }
  if (response.status === 204) return undefined as T;
  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

async function saveTriggers(candidate: TriggerRegistry): Promise<TriggerSaveResult> {
  const response = await fetch("/api/triggers", {
    method: "PUT",
    cache: "no-store",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(candidate),
  });
  if (response.status === 409) {
    return { snapshot: (await response.json()) as TriggerResponse, conflict: true };
  }
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(detail || `Trigger update failed with ${response.status}`);
  }
  return { snapshot: (await response.json()) as TriggerResponse, conflict: false };
}

async function saveSettings(
  candidate: FactorySettings,
): Promise<SettingsSaveResult> {
  const response = await fetch("/api/settings", {
    method: "PUT",
    cache: "no-store",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(candidate),
  });
  if (response.status === 409) {
    return {
      snapshot: (await response.json()) as FactorySettings,
      conflict: true,
    };
  }
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(detail || `Settings update failed with ${response.status}`);
  }
  return {
    snapshot: (await response.json()) as FactorySettings,
    conflict: false,
  };
}

function resourceSnapshot<T>(resource: Resource<T>): T | undefined {
  return resource.error ? undefined : resource();
}
function HomePage(): JSX.Element {
  const [health] = createResource(getHealth);
  const healthSnapshot = (): Health | undefined => resourceSnapshot(health);

  return (
    <main class="home-page">
      <section class="home-shell" aria-labelledby="factory-title">
        <div class="eyebrow">
          <span class="mark" aria-hidden="true">
            F
          </span>
          <span>nags.cloud</span>
        </div>

        <div class="content">
          <h1 class="home-title" id="factory-title">
            Factory
          </h1>
          <p class="lede">
            The floor is empty. The machinery is warming up. Something useful
            will be built here.
          </p>
        </div>

        <footer class="home-footer">
          <div class="status" aria-live="polite">
            <span
              classList={{
                dot: true,
                ready: healthSnapshot()?.status === "ok",
                failed: Boolean(health.error),
              }}
            />
            <Show when={!health.loading} fallback={<span>Connecting</span>}>
              <span>
                {health.error
                  ? "Offline"
                  : `Systems online · ${shortOID(healthSnapshot()?.commit)} · contract ${healthSnapshot()?.contractVersion}`}
              </span>
            </Show>
          </div>
          <a class="text-link" href="/home">
            View activity
          </a>
        </footer>
      </section>
    </main>
  );
}

function ActivityPage(): JSX.Element {
  const [activity, { refetch }] = createResource(getActivity);
  const activitySnapshot = (): ActivitySnapshot | undefined => resourceSnapshot(activity);

  onMount(() => {
    document.title = "Home | Factory";
    const timer = window.setInterval(() => void refetch(), refreshIntervalMs);
    onCleanup(() => window.clearInterval(timer));
  });

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="activity-title">
        <ActivityHeader
          section="home"
          state={resourceState(activity.loading, activity.error)}
          label={listenerLabel(activity.loading, activity.error, Boolean(activitySnapshot()))}
        />

        <div class="activity-hero overview-hero">
          <div>
            <p class="section-label">Factory telemetry</p>
            <h1 class="activity-title" id="activity-title">
              Home
            </h1>
          </div>
          <p class="activity-intro">
            A quiet overview of webhook traffic and autonomous issue loops. Open
            a dedicated workspace when you need the underlying detail.
          </p>
        </div>

        <dl class="activity-summary overview-summary">
          <div>
            <dt>Status</dt>
            <dd>{activity.error ? "Unavailable" : "Listening"}</dd>
          </div>
          <div>
            <dt>Verified deliveries</dt>
            <dd>{activitySnapshot()?.total ?? 0}</dd>
          </div>
          <div>
            <dt>Last received</dt>
            <dd>{formatTime(activitySnapshot()?.lastReceivedAt)}</dd>
          </div>
          <div>
            <dt>Agent runs</dt>
            <dd>{activitySnapshot()?.agentRuns.total ?? 0}</dd>
          </div>
          <div>
            <dt>Active loops</dt>
            <dd>{activitySnapshot()?.agentRuns.active ?? 0}</dd>
          </div>
        </dl>

        <section class="activity-destinations" aria-label="Detailed activity">
          <a class="destination destination-linear" href="/wire">
            <span class="destination-index">01 / Wire</span>
            <strong>Inspect the system event wire</strong>
            <p>
              Filter every retained source and event type, then open normalized
              metadata and available Linear payloads.
            </p>
            <span class="destination-meta">
              {activitySnapshot()?.events.filter((event) => !event.type.startsWith("github/"))
                .length ?? 0} recent events
            </span>
          </a>
          <a class="destination destination-agents" href="/agents">
            <span class="destination-index">02 / Agents</span>
            <strong>Follow autonomous work</strong>
            <p>
              Review loop state by task, then enter the authenticated live tmux
              observer for a specific run.
            </p>
            <span class="destination-meta">
              {activitySnapshot()?.agentRuns.active ?? 0} active now
            </span>
          </a>
        </section>

        <footer class="activity-footer">
          <span>Detailed activity requires operator authentication.</span>
          <a class="text-link" href="/">
            Back to Factory
          </a>
        </footer>
      </section>
    </main>
  );
}

function WirePage(): JSX.Element {
  const [page, setPage] = createSignal(1);
  const [source, setSource] = createSignal("");
  const [eventType, setEventType] = createSignal("");
  const [selectedSequence, setSelectedSequence] = createSignal<number>();
  const request = createMemo(() => {
    const query = new URLSearchParams({
      page: String(page()),
      pageSize: String(activityPageSize),
    });
    if (source()) query.set("source", source());
    if (eventType()) query.set("type", eventType());
    return `/api/wire?${query.toString()}`;
  });
  const [activity, { refetch }] = createResource(request, getWire);
  const [eventDetail] = createResource(selectedSequence, getWireEvent);
  const activitySnapshot = (): WireSnapshot | undefined => resourceSnapshot(activity);
  const eventSnapshot = (): WireEventDetail | undefined => resourceSnapshot(eventDetail);

  onMount(() => {
    document.title = "System wire | Factory";
    const timer = window.setInterval(() => void refetch(), 5000);
    onCleanup(() => window.clearInterval(timer));
  });

  function changePage(nextPage: number): void {
    setSelectedSequence(undefined);
    setPage(nextPage);
  }

  function changeFilter(setter: (value: string) => void, value: string): void {
    setter(value);
    setSelectedSequence(undefined);
    setPage(1);
  }

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="wire-title">
        <ActivityHeader
          section="wire"
          state={resourceState(activity.loading, activity.error)}
          label={activity.error ? "Event wire unavailable" : "Private system wire"}
        />

        <div class="activity-hero detail-hero">
          <div>
            <p class="section-label">Authenticated telemetry</p>
            <h1 class="activity-title compact-title" id="wire-title">
              Wire
            </h1>
          </div>
          <p class="activity-intro">
            The journal-backed stream for Linear, GitHub, and Factory events.
            Unknown future event types remain inspectable as normalized records.
          </p>
        </div>

        <dl class="activity-summary detail-summary">
          <div>
            <dt>Retained events</dt>
            <dd>{activitySnapshot()?.retained ?? 0}</dd>
          </div>
          <div>
            <dt>Matching events</dt>
            <dd>{activitySnapshot()?.matching ?? 0}</dd>
          </div>
          <div>
            <dt>Pending dispatch</dt>
            <dd>{activitySnapshot()?.status.pending ?? 0}</dd>
          </div>
          <div>
            <dt>Rejected total</dt>
            <dd>{activitySnapshot()?.status.rejectedTotal ?? 0}</dd>
          </div>
        </dl>

        <form class="wire-filters" aria-label="Wire filters" onSubmit={(event) => event.preventDefault()}>
          <label>
            <span>Source</span>
            <select value={source()} onChange={(event) => changeFilter(setSource, event.currentTarget.value)}>
              <option value="">All sources</option>
              <For each={activitySnapshot()?.sourceCounts ?? []}>
                {(count) => <option value={count.label}>{count.label} ({count.count})</option>}
              </For>
            </select>
          </label>
          <label>
            <span>Event type</span>
            <select value={eventType()} onChange={(event) => changeFilter(setEventType, event.currentTarget.value)}>
              <option value="">All event types</option>
              <For each={activitySnapshot()?.typeCounts ?? []}>
                {(count) => <option value={count.label}>{count.label} ({count.count})</option>}
              </For>
            </select>
          </label>
        </form>

        <Show
          when={!activity.error}
          fallback={<InlineError message="The system wire could not be loaded." />}
        >
          <section class="chart-grid" aria-label="System wire charts">
            <ActivityChart
              title="Events by source"
              subtitle="Current retained window"
              items={activitySnapshot()?.sourceCounts ?? []}
            />
            <ActivityChart
              title="Recent hourly volume"
              subtitle="Up to twelve active UTC hours"
              items={activitySnapshot()?.hourCounts ?? []}
            />
          </section>

          <section class="linear-browser" aria-labelledby="event-browser-title">
            <div class="feed-heading browser-heading">
              <div>
                <h2 id="event-browser-title">Event ledger</h2>
                <span>Select a record to inspect normalized metadata</span>
              </div>
              <Pagination
                page={page()}
                pageCount={activitySnapshot()?.pageCount ?? 0}
                onChange={changePage}
              />
            </div>

            <div class="event-workspace">
              <div class="event-scroll" tabIndex={0} aria-label="System events">
                <Show
                  when={!activity.loading || Boolean(activitySnapshot())}
                  fallback={<LoadingRows />}
                >
                  <Show
                    when={(activitySnapshot()?.records.length ?? 0) > 0}
                    fallback={
                      <div class="empty-state compact">
                        <strong>No events match these filters.</strong>
                        <span>Change the filters or wait for the next journal record.</span>
                      </div>
                    }
                  >
                    <ol class="event-list selectable-events">
                      <For each={activitySnapshot()?.records ?? []}>
                        {(record) => (
                          <li>
                            <button
                              class="event-row event-button"
                              classList={{ selected: selectedSequence() === record.sequence }}
                              type="button"
                              aria-pressed={selectedSequence() === record.sequence}
                              onClick={() => setSelectedSequence(record.sequence)}
                            >
                              <time datetime={record.event.receivedAt}>
                                {formatTime(record.event.receivedAt)}
                              </time>
                              <strong>{record.event.source}</strong>
                              <span>{record.event.type}</span>
                              <i>#{record.sequence} · {record.event.action}</i>
                            </button>
                          </li>
                        )}
                      </For>
                    </ol>
                  </Show>
                </Show>
              </div>

              <aside class="payload-panel" aria-live="polite" aria-labelledby="payload-title">
                <Show
                  when={selectedSequence() !== undefined}
                  fallback={
                    <div class="payload-placeholder">
                      <span class="section-label">Normalized event</span>
                      <strong>Choose a record</strong>
                      <p>Journal metadata and any retained Linear body will open here.</p>
                    </div>
                  }
                >
                  <Show
                    when={!eventDetail.loading}
                    fallback={<div class="payload-placeholder"><strong>Loading payload</strong></div>}
                  >
                    <Show
                      when={eventSnapshot()}
                      fallback={<InlineError message="This event could not be loaded." />}
                    >
                      {(detail) => (
                        <>
                          <div class="payload-heading">
                            <div>
                              <span class="section-label">{detail().record.event.source} · #{detail().record.sequence}</span>
                              <h2 id="payload-title">{detail().record.event.type}</h2>
                            </div>
                            <time datetime={detail().record.event.receivedAt}>
                              {formatTime(detail().record.event.receivedAt)}
                            </time>
                          </div>
                          <dl class="wire-metadata">
                            <div><dt>Action</dt><dd>{detail().record.event.action}</dd></div>
                            <div><dt>Subject</dt><dd>{detail().record.event.subject || "None"}</dd></div>
                            <For each={attributeEntries(detail().record.event.attributes)}>
                              {([key, values]) => <div><dt>{key}</dt><dd>{values.join(", ")}</dd></div>}
                            </For>
                          </dl>
                          <Show
                            when={detail().payloadAvailable}
                            fallback={
                              <div class="payload-unavailable">
                                <strong>Payload not retained</strong>
                                <p>Only available Linear bodies are attached to normalized records.</p>
                              </div>
                            }
                          >
                            <pre class="payload-code" tabIndex={0}>
                              <code>{formatPayload(detail().payload)}</code>
                            </pre>
                          </Show>
                        </>
                      )}
                    </Show>
                  </Show>
                </Show>
              </aside>
            </div>
          </section>
        </Show>

        <footer class="activity-footer">
          <span>Normalized records are journal authority; payloads remain private sidecars.</span>
          <a class="text-link" href="/home">
            Back to home
          </a>
        </footer>
      </section>
    </main>
  );
}

function AgentActivityPage(): JSX.Element {
  const [activity, { refetch }] = createResource(getAgentActivity);
  const activitySnapshot = (): AgentActivitySnapshot | undefined => resourceSnapshot(activity);
  const stateCounts = createMemo(() => countRunStates(activitySnapshot()?.runs ?? []));

  onMount(() => {
    document.title = "Agent activity | Factory";
    const timer = window.setInterval(() => void refetch(), 5000);
    onCleanup(() => window.clearInterval(timer));
  });

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="agents-title">
        <ActivityHeader
          section="agents"
          state={resourceState(activity.loading, activity.error)}
          label={activity.error ? "Run store unavailable" : "Private run index"}
        />

        <div class="activity-hero detail-hero">
          <div>
            <p class="section-label">Autonomous delivery</p>
            <h1 class="activity-title compact-title" id="agents-title">
              Agents
            </h1>
          </div>
          <p class="activity-intro">
            Every retained Factory loop, addressed by its task identifier and start
            time. Enter a run to observe the live session or durable result.
          </p>
        </div>

        <dl class="activity-summary detail-summary">
          <div>
            <dt>Total runs</dt>
            <dd>{activitySnapshot()?.total ?? 0}</dd>
          </div>
          <div>
            <dt>Active loops</dt>
            <dd>{activitySnapshot()?.active ?? 0}</dd>
          </div>
          <div>
            <dt>Terminal runs</dt>
            <dd>{Math.max(0, (activitySnapshot()?.runs.length ?? 0) - (activitySnapshot()?.active ?? 0))}</dd>
          </div>
        </dl>

        <Show
          when={!activity.error}
          fallback={<InlineError message="Agent activity could not be loaded." />}
        >
          <section class="agent-overview-grid">
            <ActivityChart
              title="Runs by state"
              subtitle="Current retained window"
              items={stateCounts()}
            />
            <div class="run-pulse" aria-label="Current run status">
              <span class="section-label">Live capacity</span>
              <strong>{activitySnapshot()?.active ?? 0}</strong>
              <p>
                {(activitySnapshot()?.active ?? 0) === 1 ? "loop is" : "loops are"} active across the
                Factory runner.
              </p>
            </div>
          </section>

          <section class="run-feed dedicated-run-feed" aria-labelledby="run-feed-title">
            <div class="feed-heading">
              <h2 id="run-feed-title">Run ledger</h2>
              <span>Task context is authenticated</span>
            </div>

            <Show
              when={!activity.loading || Boolean(activitySnapshot())}
              fallback={<LoadingRows />}
            >
              <Show
                when={(activitySnapshot()?.runs.length ?? 0) > 0}
                fallback={
                  <div class="empty-state compact">
                    <strong>No agent run has been claimed.</strong>
                    <span>Apply the Factory label to a Linear issue.</span>
                  </div>
                }
              >
                <ol class="run-list private-run-list">
                  <For each={activitySnapshot()?.runs ?? []}>
                    {(run) => (
                      <li class="run-row private-run-row">
                        <Show
                          when={agentRunHref(run)}
                          fallback={<span class="run-link issue-link pending">{run.issueIdentifier}</span>}
                        >
                          {(href) => <a class="run-link issue-link" href={href()}>{run.issueIdentifier}</a>}
                        </Show>
                        <strong class={`run-state ${run.state}`}>
                          {runStateLabel(run.state)}
                        </strong>
                        <span title={runLifecycleDetail(run)}>
                          {runLifecycleDetail(run)}
                        </span>
                        <time datetime={run.lastAuthoritativeRefreshAt ?? run.startedAt ?? run.createdAt}>
                          {run.lastAuthoritativeRefreshAt ? "REFRESH " : "START "}
                          {formatTime(run.lastAuthoritativeRefreshAt ?? run.startedAt ?? run.createdAt)}
                        </time>
                      </li>
                    )}
                  </For>
                </ol>
              </Show>
            </Show>
          </section>
        </Show>

        <footer class="activity-footer">
          <span>Live pane output is authenticated, redacted, and read-only.</span>
          <a class="text-link" href="/home">
            Back to home
          </a>
        </footer>
      </section>
    </main>
  );
}

function TriggersPage(): JSX.Element {
  const [triggers] = createResource(getTriggers);
  const triggerSnapshot = (): TriggerResponse | undefined => resourceSnapshot(triggers);

  onMount(() => {
    document.title = "Triggers | Factory";
  });

  return (
    <main class="activity-page settings-page" id="main-content">
      <section class="activity-shell settings-shell" aria-labelledby="triggers-title">
        <ActivityHeader
          section="triggers"
          state={resourceState(triggers.loading, triggers.error)}
          label={triggers.error ? "Trigger registry unavailable" : "Admission policy"}
        />
        <Show
          when={triggerSnapshot()}
          fallback={
            <div class="settings-loading" aria-live="polite">
              <p class="section-label">Event admission</p>
              <h1 class="activity-title compact-title" id="triggers-title">
                {triggers.error ? "Triggers unavailable" : "Opening registry"}
              </h1>
              <Show when={triggers.error}>
                <InlineError message="The trigger registry could not be loaded." />
              </Show>
            </div>
          }
        >
          {(snapshot) => <TriggersEditor initial={snapshot()} />}
        </Show>
      </section>
    </main>
  );
}

function TriggersEditor(props: { initial: TriggerResponse }): JSX.Element {
  const [response, setResponse] = createSignal(structuredClone(props.initial));
  const [draft, setDraft] = createSignal(structuredClone(props.initial.registry));
  const [saveState, setSaveState] = createSignal<SettingsSaveState>("idle");
  const [message, setMessage] = createSignal("");
  const [pendingDelete, setPendingDelete] = createSignal("");
  const [broadConfirmed, setBroadConfirmed] = createSignal(false);
  const [protectedWorkflow, setProtectedWorkflow] = createSignal(
    props.initial.protectedRoutes.find((route) => route.id === "linear-feedback")?.workflowId ?? "",
  );
  const [protectedState, setProtectedState] = createSignal<SettingsSaveState>("idle");
  const [protectedMessage, setProtectedMessage] = createSignal("");
  const enabledWorkflows = (): WorkflowSummary[] =>
    response().workflows.filter((workflow) => workflow.enabled);

  function update(mutator: (registry: TriggerRegistry) => void): void {
    setDraft((current) => {
      const next = structuredClone(current);
      mutator(next);
      return next;
    });
    setSaveState("dirty");
    setMessage("Unsaved admission changes");
    setBroadConfirmed(false);
  }

  function addRule(source?: TriggerRule): void {
    update((next) => {
      const id = uniqueTriggerID("rule", [...next.rules.map((rule) => rule.id), ...next.schedules.map((schedule) => schedule.id)]);
      const workflow = enabledWorkflows()[0];
      next.rules.push(source ? {
        ...structuredClone(source),
        id,
        revision: 0,
        name: `${source.name} copy`,
      } : {
        id,
        revision: 0,
        name: "New event rule",
        enabled: false,
        filter: { attributes: {} },
        workflowId: workflow?.id ?? "",
        target: { kind: "subject" },
        maxHop: 4,
        maxOutstanding: 10,
        admissionsPerHour: 120,
      });
    });
  }

  function addSchedule(): void {
    update((next) => {
      const id = uniqueTriggerID("schedule", [...next.rules.map((rule) => rule.id), ...next.schedules.map((schedule) => schedule.id)]);
      next.schedules.push({
        id,
        revision: 0,
        name: "New schedule",
        enabled: false,
        cron: "0 8 * * *",
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
        attributes: {},
      });
    });
  }

  function remove(kind: "rule" | "schedule", id: string): void {
    const key = `${kind}:${id}`;
    if (pendingDelete() !== key) {
      setPendingDelete(key);
      return;
    }
    update((next) => {
      if (kind === "rule") {
        next.rules = next.rules.filter((rule) => rule.id !== id);
      } else {
        next.schedules = next.schedules.filter((schedule) => schedule.id !== id);
      }
    });
    setPendingDelete("");
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const problem = validateTriggerDraft(draft(), enabledWorkflows());
    if (problem) {
      setSaveState("failed");
      setMessage(problem);
      return;
    }
    const broadRules = draft().rules.filter((rule) => rule.enabled && broadTriggerRule(rule));
    if (broadRules.length > 0 && !broadConfirmed()) {
      setBroadConfirmed(true);
      setSaveState("dirty");
      setMessage(`Review broad scope for ${broadRules.map((rule) => rule.name).join(", ")}, then save again.`);
      return;
    }
    setSaveState("saving");
    setMessage("Saving one coordinated registry revision");
    try {
      const result = await saveTriggers(draft());
      setResponse(structuredClone(result.snapshot));
      setDraft(structuredClone(result.snapshot.registry));
      setBroadConfirmed(false);
      if (result.conflict) {
        setSaveState("conflict");
        setMessage("A newer registry revision was loaded. Review it before saving again.");
      } else {
        setSaveState("saved");
        setMessage(`Revision ${result.snapshot.registry.revision} saved`);
      }
    } catch (error) {
      setSaveState("failed");
      setMessage(error instanceof Error ? error.message : "Trigger update failed");
    }
  }

  const ruleStatus = (id: string): TriggerRuleStatus | undefined =>
    response().ruleStatus.find((status) => status.ruleId === id);
  const scheduleStatus = (id: string): TriggerScheduleStatus | undefined =>
    response().scheduleStatus.find((status) => status.scheduleId === id);

  async function updateProtectedFeedback(): Promise<void> {
    setProtectedState("saving");
    setProtectedMessage("Updating protected policy binding");
    try {
      const next = await saveProtectedFeedback(response(), protectedWorkflow());
      setResponse(structuredClone(next));
      setProtectedWorkflow(next.protectedRoutes.find((route) => route.id === "linear-feedback")?.workflowId ?? "");
      setProtectedState("saved");
      setProtectedMessage(`Policy revision ${next.settingsRevision} saved`);
    } catch (error) {
      setProtectedState(error instanceof WorkflowConflict ? "conflict" : "failed");
      setProtectedMessage(error instanceof Error ? error.message : "Protected binding update failed");
    }
  }

  return (
    <>
      <div class="settings-hero trigger-hero">
        <p class="section-label">Event admission</p>
        <h1 class="activity-title compact-title" id="triggers-title">Triggers</h1>
        <p class="settings-intro">
          Match normalized wire events to pinned workflows. Every matching rule creates its own serialized invocation; schedules only produce events.
        </p>
        <dl class="settings-revision trigger-revision">
          <div><dt>Registry revision</dt><dd>{draft().revision}</dd></div>
          <div><dt>Configured rules</dt><dd>{draft().rules.length} / 32</dd></div>
          <div><dt>Schedules</dt><dd>{draft().schedules.length} / 32</dd></div>
          <div><dt>Compatibility</dt><dd>{draft().legacyRollbackIncompatible ? "Forward only" : "Legacy readable"}</dd></div>
        </dl>
      </div>

      <form class="settings-form trigger-form" onSubmit={submit}>
        <section class="settings-section" aria-labelledby="rules-title">
          <div class="settings-section-heading workflow-heading">
            <div>
              <h2 id="rules-title">Event rules</h2>
              <p>Omitted fields are wildcards. Exact attributes use AND semantics, and all matching rules fire in stable ID order.</p>
            </div>
            <button class="secondary-button" type="button" disabled={draft().rules.length >= 32} onClick={() => addRule()}>
              Add rule
            </button>
          </div>
          <Show when={draft().rules.length > 0} fallback={<TriggerEmpty title="No configured rules" detail="Add a disabled rule, define its scope, then enable it when the preview is right." />}>
            <div class="trigger-editor-list">
              <For each={draft().rules}>
                {(rule, index) => (
                  <RuleEditor
                    rule={rule}
                    status={ruleStatus(rule.id)}
                    workflows={enabledWorkflows()}
                    observedSources={response().observedSources}
                    pendingDelete={pendingDelete() === `rule:${rule.id}`}
                    onChange={(mutator) => update((next) => mutator(next.rules[index()]))}
                    onClone={() => addRule(rule)}
                    onRemove={() => remove("rule", rule.id)}
                  />
                )}
              </For>
            </div>
          </Show>
        </section>

        <section class="settings-section" aria-labelledby="schedules-title">
          <div class="settings-section-heading workflow-heading">
            <div>
              <h2 id="schedules-title">Cron schedules</h2>
              <p>Schedules emit <code>factory / cron / due</code>. They never select a workflow directly.</p>
            </div>
            <button class="secondary-button" type="button" disabled={draft().schedules.length >= 32} onClick={addSchedule}>
              Add schedule
            </button>
          </div>
          <Show when={draft().schedules.length > 0} fallback={<TriggerEmpty title="No schedules" detail="Add a five-field cron schedule when Factory should place a due event on the wire." />}>
            <div class="trigger-editor-list">
              <For each={draft().schedules}>
                {(schedule, index) => (
                  <ScheduleEditor
                    schedule={schedule}
                    status={scheduleStatus(schedule.id)}
                    pendingDelete={pendingDelete() === `schedule:${schedule.id}`}
                    onChange={(mutator) => update((next) => mutator(next.schedules[index()]))}
                    onRemove={() => remove("schedule", schedule.id)}
                  />
                )}
              </For>
            </div>
          </Show>
        </section>

        <section class="settings-section protected-section" aria-labelledby="protected-title">
          <div class="settings-section-heading">
            <h2 id="protected-title">Protected lifecycle routes</h2>
            <p>Configured rules are additive. Protected routes stay enabled; the feedback route can select any enabled published workflow.</p>
          </div>
          <div class="protected-route-list">
            <For each={response().protectedRoutes}>
              {(route) => (
                <article>
                  <span>Protected · always enabled</span>
                  <h3>{route.name}</h3>
                  <p>{route.description}</p>
                  <Show when={route.id === "linear-feedback"}>
                    <div class="protected-binding">
                      <Field label="Workflow">
                        <select value={protectedWorkflow()} onChange={(event) => { setProtectedWorkflow(event.currentTarget.value); setProtectedState("dirty"); setProtectedMessage("Binding change not saved"); }}>
                          <For each={enabledWorkflows()}>{(workflow) => <option value={workflow.id}>{workflow.name} · r{workflow.revision}</option>}</For>
                        </select>
                      </Field>
                      <button class="secondary-button" type="button" disabled={["idle", "saving", "saved"].includes(protectedState())} onClick={() => void updateProtectedFeedback()}>
                        {protectedState() === "saving" ? "Saving" : "Update binding"}
                      </button>
                      <small classList={{ failed: ["failed", "conflict"].includes(protectedState()) }} aria-live="polite">{protectedMessage()}</small>
                    </div>
                  </Show>
                </article>
              )}
            </For>
          </div>
        </section>

        <section class="settings-section" aria-labelledby="recent-title">
          <div class="settings-section-heading">
            <h2 id="recent-title">Recent routing outcomes</h2>
            <p>Safe routing summaries only. Payloads, commands, paths, and pinned workflow files stay private.</p>
          </div>
          <Show when={response().recentInvocations.length > 0} fallback={<TriggerEmpty title="No invocation has been admitted" detail="Matching events and visible suppression outcomes will appear here." />}>
            <div class="invocation-ledger" tabIndex={0}>
              <For each={response().recentInvocations}>
                {(invocation) => (
                  <article>
                    <strong>{invocation.issueIdentifier || invocation.eventId}</strong>
                    <span>{invocation.ruleId} · r{invocation.ruleRevision}</span>
                    <i class={`run-state ${invocation.state}`}>{runStateLabel(invocation.state)}</i>
                    <time datetime={invocation.updatedAt}>{formatTime(invocation.updatedAt)}</time>
                  </article>
                )}
              </For>
            </div>
          </Show>
        </section>

        <div class={`settings-save ${saveState()}`}>
          <div aria-live="polite" role={saveState() === "failed" ? "alert" : "status"}>
            <strong>{saveStateLabel(saveState())}</strong>
            <span>{message() || "No unsaved changes"}</span>
          </div>
          <button class="primary-button" type="submit" disabled={["idle", "saving", "saved"].includes(saveState())}>
            {triggerSaveButtonLabel(saveState(), broadConfirmed())}
          </button>
        </div>
      </form>

      <footer class="activity-footer settings-footer">
        <span>Every save is optimistic, coordinated, and revisioned.</span>
        <a class="text-link" href="/wire">Inspect the wire</a>
      </footer>
    </>
  );
}

function RuleEditor(props: {
  rule: TriggerRule;
  status?: TriggerRuleStatus;
  workflows: WorkflowSummary[];
  observedSources: string[];
  pendingDelete: boolean;
  onChange: (mutator: (rule: TriggerRule) => void) => void;
  onClone: () => void;
  onRemove: () => void;
}): JSX.Element {
  const [subjectSelection, setSubjectSelection] = createSignal(subjectFilterMode(props.rule.filter.subject));
  return (
    <article class="trigger-editor" aria-labelledby={`rule-${props.rule.id}`}>
      <header class="trigger-editor-header">
        <div>
          <span class="workflow-id">{props.rule.id} · revision {props.rule.revision || "new"}</span>
          <h3 id={`rule-${props.rule.id}`}>{props.rule.name || "Untitled rule"}</h3>
          <p class="scope-summary">{ruleScopeSummary(props.rule)}</p>
        </div>
        <div class="trigger-card-actions">
          <Toggle checked={props.rule.enabled} compact label={props.rule.enabled ? "Enabled" : "Disabled"} onChange={(enabled) => props.onChange((rule) => { rule.enabled = enabled; })} />
          <button class="text-button" type="button" onClick={props.onClone}>Clone</button>
          <button class="text-button danger-button" type="button" onClick={props.onRemove}>{props.pendingDelete ? "Confirm removal" : "Remove"}</button>
        </div>
      </header>
      <dl class="trigger-card-status">
        <div><dt>Outstanding</dt><dd>{props.status?.outstanding ?? 0} / {props.rule.maxOutstanding}</dd></div>
        <div><dt>Last hour</dt><dd>{props.status?.admissionsLastHour ?? 0} / {props.rule.admissionsPerHour}</dd></div>
        <div><dt>Hop ceiling</dt><dd>{props.rule.maxHop}</dd></div>
      </dl>
      <div class="trigger-field-grid identity-fields">
        <Field label="Stable ID" hint={props.rule.revision ? "IDs cannot change after creation." : "Lowercase letters, numbers, and hyphens."}>
          <input required readOnly={props.rule.revision > 0} pattern="[a-z0-9][a-z0-9-]{0,47}" maxlength={48} value={props.rule.id} onInput={(event) => props.onChange((rule) => { rule.id = event.currentTarget.value; })} />
        </Field>
        <Field label="Rule name"><input required maxlength={80} value={props.rule.name} onInput={(event) => props.onChange((rule) => { rule.name = event.currentTarget.value; })} /></Field>
      </div>
      <fieldset class="trigger-subsection">
        <legend>Exact event filter</legend>
        <div class="trigger-field-grid filter-fields">
          <Field label="Source" hint="Leave empty for any source.">
            <input list={`observed-trigger-sources-${props.rule.id}`} maxlength={48} pattern="[a-z0-9-]*" value={props.rule.filter.source ?? ""} onInput={(event) => props.onChange((rule) => { rule.filter.source = optional(event.currentTarget.value); })} />
          </Field>
          <datalist id={`observed-trigger-sources-${props.rule.id}`}><For each={props.observedSources}>{(source) => <option value={source} />}</For></datalist>
          <Field label="Type"><input maxlength={256} value={props.rule.filter.type ?? ""} onInput={(event) => props.onChange((rule) => { rule.filter.type = optional(event.currentTarget.value); })} /></Field>
          <Field label="Action"><input maxlength={256} value={props.rule.filter.action ?? ""} onInput={(event) => props.onChange((rule) => { rule.filter.action = optional(event.currentTarget.value); })} /></Field>
          <Field label="Subject mode">
            <select value={subjectSelection()} onChange={(event) => {
              const mode = event.currentTarget.value as SubjectFilterMode;
              setSubjectSelection(mode);
              props.onChange((rule) => { rule.filter.subject = subjectFilterValue(mode); });
            }}>
              <option value="wildcard">Any subject</option><option value="absent">Subject absent</option><option value="exact">Exact subject</option>
            </select>
          </Field>
          <Show when={subjectSelection() === "exact"}>
            <Field label="Exact subject"><input required maxlength={256} value={props.rule.filter.subject ?? ""} onInput={(event) => props.onChange((rule) => { rule.filter.subject = event.currentTarget.value; })} /></Field>
          </Show>
        </div>
        <ExactAttributeEditor values={props.rule.filter.attributes ?? {}} onChange={(attributes) => props.onChange((rule) => { rule.filter.attributes = attributes; })} />
      </fieldset>
      <fieldset class="trigger-subsection">
        <legend>Workflow and target</legend>
        <div class="trigger-field-grid target-fields">
          <Field label="Workflow"><select required value={props.rule.workflowId} onChange={(event) => props.onChange((rule) => { rule.workflowId = event.currentTarget.value; })}><For each={props.workflows}>{(workflow) => <option value={workflow.id}>{workflow.name}</option>}</For></select></Field>
          <Field label="Issue target">
            <select value={props.rule.target.kind} onChange={(event) => props.onChange((rule) => { rule.target = { kind: event.currentTarget.value as TriggerRule["target"]["kind"] }; })}>
              <option value="subject">Event subject</option><option value="attribute">One event attribute</option><option value="fixed">Fixed Linear issue</option>
            </select>
          </Field>
          <Show when={props.rule.target.kind !== "subject"}>
            <Field label={props.rule.target.kind === "fixed" ? "Linear issue" : "Attribute key"}>
              <input required maxlength={256} placeholder={props.rule.target.kind === "fixed" ? "ENG-40" : "issueIdentifier"} value={props.rule.target.value ?? ""} onInput={(event) => props.onChange((rule) => { rule.target.value = event.currentTarget.value; })} />
            </Field>
          </Show>
        </div>
      </fieldset>
      <fieldset class="trigger-subsection">
        <legend>Admission limits</legend>
        <div class="trigger-field-grid limit-fields">
          <Field label="Maximum hop"><input required type="number" min="1" max="8" value={props.rule.maxHop} onInput={(event) => props.onChange((rule) => { rule.maxHop = event.currentTarget.valueAsNumber; })} /></Field>
          <Field label="Outstanding"><input required type="number" min="1" max="100" value={props.rule.maxOutstanding} onInput={(event) => props.onChange((rule) => { rule.maxOutstanding = event.currentTarget.valueAsNumber; })} /></Field>
          <Field label="Admissions / hour"><input required type="number" min="1" max="10000" value={props.rule.admissionsPerHour} onInput={(event) => props.onChange((rule) => { rule.admissionsPerHour = event.currentTarget.valueAsNumber; })} /></Field>
        </div>
      </fieldset>
    </article>
  );
}

function ScheduleEditor(props: {
  schedule: TriggerSchedule;
  status?: TriggerScheduleStatus;
  pendingDelete: boolean;
  onChange: (mutator: (schedule: TriggerSchedule) => void) => void;
  onRemove: () => void;
}): JSX.Element {
  return (
    <article class="trigger-editor schedule-editor" aria-labelledby={`schedule-${props.schedule.id}`}>
      <header class="trigger-editor-header">
        <div><span class="workflow-id">{props.schedule.id} · revision {props.schedule.revision || "new"}</span><h3 id={`schedule-${props.schedule.id}`}>{props.schedule.name || "Untitled schedule"}</h3><p class="scope-summary">factory / cron / due · {props.schedule.timezone}</p></div>
        <div class="trigger-card-actions">
          <Toggle checked={props.schedule.enabled} compact label={props.schedule.enabled ? "Enabled" : "Disabled"} onChange={(enabled) => props.onChange((schedule) => { schedule.enabled = enabled; })} />
          <button class="text-button danger-button" type="button" onClick={props.onRemove}>{props.pendingDelete ? "Confirm removal" : "Remove"}</button>
        </div>
      </header>
      <dl class="trigger-card-status">
        <div><dt>Last due</dt><dd>{props.status?.last ? formatTime(props.status.last) : "Not emitted"}</dd></div>
        <div><dt>Next due</dt><dd>{props.status?.next ? formatTime(props.status.next) : "Inactive"}</dd></div>
        <div><dt>Skipped</dt><dd>{props.status?.skipped ?? 0}</dd></div>
      </dl>
      <div class="trigger-field-grid identity-fields">
        <Field label="Stable ID"><input required readOnly={props.schedule.revision > 0} pattern="[a-z0-9][a-z0-9-]{0,47}" maxlength={48} value={props.schedule.id} onInput={(event) => props.onChange((schedule) => { schedule.id = event.currentTarget.value; })} /></Field>
        <Field label="Schedule name"><input required maxlength={80} value={props.schedule.name} onInput={(event) => props.onChange((schedule) => { schedule.name = event.currentTarget.value; })} /></Field>
      </div>
      <div class="trigger-field-grid schedule-fields">
        <Field label="Five-field cron" hint="Minute hour day-of-month month day-of-week. No descriptors or embedded timezone."><input required maxlength={128} value={props.schedule.cron} onInput={(event) => props.onChange((schedule) => { schedule.cron = event.currentTarget.value; })} /></Field>
        <Field label="IANA timezone"><input required maxlength={128} placeholder="America/Los_Angeles" value={props.schedule.timezone} onInput={(event) => props.onChange((schedule) => { schedule.timezone = event.currentTarget.value; })} /></Field>
        <Field label="Optional subject"><input maxlength={256} value={props.schedule.subject ?? ""} onInput={(event) => props.onChange((schedule) => { schedule.subject = optional(event.currentTarget.value); })} /></Field>
      </div>
      <ContextAttributeEditor values={props.schedule.attributes ?? {}} onChange={(attributes) => props.onChange((schedule) => { schedule.attributes = attributes; })} />
    </article>
  );
}

function ExactAttributeEditor(props: { values: Record<string, string>; onChange: (values: Record<string, string>) => void }): JSX.Element {
  const entries = (): [string, string][] => Object.entries(props.values);
  return (
    <div class="attribute-editor">
      <div class="attribute-heading"><strong>Attribute membership</strong><button class="text-button" type="button" disabled={entries().length >= 32} onClick={() => props.onChange({ ...props.values, [uniqueAttributeKey(props.values)]: "" })}>Add attribute</button></div>
      <For each={entries()}>{([key, value], index) => <div class="attribute-row"><input aria-label="Attribute key" maxlength={256} value={key} onInput={(event) => props.onChange(renameAttribute(props.values, key, event.currentTarget.value))} /><input aria-label="Required attribute value" maxlength={256} value={value} onInput={(event) => props.onChange({ ...props.values, [key]: event.currentTarget.value })} /><button type="button" aria-label={`Remove attribute ${index() + 1}`} onClick={() => props.onChange(withoutAttribute(props.values, key))}>Remove</button></div>}</For>
    </div>
  );
}

function ContextAttributeEditor(props: { values: Record<string, string[]>; onChange: (values: Record<string, string[]>) => void }): JSX.Element {
  const entries = (): [string, string[]][] => Object.entries(props.values);
  return (
    <div class="attribute-editor">
      <div class="attribute-heading"><strong>Event context</strong><button class="text-button" type="button" disabled={entries().length >= 32} onClick={() => props.onChange({ ...props.values, [uniqueAttributeKey(props.values)]: [""] })}>Add context</button></div>
      <For each={entries()}>{([key, values], index) => <div class="attribute-row"><input aria-label="Context key" maxlength={256} value={key} onInput={(event) => props.onChange(renameAttribute(props.values, key, event.currentTarget.value))} /><input aria-label="Comma-separated context values" maxlength={512} value={values.join(", ")} onInput={(event) => props.onChange({ ...props.values, [key]: event.currentTarget.value.split(",").map((value) => value.trim()) })} /><button type="button" aria-label={`Remove context ${index() + 1}`} onClick={() => props.onChange(withoutAttribute(props.values, key))}>Remove</button></div>}</For>
    </div>
  );
}

function TriggerEmpty(props: { title: string; detail: string }): JSX.Element {
  return <div class="empty-state compact trigger-empty"><strong>{props.title}</strong><span>{props.detail}</span></div>;
}

function uniqueTriggerID(prefix: string, ids: string[]): string {
  const existing = new Set(ids);
  let sequence = 1;
  while (existing.has(`${prefix}-${sequence}`)) sequence += 1;
  return `${prefix}-${sequence}`;
}

function uniqueAttributeKey(values: Record<string, unknown>): string {
  let sequence = 1;
  while (`attribute-${sequence}` in values) sequence += 1;
  return `attribute-${sequence}`;
}

function renameAttribute<T>(values: Record<string, T>, oldKey: string, newKey: string): Record<string, T> {
  const next: Record<string, T> = {};
  for (const [key, value] of Object.entries(values) as [string, T][]) next[key === oldKey ? newKey : key] = value;
  return next;
}

function withoutAttribute<T>(values: Record<string, T>, removed: string): Record<string, T> {
  return Object.fromEntries(Object.entries(values).filter(([key]) => key !== removed)) as Record<string, T>;
}

function optional(value: string): string | undefined {
  return value === "" ? undefined : value;
}

function subjectFilterMode(subject: string | undefined): SubjectFilterMode {
  if (subject === undefined) {
    return "wildcard";
  }
  if (subject === "") {
    return "absent";
  }
  return "exact";
}

function subjectFilterValue(mode: SubjectFilterMode): string | undefined {
  if (mode === "wildcard") {
    return undefined;
  }
  if (mode === "absent") {
    return "";
  }
  return "ENG-40";
}

function triggerSaveButtonLabel(state: SettingsSaveState, broadConfirmed: boolean): string {
  if (broadConfirmed) {
    return "Confirm broad scope";
  }
  if (state === "saving") {
    return "Saving";
  }
  return "Save registry";
}

function broadTriggerRule(rule: TriggerRule): boolean {
  const filter = rule.filter;
  return (!filter.source && !filter.type && !filter.action && filter.subject === undefined && Object.keys(filter.attributes ?? {}).length === 0) ||
    filter.source === "telemetry" || filter.type === "telemetry" || filter.type === "lifecycle" || filter.type === "service" ||
    filter.type === "agent-record" || filter.type === "agent-run";
}

function ruleScopeSummary(rule: TriggerRule): string {
  const parts = [rule.filter.source || "any source", rule.filter.type || "any type", rule.filter.action || "any action"];
  if (rule.filter.subject !== undefined) parts.push(rule.filter.subject === "" ? "subject absent" : `subject ${rule.filter.subject}`);
  const attributes = Object.keys(rule.filter.attributes ?? {}).length;
  if (attributes) parts.push(`${attributes} attribute ${attributes === 1 ? "match" : "matches"}`);
  return parts.join(" / ");
}

function validateTriggerDraft(registry: TriggerRegistry, workflows: WorkflowSummary[]): string | undefined {
  const ids = new Set<string>();
  const idPattern = /^[a-z0-9][a-z0-9-]{0,47}$/;
  const workflowIDs = new Set(workflows.map((workflow) => workflow.id));
  for (const rule of registry.rules) {
    if (!idPattern.test(rule.id) || ids.has(rule.id)) return `Rule ID ${rule.id || "(empty)"} is invalid or duplicated.`;
    ids.add(rule.id);
    if (!rule.name.trim() || !workflowIDs.has(rule.workflowId)) return `Rule ${rule.id} needs a name and enabled workflow.`;
    if (rule.target.kind !== "subject" && !rule.target.value?.trim()) return `Rule ${rule.id} needs a target value.`;
    if (Object.keys(rule.filter.attributes ?? {}).some((key) => !key.trim())) return `Rule ${rule.id} has an empty attribute key.`;
  }
  for (const schedule of registry.schedules) {
    if (!idPattern.test(schedule.id) || ids.has(schedule.id)) return `Schedule ID ${schedule.id || "(empty)"} is invalid or duplicated.`;
    ids.add(schedule.id);
    if (!schedule.name.trim() || schedule.cron.trim().split(/\s+/).length !== 5 || schedule.cron.startsWith("@") || /(?:CRON_)?TZ=/.test(schedule.cron)) return `Schedule ${schedule.id} needs a name and standard five-field cron.`;
    try { new Intl.DateTimeFormat("en-US", { timeZone: schedule.timezone }).format(); } catch { return `Schedule ${schedule.id} has an invalid IANA timezone.`; }
    if (Object.keys(schedule.attributes ?? {}).some((key) => !key.trim())) return `Schedule ${schedule.id} has an empty context key.`;
  }
  return undefined;
}

type WorkflowEditorState = "published" | "unpublished" | "dirty" | "saving" | "conflict" | "failed" | "invalid";

function WorkflowsPage(): JSX.Element {
  const [workflows] = createResource(getWorkflows);
  const workflowSnapshot = (): WorkflowsResponse | undefined => resourceSnapshot(workflows);

  onMount(() => {
    document.title = "Workflows | Factory";
  });

  return (
    <main class="activity-page settings-page" id="main-content">
      <section class="activity-shell settings-shell" aria-labelledby="workflows-title">
        <ActivityHeader
          section="workflows"
          state={resourceState(workflows.loading, workflows.error)}
          label={workflows.error ? "Workflow policy unavailable" : "Markdown workflow policy"}
        />
        <Show
          when={workflowSnapshot()}
          fallback={
            <div class="settings-loading" aria-live="polite">
              <p class="section-label">Procedural policy</p>
              <h1 class="activity-title compact-title" id="workflows-title">
                {workflows.error ? "Workflows unavailable" : "Opening workflows"}
              </h1>
              <Show when={workflows.error}><InlineError message="Published workflows could not be loaded." /></Show>
            </div>
          }
        >
          {(snapshot) => <WorkflowsEditor initial={snapshot()} />}
        </Show>
      </section>
    </main>
  );
}

function WorkflowsEditor(props: { initial: WorkflowsResponse }): JSX.Element {
  const [catalog, setCatalog] = createSignal(structuredClone(props.initial));
  const [selectedID, setSelectedID] = createSignal(props.initial.workflows[0]?.workflowId ?? "");
  const initialDocument = props.initial.workflows[0];
  const [local, setLocal] = createSignal<WorkflowDraft>(structuredClone(initialDocument?.draft ?? {
    workflowId: "", revision: 0, baseWorkflowRevision: 0, name: "", enabled: false, markdown: "",
  }));
  const [editorState, setEditorState] = createSignal<WorkflowEditorState>(workflowDocumentState(initialDocument));
  const [message, setMessage] = createSignal("Published policy is unchanged");
  const [localUnacknowledged, setLocalUnacknowledged] = createSignal(false);
  let saveTimer: number | undefined;
  let saving = false;
  let saveQueued = false;

  const selectedDocument = (): WorkflowDocument | undefined =>
    catalog().workflows.find((document) => document.workflowId === selectedID());

  onMount(() => {
    const warn = (event: BeforeUnloadEvent): void => {
      if (!localUnacknowledged()) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    onCleanup(() => {
      window.removeEventListener("beforeunload", warn);
      if (saveTimer !== undefined) window.clearTimeout(saveTimer);
    });
  });

  function selectWorkflow(id: string): void {
    if (id === selectedID()) return;
    if (localUnacknowledged() && !window.confirm("Discard local edits that have not reached the draft store?")) return;
    const document = catalog().workflows.find((candidate) => candidate.workflowId === id);
    if (!document) return;
    if (saveTimer !== undefined) window.clearTimeout(saveTimer);
    setSelectedID(id);
    setLocal(structuredClone(document.draft));
    setLocalUnacknowledged(false);
    setEditorState(workflowDocumentState(document));
    setMessage(document.savedDraft ? "Saved draft loaded" : "Editing the published revision");
  }

  function edit(mutator: (draft: WorkflowDraft) => void): void {
    setLocal((current) => {
      const next = structuredClone(current);
      mutator(next);
      return next;
    });
    setLocalUnacknowledged(true);
    const problem = validateWorkflowDraft(local());
    if (problem) {
      setEditorState("invalid");
      setMessage(problem);
      return;
    }
    setEditorState("dirty");
    setMessage("Local edits will autosave shortly");
    if (saveTimer !== undefined) window.clearTimeout(saveTimer);
    saveTimer = window.setTimeout(() => void autosave(), 700);
  }

  async function autosave(): Promise<void> {
    if (saving) {
      saveQueued = true;
      return;
    }
    const problem = validateWorkflowDraft(local());
    if (problem) {
      setEditorState("invalid");
      setMessage(problem);
      return;
    }
    saving = true;
    const captured = structuredClone(local());
    setEditorState("saving");
    setMessage("Saving private draft");
    try {
      const saved = await saveWorkflowDraft(captured);
      setCatalog((current) => ({
        ...current,
        workflows: current.workflows.map((document) => document.workflowId === saved.workflowId
          ? { ...document, draft: structuredClone(saved), savedDraft: true, draftConflict: false }
          : document),
      }));
      const unchanged = workflowEditableEqual(local(), captured);
      setLocal((current) => ({ ...current, revision: saved.revision, baseWorkflowRevision: saved.baseWorkflowRevision, updatedAt: saved.updatedAt }));
      if (unchanged) {
        setLocalUnacknowledged(false);
        const published = selectedDocument()?.published;
        const current = { ...saved };
        setEditorState(published && workflowPublishedEqual(published, current) ? "published" : "unpublished");
        setMessage(published && workflowPublishedEqual(published, current) ? "Draft matches the published revision" : "Draft saved · publication required");
      } else {
        saveQueued = true;
      }
    } catch (error) {
      if (error instanceof WorkflowConflict) {
        setCatalog(structuredClone(error.snapshot));
        setEditorState("conflict");
        setMessage("A newer server revision exists. Local text has been preserved.");
      } else {
        setEditorState("failed");
        setMessage(error instanceof Error ? error.message : "Draft autosave failed");
      }
    } finally {
      saving = false;
      if (saveQueued) {
        saveQueued = false;
        void autosave();
      }
    }
  }

  async function refresh(preferredID = selectedID()): Promise<void> {
    const next = await getWorkflows();
    setCatalog(structuredClone(next));
    const document = next.workflows.find((candidate) => candidate.workflowId === preferredID) ?? next.workflows[0];
    setSelectedID(document?.workflowId ?? "");
    setLocal(structuredClone(document?.draft ?? { workflowId: "", revision: 0, baseWorkflowRevision: 0, name: "", enabled: false, markdown: "" }));
    setLocalUnacknowledged(false);
    setEditorState(workflowDocumentState(document));
  }

  async function createDraft(): Promise<void> {
    try {
      const created = await createWorkflowDraft();
      await refresh(created.workflowId);
      setMessage("Disabled draft created");
    } catch (error) {
      setEditorState("failed");
      setMessage(error instanceof Error ? error.message : "Workflow creation failed");
    }
  }

  async function publish(): Promise<void> {
    if (localUnacknowledged() || !selectedDocument()?.savedDraft) return;
    setEditorState("saving");
    setMessage("Publishing the exact saved draft");
    try {
      await publishWorkflowDraft(local(), catalog().policyRevision);
      await refresh(local().workflowId);
      setEditorState("published");
      setMessage("Published for later admissions");
    } catch (error) {
      if (error instanceof WorkflowConflict) setCatalog(structuredClone(error.snapshot));
      setEditorState(error instanceof WorkflowConflict ? "conflict" : "failed");
      setMessage(error instanceof Error ? error.message : "Workflow publish failed");
    }
  }

  async function discard(): Promise<void> {
    const document = selectedDocument();
    if (!document) return;
    if (localUnacknowledged() && !window.confirm("Discard local edits and the saved draft?")) return;
    try {
      if (document.savedDraft) await discardWorkflowDraft(local());
      await refresh(document.workflowId);
      setMessage(document.published ? "Draft discarded · published revision restored" : "Draft discarded");
    } catch (error) {
      if (error instanceof WorkflowConflict) setCatalog(structuredClone(error.snapshot));
      setEditorState(error instanceof WorkflowConflict ? "conflict" : "failed");
      setMessage(error instanceof Error ? error.message : "Draft discard failed");
    }
  }

  async function duplicateLocal(): Promise<void> {
    try {
      const created = await createWorkflowDraft();
      const copied = await saveWorkflowDraft({ ...created, name: `${local().name} copy`, enabled: false, markdown: local().markdown });
      await refresh(copied.workflowId);
      setMessage("Local text duplicated into a disabled draft");
    } catch (error) {
      setEditorState("failed");
      setMessage(error instanceof Error ? error.message : "Workflow duplication failed");
    }
  }

  async function deleteWorkflow(): Promise<void> {
    const document = selectedDocument();
    if (!document?.published || !window.confirm(`Delete published workflow ${document.published.name}?`)) return;
    try {
      await deletePublishedWorkflow(document, catalog().policyRevision);
      await refresh("");
      setMessage("Published workflow deleted");
    } catch (error) {
      if (error instanceof WorkflowConflict) setCatalog(structuredClone(error.snapshot));
      setEditorState(error instanceof WorkflowConflict ? "conflict" : "failed");
      setMessage(error instanceof Error ? error.message : "Workflow deletion failed");
    }
  }

  return (
    <>
      <div class="settings-hero workflow-hero">
        <p class="section-label">Procedural policy</p>
        <h1 class="activity-title compact-title" id="workflows-title">Workflows</h1>
        <p class="settings-intro">Write procedures as Markdown notes. Drafts autosave privately; only an explicit publish changes later Factory admissions.</p>
        <dl class="settings-revision workflow-revision">
          <div><dt>Policy revision</dt><dd>{catalog().policyRevision}</dd></div>
          <div><dt>Documents</dt><dd>{catalog().workflows.length} / 8</dd></div>
          <div><dt>Authoring</dt><dd>{catalog().draftAvailable ? "Available" : "Read only"}</dd></div>
        </dl>
      </div>

      <Show when={catalog().draftError}><InlineError message={catalog().draftError ?? "Draft store unavailable"} /></Show>
      <div class="workflow-workspace">
        <aside class="workflow-index" aria-label="Workflow documents">
          <div class="workflow-index-heading">
            <strong>Documents</strong>
            <button class="text-button" type="button" disabled={!catalog().draftAvailable || catalog().workflows.length >= 8} onClick={() => void createDraft()}>New</button>
          </div>
          <For each={catalog().workflows}>
            {(document) => (
              <button type="button" classList={{ selected: document.workflowId === selectedID() }} onClick={() => selectWorkflow(document.workflowId)}>
                <strong>{document.draft.name || document.published?.name || "Untitled"}</strong>
                <span>{document.published ? `Published r${document.published.revision}` : "Draft only"}</span>
                <i>{document.draftConflict ? "Conflict" : document.savedDraft ? "Saved draft" : "Published"}</i>
              </button>
            )}
          </For>
        </aside>

        <Show when={selectedDocument()} fallback={<div class="workflow-empty"><strong>No workflow document</strong><p>Create a disabled draft to begin.</p></div>}>
          {(document) => (
            <section class="workflow-note" aria-labelledby="workflow-note-title">
              <header class="workflow-note-header">
                <div>
                  <span class="workflow-id">{document().workflowId}</span>
                  <h2 id="workflow-note-title">{local().name || "Untitled workflow"}</h2>
                </div>
                <Toggle checked={local().enabled} compact label={local().enabled ? "Enabled on publish" : "Disabled on publish"} onChange={(enabled) => edit((draft) => { draft.enabled = enabled; })} />
              </header>
              <div class="workflow-note-meta">
                <Field label="Name"><input maxlength={80} value={local().name} onInput={(event) => edit((draft) => { draft.name = event.currentTarget.value; })} /></Field>
                <Field label="Stable ID" hint="IDs are server assigned and immutable."><input readOnly value={local().workflowId} /></Field>
              </div>
              <label class="workflow-markdown-field">
                <span>Markdown procedure</span>
                <textarea spellcheck={false} value={local().markdown} onInput={(event) => edit((draft) => { draft.markdown = event.currentTarget.value; })} />
                <small>{new TextEncoder().encode(local().markdown).length.toLocaleString()} / 131,072 bytes</small>
              </label>
              <Show when={document().references.length > 0}>
                <div class="workflow-references"><strong>Live references</strong><For each={document().references}>{(reference) => <span>{reference.kind === "protected" ? "Protected" : reference.enabled ? "Enabled rule" : "Disabled rule"} · {reference.name}</span>}</For></div>
              </Show>
              <div class={`workflow-editor-status ${editorState()}`}>
                <div aria-live="polite" role={["failed", "conflict", "invalid"].includes(editorState()) ? "alert" : "status"}>
                  <strong>{workflowStateLabel(editorState())}</strong><span>{message()}</span>
                </div>
                <div class="workflow-note-actions">
                  <Show when={editorState() === "conflict"}>
                    <button class="text-button" type="button" onClick={() => void refresh(local().workflowId)}>Reload server</button>
                    <button class="text-button" type="button" onClick={() => void duplicateLocal()}>Duplicate local text</button>
                  </Show>
                  <Show when={editorState() === "failed"}><button class="text-button" type="button" onClick={() => void autosave()}>Retry save</button></Show>
                  <button class="text-button" type="button" disabled={!catalog().draftAvailable || (!document().savedDraft && !localUnacknowledged())} onClick={() => void discard()}>Discard draft</button>
                  <button class="text-button danger-button" type="button" disabled={!document().published || document().references.length > 0} onClick={() => void deleteWorkflow()}>Delete published</button>
                  <button class="primary-button" type="button" disabled={!document().savedDraft || localUnacknowledged() || ["saving", "dirty", "invalid", "conflict", "published"].includes(editorState())} onClick={() => void publish()}>Publish saved draft</button>
                </div>
              </div>
            </section>
          )}
        </Show>
      </div>
      <footer class="activity-footer settings-footer"><span>Current Runs retain the workflow revision they admitted.</span><a class="text-link" href="/triggers">Manage trigger bindings</a></footer>
    </>
  );
}

function workflowDocumentState(document: WorkflowDocument | undefined): WorkflowEditorState {
  if (!document) return "published";
  if (document.draftConflict) return "conflict";
  if (!document.savedDraft || (document.published && workflowPublishedEqual(document.published, document.draft))) return "published";
  return "unpublished";
}

function workflowEditableEqual(left: WorkflowDraft, right: WorkflowDraft): boolean {
  return left.name === right.name && left.enabled === right.enabled && left.markdown === right.markdown;
}

function workflowPublishedEqual(published: WorkflowDefinition, draft: WorkflowDraft): boolean {
  return published.name === draft.name && published.enabled === draft.enabled && published.markdown === draft.markdown;
}

function validateWorkflowDraft(draft: WorkflowDraft): string | undefined {
  if (!draft.name || draft.name !== draft.name.trim() || new TextEncoder().encode(draft.name).length > 80) return "Name must be trimmed and at most 80 bytes";
  const size = new TextEncoder().encode(draft.markdown).length;
  if (!draft.markdown.trim()) return "Markdown cannot be blank";
  if (size > 131072) return "Markdown exceeds 131,072 bytes";
  if (draft.markdown.includes("\0")) return "Markdown cannot contain NUL";
  return undefined;
}

function workflowStateLabel(state: WorkflowEditorState): string {
  switch (state) {
    case "dirty": return "Local edits";
    case "saving": return "Saving draft";
    case "unpublished": return "Unpublished changes";
    case "conflict": return "Draft conflict";
    case "failed": return "Autosave failed";
    case "invalid": return "Validation required";
    default: return "Published";
  }
}

function SettingsPage(): JSX.Element {
  const [settings] = createResource(getSettings);
  const settingsSnapshot = (): FactorySettings | undefined => resourceSnapshot(settings);

  onMount(() => {
    document.title = "Settings | Factory";
  });

  return (
    <main class="activity-page settings-page" id="main-content">
      <section class="activity-shell settings-shell" aria-labelledby="settings-title">
        <ActivityHeader
          section="settings"
          state={resourceState(settings.loading, settings.error)}
          label={settings.error ? "Settings unavailable" : "Private configuration"}
        />

        <Show
          when={settingsSnapshot()}
          fallback={
            <div class="settings-loading" aria-live="polite">
              <p class="section-label">Runtime policy</p>
              <h1 class="activity-title compact-title" id="settings-title">
                {settings.error ? "Settings unavailable" : "Opening settings"}
              </h1>
              <Show when={settings.error}>
                <InlineError message="Factory settings could not be loaded." />
              </Show>
            </div>
          }
        >
          {(snapshot) => <SettingsEditor initial={snapshot()} />}
        </Show>
      </section>
    </main>
  );
}

function SettingsEditor(props: { initial: FactorySettings }): JSX.Element {
  const [draft, setDraft] = createSignal(cloneSettings(props.initial));
  const [saveState, setSaveState] = createSignal<SettingsSaveState>("idle");
  const [message, setMessage] = createSignal("");

  function update(mutator: (value: FactorySettings) => void): void {
    setDraft((current) => {
      const next = cloneSettings(current);
      mutator(next);
      return next;
    });
    setSaveState("dirty");
    setMessage("Unsaved changes");
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    setSaveState("saving");
    setMessage("Saving revision");
    try {
      const result = await saveSettings(draft());
      setDraft(cloneSettings(result.snapshot));
      if (result.conflict) {
        setSaveState("conflict");
        setMessage("A newer revision was loaded. Review it before saving again.");
        return;
      }
      setSaveState("saved");
      setMessage(`Revision ${result.snapshot.revision} saved`);
    } catch (error) {
      setSaveState("failed");
      setMessage(error instanceof Error ? error.message : "Settings update failed");
    }
  }

  return (
    <>
      <div class="settings-hero">
        <p class="section-label">Runtime policy</p>
        <h1 class="activity-title compact-title" id="settings-title">
          Settings
        </h1>
        <p class="settings-intro">
          Change how new Factory runs begin and which provider settings they inherit.
          Active provider processes keep the snapshot they started with.
        </p>
        <dl class="settings-revision">
          <div>
            <dt>Revision</dt>
            <dd>{draft().revision}</dd>
          </div>
          <div>
            <dt>Last updated</dt>
            <dd>{draft().updatedAt ? formatTime(draft().updatedAt) : "Compiled defaults"}</dd>
          </div>
          <div><dt>Scope</dt><dd>Agents & capacity</dd></div>
        </dl>
      </div>

      <form class="settings-form" onSubmit={submit}>
        <section class="settings-section settings-routing-note" aria-labelledby="trigger-settings-title">
          <div class="settings-section-heading">
            <h2 id="trigger-settings-title">Admission moved to Triggers</h2>
            <p>Legacy trigger fields remain readable for rollback compatibility. Configure new event rules and cron schedules in the dedicated registry.</p>
          </div>
          <a class="secondary-button settings-route-link" href="/triggers">Open triggers</a>
        </section>

        <section class="settings-section settings-routing-note" aria-labelledby="workflow-settings-title">
          <div class="settings-section-heading">
            <h2 id="workflow-settings-title">Workflow authoring has its own workspace</h2>
            <p>Draft and publish Markdown procedures without mixing executable policy into provider configuration.</p>
          </div>
          <a class="secondary-button settings-route-link" href="/workflows">Open workflows</a>
        </section>

        <section class="settings-section" aria-labelledby="agent-settings-title">
          <div class="settings-section-heading">
            <h2 id="agent-settings-title">Agent launches</h2>
            <p>Model values become direct provider arguments. They are never interpreted by a shell.</p>
          </div>
          <div class="agent-settings-grid">
            <ProviderEditor
              title="Principal"
              provider="codex"
              value={draft().agents.principal}
              onChange={(value) => update((next) => { next.agents.principal.model = value.model; next.agents.principal.effort = value.effort; })}
            >
              <Field label="Attempt limit" hint="Includes resumable provider failures.">
                <input
                  type="number"
                  required
                  min="1"
                  max="5"
                  value={draft().agents.principal.maxAttempts}
                  onInput={(event) => update((next) => { next.agents.principal.maxAttempts = event.currentTarget.valueAsNumber; })}
                />
              </Field>
            </ProviderEditor>
            <ProviderEditor
              title="Codex children"
              provider="codex"
              value={draft().agents.codexChild}
              onChange={(value) => update((next) => { next.agents.codexChild = value; })}
            />
            <ProviderEditor
              title="Claude children"
              provider="claude"
              value={draft().agents.claudeChild}
              onChange={(value) => update((next) => { next.agents.claudeChild = value; })}
            />
          </div>
        </section>

        <section class="settings-section capacity-section" aria-labelledby="capacity-settings-title">
          <div class="settings-section-heading">
            <h2 id="capacity-settings-title">Capacity</h2>
            <p>The manager reads this limit at the start of each reconcile pass and never interrupts active runs.</p>
          </div>
          <Field label="Maximum concurrent runs" hint="Allowed range: 1 to 10.">
            <input
              type="number"
              required
              min="1"
              max="10"
              value={draft().runtime.maxConcurrentRuns}
              onInput={(event) => update((next) => { next.runtime.maxConcurrentRuns = event.currentTarget.valueAsNumber; })}
            />
          </Field>
        </section>

        <div class={`settings-save ${saveState()}`}>
          <div aria-live="polite" role={saveState() === "failed" ? "alert" : "status"}>
            <strong>{saveStateLabel(saveState())}</strong>
            <span>{message() || "No unsaved changes"}</span>
          </div>
          <button class="primary-button" type="submit" disabled={saveState() === "saving" || saveState() === "idle" || saveState() === "saved"}>
            {saveState() === "saving" ? "Saving" : "Save settings"}
          </button>
        </div>
      </form>

      <footer class="activity-footer settings-footer">
        <span>Routing, secrets, merge authority, and deployment gates stay locked in code.</span>
        <a class="text-link" href="/agents">View agent runs</a>
      </footer>
    </>
  );
}

function Field(props: { label: string; hint?: string; children: JSX.Element }): JSX.Element {
  return (
    <label class="settings-field">
      <span>{props.label}</span>
      {props.children}
      <Show when={props.hint}>{(hint) => <small>{hint()}</small>}</Show>
    </label>
  );
}

function Toggle(props: {
  checked: boolean;
  disabled?: boolean;
  compact?: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}): JSX.Element {
  return (
    <label classList={{ "settings-toggle": true, compact: Boolean(props.compact) }}>
      <input
        type="checkbox"
        checked={props.checked}
        disabled={props.disabled}
        onChange={(event) => props.onChange(event.currentTarget.checked)}
      />
      <span>{props.label}</span>
    </label>
  );
}

function ProviderEditor(props: {
  title: string;
  provider: "codex" | "claude";
  value: ProviderSettings;
  onChange: (value: ProviderSettings) => void;
  children?: JSX.Element;
}): JSX.Element {
  const efforts = (): string[] => props.provider === "codex"
    ? ["low", "medium", "high", "xhigh"]
    : ["low", "medium", "high", "max"];
  return (
    <fieldset class="settings-group provider-editor">
      <legend>{props.title}</legend>
      <Field label="Model" hint="Letters, numbers, dots, slashes, colons, underscores, and hyphens.">
        <input
          required
          maxlength={64}
          pattern="[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}"
          value={props.value.model}
          onInput={(event) => props.onChange({ ...props.value, model: event.currentTarget.value })}
        />
      </Field>
      <Field label="Reasoning effort">
        <select
          value={props.value.effort}
          onChange={(event) => props.onChange({ ...props.value, effort: event.currentTarget.value })}
        >
          <For each={efforts()}>{(effort) => <option value={effort}>{effort}</option>}</For>
        </select>
      </Field>
      {props.children}
    </fieldset>
  );
}

function cloneSettings(value: FactorySettings): FactorySettings {
  return structuredClone(value);
}

function saveStateLabel(state: SettingsSaveState): string {
  switch (state) {
    case "dirty":
      return "Ready to save";
    case "saving":
      return "Saving";
    case "saved":
      return "Saved";
    case "conflict":
      return "Newer revision loaded";
    case "failed":
      return "Save failed";
    default:
      return "Current revision";
  }
}

function ActivityChart(props: {
  title: string;
  subtitle: string;
  items: ActivityCount[];
}): JSX.Element {
  const maximum = (): number => Math.max(1, ...props.items.map((item) => item.count));

  return (
    <article class="activity-chart">
      <header>
        <h2>{props.title}</h2>
        <span>{props.subtitle}</span>
      </header>
      <Show
        when={props.items.length > 0}
        fallback={<p class="chart-empty">Waiting for retained activity.</p>}
      >
        <ol>
          <For each={props.items}>
            {(item) => (
              <li>
                <span>{item.label}</span>
                <progress max={maximum()} value={item.count} aria-label={`${item.label}: ${item.count}`} />
                <strong>{item.count}</strong>
              </li>
            )}
          </For>
        </ol>
      </Show>
    </article>
  );
}

function Pagination(props: {
  page: number;
  pageCount: number;
  onChange: (page: number) => void;
}): JSX.Element {
  const lastPage = (): number => Math.max(1, props.pageCount);
  return (
    <nav class="pagination" aria-label="Event pages">
      <button
        type="button"
        disabled={props.page <= 1}
        onClick={() => props.onChange(props.page - 1)}
      >
        Previous
      </button>
      <span>
        {props.page} / {lastPage()}
      </span>
      <button
        type="button"
        disabled={props.page >= lastPage()}
        onClick={() => props.onChange(props.page + 1)}
      >
        Next
      </button>
    </nav>
  );
}

function TasksPage(): JSX.Element {
  const [provider, setProvider] = createSignal("");
  const [state, setState] = createSignal("");
  const [project, setProject] = createSignal("");
  const [approval, setApproval] = createSignal("");
  const [activity, setActivity] = createSignal("");
  const [cursor, setCursor] = createSignal("");
  const [cursorHistory, setCursorHistory] = createSignal<string[]>([]);
  const [createState, setCreateState] = createSignal<"idle" | "saving" | "failed">("idle");
  const [createMessage, setCreateMessage] = createSignal("");
  const [title, setTitle] = createSignal("");
  const [description, setDescription] = createSignal("");
  const [createProject, setCreateProject] = createSignal("");
  const [createApproval, setCreateApproval] = createSignal<"gated" | "automatic">("gated");
  const request = createMemo(() => {
    const query = new URLSearchParams({ limit: "100" });
    if (provider()) query.set("provider", provider());
    if (state()) query.set("state", state());
    if (project()) query.set("project", project());
    if (approval()) query.set("approval", approval());
    if (activity()) query.set("activity", activity());
    if (cursor()) query.set("cursor", cursor());
    return `/api/tasks?${query.toString()}`;
  });
  const [tasks, { refetch }] = createResource(request, getTasks);
  const [projects] = createResource(getTaskProjects);
  const taskSnapshot = (): TasksResponse | undefined => resourceSnapshot(tasks);
  const projectSnapshot = (): TaskProjectsResponse | undefined => resourceSnapshot(projects);
  const enabledProjects = createMemo(() => (projectSnapshot()?.projects ?? []).filter((choice) => choice.enabled));

  createEffect(() => {
    const choices = enabledProjects();
    if (choices.length > 0 && !choices.some((choice) => choice.projectId === createProject())) {
      setCreateProject(choices[0].projectId);
    }
  });

  onMount(() => {
    document.title = "Tasks | Factory";
  });

  function changeFilter(setter: (value: string) => void, value: string): void {
    setter(value);
    setCursor("");
    setCursorHistory([]);
  }

  function nextTaskPage(): void {
    const next = taskSnapshot()?.nextCursor;
    if (!next) return;
    setCursorHistory((history) => [...history, cursor()]);
    setCursor(next);
  }

  function previousTaskPage(): void {
    const history = cursorHistory();
    if (history.length === 0) return;
    setCursor(history[history.length - 1]);
    setCursorHistory(history.slice(0, -1));
  }

  async function createTask(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    setCreateState("saving");
    setCreateMessage("Creating task…");
    try {
      const result = await taskRequest<TaskMutationResult>("/api/tasks", "POST", {
        title: title(),
        description: description(),
        projectId: createProject(),
        approvalMode: createApproval(),
      });
      window.location.assign(`/tasks/factory/${encodeURIComponent(result.task.ref.providerId)}`);
    } catch (error) {
      setCreateState("failed");
      setCreateMessage(taskErrorMessage(error));
    }
  }

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="tasks-title">
        <ActivityHeader
          section="tasks"
          state={resourceState(tasks.loading || projects.loading, tasks.error || projects.error)}
          label={tasks.error || projects.error ? "Task workspace unavailable" : "Task journal online"}
        />

        <div class="activity-hero detail-hero task-hero">
          <div>
            <p class="section-label">Source-neutral work</p>
            <h1 class="activity-title compact-title" id="tasks-title">Tasks</h1>
          </div>
          <p class="activity-intro">
            Native Factory tasks share one durable ledger with managed Linear work.
            Factory tasks are actionable here; Linear remains read-only during coexistence.
          </p>
        </div>

        <form class="task-filters" aria-label="Task filters" onSubmit={(event) => event.preventDefault()}>
          <label><span>Source</span><select value={provider()} onChange={(event) => changeFilter(setProvider, event.currentTarget.value)}><option value="">All sources</option><option value="factory">Factory</option><option value="linear">Linear</option></select></label>
          <label><span>State</span><select value={state()} onChange={(event) => changeFilter(setState, event.currentTarget.value)}><option value="">All states</option><option value="open">Open</option><option value="in_progress">In progress</option><option value="completed">Completed</option><option value="canceled">Canceled</option></select></label>
          <label><span>Project</span><select value={project()} onChange={(event) => changeFilter(setProject, event.currentTarget.value)}><option value="">All projects</option><For each={projectSnapshot()?.projects ?? []}>{(choice) => <option value={choice.projectId}>{choice.projectName}</option>}</For></select></label>
          <label><span>Approval</span><select value={approval()} onChange={(event) => changeFilter(setApproval, event.currentTarget.value)}><option value="">All modes</option><option value="gated">Gated</option><option value="automatic">Automatic</option></select></label>
          <label><span>Lifecycle</span><select value={activity()} onChange={(event) => changeFilter(setActivity, event.currentTarget.value)}><option value="">Any activity</option><option value="active">Active Run</option><option value="inactive">No active Run</option></select></label>
        </form>

        <Show when={!tasks.error && !projects.error} fallback={<InlineError message="The task ledger could not be loaded. Check the Factory connection and try again." />}>
          <div class="task-workspace">
            <section class="task-ledger" aria-labelledby="task-ledger-title">
              <div class="task-section-heading">
                <div><p class="section-label">Current scope</p><h2 id="task-ledger-title">Task ledger</h2></div>
                <span>{taskSnapshot()?.tasks.length ?? 0} shown</span>
              </div>
              <Show when={!tasks.loading || Boolean(taskSnapshot())} fallback={<LoadingRows />}>
                <Show when={(taskSnapshot()?.tasks.length ?? 0) > 0} fallback={<div class="empty-state task-empty"><strong>No tasks match this view.</strong><span>Adjust the filters or create the first native Factory task.</span></div>}>
                  <ol class="task-list">
                    <For each={taskSnapshot()?.tasks ?? []}>
                      {(task) => (
                        <li>
                          <a href={`/tasks/${task.ref.source}/${encodeURIComponent(task.ref.providerId)}`}>
                            <div class="task-row-identity"><span class={`task-source ${task.ref.source}`}>{task.ref.source}</span><strong>{task.ref.identifier}</strong></div>
                            <div class="task-row-title"><strong>{task.title}</strong><span>{task.projectId || "Managed Linear task"}</span></div>
                            <div class="task-row-status"><span>{runStateLabel(task.state)}</span><time datetime={task.updatedAt}>{formatTime(task.updatedAt)}</time></div>
                          </a>
                        </li>
                      )}
                    </For>
                  </ol>
                </Show>
              </Show>
              <Show when={cursorHistory().length > 0 || Boolean(taskSnapshot()?.nextCursor)}><nav class="task-pagination" aria-label="Task pages"><button class="text-button" type="button" disabled={cursorHistory().length === 0} onClick={previousTaskPage}>Previous</button><span>Page {cursorHistory().length + 1}</span><button class="text-button" type="button" disabled={!taskSnapshot()?.nextCursor} onClick={nextTaskPage}>Next</button></nav></Show>
            </section>

            <aside class="task-create" aria-labelledby="task-create-title">
              <div class="task-section-heading"><div><p class="section-label">Native intake</p><h2 id="task-create-title">Create task</h2></div></div>
              <Show when={enabledProjects().length > 0} fallback={<div class="task-dark-state"><strong>Native intake is dark.</strong><p>Enable a provisioned project through Factory task controls before creating native work.</p></div>}>
                <form onSubmit={(event) => void createTask(event)}>
                  <label><span>Title</span><input required maxlength="200" value={title()} onInput={(event) => setTitle(event.currentTarget.value)} /></label>
                  <label><span>Description</span><textarea maxlength="65536" rows="7" value={description()} onInput={(event) => setDescription(event.currentTarget.value)} /></label>
                  <label><span>Project</span><select value={createProject()} onChange={(event) => setCreateProject(event.currentTarget.value)}><For each={enabledProjects()}>{(choice) => <option value={choice.projectId}>{choice.projectName} · {choice.repository}</option>}</For></select></label>
                  <label><span>Approval</span><select value={createApproval()} onChange={(event) => setCreateApproval(event.currentTarget.value as "gated" | "automatic")}><option value="gated">Human gates</option><option value="automatic">Automatic progression</option></select></label>
                  <button class="primary-button" type="submit" disabled={createState() === "saving" || title().trim() === "" || createProject() === ""}>{createState() === "saving" ? "Creating…" : "Create native task"}</button>
                  <p classList={{ "task-form-status": true, failed: createState() === "failed" }} aria-live="polite">{createMessage()}</p>
                </form>
              </Show>
            </aside>
          </div>
        </Show>

        <footer class="activity-footer"><span>Native task mutations are journaled and revision-checked.</span><button class="text-button" type="button" onClick={() => void refetch()}>Refresh ledger</button></footer>
      </section>
    </main>
  );
}

function TaskDetailPage(props: { provider: string; id: string }): JSX.Element {
  const [detail, { refetch }] = createResource(
    () => `${props.provider}:${props.id}`,
    () => getTaskDetail(props.provider, props.id),
  );
  const detailSnapshot = (): NativeTaskDetail | TaskSummary | undefined => resourceSnapshot(detail);
  const native = createMemo(() => {
    const value = detailSnapshot();
    return value && "task" in value ? value as NativeTaskDetail : undefined;
  });
  const linear = createMemo(() => {
    const value = detailSnapshot();
    return value && !("task" in value) ? value as TaskSummary : undefined;
  });
  const [title, setTitle] = createSignal("");
  const [description, setDescription] = createSignal("");
  const [approval, setApproval] = createSignal<"gated" | "automatic">("gated");
  const [message, setMessage] = createSignal("");
  const [replyTo, setReplyTo] = createSignal("");
  const [linkLabel, setLinkLabel] = createSignal("");
  const [linkURL, setLinkURL] = createSignal("");
  const [gateKind, setGateKind] = createSignal("review");
  const [gateMode, setGateMode] = createSignal<"gated" | "automatic">("gated");
  const [artifactURL, setArtifactURL] = createSignal("");
  const [decisionReason, setDecisionReason] = createSignal("");
  const [busy, setBusy] = createSignal("");
  const [notice, setNotice] = createSignal("");
  const [noticeFailed, setNoticeFailed] = createSignal(false);
  let loadedRevision = 0;

  createEffect(() => {
    const task = native()?.task;
    if (!task || task.revision === loadedRevision) return;
    loadedRevision = task.revision;
    setTitle(task.title);
    setDescription(task.description ?? "");
    setApproval(task.approvalMode);
    document.title = `${task.ref.identifier} | Factory`;
  });
  createEffect(() => {
    const task = linear();
    if (task) document.title = `${task.ref.identifier} | Factory`;
  });

  async function mutate(label: string, path: string, body: Record<string, unknown>): Promise<boolean> {
    setBusy(label);
    setNoticeFailed(false);
    setNotice(`${label}…`);
    try {
      await taskRequest<unknown>(`/api/tasks/factory/${encodeURIComponent(props.id)}${path}`, "POST", body);
      await refetch();
      setNotice(`${label} succeeded.`);
      return true;
    } catch (error) {
      setNoticeFailed(true);
      setNotice(taskErrorMessage(error));
      if (error instanceof TaskConflict) await refetch();
      return false;
    } finally {
      setBusy("");
    }
  }

  async function saveTask(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const task = native()?.task;
    if (!task) return;
    setBusy("Saving task");
    setNoticeFailed(false);
    setNotice("Saving task…");
    try {
      await taskRequest<TaskMutationResult>(`/api/tasks/factory/${encodeURIComponent(props.id)}`, "PATCH", {
        expectedRevision: task.revision,
        title: title(),
        description: description(),
        approvalMode: approval(),
      });
      await refetch();
      setNotice("Task saved.");
    } catch (error) {
      setNoticeFailed(true);
      setNotice(taskErrorMessage(error));
      if (error instanceof TaskConflict) await refetch();
    } finally {
      setBusy("");
    }
  }

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="task-title">
        <ActivityHeader section="tasks" state={resourceState(detail.loading, detail.error, Boolean(detailSnapshot()))} label={detail.error ? "Task unavailable" : props.provider === "linear" ? "Managed Linear record" : "Native task journal"} />
        <Show when={!detail.error} fallback={<InlineError message="This task could not be loaded. It may have moved or Factory may be offline." />}>
          <Show when={!detail.loading || Boolean(detailSnapshot())} fallback={<LoadingRows />}>
            <Show when={linear()}>
              {(task) => (
                <article class="task-detail linear-detail">
                  <header class="task-detail-header"><div><span class="task-source linear">Linear · read only</span><h1 id="task-title">{task().title}</h1><p>{task().ref.identifier}</p></div><span class="task-state">{runStateLabel(task().state)}</span></header>
                  <div class="task-readonly-note"><strong>Managed coexistence record</strong><p>Factory loads this detail live from Linear without persisting its body. Continue discussion and edits in Linear.</p><dl class="task-metadata linear-task-metadata"><div><dt>Provider state</dt><dd>{task().stateName || runStateLabel(task().state)}</dd></div><div><dt>Project</dt><dd>{task().projectName || "No project"}</dd></div><Show when={task().latestRun}><div><dt>Latest Factory Run</dt><dd>{runStateLabel(task().latestRun!.state)}</dd></div><div><dt>Run updated</dt><dd>{formatTime(task().latestRun!.updatedAt)}</dd></div></Show></dl><Show when={task().description}><p class="task-linear-description">{task().description}</p></Show><Show when={(task().messages?.length ?? 0) > 0}><section class="linear-task-thread" aria-labelledby="linear-thread-title"><div class="task-section-heading"><div><p class="section-label">Live from Linear</p><h2 id="linear-thread-title">Discussion</h2></div><span>{task().messages?.length} messages</span></div><ol class="task-messages"><For each={task().messages ?? []}>{(message) => <li classList={{ reply: Boolean(message.parentId) }}><div><strong>{taskActorLabel(message.author)}</strong><time datetime={message.createdAt}>{formatTime(message.createdAt)}</time></div><p>{message.body}</p></li>}</For></ol></section></Show><Show when={task().externalUrl}><a class="primary-button task-external-link" href={task().externalUrl} target="_blank" rel="noreferrer">Open in Linear</a></Show></div>
                </article>
              )}
            </Show>
            <Show when={native()}>
              {(snapshot) => {
                const task = () => snapshot().task;
                const terminal = () => task().state === "completed" || task().state === "canceled";
                return (
                  <article class="task-detail">
                    <header class="task-detail-header"><div><span class="task-source factory">Factory native</span><h1 id="task-title">{task().title}</h1><p>{task().ref.identifier} · {task().projectId} · revision {task().revision}</p></div><span class="task-state">{runStateLabel(task().state)}</span></header>
                    <div class="task-detail-grid">
                      <section class="task-main-column">
                        <form class="task-edit-form task-panel" onSubmit={(event) => void saveTask(event)}>
                          <div class="task-section-heading"><div><p class="section-label">Definition</p><h2>Task brief</h2></div></div>
                          <label><span>Title</span><input required disabled={terminal()} maxlength="200" value={title()} onInput={(event) => setTitle(event.currentTarget.value)} /></label>
                          <label><span>Description</span><textarea disabled={terminal()} maxlength="65536" rows="10" value={description()} onInput={(event) => setDescription(event.currentTarget.value)} /></label>
                          <label><span>Approval mode</span><select disabled={terminal()} value={approval()} onChange={(event) => setApproval(event.currentTarget.value as "gated" | "automatic")}><option value="gated">Human gates</option><option value="automatic">Automatic progression</option></select></label>
                          <button class="secondary-button" type="submit" disabled={terminal() || busy() !== ""}>Save task</button>
                        </form>

                        <section class="task-panel" aria-labelledby="conversation-title">
                          <div class="task-section-heading"><div><p class="section-label">Durable thread</p><h2 id="conversation-title">Conversation</h2></div><span>{snapshot().messages.messages.length} messages</span></div>
                          <Show when={snapshot().messages.messages.length > 0} fallback={<div class="empty-state task-empty"><strong>No messages yet.</strong><span>Leave context for the next human or agent continuation.</span></div>}>
                            <ol class="task-messages"><For each={snapshot().messages.messages}>{(item) => <li classList={{ reply: Boolean(item.parentId) }}><div><strong>{taskActorLabel(item.author)}</strong><time datetime={item.createdAt}>{formatTime(item.createdAt)}</time></div><p>{item.body}</p><Show when={!terminal()}><button class="text-button" type="button" onClick={() => { setReplyTo(item.id); setMessage(""); }}>Reply</button></Show></li>}</For></ol>
                          </Show>
                          <Show when={!terminal()}><form class="task-inline-form" onSubmit={async (event) => { event.preventDefault(); const ok = await mutate(replyTo() ? "Posting reply" : "Posting message", "/messages", { expectedRevision: task().revision, parentId: replyTo(), body: message() }); if (ok) { setMessage(""); setReplyTo(""); } }}>
                            <label><span>{replyTo() ? `Replying to ${replyTo()}` : "New message"}</span><textarea required maxlength="32768" rows="4" value={message()} onInput={(event) => setMessage(event.currentTarget.value)} /></label>
                            <div class="task-form-actions"><button class="primary-button" type="submit" disabled={busy() !== "" || message().trim() === ""}>Post {replyTo() ? "reply" : "message"}</button><Show when={replyTo()}><button class="text-button" type="button" onClick={() => setReplyTo("")}>Cancel reply</button></Show></div>
                          </form></Show>
                        </section>

                        <section class="task-panel" aria-labelledby="evidence-title">
                          <div class="task-section-heading"><div><p class="section-label">References</p><h2 id="evidence-title">Evidence links</h2></div><span>{snapshot().links.length} links</span></div>
                          <ul class="task-links"><For each={snapshot().links}>{(link) => <li><a href={link.url} target="_blank" rel="noreferrer"><strong>{link.label}</strong><span>{link.url}</span></a></li>}</For></ul>
                          <Show when={!terminal()}><form class="task-inline-form task-link-form" onSubmit={async (event) => { event.preventDefault(); const ok = await mutate("Adding link", "/links", { expectedRevision: task().revision, label: linkLabel(), url: linkURL() }); if (ok) { setLinkLabel(""); setLinkURL(""); } }}><label><span>Label</span><input required maxlength="160" value={linkLabel()} onInput={(event) => setLinkLabel(event.currentTarget.value)} /></label><label><span>HTTPS URL</span><input required type="url" pattern="https://.*" value={linkURL()} onInput={(event) => setLinkURL(event.currentTarget.value)} /></label><button class="secondary-button" type="submit" disabled={busy() !== ""}>Add evidence</button></form></Show>
                        </section>
                      </section>

                      <aside class="task-side-column">
                        <section class="task-panel task-control-panel" aria-labelledby="control-title">
                          <div class="task-section-heading"><div><p class="section-label">Lifecycle</p><h2 id="control-title">Run control</h2></div></div>
                          <dl class="task-metadata"><div><dt>State</dt><dd>{runStateLabel(task().state)}</dd></div><div><dt>Approval</dt><dd>{runStateLabel(task().approvalMode)}</dd></div><div><dt>Updated</dt><dd>{formatTime(task().updatedAt)}</dd></div><div><dt>Messages</dt><dd>{task().messageCount}</dd></div></dl>
                          <button class="primary-button" type="button" disabled={busy() !== "" || !["open", "in_progress"].includes(task().state)} onClick={() => void mutate("Starting workflow", "/start", {})}>Start workflow</button>
                          <Show when={["open", "in_progress"].includes(task().state)}><button class="secondary-button danger-button task-cancel-button" type="button" disabled={busy() !== ""} onClick={() => void mutate("Canceling task", "/state", { expectedRevision: task().revision, state: "canceled" })}>Cancel task</button></Show>
                        </section>

                        <section class="task-panel" aria-labelledby="lifecycle-evidence-title">
                          <div class="task-section-heading"><div><p class="section-label">Mechanical record</p><h2 id="lifecycle-evidence-title">Lifecycle evidence</h2></div></div>
                          <Show when={task().routing}>{(routing) => <dl class="task-metadata"><div><dt>Repository</dt><dd>{routing().repository}</dd></div><div><dt>Workflow</dt><dd>{routing().workflowId}</dd></div><div><dt>Base branch</dt><dd>{routing().baseBranch}</dd></div><div><dt>Admitted</dt><dd>{formatTime(routing().admittedAt)}</dd></div></dl>}</Show>
                          <Show when={snapshot().latestRun}>{(run) => <><dl class="task-metadata"><div><dt>Latest Run</dt><dd>{runStateLabel(run().state)}</dd></div><div><dt>Run updated</dt><dd>{formatTime(run().updatedAt)}</dd></div></dl><div class="task-lifecycle-links"><Show when={agentRunHref(run())}>{(href) => <a class="text-link" href={href()}>Open Run</a>}</Show><Show when={run().ready}>{(ready) => <a class="text-link" href={`https://github.com/${ready().repository}/pull/${ready().pullRequest}`} target="_blank" rel="noreferrer">Open pull request</a>}</Show></div></> }</Show>
                          <Show when={task().completion}>{(completion) => <div class="task-completion-evidence"><strong>Completion verified</strong><p>{completion().evidenceRef}</p><time datetime={completion().completedAt}>{formatTime(completion().completedAt)}</time></div>}</Show>
                          <Show when={!task().routing && !snapshot().latestRun && !task().completion}><p class="task-limit-note">Lifecycle evidence appears after this task is admitted.</p></Show>
                        </section>

                        <section class="task-panel" aria-labelledby="gates-title">
                          <div class="task-section-heading"><div><p class="section-label">Authority</p><h2 id="gates-title">Gates</h2></div><span>{snapshot().gates.length}</span></div>
                          <ol class="task-gates"><For each={snapshot().gates}>{(gate) => <li><div><strong>{runStateLabel(gate.kind)}</strong><span class={`gate-status ${gate.status}`}>{runStateLabel(gate.status)}</span></div><p>{gate.mode === "gated" ? "Human decision required" : "Automatic gate"}</p><Show when={gate.artifactUrl}><a class="text-link" href={gate.artifactUrl} target="_blank" rel="noreferrer">Open artifact</a></Show><Show when={!terminal() && gate.status === "open" && gate.mode === "gated"}><label><span>Decision note</span><textarea rows="2" value={decisionReason()} onInput={(event) => setDecisionReason(event.currentTarget.value)} /></label><div class="task-form-actions"><button class="primary-button" type="button" disabled={busy() !== ""} onClick={() => void mutate("Approving gate", `/gates/${gate.id}/decision`, { expectedRevision: task().revision, action: "approve", reason: decisionReason() })}>Approve</button><button class="secondary-button" type="button" disabled={busy() !== ""} onClick={() => void mutate("Requesting revision", `/gates/${gate.id}/decision`, { expectedRevision: task().revision, action: "revise", reason: decisionReason() })}>Request revision</button></div></Show></li>}</For></ol>
                          <Show when={!terminal()}><form class="task-inline-form" onSubmit={async (event) => { event.preventDefault(); const ok = await mutate("Opening gate", "/gates", { expectedRevision: task().revision, kind: gateKind(), mode: gateMode(), artifactUrl: artifactURL() }); if (ok) setArtifactURL(""); }}><label><span>Gate kind</span><input required maxlength="80" value={gateKind()} onInput={(event) => setGateKind(event.currentTarget.value)} /></label><label><span>Mode</span><select value={gateMode()} onChange={(event) => setGateMode(event.currentTarget.value as "gated" | "automatic")}><option value="gated">Human gate</option><option value="automatic">Automatic</option></select></label><label><span>Artifact URL</span><input type="url" pattern="https://.*" value={artifactURL()} onInput={(event) => setArtifactURL(event.currentTarget.value)} /></label><button class="secondary-button" type="submit" disabled={busy() !== ""}>Open gate</button></form></Show>
                        </section>
                      </aside>
                    </div>
                    <Show when={notice()}><div classList={{ "task-notice": true, failed: noticeFailed() }} aria-live="polite"><span classList={{ dot: true, ready: !noticeFailed(), failed: noticeFailed() }} /><span>{notice()}</span></div></Show>
                  </article>
                );
              }}
            </Show>
          </Show>
        </Show>
        <footer class="activity-footer"><a class="text-link" href="/tasks">Back to task ledger</a><button class="text-button" type="button" onClick={() => void refetch()}>Refresh task</button></footer>
      </section>
    </main>
  );
}

function taskActorLabel(actor: TaskActor): string {
  if (actor.kind === "agent") return "Factory agent";
  if (actor.kind === "system") return "Factory system";
  const email = actor.id.split(":").at(-1);
  return email || "Operator";
}

function taskErrorMessage(error: unknown): string {
  if (error instanceof TaskConflict) return "This task changed elsewhere. The latest revision has been loaded; review it before retrying.";
  if (error instanceof TypeError) return "Factory is offline or unreachable. Your text remains in this form; reconnect and retry.";
  if (error instanceof Error) return error.message;
  return "The task request failed.";
}

function AgentPage(props: { load: () => Promise<AgentView> }): JSX.Element {
  const [agent, { refetch }] = createResource(props.load);
  const agentSnapshot = (): AgentView | undefined => resourceSnapshot(agent);
  const [selectedWindowID, setSelectedWindowID] = createSignal("");
  const [expandedStepIDs, setExpandedStepIDs] = createSignal<Set<string>>(
    new Set(),
  );
  const selectedWindow = (): AgentWindow | undefined => {
    const windows = agentSnapshot()?.windows ?? [];
    return windows.find((window) => window.id === selectedWindowID()) ?? windows[0];
  };
  const setStepExpanded = (stepID: string, expanded: boolean): void => {
    const windowID = selectedWindow()?.id ?? "";
    const key = `${windowID}:${stepID}`;
    const next = new Set(expandedStepIDs());
    if (expanded) {
      next.add(key);
    } else {
      next.delete(key);
    }
    setExpandedStepIDs(next);
  };
  const stepExpanded = (stepID: string): boolean => {
    const windowID = selectedWindow()?.id ?? "";
    return expandedStepIDs().has(`${windowID}:${stepID}`);
  };

  onMount(() => {
    document.title = "Agent run | Factory";
    const timer = window.setInterval(() => {
      if (shouldRefreshAgent(agentSnapshot())) {
        void refetch();
      }
    }, refreshIntervalMs);
    onCleanup(() => window.clearInterval(timer));
  });

  return (
    <main class="agent-page" id="main-content">
      <section class="agent-shell" aria-labelledby="agent-title">
        <ActivityHeader
          section="agents"
          state={resourceState(agent.loading, agent.error, agentSnapshot()?.live)}
          label={agentStatusLabel(agent.loading, agent.error, agentSnapshot())}
        />

        <Show
          when={agentSnapshot()}
          fallback={
            <div class="agent-loading">
              <p class="section-label">Agent observer</p>
              <h1 class="agent-title" id="agent-title">
                {agent.error ? "Run unavailable" : "Connecting"}
              </h1>
              <p>
                {agent.error
                  ? "This run could not be loaded. Check the route and try again."
                  : "Opening the authenticated tmux view."}
              </p>
            </div>
          }
        >
          {(snapshot) => (
            <>
              <div class="agent-hero">
                <div>
                  <p class="section-label">Agent run</p>
                  <h1 class="agent-title" id="agent-title">
                    {snapshot().issueIdentifier}
                  </h1>
                </div>
                <div class="agent-identity">
                  <code>{snapshot().id}</code>
                  <span>Read-only view, refreshes every 2 seconds</span>
                </div>
              </div>

              <dl class="agent-summary">
                <div>
                  <dt>State</dt>
                  <dd>{runStateLabel(snapshot().state)}</dd>
                </div>
                <div>
                  <dt>Attempt</dt>
                  <dd>{snapshot().attempts || "Queued"}</dd>
                </div>
                <div>
                  <dt>Windows</dt>
                  <dd>{snapshot().windows.length}</dd>
                </div>
                <div>
                  <dt>Observed</dt>
                  <dd>{formatObservationTime(snapshot().observedAt)}</dd>
                </div>
              </dl>

              <section class="agent-console" aria-labelledby="agent-console-title">
                <div class="console-heading">
                  <div>
                    <h2 id="agent-console-title">Session windows</h2>
                    <span>{agentConsoleLabel(snapshot())}</span>
                  </div>
                  <Show when={snapshot().attachCommand}>
                    {(command) => <code class="attach-command">{command()}</code>}
                  </Show>
                </div>

                <Show
                  when={snapshot().windows.length > 0}
                  fallback={
                    <div class="empty-state compact">
                      <strong>
                        {snapshot().state === "pending"
                          ? "Waiting for a session window."
                          : "No live windows are available."}
                      </strong>
                      <span>
                        Pending runs appear here after tmux starts. Completed runs
                        show their retained principal and child-agent histories.
                      </span>
                    </div>
                  }
                >
                  <div class="window-tabs" aria-label="Agent windows">
                    <For each={snapshot().windows}>
                      {(window) => (
                        <button
                          type="button"
                          aria-pressed={selectedWindow()?.id === window.id}
                          onClick={() => setSelectedWindowID(window.id)}
                        >
                          <strong>{window.name}</strong>
                          <span>{window.command}</span>
                        </button>
                      )}
                    </For>
                  </div>
                  <Show
                    when={(selectedWindow()?.steps?.length ?? 0) > 0}
                    fallback={
                      <pre class="terminal-output" tabIndex={0}>
                        <code>
                          {selectedWindow()?.output ||
                            "The window is active. Waiting for output."}
                        </code>
                      </pre>
                    }
                  >
                    <div class="step-list">
                      <For each={selectedWindow()?.steps ?? []}>
                        {(step) => (
                          <details
                            classList={{
                              "log-step": true,
                              narrative: agentStepIsNarrative(step),
                              running: agentStepState(step) === "running",
                              failed: agentStepState(step) === "failed",
                            }}
                            open={stepExpanded(step.id)}
                            onToggle={(event) =>
                              setStepExpanded(
                                step.id,
                                event.currentTarget.open,
                              )
                            }
                          >
                            <summary>
                              <span class="step-icon" aria-hidden="true">
                                <AgentStepIcon step={step} />
                              </span>
                              <span class="step-copy">
                                <span class="step-action">{step.action || "Observed"}</span>
                                <strong>{step.summary}</strong>
                              </span>
                              <Show when={agentStepStateLabel(step)}>
                                {(label) => <span class="step-state">{label()}</span>}
                              </Show>
                            </summary>
                            <div class="step-evidence">
                              <Show when={agentStepDetail(step)}>
                                {(detail) => (
                                  <AgentStepEvidence
                                    label={agentStepDetailLabel(step)}
                                    value={detail()}
                                  />
                                )}
                              </Show>
                              <Show when={step.output}>
                                {(output) => <AgentStepEvidence label="Output" value={output()} />}
                              </Show>
                              <Show when={step.error}>
                                {(error) => <AgentStepEvidence label="Error" value={error()} error />}
                              </Show>
                              <details class="raw-event" open={!agentStepHasEvidence(step)}>
                                <summary>
                                  <span>Raw event</span>
                                  <code>{runStateLabel(step.type || "event")}</code>
                                </summary>
                                <pre class="step-payload" tabIndex={0}>
                                  <code>{step.payload}</code>
                                </pre>
                              </details>
                            </div>
                          </details>
                        )}
                      </For>
                    </div>
                  </Show>
                </Show>
              </section>

              <Show when={snapshot().detail}>
                {(detail) => (
                  <section class="run-detail" aria-labelledby="run-detail-title">
                    <h2 id="run-detail-title">Run detail</h2>
                    <p>{detail()}</p>
                  </section>
                )}
              </Show>

              <footer class="activity-footer">
                <span>Live and retained output is authenticated and read-only.</span>
                <a class="text-link" href="/agents">
                  Back to agents
                </a>
              </footer>
            </>
          )}
        </Show>
      </section>
    </main>
  );
}

function AgentStepEvidence(props: {
  label: string;
  value: string;
  error?: boolean;
}): JSX.Element {
  return (
    <section classList={{ "step-evidence-section": true, error: Boolean(props.error) }}>
      <span>{props.label}</span>
      <pre tabIndex={0}><code>{props.value}</code></pre>
    </section>
  );
}

function AgentStepIcon(props: { step: AgentStep }): JSX.Element {
  const kind = (): "narrative" | "command" | "search" | "file" | "tool" | "error" | "event" => {
    const type = props.step.type.toLowerCase();
    if (agentStepState(props.step) === "failed" || type === "error") return "error";
    if (agentStepIsNarrative(props.step)) return "narrative";
    if (["command_execution", "bash", "shell"].includes(type)) return "command";
    if (type.includes("search") || ["grep", "glob"].includes(type)) return "search";
    if (type.includes("file") || ["read", "write", "edit"].includes(type)) return "file";
    if (type.includes("tool") || type.includes("mcp")) return "tool";
    return "event";
  };
  return (
    <svg viewBox="0 0 20 20">
      <Show when={kind() === "narrative"}>
        <path d="M3.5 4.5h13v8h-7l-4 3v-3h-2z" />
      </Show>
      <Show when={kind() === "command"}>
        <path d="m4 6 3.5 4L4 14m6 0h6" />
      </Show>
      <Show when={kind() === "search"}>
        <circle cx="8.5" cy="8.5" r="4.5" />
        <path d="m12 12 4 4" />
      </Show>
      <Show when={kind() === "file"}>
        <path d="M5 2.75h6l4 4v10.5H5zM11 2.75v4h4M7.5 11h5M7.5 14h4" />
      </Show>
      <Show when={kind() === "tool"}>
        <path d="M6 4.5h8v4H6zM3.5 11.5h5v4h-5zM11.5 11.5h5v4h-5zM10 8.5v3M6 11.5v-1.25h8v1.25" />
      </Show>
      <Show when={kind() === "error"}>
        <circle cx="10" cy="10" r="7" />
        <path d="M10 6v5m0 3v.1" />
      </Show>
      <Show when={kind() === "event"}>
        <circle cx="10" cy="10" r="5.5" />
        <circle cx="10" cy="10" r="1" class="step-icon-fill" />
      </Show>
    </svg>
  );
}

function agentStepIsNarrative(step: AgentStep): boolean {
  return ["text", "agent_message", "reasoning", "result"].includes(step.type.toLowerCase());
}

function agentStepState(step: AgentStep): "running" | "failed" | "complete" {
  const status = (step.status ?? "").toLowerCase().replaceAll("-", "_");
  if (step.error || ["failed", "error", "blocked", "cancelled", "canceled"].includes(status)) {
    return "failed";
  }
  if (["in_progress", "running", "started", "pending"].includes(status)) {
    return "running";
  }
  return "complete";
}

function agentStepStateLabel(step: AgentStep): string | undefined {
  const state = agentStepState(step);
  if (state === "running") return "Running";
  if (state === "failed") return "Failed";
  return undefined;
}

function agentStepDetail(step: AgentStep): string | undefined {
  if (!step.detail || (agentStepIsNarrative(step) && step.detail === step.summary)) return undefined;
  return step.detail;
}

function agentStepDetailLabel(step: AgentStep): string {
  const type = step.type.toLowerCase();
  if (["command_execution", "bash", "shell"].includes(type)) return "Command";
  if (type === "file_change") return "Files";
  return "Input";
}

function agentStepHasEvidence(step: AgentStep): boolean {
  return Boolean(agentStepDetail(step) || step.output || step.error);
}
function countRunStates(runs: AgentActivityRun[]): ActivityCount[] {
  const counts = new Map<string, number>();
  for (const run of runs) {
    const label = runStateLabel(run.state);
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([label, count]) => ({ label, count }))
    .sort((left, right) => right.count - left.count || left.label.localeCompare(right.label));
}

function runLifecycleDetail(run: AgentActivityRun): string {
  const details: string[] = [];
  if (run.ready) {
    details.push(`PR #${run.ready.pullRequest}`);
    details.push(`HEAD ${shortOID(run.ready.verifiedHeadOid)}`);
  }
  if (run.mergeCommitOid) {
    details.push(`MERGE ${shortOID(run.mergeCommitOid)}`);
  }
  if (run.nextReconcileAt) {
    details.push(`NEXT ${formatTime(run.nextReconcileAt)}`);
  }
  if (run.resumeCount) {
    details.push(`RESUMES ${run.resumeCount}`);
  }
  if (run.reconcileFailures) {
    details.push(`REFRESH FAILURES ${run.reconcileFailures}`);
  }
  if (run.completion?.deploymentId) {
    details.push(`DEPLOY ${run.completion.deploymentId}`);
  }
  if (run.terminalRejection) {
    details.push(`REJECTED ${run.terminalRejection}`);
  }
  if (details.length === 0) {
    return run.attempts ? `ATTEMPT ${run.attempts}` : "QUEUED";
  }
  return details.join(" · ");
}

function shortOID(value: string | undefined): string {
  return value ? value.slice(0, 12) : "unknown";
}

function formatObservationTime(value: string): string {
  return observationTimeFormatter.format(new Date(value));
}

function attributeEntries(attributes: Record<string, string[]> | undefined): [string, string[]][] {
  return Object.entries(attributes ?? {}).sort(([left], [right]) => left.localeCompare(right));
}

function formatPayload(value: unknown): string {
  return JSON.stringify(value, null, 2) ?? "null";
}

function listenerLabel(
  loading: boolean,
  error: unknown,
  hasSnapshot: boolean,
): string {
  if (error) {
    return "Connection error";
  }
  if (loading && !hasSnapshot) {
    return "Connecting";
  }
  return "Listener online";
}

function agentStatusLabel(
  loading: boolean,
  error: unknown,
  agent: AgentView | undefined,
): string {
  if (error) {
    return "Observer error";
  }
  if (loading && !agent) {
    return "Connecting";
  }
  if (agent?.live) {
    return "Session live";
  }
  if (agent?.state === "awaiting_human_merge") {
    return "Run parked";
  }
  if (agent?.state === "post_merge_pending") {
    return "Continuation queued";
  }
  if (agent && !runStateIsActive(agent.state) && agent.windows.length > 0) {
    return "History retained";
  }
  return "Session offline";
}

function shouldRefreshAgent(agent: AgentView | undefined): boolean {
  if (!agent) {
    return true;
  }
  return agent.live || runStateIsActive(agent.state);
}

function runStateIsActive(state: string): boolean {
  return [
    "pending",
    "post_merge_pending",
    "starting",
    "running",
    "awaiting_human_merge",
  ].includes(state);
}

function agentConsoleLabel(agent: AgentView): string {
  if (agent.live) {
    return "Live steps · expand for evidence and raw event";
  }
  if (agent.windows.length > 0) {
    return "Retained run history · expand for evidence and raw event";
  }
  return "This run has no retained output";
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("Root element not found");
}

const currentPath = window.location.pathname;
const agentActivityRoute = /^\/agents\/([^/]+)\/(\d+)\/run$/.exec(currentPath);
const requestedAgentSource = new URLSearchParams(window.location.search).get("source");
const agentSource = requestedAgentSource === "factory" || requestedAgentSource === "linear"
  ? requestedAgentSource
  : undefined;
const taskDetailRoute = /^\/tasks\/(factory|linear)\/([^/]+)$/.exec(currentPath);

render(() => {
  if (currentPath === "/") {
    return <HomePage />;
  }
  if (currentPath === "/home") {
    return <ActivityPage />;
  }
  if (currentPath === "/wire") {
    return <WirePage />;
  }
  if (currentPath === "/agents") {
    return <AgentActivityPage />;
  }
  if (currentPath === "/tasks") {
    return <TasksPage />;
  }
  if (currentPath === "/settings") {
    return <SettingsPage />;
  }
  if (currentPath === "/workflows") {
    return <WorkflowsPage />;
  }
  if (currentPath === "/triggers") {
    return <TriggersPage />;
  }
  if (agentActivityRoute) {
    return (
      <AgentPage
        load={() => getAgentByReference(agentActivityRoute[1], agentActivityRoute[2], agentSource)}
      />
    );
  }
  if (taskDetailRoute) {
    return <TaskDetailPage provider={taskDetailRoute[1]} id={decodeURIComponent(taskDetailRoute[2])} />;
  }
  return <main class="home-page"><section class="home-shell"><h1>Not found</h1></section></main>;
}, root);
