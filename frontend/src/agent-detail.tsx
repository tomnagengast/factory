import { createResource, createSignal, For, onMount, Show, type JSX, type Resource } from "solid-js";
import { ActivityHeader, resourceState, runStateLabel } from "./activity";
import { getJSON } from "./http";
import { usePolling } from "./poll";

const refreshIntervalMs = 2000;

function resourceSnapshot<T>(resource: Resource<T>): T | undefined {
  return resource.error ? undefined : resource();
}

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

const observationTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "medium",
});

export async function getAgentByReference(
  taskIdentifier: string,
  startedAt: string,
  source?: "factory" | "linear",
): Promise<AgentView> {
  const query = source ? `?source=${source}` : "";
  return getJSON<AgentView>(
    `/api/agents/${encodeURIComponent(taskIdentifier)}/${encodeURIComponent(startedAt)}/run${query}`,
    "Agent request",
  );
}

export function AgentPage(props: { load: () => Promise<AgentView> }): JSX.Element {
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
  });
  usePolling(
    () => void refetch(),
    refreshIntervalMs,
    () => shouldRefreshAgent(agentSnapshot()),
  );

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

function formatObservationTime(value: string): string {
  return observationTimeFormatter.format(new Date(value));
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
