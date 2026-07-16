import {
  createEffect,
  createMemo,
  createResource,
  createSignal,
  For,
  onMount,
  Show,
  type JSX,
} from "solid-js";
import {
  ActivityHeader,
  formatTime,
  InlineError,
  LoadingRows,
  resourceState,
  runStateLabel,
} from "./activity";
import { agentRunHref, type AgentActivityRun } from "./agent";
import { getJSON } from "./http";

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

async function getTasks(request: string): Promise<TasksResponse> {
  return getJSON<TasksResponse>(request, "Task index request");
}

async function getTaskProjects(): Promise<TaskProjectsResponse> {
  return getJSON<TaskProjectsResponse>("/api/task-projects", "Task project request");
}

async function getNativeTask(id: string): Promise<NativeTaskDetail> {
  return getJSON<NativeTaskDetail>(
    `/api/tasks/factory/${encodeURIComponent(id)}`,
    "Task detail request",
  );
}

async function getLinearTask(id: string): Promise<TaskSummary> {
  return getJSON<TaskSummary>(
    `/api/tasks/linear/${encodeURIComponent(id)}`,
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

export function TasksPage(): JSX.Element {
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
  const enabledProjects = createMemo(() => (projects()?.projects ?? []).filter((choice) => choice.enabled));

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
    const next = tasks()?.nextCursor;
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
          <label><span>Project</span><select value={project()} onChange={(event) => changeFilter(setProject, event.currentTarget.value)}><option value="">All projects</option><For each={projects()?.projects ?? []}>{(choice) => <option value={choice.projectId}>{choice.projectName}</option>}</For></select></label>
          <label><span>Approval</span><select value={approval()} onChange={(event) => changeFilter(setApproval, event.currentTarget.value)}><option value="">All modes</option><option value="gated">Gated</option><option value="automatic">Automatic</option></select></label>
          <label><span>Lifecycle</span><select value={activity()} onChange={(event) => changeFilter(setActivity, event.currentTarget.value)}><option value="">Any activity</option><option value="active">Active Run</option><option value="inactive">No active Run</option></select></label>
        </form>

        <Show when={!tasks.error && !projects.error} fallback={<InlineError message="The task ledger could not be loaded. Check the Factory connection and try again." />}>
          <div class="task-workspace">
            <section class="task-ledger" aria-labelledby="task-ledger-title">
              <div class="task-section-heading">
                <div><p class="section-label">Current scope</p><h2 id="task-ledger-title">Task ledger</h2></div>
                <span>{tasks()?.tasks.length ?? 0} shown</span>
              </div>
              <Show when={!tasks.loading || Boolean(tasks())} fallback={<LoadingRows />}>
                <Show when={(tasks()?.tasks.length ?? 0) > 0} fallback={<div class="empty-state task-empty"><strong>No tasks match this view.</strong><span>Adjust the filters or create the first native Factory task.</span></div>}>
                  <ol class="task-list">
                    <For each={tasks()?.tasks ?? []}>
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
              <Show when={cursorHistory().length > 0 || Boolean(tasks()?.nextCursor)}><nav class="task-pagination" aria-label="Task pages"><button class="text-button" type="button" disabled={cursorHistory().length === 0} onClick={previousTaskPage}>Previous</button><span>Page {cursorHistory().length + 1}</span><button class="text-button" type="button" disabled={!tasks()?.nextCursor} onClick={nextTaskPage}>Next</button></nav></Show>
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

export function LinearTaskDetailPage(props: { id: string }): JSX.Element {
  const [detail, { refetch }] = createResource(() => props.id, getLinearTask);

  createEffect(() => {
    const task = detail();
    if (task) document.title = `${task.ref.identifier} | Factory`;
  });

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="task-title">
        <ActivityHeader
          section="tasks"
          state={resourceState(detail.loading, detail.error, Boolean(detail()))}
          label={detail.error ? "Task unavailable" : "Managed Linear record"}
        />
        <Show when={!detail.error} fallback={<InlineError message="This task could not be loaded. It may have moved or Factory may be offline." />}>
          <Show when={!detail.loading || Boolean(detail())} fallback={<LoadingRows />}>
            <Show when={detail()}>
              {(task) => (
                <article class="task-detail linear-detail">
                  <header class="task-detail-header"><div><span class="task-source linear">Linear · read only</span><h1 id="task-title">{task().title}</h1><p>{task().ref.identifier}</p></div><span class="task-state">{runStateLabel(task().state)}</span></header>
                  <div class="task-readonly-note"><strong>Managed coexistence record</strong><p>Factory loads this detail live from Linear without persisting its body. Continue discussion and edits in Linear.</p><dl class="task-metadata linear-task-metadata"><div><dt>Provider state</dt><dd>{task().stateName || runStateLabel(task().state)}</dd></div><div><dt>Project</dt><dd>{task().projectName || "No project"}</dd></div><Show when={task().latestRun}><div><dt>Latest Factory Run</dt><dd>{runStateLabel(task().latestRun!.state)}</dd></div><div><dt>Run updated</dt><dd>{formatTime(task().latestRun!.updatedAt)}</dd></div></Show></dl><Show when={task().description}><p class="task-linear-description">{task().description}</p></Show><Show when={(task().messages?.length ?? 0) > 0}><section class="linear-task-thread" aria-labelledby="linear-thread-title"><div class="task-section-heading"><div><p class="section-label">Live from Linear</p><h2 id="linear-thread-title">Discussion</h2></div><span>{task().messages?.length} messages</span></div><ol class="task-messages"><For each={task().messages ?? []}>{(message) => <li classList={{ reply: Boolean(message.parentId) }}><div><strong>{taskActorLabel(message.author)}</strong><time datetime={message.createdAt}>{formatTime(message.createdAt)}</time></div><p>{message.body}</p></li>}</For></ol></section></Show><Show when={task().externalUrl}><a class="primary-button task-external-link" href={task().externalUrl} target="_blank" rel="noreferrer">Open in Linear</a></Show></div>
                </article>
              )}
            </Show>
          </Show>
        </Show>
        <footer class="activity-footer"><a class="text-link" href="/tasks">Back to task ledger</a><button class="text-button" type="button" onClick={() => void refetch()}>Refresh task</button></footer>
      </section>
    </main>
  );
}

export function NativeTaskDetailPage(props: { id: string }): JSX.Element {
  const [detail, { refetch }] = createResource(() => props.id, getNativeTask);
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
    const task = detail()?.task;
    if (!task || task.revision === loadedRevision) return;
    loadedRevision = task.revision;
    setTitle(task.title);
    setDescription(task.description ?? "");
    setApproval(task.approvalMode);
    document.title = `${task.ref.identifier} | Factory`;
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
    const task = detail()?.task;
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
        <ActivityHeader
          section="tasks"
          state={resourceState(detail.loading, detail.error, Boolean(detail()))}
          label={detail.error ? "Task unavailable" : "Native task journal"}
        />
        <Show when={!detail.error} fallback={<InlineError message="This task could not be loaded. It may have moved or Factory may be offline." />}>
          <Show when={!detail.loading || Boolean(detail())} fallback={<LoadingRows />}>
            <Show when={detail()}>
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
