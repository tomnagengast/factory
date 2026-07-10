import {
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
};

type ActivityEvent = {
  type: string;
  action: string;
  receivedAt: string;
};

type ActivitySnapshot = {
  status: string;
  total: number;
  lastReceivedAt: string | null;
  events: ActivityEvent[];
  agentRuns: AgentRunSnapshot;
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

type AgentWindow = {
  id: string;
  name: string;
  command: string;
  output: string;
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
  live: boolean;
  attachCommand?: string;
  windows: AgentWindow[];
};

const timeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

async function getHealth(): Promise<Health> {
  const response = await fetch("/api/healthz");
  if (!response.ok) {
    throw new Error(`Health check failed with ${response.status}`);
  }
  return response.json() as Promise<Health>;
}

async function getActivity(): Promise<ActivitySnapshot> {
  const response = await fetch("/api/activity", { cache: "no-store" });
  if (!response.ok) {
    throw new Error(`Activity request failed with ${response.status}`);
  }
  return response.json() as Promise<ActivitySnapshot>;
}

async function getAgent(runID: string): Promise<AgentView> {
  const response = await fetch(`/api/agents/${encodeURIComponent(runID)}`, {
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) {
    throw new Error(`Agent request failed with ${response.status}`);
  }
  return response.json() as Promise<AgentView>;
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
            <Show
              when={!health.loading}
              fallback={<span>Connecting</span>}
            >
              <span>{health.error ? "Offline" : "Systems online"}</span>
            </Show>
          </div>
          <a class="text-link" href="/activity">
            View activity
          </a>
        </footer>
      </section>
    </main>
  );
}

function ActivityPage(): JSX.Element {
  const [activity, { refetch }] = createResource(getActivity);

  onMount(() => {
    document.title = "Activity | Factory";
    const timer = window.setInterval(() => void refetch(), 5000);
    onCleanup(() => window.clearInterval(timer));
  });

  return (
    <main class="activity-page">
      <section class="activity-shell" aria-labelledby="activity-title">
        <header class="activity-header">
          <a class="brand-link" href="/">
            <span class="mark" aria-hidden="true">
              F
            </span>
            <span>Factory</span>
          </a>
          <div class="listener" aria-live="polite">
            <span
              classList={{
                dot: true,
                ready: activity()?.status === "listening",
                failed: Boolean(activity.error),
              }}
            />
            <span>
              {listenerLabel(
                activity.loading,
                activity.error,
                Boolean(activity()),
              )}
            </span>
          </div>
        </header>

        <div class="activity-hero">
          <div>
            <p class="section-label">Linear webhook</p>
            <h1 class="activity-title" id="activity-title">
              Activity
            </h1>
          </div>
          <p class="activity-intro">
            Signed Linear events enter here. An exact <code>/do ISSUE-NNN</code>
            comment starts one durable agent loop per issue.
          </p>
        </div>

        <dl class="activity-summary">
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

        <section class="run-feed" aria-labelledby="run-feed-title">
          <div class="feed-heading">
            <h2 id="run-feed-title">Agent loops</h2>
            <span>Issue context and output remain private</span>
          </div>

          <Show
            when={(activity()?.agentRuns.runs.length ?? 0) > 0}
            fallback={
              <div class="empty-state compact">
                <strong>No agent run has been claimed.</strong>
                <span>Comment with an exact command to start one.</span>
                <code>/do ISSUE-NNN</code>
              </div>
            }
          >
            <ol class="run-list">
              <For each={activity()?.agentRuns.runs ?? []}>
                {(run) => (
                  <li class="run-row">
                    <a class="run-link" href={`/agents/${run.id}`}>
                      <code>{shortRunID(run.id)}</code>
                    </a>
                    <strong class={`run-state ${run.state}`}>
                      {runStateLabel(run.state)}
                    </strong>
                    <span>{run.attempts || "Queued"}</span>
                    <time datetime={run.updatedAt}>{formatTime(run.updatedAt)}</time>
                  </li>
                )}
              </For>
            </ol>
          </Show>
        </section>

        <section class="event-feed" aria-labelledby="event-feed-title">
          <div class="feed-heading">
            <h2 id="event-feed-title">Recent deliveries</h2>
            <span>Refreshes every 5 seconds</span>
          </div>

          <Show
            when={(activity()?.events.length ?? 0) > 0}
            fallback={
              <div class="empty-state">
                <strong>Waiting for the first signed delivery.</strong>
                <span>Events will appear here as Linear sends them.</span>
                <code>/api/webhooks/linear</code>
              </div>
            }
          >
            <ol class="event-list">
              <For each={activity()?.events ?? []}>
                {(event) => (
                  <li class="event-row">
                    <time datetime={event.receivedAt}>
                      {formatTime(event.receivedAt)}
                    </time>
                    <strong>{event.type}</strong>
                    <span>{event.action}</span>
                  </li>
                )}
              </For>
            </ol>
          </Show>
        </section>

        <footer class="activity-footer">
          <span>Agent details require operator authentication.</span>
          <a class="text-link" href="/">
            Back to Factory
          </a>
        </footer>
      </section>
    </main>
  );
}

function AgentPage(props: { runID: string }): JSX.Element {
  const [agent, { refetch }] = createResource(
    () => props.runID,
    getAgent,
  );
  const [selectedWindowID, setSelectedWindowID] = createSignal("");
  const selectedWindow = (): AgentWindow | undefined => {
    const windows = agent()?.windows ?? [];
    return (
      windows.find((window) => window.id === selectedWindowID()) ?? windows[0]
    );
  };

  onMount(() => {
    document.title = "Agent | Factory";
    const timer = window.setInterval(() => void refetch(), 2000);
    onCleanup(() => window.clearInterval(timer));
  });

  return (
    <main class="agent-page">
      <section class="agent-shell" aria-labelledby="agent-title">
        <header class="activity-header">
          <a class="brand-link" href="/">
            <span class="mark" aria-hidden="true">
              F
            </span>
            <span>Factory</span>
          </a>
          <div class="listener" aria-live="polite">
            <span
              classList={{
                dot: true,
                ready: agent()?.live,
                failed: Boolean(agent.error),
              }}
            />
            <span>{agentStatusLabel(agent.loading, agent.error, agent())}</span>
          </div>
        </header>

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
                  ? "This run could not be loaded. Check the run ID and try again."
                  : "Opening the authenticated tmux view."}
              </p>
            </div>
          }
        >
          {(snapshot) => (
            <>
              <div class="agent-hero">
                <div>
                  <p class="section-label">Live agent</p>
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
                  <dt>Updated</dt>
                  <dd>{formatTime(snapshot().updatedAt)}</dd>
                </div>
              </dl>

              <section class="agent-console" aria-labelledby="agent-console-title">
                <div class="console-heading">
                  <div>
                    <h2 id="agent-console-title">Session windows</h2>
                    <span>
                      {snapshot().live
                        ? "Live tmux pane output"
                        : "This tmux session is not running"}
                    </span>
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
                        Pending runs appear here after tmux starts. Finished runs
                        keep their durable output on the host.
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
                  <pre class="terminal-output" tabIndex={0}>
                    <code>
                      {selectedWindow()?.output ||
                        "The window is active. Waiting for output."}
                    </code>
                  </pre>
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
                <span>Live output is authenticated and read-only.</span>
                <a class="text-link" href="/activity">
                  Back to activity
                </a>
              </footer>
            </>
          )}
        </Show>
      </section>
    </main>
  );
}

function shortRunID(value: string): string {
  return value.slice(0, 12);
}

function runStateLabel(value: string): string {
  return value.replace(/(^|[-_])([a-z])/g, (_, prefix, letter: string) =>
    `${prefix ? " " : ""}${letter.toUpperCase()}`,
  );
}

function formatTime(value: string | null | undefined): string {
  if (!value) {
    return "No deliveries yet";
  }
  return timeFormatter.format(new Date(value));
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
  return agent?.live ? "Session live" : "Session offline";
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("Root element not found");
}

const currentPath = window.location.pathname.replace(/\/+$/, "") || "/";
const agentRoute = /^\/agents\/([^/]+)$/.exec(currentPath);

render(() => {
  if (currentPath === "/activity") {
    return <ActivityPage />;
  }
  if (agentRoute) {
    return <AgentPage runID={agentRoute[1]} />;
  }
  return <HomePage />;
}, root);
