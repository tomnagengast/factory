import {
  createMemo,
  createResource,
  createSignal,
  For,
  onCleanup,
  onMount,
  Show,
  type JSX,
} from "solid-js";
import { render } from "solid-js/web";
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

type AgentRun = {
  id: string;
  state: string;
  attempts: number;
  duplicateTriggers: number;
  createdAt: string;
  updatedAt: string;
  startedAt?: string;
  finishedAt?: string;
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

type ReadyCheckpoint = {
  contractVersion: number;
  repository: string;
  pullRequest: number;
  baseBranch: string;
  headBranch: string;
  verifiedHeadOid: string;
  pullRequestUpdatedAt?: string;
  createdAt: string;
  validatedAt?: string;
};

type CompletionValidation = {
  accepted: boolean;
  intent: string;
  blocker?: string;
  state: string;
  reason: string;
  validatedAt: string;
  mergeCommitOid?: string;
  deploymentId?: string;
  deploymentCommit?: string;
};

type AgentActivityRun = AgentRun & {
  issueIdentifier: string;
  ready?: ReadyCheckpoint;
  mergeCommitOid?: string;
  lastGitHubCursor?: number;
  lastAuthoritativeRefreshAt?: string;
  nextReconcileAt?: string;
  reconcileFailures?: number;
  resumeCount?: number;
  terminalRejection?: string;
  completion?: CompletionValidation;
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
  summary: string;
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

type TriggerSettings = {
  enabled: boolean;
  workflowId: string;
};

type LinearLabelTriggerSettings = TriggerSettings & {
  label: string;
};

type WorkflowSettings = {
  id: string;
  name: string;
  enabled: boolean;
  runner: "do";
  steps: string[];
};

type ProviderSettings = {
  model: string;
  effort: string;
};

type FactorySettings = {
  schema: number;
  revision: number;
  updatedAt?: string;
  triggers: {
    linearLabel: LinearLabelTriggerSettings;
    linearComment: TriggerSettings;
  };
  workflows: WorkflowSettings[];
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
  workflows: WorkflowSettings[];
  observedSources: string[];
  ruleStatus: TriggerRuleStatus[];
  scheduleStatus: TriggerScheduleStatus[];
  recentInvocations: TriggerInvocation[];
  protectedRoutes: { id: string; name: string; description: string }[];
};

type TriggerSaveResult = { snapshot: TriggerResponse; conflict: boolean };
type SubjectFilterMode = "wildcard" | "absent" | "exact";

const refreshIntervalMs = 2000;
type ActivitySection = "home" | "wire" | "agents" | "triggers" | "settings";

const activityPageSize = 25;
const timeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

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

async function getAgentByReference(
  issueIdentifier: string,
  startedAt: string,
): Promise<AgentView> {
  return getJSON<AgentView>(
    `/api/agents/${encodeURIComponent(issueIdentifier)}/${encodeURIComponent(startedAt)}/run`,
    "Agent request",
  );
}

async function getSettings(): Promise<FactorySettings> {
  return getJSON<FactorySettings>("/api/settings", "Settings request");
}

async function getTriggers(): Promise<TriggerResponse> {
  return getJSON<TriggerResponse>("/api/triggers", "Triggers request");
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

async function getJSON<T>(url: string, label: string): Promise<T> {
  const response = await fetch(url, {
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) {
    throw new Error(`${label} failed with ${response.status}`);
  }
  return response.json() as Promise<T>;
}

function HomePage(): JSX.Element {
  const [health] = createResource(getHealth);

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
                ready: health()?.status === "ok",
                failed: Boolean(health.error),
              }}
            />
            <Show when={!health.loading} fallback={<span>Connecting</span>}>
              <span>
                {health.error
                  ? "Offline"
                  : `Systems online · ${shortOID(health()?.commit)} · contract ${health()?.contractVersion}`}
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

function ActivityHeader(props: {
  section: ActivitySection;
  state: "loading" | "ready" | "failed";
  label: string;
}): JSX.Element {
  return (
    <header class="activity-header">
      <a class="brand-link" href="/">
        <span class="mark" aria-hidden="true">
          F
        </span>
        <span>Factory</span>
      </a>
      <nav class="activity-nav" aria-label="Activity sections">
        <a
          classList={{ active: props.section === "home" }}
          aria-current={props.section === "home" ? "page" : undefined}
          href="/home"
        >
          Overview
        </a>
        <a
          classList={{ active: props.section === "wire" }}
          aria-current={props.section === "wire" ? "page" : undefined}
          href="/wire"
        >
          Wire
        </a>
        <a
          classList={{ active: props.section === "agents" }}
          aria-current={props.section === "agents" ? "page" : undefined}
          href="/agents"
        >
          Agents
        </a>
        <a
          classList={{ active: props.section === "triggers" }}
          aria-current={props.section === "triggers" ? "page" : undefined}
          href="/triggers"
        >
          Triggers
        </a>
        <a
          classList={{ active: props.section === "settings" }}
          aria-current={props.section === "settings" ? "page" : undefined}
          href="/settings"
        >
          Settings
        </a>
      </nav>
      <div class="listener" aria-live="polite">
        <span
          classList={{
            dot: true,
            ready: props.state === "ready",
            failed: props.state === "failed",
          }}
        />
        <span>{props.label}</span>
      </div>
    </header>
  );
}

function ActivityPage(): JSX.Element {
  const [activity, { refetch }] = createResource(getActivity);

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
          label={listenerLabel(activity.loading, activity.error, Boolean(activity()))}
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
            <dd>{activity()?.total ?? 0}</dd>
          </div>
          <div>
            <dt>Last received</dt>
            <dd>{formatTime(activity()?.lastReceivedAt)}</dd>
          </div>
          <div>
            <dt>Agent runs</dt>
            <dd>{activity()?.agentRuns.total ?? 0}</dd>
          </div>
          <div>
            <dt>Active loops</dt>
            <dd>{activity()?.agentRuns.active ?? 0}</dd>
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
              {activity()?.events.filter((event) => !event.type.startsWith("github/"))
                .length ?? 0} recent events
            </span>
          </a>
          <a class="destination destination-agents" href="/agents">
            <span class="destination-index">02 / Agents</span>
            <strong>Follow autonomous work</strong>
            <p>
              Review loop state by issue, then enter the authenticated live tmux
              observer for a specific run.
            </p>
            <span class="destination-meta">
              {activity()?.agentRuns.active ?? 0} active now
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
            <dd>{activity()?.retained ?? 0}</dd>
          </div>
          <div>
            <dt>Matching events</dt>
            <dd>{activity()?.matching ?? 0}</dd>
          </div>
          <div>
            <dt>Pending dispatch</dt>
            <dd>{activity()?.status.pending ?? 0}</dd>
          </div>
          <div>
            <dt>Rejected total</dt>
            <dd>{activity()?.status.rejectedTotal ?? 0}</dd>
          </div>
        </dl>

        <form class="wire-filters" aria-label="Wire filters" onSubmit={(event) => event.preventDefault()}>
          <label>
            <span>Source</span>
            <select value={source()} onChange={(event) => changeFilter(setSource, event.currentTarget.value)}>
              <option value="">All sources</option>
              <For each={activity()?.sourceCounts ?? []}>
                {(count) => <option value={count.label}>{count.label} ({count.count})</option>}
              </For>
            </select>
          </label>
          <label>
            <span>Event type</span>
            <select value={eventType()} onChange={(event) => changeFilter(setEventType, event.currentTarget.value)}>
              <option value="">All event types</option>
              <For each={activity()?.typeCounts ?? []}>
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
              items={activity()?.sourceCounts ?? []}
            />
            <ActivityChart
              title="Recent hourly volume"
              subtitle="Up to twelve active UTC hours"
              items={activity()?.hourCounts ?? []}
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
                pageCount={activity()?.pageCount ?? 0}
                onChange={changePage}
              />
            </div>

            <div class="event-workspace">
              <div class="event-scroll" tabIndex={0} aria-label="System events">
                <Show
                  when={!activity.loading || Boolean(activity())}
                  fallback={<LoadingRows />}
                >
                  <Show
                    when={(activity()?.records.length ?? 0) > 0}
                    fallback={
                      <div class="empty-state compact">
                        <strong>No events match these filters.</strong>
                        <span>Change the filters or wait for the next journal record.</span>
                      </div>
                    }
                  >
                    <ol class="event-list selectable-events">
                      <For each={activity()?.records ?? []}>
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
                      when={!eventDetail.error && eventDetail()}
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
  const stateCounts = createMemo(() => countRunStates(activity()?.runs ?? []));

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
            Every retained Factory loop, addressed by its Linear issue and start
            time. Enter a run to observe the live session or durable result.
          </p>
        </div>

        <dl class="activity-summary detail-summary">
          <div>
            <dt>Total runs</dt>
            <dd>{activity()?.total ?? 0}</dd>
          </div>
          <div>
            <dt>Active loops</dt>
            <dd>{activity()?.active ?? 0}</dd>
          </div>
          <div>
            <dt>Terminal runs</dt>
            <dd>{Math.max(0, (activity()?.runs.length ?? 0) - (activity()?.active ?? 0))}</dd>
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
              <strong>{activity()?.active ?? 0}</strong>
              <p>
                {(activity()?.active ?? 0) === 1 ? "loop is" : "loops are"} active across the
                Factory runner.
              </p>
            </div>
          </section>

          <section class="run-feed dedicated-run-feed" aria-labelledby="run-feed-title">
            <div class="feed-heading">
              <h2 id="run-feed-title">Run ledger</h2>
              <span>Issue context is authenticated</span>
            </div>

            <Show
              when={!activity.loading || Boolean(activity())}
              fallback={<LoadingRows />}
            >
              <Show
                when={(activity()?.runs.length ?? 0) > 0}
                fallback={
                  <div class="empty-state compact">
                    <strong>No agent run has been claimed.</strong>
                    <span>Apply the Factory label to a Linear issue.</span>
                  </div>
                }
              >
                <ol class="run-list private-run-list">
                  <For each={activity()?.runs ?? []}>
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
          when={triggers()}
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
  const enabledWorkflows = (): WorkflowSettings[] =>
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
            <p>Configured rules are additive. These routes cannot be changed here.</p>
          </div>
          <div class="protected-route-list">
            <For each={response().protectedRoutes}>
              {(route) => <article><span>Locked</span><h3>{route.name}</h3><p>{route.description}</p></article>}
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
  workflows: WorkflowSettings[];
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
    filter.type === "telemetry" || filter.type === "lifecycle" || filter.type === "agent-record" || filter.type === "agent-run";
}

function ruleScopeSummary(rule: TriggerRule): string {
  const parts = [rule.filter.source || "any source", rule.filter.type || "any type", rule.filter.action || "any action"];
  if (rule.filter.subject !== undefined) parts.push(rule.filter.subject === "" ? "subject absent" : `subject ${rule.filter.subject}`);
  const attributes = Object.keys(rule.filter.attributes ?? {}).length;
  if (attributes) parts.push(`${attributes} attribute ${attributes === 1 ? "match" : "matches"}`);
  return parts.join(" / ");
}

function validateTriggerDraft(registry: TriggerRegistry, workflows: WorkflowSettings[]): string | undefined {
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

function SettingsPage(): JSX.Element {
  const [settings] = createResource(getSettings);

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
          when={settings()}
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

  function workflowAssigned(id: string): boolean {
    const triggers = draft().triggers;
    return triggers.linearLabel.workflowId === id || triggers.linearComment.workflowId === id;
  }

  function addWorkflow(): void {
    update((next) => {
      const ids = new Set(next.workflows.map((workflow) => workflow.id));
      let sequence = next.workflows.length + 1;
      while (ids.has(`workflow-${sequence}`)) {
        sequence += 1;
      }
      next.workflows.push({
        id: `workflow-${sequence}`,
        name: `Workflow ${sequence}`,
        enabled: true,
        runner: "do",
        steps: ["Describe the first workflow step"],
      });
    });
  }

  function removeWorkflow(id: string): void {
    if (workflowAssigned(id) || draft().workflows.length === 1) {
      return;
    }
    update((next) => {
      next.workflows = next.workflows.filter((workflow) => workflow.id !== id);
    });
  }

  function moveStep(workflowIndex: number, stepIndex: number, direction: -1 | 1): void {
    const target = stepIndex + direction;
    if (target < 0 || target >= draft().workflows[workflowIndex].steps.length) {
      return;
    }
    update((next) => {
      const steps = next.workflows[workflowIndex].steps;
      [steps[stepIndex], steps[target]] = [steps[target], steps[stepIndex]];
    });
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
          <div>
            <dt>Schema</dt>
            <dd>{draft().schema}</dd>
          </div>
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

        <section class="settings-section" aria-labelledby="workflow-settings-title">
          <div class="settings-section-heading workflow-heading">
            <div>
              <h2 id="workflow-settings-title">Workflows</h2>
              <p>Ordered declarative steps are added to the fixed, safety-gated `$do` lifecycle.</p>
            </div>
            <button
              class="secondary-button"
              type="button"
              disabled={draft().workflows.length >= 8}
              onClick={addWorkflow}
            >
              Add workflow
            </button>
          </div>
          <div class="workflow-list">
            <For each={draft().workflows}>
              {(workflow, workflowIndex) => (
                <article class="workflow-editor" aria-labelledby={`workflow-${workflow.id}`}>
                  <div class="workflow-meta">
                    <div>
                      <span class="workflow-id">{workflow.id}</span>
                      <h3 id={`workflow-${workflow.id}`}>{workflow.name || "Untitled workflow"}</h3>
                    </div>
                    <div class="workflow-actions">
                      <Toggle
                        checked={workflow.enabled}
                        disabled={workflowAssigned(workflow.id)}
                        label={workflowAssigned(workflow.id) ? "Assigned" : "Enabled"}
                        compact
                        onChange={(checked) => update((next) => { next.workflows[workflowIndex()].enabled = checked; })}
                      />
                      <button
                        class="text-button danger-button"
                        type="button"
                        disabled={workflowAssigned(workflow.id) || draft().workflows.length === 1}
                        onClick={() => removeWorkflow(workflow.id)}
                      >
                        Remove
                      </button>
                    </div>
                  </div>
                  <div class="workflow-fields">
                    <Field label="Workflow name">
                      <input
                        required
                        maxlength={80}
                        value={workflow.name}
                        onInput={(event) => update((next) => { next.workflows[workflowIndex()].name = event.currentTarget.value; })}
                      />
                    </Field>
                    <Field label="Runner" hint="The executable lifecycle is intentionally fixed.">
                      <input value="$do" readOnly />
                    </Field>
                  </div>
                  <div class="workflow-steps">
                    <div class="step-heading">
                      <h4>Ordered steps</h4>
                      <span>{workflow.steps.length} / 20</span>
                    </div>
                    <ol>
                      <For each={workflow.steps}>
                        {(step, stepIndex) => (
                          <li>
                            <span class="step-number">{String(stepIndex() + 1).padStart(2, "0")}</span>
                            <input
                              aria-label={`Step ${stepIndex() + 1} for ${workflow.name}`}
                              required
                              maxlength={240}
                              value={step}
                              onInput={(event) => update((next) => { next.workflows[workflowIndex()].steps[stepIndex()] = event.currentTarget.value; })}
                            />
                            <div class="step-actions" aria-label={`Reorder step ${stepIndex() + 1}`}>
                              <button type="button" disabled={stepIndex() === 0} onClick={() => moveStep(workflowIndex(), stepIndex(), -1)}>
                                Up
                              </button>
                              <button type="button" disabled={stepIndex() === workflow.steps.length - 1} onClick={() => moveStep(workflowIndex(), stepIndex(), 1)}>
                                Down
                              </button>
                              <button
                                type="button"
                                disabled={workflow.steps.length === 1}
                                onClick={() => update((next) => { next.workflows[workflowIndex()].steps.splice(stepIndex(), 1); })}
                              >
                                Remove
                              </button>
                            </div>
                          </li>
                        )}
                      </For>
                    </ol>
                    <button
                      class="text-button"
                      type="button"
                      disabled={workflow.steps.length >= 20}
                      onClick={() => update((next) => { next.workflows[workflowIndex()].steps.push("Describe the next workflow step"); })}
                    >
                      Add step
                    </button>
                  </div>
                </article>
              )}
            </For>
          </div>
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

function InlineError(props: { message: string }): JSX.Element {
  return (
    <div class="inline-error" role="alert">
      <strong>Connection failed</strong>
      <span>{props.message}</span>
    </div>
  );
}

function LoadingRows(): JSX.Element {
  return (
    <div class="loading-rows" aria-label="Loading activity">
      <span />
      <span />
      <span />
    </div>
  );
}

function AgentPage(props: { load: () => Promise<AgentView> }): JSX.Element {
  const [agent, { refetch }] = createResource(props.load);
  const [selectedWindowID, setSelectedWindowID] = createSignal("");
  const [expandedStepIDs, setExpandedStepIDs] = createSignal<Set<string>>(
    new Set(),
  );
  const selectedWindow = (): AgentWindow | undefined => {
    const windows = agent()?.windows ?? [];
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
      if (shouldRefreshAgent(agent())) {
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
          state={resourceState(agent.loading, agent.error, agent()?.live)}
          label={agentStatusLabel(agent.loading, agent.error, agent())}
        />

        <Show
          when={agent()}
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
                            class="log-step"
                            open={stepExpanded(step.id)}
                            onToggle={(event) =>
                              setStepExpanded(
                                step.id,
                                event.currentTarget.open,
                              )
                            }
                          >
                            <summary>
                              <span class={`step-status ${step.status ?? ""}`}>
                                {step.status
                                  ? runStateLabel(step.status)
                                  : "Event"}
                              </span>
                              <strong>{step.summary}</strong>
                              <code>{runStateLabel(step.type)}</code>
                            </summary>
                            <pre class="step-payload" tabIndex={0}>
                              <code>{step.payload}</code>
                            </pre>
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

function agentRunHref(run: AgentActivityRun): string | undefined {
  if (!run.startedAt) {
    return undefined;
  }
  return `/agents/${encodeURIComponent(run.issueIdentifier)}/${new Date(run.startedAt).getTime()}/run`;
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

function runStateLabel(value: string): string {
  return value.replace(/(^|[-_])([a-z])/g, (_, prefix, letter: string) =>
    `${prefix ? " " : ""}${letter.toUpperCase()}`,
  );
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

function formatTime(value: string | null | undefined): string {
  if (!value) {
    return "No activity yet";
  }
  return timeFormatter.format(new Date(value));
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
    return "Live steps · expand for raw payload";
  }
  if (agent.windows.length > 0) {
    return "Retained run history · expand for raw payload";
  }
  return "This run has no retained output";
}

function resourceState(
  loading: boolean,
  error: unknown,
  ready = true,
): "loading" | "ready" | "failed" {
  if (error) {
    return "failed";
  }
  if (loading || !ready) {
    return "loading";
  }
  return "ready";
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("Root element not found");
}

const currentPath = window.location.pathname;
const agentActivityRoute = /^\/agents\/([^/]+)\/(\d+)\/run$/.exec(currentPath);

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
  if (currentPath === "/settings") {
    return <SettingsPage />;
  }
  if (currentPath === "/triggers") {
    return <TriggersPage />;
  }
  if (agentActivityRoute) {
    return (
      <AgentPage
        load={() => getAgentByReference(agentActivityRoute[1], agentActivityRoute[2])}
      />
    );
  }
  return <main class="home-page"><section class="home-shell"><h1>Not found</h1></section></main>;
}, root);
