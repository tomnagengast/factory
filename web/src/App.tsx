import { A, Route, Router, useNavigate, useParams, useSearchParams } from "@solidjs/router";
import {
  createEffect,
  createMemo,
  createResource,
  createSignal,
  For,
  onCleanup,
  onMount,
  Show,
  type JSX,
} from "solid-js";
import hljs from "highlight.js/lib/common";
import { ArrowDownWideNarrow, ArrowUpNarrowWide, ListTodo, Play, Trash2 } from "lucide-solid";
import "highlight.js/styles/github-dark.css";
import { get, optional, optionalID, post, put, remove, uploadMedia } from "./api";
import { eventTypeOptions, filterEvents, shouldContinueEventPaging } from "./events";
import { bindNewestFollower, type NewestFollower } from "./follow-newest";
import {
  HISTORY_OVERVIEW_LIMIT,
  HISTORY_PAGE_SIZE,
  canLoadHistoryPage,
  historyPageRequestIsCurrent,
  historyPageURL,
  historyResourceLink,
  historyRunHref,
  historyStatuses,
  mergeHistoryRuns,
  observeHistorySentinel,
} from "./history";
import { renderMarkdown } from "./markdown";
import { insertMediaMarkup, mediaAccept, mediaFiles, mediaKind, mediaMarkup } from "./media";
import {
  activeTaskCreationProjectId,
  loadLastTaskProjectId,
  saveLastTaskProjectId,
} from "./task-creation-preferences";
import {
  filterTasks,
  loadTaskViewPreferences,
  parseTaskViewSearchParams,
  saveTaskViewPreferences,
  taskFields,
  taskProjectOptions,
  taskViewSearchParams,
  taskViewSearchParamsAreCanonical,
  taskViewSearchParamsHaveOwnedKeys,
  type TaskField,
  type TaskProjectOption,
  type TaskViewPreferences,
} from "./task-view-preferences";
import { filterTriggers, triggerEventTypeOptions, triggerWorkflowOptions } from "./triggers";
import { highlightWorkflowSource } from "./workflow-source";
import {
  taskStatuses,
  type Artifact,
  type Comment,
  type CommentDetail,
  type Event,
  type Health,
  type HistoryDetail,
  type HistoryListResponse,
  type Project,
  type ProjectDetail,
  type SettingsDetail,
  type Task,
  type TaskDetail,
  type TaskListResponse,
  type TaskStatus,
  type TaskSummary,
  type TaskWorkflowRun,
  type Trigger,
  type Workflow,
  type WorkflowDetail,
  type WorkflowRun,
  type WorkflowRunStatus,
} from "./types";
import { sortWorkflowsByUsage } from "./workflows";
import { workflowCommentPresentation, workflowConversationWorking } from "./workflow-conversation";
import { formatWorkflowRunDuration } from "./workflow-run-duration";

const REACTION_EMOJIS = ["👍", "👎", "❤️", "🎉", "😂", "👀"] as const;
const PAGE_SIZE = 200;
const EVENT_PAGE_SIZE = 25;

export function App() {
  return (
    <Router root={Shell}>
      <Route path="/" component={Home} />
      <Route path="/projects" component={Projects} />
      <Route path="/projects/new" component={ProjectNew} />
      <Route path="/projects/:project" component={ProjectView} />
      <Route path="/tasks" component={Tasks} />
      <Route path="/tasks/new" component={TaskNew} />
      <Route path="/tasks/:task" component={TaskView} />
      <Route path="/tasks/:task/comments/:comment" component={CommentView} />
      <Route path="/events" component={Events} />
      <Route path="/events/:event" component={EventView} />
      <Route path="/triggers" component={Triggers} />
      <Route path="/triggers/new" component={TriggerNew} />
      <Route path="/triggers/:trigger" component={TriggerView} />
      <Route path="/workflows" component={Workflows} />
      <Route path="/workflows/new" component={WorkflowNew} />
      <Route path="/workflows/:workflow" component={WorkflowView} />
      <Route path="/history" component={History} />
      <Route path="/history/running" component={() => <HistoryStatusPage status="running" label="Running" />} />
      <Route path="/history/waiting" component={() => <HistoryStatusPage status="waiting" label="Waiting" />} />
      <Route path="/history/failed" component={() => <HistoryStatusPage status="failed" label="Failed" />} />
      <Route path="/history/completed" component={() => <HistoryStatusPage status="completed" label="Completed" />} />
      <Route path="/history/:item" component={HistoryView} />
      <Route path="/settings" component={SettingsPage} />
    </Router>
  );
}

function Shell(props: { children?: JSX.Element }) {
  const links = [
    ["/", "Overview"],
    ["/projects", "Projects"],
    ["/tasks", "Tasks"],
    ["/events", "Event wire"],
    ["/triggers", "Triggers"],
    ["/workflows", "Workflows"],
    ["/history", "History"],
    ["/settings", "Settings"],
  ];
  return (
    <div class="shell">
      <aside class="rail">
        <A href="/" class="brand" aria-label="Factory overview">
          <span class="brand-mark">F</span>
          <span>
            <strong>Factory</strong>
            <small>one wire / bounded runs</small>
          </span>
        </A>
        <nav aria-label="Primary navigation">
          <For each={links}>
            {([href, label]) => (
              <A href={href} end={href === "/"} activeClass="active">
                {label}
              </A>
            )}
          </For>
        </nav>
        <div class="rail-status">
          <span class="pulse" aria-hidden="true" />
          Coordinator connected
        </div>
      </aside>
      <main>{props.children}</main>
    </div>
  );
}

function PageHeader(props: { eyebrow?: string; title: JSX.Element; description?: string; actions?: JSX.Element }) {
  return (
    <header class="page-header">
      <div>
        <Show when={props.eyebrow}>
          <p class="eyebrow">{props.eyebrow}</p>
        </Show>
        <h1>{props.title}</h1>
        <Show when={props.description}>
          <p>{props.description}</p>
        </Show>
      </div>
      <Show when={props.actions}>
        <div class="header-actions">{props.actions}</div>
      </Show>
    </header>
  );
}

function Load<T>(props: {
  data: () => T | undefined;
  error: () => unknown;
  children: (value: T) => JSX.Element;
}) {
  return (
    <Show
      when={props.data()}
      fallback={
        <div class="state">
          <Show when={props.error()} fallback="Loading…">
            {errorMessage(props.error())}
          </Show>
        </div>
      }
    >
      {(value) => props.children(value())}
    </Show>
  );
}

function Empty(props: { children: JSX.Element }) {
  return <div class="empty">{props.children}</div>;
}

function Markdown(props: { content?: string; inline?: boolean }) {
  let body: HTMLDivElement | undefined;
  const html = createMemo(() => renderMarkdown(props.content ?? "", props.inline));
  createEffect(() => {
    html();
    if (!props.inline) queueMicrotask(() =>
      body?.querySelectorAll<HTMLElement>("pre code").forEach((code) => hljs.highlightElement(code)));
  });
  return props.inline
    ? <span class="markdown inline" innerHTML={html()} />
    : <div ref={body} class="markdown" innerHTML={html()} />;
}

function Meta(props: { value: { id: number; createdAt: string; updatedAt: string; deletedAt?: string } }) {
  return (
    <dl class="meta">
      <div><dt>ID</dt><dd>{props.value.id}</dd></div>
      <div><dt>Created</dt><dd>{date(props.value.createdAt)}</dd></div>
      <div><dt>Updated</dt><dd>{date(props.value.updatedAt)}</dd></div>
      <div><dt>Deleted</dt><dd>{props.value.deletedAt ? date(props.value.deletedAt) : "No"}</dd></div>
    </dl>
  );
}

function ReactionBar(props: {
  targetKind: "task" | "comment";
  targetID: number;
  reactions: string[];
  onChange: () => unknown;
}) {
  const action = mutation();
  const targetLabel = () => `${props.targetKind} ${props.targetID}`;
  return (
    <div class="reaction-control">
      <div classList={{ "reaction-bar": true, pending: action.pending() }}
        role="group" aria-label={`Reactions for ${targetLabel()}`}>
        <For each={REACTION_EMOJIS}>{(emoji) => {
          const active = () => props.reactions.includes(emoji);
          const label = () => `${active() ? "Clear" : "Add"} ${emoji} reaction ${active() ? "from" : "to"} ${targetLabel()}`;
          return <button type="button" classList={{ "reaction-button": true, selected: active() }}
            aria-pressed={active()} aria-label={label()} title={label()} disabled={action.pending()}
            onClick={() => action.run(async () => {
              await put(`/api/${props.targetKind}s/${props.targetID}/reactions`, {
                emoji, active: !active(),
              });
              await props.onChange();
            })}>{emoji}</button>;
        }}</For>
      </div>
      <Show when={action.error()}><span class="form-error" role="alert">{action.error()}</span></Show>
    </div>
  );
}

function Home() {
  const [data, { refetch }] = createResource(async () => {
    const [health, projects, tasks, events] = await Promise.all([
      get<Health>("/api/health"),
      get<{ projects: Project[] }>("/api/projects"),
      get<TaskListResponse>("/api/tasks"),
      get<{ events: Event[] }>("/api/events"),
    ]);
    return {
      health,
      projects: projects.projects.slice(0, 4),
      tasks: tasks.tasks.slice(0, 5),
      events: events.events.slice(0, 6),
      checkpointEventId: Math.min(tasks.checkpointEventId, health.checkpointEventId),
    };
  });
  liveTaskRows(() => data()?.checkpointEventId, refetch);
  return (
    <div class="page">
      <PageHeader
        eyebrow="Trusted environment demonstrator"
        title="Factory overview"
        description="Projects and tasks enter one observable wire. The selected harness authors workflows and executes triggered runs within the configured capacity."
      />
      <Load data={data} error={() => data.error}>
        {(value) => {
          const current = () => data() ?? value;
          return <>
            <section class="metrics" aria-label="Factory overview metrics">
              <Metric label="Projects" value={current().health.projects} href="/projects" />
              <Metric label="Tasks" value={current().health.tasks} href="/tasks" />
              <Metric label="Events" value={current().health.events} href="/events" />
              <Metric label="Running workflows" value={current().health.workflowRunning} href="/history" />
            </section>
            <div class="split">
              <section>
                <SectionTitle title="Recent tasks" href="/tasks" />
                <Show when={current().tasks.length} fallback={<Empty>No tasks yet.</Empty>}>
                  <div class="rows">
                    <For each={current().tasks}>{(task) => <TaskRow task={task} projects={current().projects} />}</For>
                  </div>
                </Show>
              </section>
              <section>
                <SectionTitle title="Latest on the wire" href="/events" />
                <Show when={current().events.length} fallback={<Empty>The wire is quiet.</Empty>}>
                  <div class="wire-list">
                    <For each={current().events}>{(event) => <EventRow event={event} />}</For>
                  </div>
                </Show>
              </section>
            </div>
          </>;
        }}
      </Load>
    </div>
  );
}

function SettingsPage() {
  const [data, { refetch }] = createResource(() => get<SettingsDetail>("/api/settings"));
  const action = mutation();
  return (
    <div class="page narrow">
      <PageHeader
        eyebrow="Factory"
        title="Settings"
        description="Harness selection applies to new work. Workflow capacity controls how many triggered runs may execute at once."
      />
      <Load data={data} error={() => data.error}>
        {(value) => (
          <SettingsForm
            detail={value}
            pending={action.pending()}
            error={action.error()}
            onSave={(body) => action.run(async () => {
              await put("/api/settings", body);
              await refetch();
            })}
          />
        )}
      </Load>
    </div>
  );
}

function SettingsForm(props: {
  detail: SettingsDetail;
  pending: boolean;
  error?: string;
  onSave: (body: unknown) => void;
}) {
  const [harness, setHarness] = createSignal(props.detail.settings.harness);
  const [model, setModel] = createSignal(props.detail.settings.model);
  const [reasoning, setReasoning] = createSignal(props.detail.settings.reasoning);
  const [workflowCapacity, setWorkflowCapacity] = createSignal(props.detail.settings.workflowCapacity);
  const selectedHarness = createMemo(() =>
    props.detail.harnesses.find((option) => option.id === harness()) ?? props.detail.harnesses[0]);
  const selectedModel = createMemo(() =>
    selectedHarness()?.models.find((option) => option.id === model()) ?? selectedHarness()?.models[0]);
  const changeHarness = (value: string) => {
    const option = props.detail.harnesses.find((candidate) => candidate.id === value)!;
    setHarness(value);
    setModel(option.models[0].id);
    setReasoning(option.models[0].defaultReasoning);
  };
  const changeModel = (value: string) => {
    const option = selectedHarness()!.models.find((candidate) => candidate.id === value)!;
    setModel(value);
    setReasoning(option.defaultReasoning);
  };
  return (
    <form class="form-panel" onSubmit={(event) => {
      event.preventDefault();
      props.onSave({
        harness: harness(), model: model(), reasoning: reasoning(),
        workflowCapacity: workflowCapacity(),
      });
    }}>
      <label>Harness<select name="harness" value={harness()}
        onChange={(event) => changeHarness(event.currentTarget.value)}>
        <For each={props.detail.harnesses}>{(option) => <option value={option.id}>{option.name}</option>}</For>
      </select></label>
      <label>Model<select name="model" value={model()}
        onChange={(event) => changeModel(event.currentTarget.value)}>
        <For each={selectedHarness()?.models}>{(option) => <option value={option.id}>{option.id}</option>}</For>
      </select></label>
      <label>Reasoning level<select name="reasoning" value={reasoning()}
        onChange={(event) => setReasoning(event.currentTarget.value)}>
        <For each={selectedModel()?.reasoning}>{(level) => <option value={level}>{level}</option>}</For>
      </select></label>
      <label>Workflow capacity<input name="workflowCapacity" type="number" min="0" max="10" step="1" required
        value={workflowCapacity()}
        onInput={(event) => setWorkflowCapacity(event.currentTarget.valueAsNumber)} />
        <small>Maximum triggered workflow runs at once. Set to 0 to pause new runs.</small>
      </label>
      <FormFooter pending={props.pending} error={props.error} label="Save settings" />
    </form>
  );
}

function Metric(props: { label: string; value: number; href: string }) {
  return (
    <A href={props.href} class="metric">
      <span>{props.label}</span>
      <strong>{props.value}</strong>
    </A>
  );
}

function SectionTitle(props: { title: string; href?: string }) {
  return (
    <header class="section-title">
      <h2>{props.title}</h2>
      <Show when={props.href}><A href={props.href!}>View all</A></Show>
    </header>
  );
}

function Projects() {
  const [data] = createResource(() => get<{ projects: Project[] }>("/api/projects"));
  return (
    <div class="page">
      <PageHeader
        title="Projects"
        description="Lightweight context for grouping tasks and pointing agents at working material."
        actions={<A class="button primary" href="/projects/new">New project</A>}
      />
      <Load data={data} error={() => data.error}>
        {(value) => (
          <Show when={value.projects.length} fallback={<Empty>No projects yet.</Empty>}>
            <div class="card-grid">
              <For each={value.projects}>
                {(project) => (
                  <A class="project-card" href={`/projects/${project.id}`}>
                    <span class="id">#{project.id}</span>
                    <h2>{project.name}</h2>
                    <p>{project.description || "No description"}</p>
                    <small>Updated {date(project.updatedAt)}</small>
                  </A>
                )}
              </For>
            </div>
          </Show>
        )}
      </Load>
    </div>
  );
}

function ProjectNew() {
  const navigate = useNavigate();
  const action = mutation();
  return (
    <div class="page narrow">
      <PageHeader eyebrow="Projects" title="Create a project"
        description="The local path becomes the working directory for task workflows." />
      <ProjectForm
        pending={action.pending()}
        error={action.error()}
        onSave={(body) => action.run(async () => {
          const created = await post<Project>("/api/projects", body);
          navigate(`/projects/${created.id}`);
        })}
      />
    </div>
  );
}

function ProjectView() {
  const params = useParams();
  const navigate = useNavigate();
  const [data, { refetch }] = createResource(() => get<ProjectDetail>(`/api/projects/${params.project}`));
  liveTaskRows(() => data()?.checkpointEventId, refetch);
  const action = mutation();
  return (
    <div class="page">
      <Load data={data} error={() => data.error}>
        {(value) => (
          <>
            <PageHeader eyebrow={`Project ${value.project.id}`} title={value.project.name} />
            <div class="detail-grid">
              <ProjectForm
                project={value.project}
                pending={action.pending()}
                error={action.error()}
                onSave={(body) => action.run(async () => {
                  await put<Project>(`/api/projects/${value.project.id}`, body);
                  await refetch();
                })}
              />
              <aside class="side-detail">
                <Meta value={value.project} />
                <button class="button danger" onClick={() => action.run(async () => {
                  await remove(`/api/projects/${value.project.id}`);
                  navigate("/projects");
                })}>Delete project</button>
              </aside>
            </div>
            <section>
              <SectionTitle title="Project tasks" href="/tasks/new" />
              <Show when={value.tasks.length} fallback={<Empty>No tasks belong to this project.</Empty>}>
                <div class="rows"><For each={value.tasks}>{(task) => <TaskRow task={task} projects={[value.project]} />}</For></div>
              </Show>
            </section>
          </>
        )}
      </Load>
    </div>
  );
}

function ProjectForm(props: {
  project?: Project;
  pending: boolean;
  error?: string;
  onSave: (body: unknown) => void;
}) {
  return (
    <form class="form-panel" onSubmit={(event) => {
      event.preventDefault();
      const data = new FormData(event.currentTarget);
      props.onSave({
        name: String(data.get("name") ?? "").trim(),
        description: optional(data.get("description")),
        repo: optional(data.get("repo")),
        path: String(data.get("path") ?? "").trim(),
        url: optional(data.get("url")),
      });
    }}>
      <label>Name<input name="name" required value={props.project?.name ?? ""} /></label>
      <label>Description<textarea name="description" rows="4">{props.project?.description ?? ""}</textarea></label>
      <div class="field-pair">
        <label>Repository<input name="repo" value={props.project?.repo ?? ""} /></label>
        <label>Local path<input name="path" required value={props.project?.path ?? ""} /></label>
      </div>
      <label>URL<input name="url" type="url" value={props.project?.url ?? ""} /></label>
      <FormFooter pending={props.pending} error={props.error} label={props.project ? "Save project" : "Create project"} />
    </form>
  );
}

function Tasks() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rememberedSearchParams = taskViewSearchParams(loadTaskViewPreferences());
  const [restoreRememberedView, setRestoreRememberedView] = createSignal(
    !taskViewSearchParamsHaveOwnedKeys(searchParams),
  );
  const [data, { refetch }] = createResource(async () => {
    const [tasks, projects] = await Promise.all([
      get<TaskListResponse>("/api/tasks"), get<{ projects: Project[] }>("/api/projects"),
    ]);
    return {
      tasks: tasks.tasks, projects: projects.projects, checkpointEventId: tasks.checkpointEventId,
    };
  });
  liveTaskRows(() => data()?.checkpointEventId, refetch);
  const projectOptions = createMemo(() => taskProjectOptions(data()?.tasks ?? [], data()?.projects ?? []));
  const preferences = createMemo(() => parseTaskViewSearchParams(
    restoreRememberedView() ? rememberedSearchParams : searchParams,
    data() ? projectOptions().map((project) => project.id) : undefined,
  ));
  const updateView = (updates: Partial<TaskViewPreferences>) => {
    const updated = { ...preferences(), ...updates };
    setRestoreRememberedView(false);
    saveTaskViewPreferences(updated);
    setSearchParams(taskViewSearchParams(updated));
  };
  const clearFilters = () => updateView({ statuses: [], projectIds: [] });
  const filtered = createMemo(() => filterTasks(data()?.tasks ?? [], preferences()));
  const activeFilterCount = createMemo(() => preferences().statuses.length + preferences().projectIds.length);
  createEffect(() => {
    if (!data()) return;
    const current = preferences();
    const canonical = taskViewSearchParams(current);
    if (restoreRememberedView()) setRestoreRememberedView(false);
    if (!taskViewSearchParamsAreCanonical(searchParams, canonical)) {
      setSearchParams(canonical, { replace: true });
    }
    saveTaskViewPreferences(current);
  });
  const groups = createMemo(() => {
    const value = data();
    if (!value) return [] as Array<[string, TaskSummary[]]>;
    const sorted = [...filtered()].sort((left, right) => {
      const result = compare(taskValue(left, preferences().sortField), taskValue(right, preferences().sortField));
      return preferences().direction === "desc" ? -result : result;
    });
    const field = preferences().groupField;
    if (!field) return [["", sorted]] as Array<[string, TaskSummary[]]>;
    const grouped = new Map<string, TaskSummary[]>();
    for (const task of sorted) {
      const key = displayTaskValue(task, field, value.projects);
      grouped.set(key, [...(grouped.get(key) ?? []), task]);
    }
    return [...grouped.entries()];
  });
  return (
    <div class="page">
      <PageHeader
        title="Tasks"
        description="Filter, sort, and group the loaded tasks without changing the underlying wire."
        actions={<A class="button primary" href="/tasks/new">New task</A>}
      />
      <Load data={data} error={() => data.error}>
        {(value) => (
          <Show when={value.tasks.length} fallback={<Empty>No tasks yet.</Empty>}>
            <div class="task-view-controls">
              <div class="task-order-controls" aria-label="Task order and grouping">
                <div class="task-sort-control">
                  <label>Sort by<select value={preferences().sortField}
                    onChange={(event) => updateView({ sortField: event.currentTarget.value as TaskField })}>
                    <For each={taskFields}>{(field) => <option value={field[0]}>{field[1]}</option>}</For>
                  </select></label>
                  <button type="button" class="sort-direction"
                    aria-label={sortDirectionLabel(preferences().direction)}
                    title={sortDirectionLabel(preferences().direction)}
                    onClick={() => updateView({ direction: preferences().direction === "desc" ? "asc" : "desc" })}>
                    <Show when={preferences().direction === "desc"}
                      fallback={<ArrowUpNarrowWide aria-hidden="true" />}>
                      <ArrowDownWideNarrow aria-hidden="true" />
                    </Show>
                  </button>
                </div>
                <label>Group by<select value={preferences().groupField}
                  onChange={(event) => updateView({ groupField: event.currentTarget.value as TaskField | "" })}>
                  <option value="">No grouping</option>
                  <For each={taskFields}>{(field) => <option value={field[0]}>{field[1]}</option>}</For>
                </select></label>
              </div>
              <TaskFilters
                projects={projectOptions()}
                selectedStatuses={preferences().statuses}
                selectedProjectIds={preferences().projectIds}
                activeFilterCount={activeFilterCount()}
                displayedResultCount={filtered().length}
                totalResultCount={value.tasks.length}
                onStatusChange={(status, selected) => updateView({
                  statuses: selected
                    ? [...preferences().statuses, status]
                    : preferences().statuses.filter((value) => value !== status),
                })}
                onProjectChange={(projectId, selected) => updateView({
                  projectIds: selected
                    ? [...preferences().projectIds, projectId]
                    : preferences().projectIds.filter((value) => value !== projectId),
                })}
                onSelectAllStatuses={() => updateView({ statuses: [...taskStatuses] })}
                onUnselectAllStatuses={() => updateView({ statuses: [] })}
                onSelectAllProjects={() => updateView({
                  projectIds: projectOptions().map(({ id }) => id),
                })}
                onUnselectAllProjects={() => updateView({ projectIds: [] })}
                onClear={clearFilters}
              />
            </div>
            <Show when={filtered().length} fallback={<div class="empty task-filter-empty">
              <p>No tasks match these filters.</p>
              <button type="button" class="button quiet" onClick={clearFilters}>Clear filters</button>
            </div>}>
              <For each={groups()}>
                {([label, tasks]) => (
                  <section class="task-group">
                    <Show when={label}><h2>{label}<span>{tasks.length}</span></h2></Show>
                    <div class="rows"><For each={tasks}>{(task) => <TaskRow task={task} projects={value.projects} />}</For></div>
                  </section>
                )}
              </For>
            </Show>
          </Show>
        )}
      </Load>
    </div>
  );
}

function TaskFilters(props: {
  projects: TaskProjectOption[];
  selectedStatuses: TaskStatus[];
  selectedProjectIds: number[];
  activeFilterCount: number;
  displayedResultCount: number;
  totalResultCount: number;
  onStatusChange: (status: TaskStatus, selected: boolean) => void;
  onProjectChange: (projectId: number, selected: boolean) => void;
  onSelectAllStatuses: () => void;
  onUnselectAllStatuses: () => void;
  onSelectAllProjects: () => void;
  onUnselectAllProjects: () => void;
  onClear: () => void;
}) {
  const allStatusesSelected = () => taskStatuses.every((status) => props.selectedStatuses.includes(status));
  const allProjectsSelected = () => props.projects.length > 0
    && props.projects.every((project) => props.selectedProjectIds.includes(project.id));
  return (
    <div class="task-filters" aria-label="Task filters">
      <details class="task-filter">
        <summary>Filter <span>{props.activeFilterCount} selected</span></summary>
        <div class="task-filter-panel">
          <fieldset>
            <legend class="filter-field-legend">
              <span>Status</span>
              <FilterFieldActions
                selectLabel="Select all statuses"
                unselectLabel="Unselect all statuses"
                selectDisabled={allStatusesSelected()}
                unselectDisabled={props.selectedStatuses.length === 0}
                onSelect={props.onSelectAllStatuses}
                onUnselect={props.onUnselectAllStatuses}
              />
            </legend>
            <div class="task-filter-options" role="group" aria-label="Filter by status">
              <For each={taskStatuses}>{(status) => <label>
                <input type="checkbox" checked={props.selectedStatuses.includes(status)}
                  onChange={(event) => props.onStatusChange(status, event.currentTarget.checked)} />
                <span>{status}</span>
              </label>}</For>
            </div>
          </fieldset>
          <fieldset>
            <legend class="filter-field-legend">
              <span>Project</span>
              <FilterFieldActions
                selectLabel="Select all projects"
                unselectLabel="Unselect all projects"
                selectDisabled={allProjectsSelected()}
                unselectDisabled={props.selectedProjectIds.length === 0}
                onSelect={props.onSelectAllProjects}
                onUnselect={props.onUnselectAllProjects}
              />
            </legend>
            <div class="task-filter-options" role="group" aria-label="Filter by project">
              <For each={props.projects}>{(project) => <label>
                <input type="checkbox" checked={props.selectedProjectIds.includes(project.id)}
                  onChange={(event) => props.onProjectChange(project.id, event.currentTarget.checked)} />
                <span>{project.label}</span>
              </label>}</For>
            </div>
          </fieldset>
          <button type="button" class="button quiet" disabled={props.activeFilterCount === 0} onClick={props.onClear}>
            Clear filters
          </button>
        </div>
      </details>
      <p class="task-filter-results" role="status">
        Showing {props.displayedResultCount} of {props.totalResultCount} tasks
      </p>
    </div>
  );
}

function FilterFieldActions(props: {
  selectLabel: string;
  unselectLabel: string;
  selectDisabled: boolean;
  unselectDisabled: boolean;
  onSelect: () => void;
  onUnselect: () => void;
}) {
  return (
    <span class="filter-field-actions">
      <button type="button" aria-label={props.selectLabel} disabled={props.selectDisabled}
        onClick={props.onSelect}>Select all</button>
      <button type="button" aria-label={props.unselectLabel} disabled={props.unselectDisabled}
        onClick={props.onUnselect}>Unselect all</button>
    </span>
  );
}

function TaskRow(props: { task: TaskSummary; projects: Project[] }) {
  return (
    <A href={`/tasks/${props.task.id}`} class="task-row">
      <span class="task-row-meta" role="group" aria-label={`Status: ${props.task.status}`}
        title={`Status: ${props.task.status}`}>
        <TaskStatusIcon status={props.task.status} />
        <span class="task-id">#{props.task.id}</span>
      </span>
      <span class="task-title"><strong>{props.task.title}</strong><small>{projectName(props.task.projectId, props.projects)}</small></span>
      <span class="task-row-signals">
        <WorkflowStatusIndicators runs={props.task.workflowRuns} />
        <CommentCount count={props.task.commentCount} />
        <time>{date(props.task.updatedAt)}</time>
      </span>
    </A>
  );
}

function TaskStatusIcon(props: { status: TaskStatus }) {
  return (
    <svg class={`task-status-icon ${slug(props.status)}`} viewBox="0 0 24 24" aria-hidden="true">
      <circle cx="12" cy="12" r="8" />
      <Show when={props.status === "todo"}><path d="M8 12h8" /></Show>
      <Show when={props.status === "in progress"}><path class="status-fill" d="M12 4a8 8 0 0 1 0 16Z" /></Show>
      <Show when={props.status === "in review"}><circle class="status-fill" cx="12" cy="12" r="3" /></Show>
      <Show when={props.status === "done"}><path d="m8 12 2.5 2.5L16 9" /></Show>
      <Show when={props.status === "canceled"}><path d="m9 9 6 6m0-6-6 6" /></Show>
    </svg>
  );
}

function WorkflowStatusIndicators(props: { runs: TaskWorkflowRun[] }) {
  const label = () => props.runs.map((run) =>
    `${run.workflowName || `Workflow ${run.workflowId}`} run #${run.runId}: ${run.status}`).join("; ");
  return (
    <Show when={props.runs.length}>
      <span class="workflow-status-indicators" role="img" aria-label={label()} title={label()}>
        <For each={props.runs}>{(run) =>
          <svg class={`workflow-status-indicator ${run.status}`} viewBox="0 0 10 10" aria-hidden="true">
            <circle cx="5" cy="5" r="4" />
          </svg>}
        </For>
      </span>
    </Show>
  );
}

function CommentCount(props: { count: number }) {
  const label = () => `${props.count} ${props.count === 1 ? "comment" : "comments"}`;
  return (
    <span class="task-comment-count" role="img" aria-label={label()} title={label()}>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d="M5 5h14v10H9l-4 4Z" />
      </svg>
      <span>{props.count}</span>
    </span>
  );
}

function TaskNew() {
  const navigate = useNavigate();
  const rememberedProjectID = loadLastTaskProjectId();
  const [options] = createResource(async () => {
    const [projects, tasks] = await Promise.all([
      get<{ projects: Project[] }>("/api/projects"), get<TaskListResponse>("/api/tasks"),
    ]);
    return { projects: projects.projects, tasks: tasks.tasks };
  });
  const action = mutation();
  return (
    <div class="page narrow">
      <PageHeader eyebrow="Tasks" title="Create a task" />
      <Load data={options} error={() => options.error}>
        {(value) => <TaskForm projects={value.projects} tasks={value.tasks} pending={action.pending()} error={action.error()}
          initialProjectId={activeTaskCreationProjectId(value.projects, rememberedProjectID)}
          onSave={(body) => action.run(async () => {
            const created = await post<Task>("/api/tasks", body);
            saveLastTaskProjectId(created.projectId);
            navigate(`/tasks/${created.id}`);
          })} />}
      </Load>
    </div>
  );
}

function TaskView() {
  const params = useParams();
  const navigate = useNavigate();
  const [data, { refetch }] = createResource(() => get<TaskDetail>(`/api/tasks/${params.task}`));
  liveTaskRows(() => data()?.checkpointEventId, refetch);
  const [options] = createResource(async () => {
    const [projects, tasks] = await Promise.all([
      get<{ projects: Project[] }>("/api/projects"), get<TaskListResponse>("/api/tasks"),
    ]);
    return { projects: projects.projects, tasks: tasks.tasks };
  });
  const action = mutation();
  const [editing, setEditing] = createSignal(false);
  return (
    <div class="page">
      <Load data={data} error={() => data.error}>
        {(value) => {
          const current = () => data() ?? value;
          return (
          <>
            <PageHeader eyebrow={`Task ${current().task.id}`} title={<Markdown content={current().task.title} inline />}
              actions={<Show when={!editing()}>
                <button class="button" onClick={() => setEditing(true)}>Edit task</button>
              </Show>} />
            <TaskProperties task={current().task} projects={options()?.projects ?? []} tasks={options()?.tasks ?? []} />
            <Show when={!current().task.deletedAt}>
              <ReactionBar targetKind="task" targetID={current().task.id}
                reactions={current().task.reactions} onChange={refetch} />
            </Show>
            <div class="detail-grid">
              <Show when={editing()} fallback={<section class="task-document">
                <Show when={current().task.description} fallback={<p class="muted">No description.</p>}>
                  <Markdown content={current().task.description} />
                </Show>
              </section>}>
                <Show when={options()} fallback={<div class="state">Loading task editor…</div>}>
                  {(available) => <TaskForm task={current().task} projects={available().projects} tasks={available().tasks}
                    pending={action.pending()} error={action.error()} onCancel={() => setEditing(false)}
                    onSave={(body) => action.run(async () => {
                      await put<Task>(`/api/tasks/${current().task.id}`, body);
                      await refetch();
                      setEditing(false);
                    })} />}
                </Show>
              </Show>
              <aside class="side-detail">
                <Meta value={current().task} />
                <button class="button danger" onClick={() => action.run(async () => {
                  await remove(`/api/tasks/${current().task.id}`);
                  navigate("/tasks");
                })}>Delete task</button>
              </aside>
            </div>
            <TaskWorkflowRuns runs={current().workflowRuns} />
            <div class="split lower">
              <section>
                <SectionTitle title="Comments" />
                <CommentThread comments={current().comments} taskID={current().task.id} onChange={refetch} />
              </section>
              <ArtifactPanel artifacts={current().artifacts} relationType="task" relationID={current().task.id} onChange={refetch} />
            </div>
          </>
          );
        }}
      </Load>
    </div>
  );
}

function TaskWorkflowRuns(props: { runs: TaskWorkflowRun[] }) {
  const [now, setNow] = createSignal(Date.now());
  let timer: number | undefined;

  createEffect(() => {
    const hasActiveRun = props.runs.some((run) => run.status === "running" || run.status === "waiting");
    if (hasActiveRun && timer == null) {
      setNow(Date.now());
      timer = window.setInterval(() => setNow(Date.now()), 1000);
    } else if (!hasActiveRun && timer != null) {
      window.clearInterval(timer);
      timer = undefined;
    }
  });
  onCleanup(() => window.clearInterval(timer));

  const duration = (run: TaskWorkflowRun) => formatWorkflowRunDuration(
    run.createdAt,
    run.status === "completed" || run.status === "failed" ? run.updatedAt : now(),
  );

  return (
    <section class="task-workflow-runs">
      <SectionTitle title="Workflow runs" />
      <Show when={props.runs.length} fallback={<Empty>No workflow runs for this task.</Empty>}>
        <div class="rows">
          <For each={props.runs}>{(run) => <A class="task-workflow-run" href={`/history/${run.runId}`}>
            <span class={`run-status ${run.status}`}>{run.status}</span>
            <span class="run-title">
              <strong>{run.workflowName || `Workflow ${run.workflowId}`}</strong>
              <small>Workflow #{run.workflowId} · Trigger #{run.triggerId}</small>
            </span>
            <span class="task-workflow-run-meta">
              <span class="id">#{run.runId}</span>
              <span class="task-workflow-run-duration" title="Run length" aria-label={`Run length ${duration(run)}`}>
                {duration(run)}
              </span>
            </span>
          </A>}</For>
        </div>
      </Show>
    </section>
  );
}

function TaskProperties(props: { task: Task; projects: Project[]; tasks: Task[] }) {
  const parent = () => props.tasks.find((task) => task.id === props.task.parentTaskId);
  return (
    <div class="task-properties">
      <span class={`status ${slug(props.task.status)}`}>{props.task.status}</span>
      <A href={`/projects/${props.task.projectId}`}>{projectName(props.task.projectId, props.projects)}</A>
      <Show when={props.task.parentTaskId}>
        <A href={`/tasks/${props.task.parentTaskId}`}>Parent #{props.task.parentTaskId} {parent()?.title}</A>
      </Show>
    </div>
  );
}

function TaskForm(props: {
  task?: Task;
  initialProjectId?: number;
  projects: Project[];
  tasks: Task[];
  pending: boolean;
  error?: string;
  onCancel?: () => void;
  onSave: (body: unknown) => void;
}) {
  const [uploading, setUploading] = createSignal(false);
  return (
    <form class="form-panel" onSubmit={(event) => {
      event.preventDefault();
      if (uploading()) return;
      const data = new FormData(event.currentTarget);
      props.onSave({
        title: String(data.get("title") ?? "").trim(),
        description: optional(data.get("description")),
        parentTaskId: optionalID(data.get("parentTaskId")),
        status: data.get("status") as TaskStatus,
        projectId: Number(data.get("projectId")),
      });
    }}>
      <label>Title<input name="title" required disabled={uploading()} value={props.task?.title ?? ""} /></label>
      <div class="form-field">
        <label for="task-description">Description</label>
        <MediaTextarea id="task-description" name="description" rows={5} disabled={props.pending}
          initialValue={props.task?.description ?? ""} onUploadingChange={setUploading} />
      </div>
      <div class="field-pair">
        <label>Status<select name="status" disabled={uploading()} value={props.task?.status ?? "backlog"}>
          <For each={taskStatuses}>{(status) => <option value={status}>{status}</option>}</For>
        </select></label>
        <label>Project<select name="projectId" required disabled={uploading()}
          value={props.task?.projectId ?? props.initialProjectId ?? ""}>
          <option value="" disabled>Select a project</option>
          <For each={props.projects}>{(project) => <option value={project.id}>{project.name}</option>}</For>
        </select></label>
      </div>
      <label>Parent task<select name="parentTaskId" disabled={uploading()} value={props.task?.parentTaskId ?? ""}>
        <option value="">No parent</option>
        <For each={props.tasks.filter((task) => task.id !== props.task?.id)}>
          {(task) => <option value={task.id}>#{task.id} {task.title}</option>}
        </For>
      </select></label>
      <FormFooter pending={props.pending || uploading()} error={props.error} onCancel={props.onCancel}
        label={props.task ? "Save task" : "Create task"} />
    </form>
  );
}

function CommentThread(props: { comments: Comment[]; taskID: number; onChange: () => void }) {
  const roots = () => props.comments.filter((comment) => !comment.parentCommentId);
  return (
    <div class="comments">
      <CommentForm taskID={props.taskID} onChange={props.onChange} />
      <Show when={roots().length} fallback={<Empty>No comments yet.</Empty>}>
        <CommentBranch comments={props.comments} nodes={roots()} taskID={props.taskID} onChange={props.onChange} />
      </Show>
    </div>
  );
}

function CommentDeleteButton(props: { commentID: number; onDeleted: () => unknown }) {
  const action = mutation();
  const label = () => `Delete comment #${props.commentID}`;
  return (
    <span class="comment-delete-control">
      <button type="button" class="comment-delete" aria-label={label()} title="Delete comment"
        disabled={action.pending()} onClick={() => action.run(async () => {
          await remove(`/api/comments/${props.commentID}`);
          await props.onDeleted();
        })}>
        <Trash2 aria-hidden="true" />
      </button>
      <Show when={action.error()}><span class="form-error" role="alert">{action.error()}</span></Show>
    </span>
  );
}

function CommentBranch(props: {
  comments: Comment[];
  nodes: Comment[];
  taskID: number;
  onChange: () => void;
}) {
  return (
    <For each={props.nodes}>
      {(comment) => (
        <article class="comment">
          <header>
            <strong>{comment.author}</strong>
            <A href={`/tasks/${props.taskID}/comments/${comment.id}`}>#{comment.id}</A>
            <time>{date(comment.createdAt)}</time>
            <CommentDeleteButton commentID={comment.id} onDeleted={props.onChange} />
          </header>
          <Markdown content={comment.content} />
          <Show when={!comment.deletedAt}>
            <ReactionBar targetKind="comment" targetID={comment.id}
              reactions={comment.reactions} onChange={props.onChange} />
          </Show>
          <CommentForm taskID={props.taskID} parentCommentID={comment.id} compact onChange={props.onChange} />
          <div class="replies">
            <CommentBranch
              comments={props.comments}
              nodes={props.comments.filter((candidate) => candidate.parentCommentId === comment.id)}
              taskID={props.taskID}
              onChange={props.onChange}
            />
          </div>
        </article>
      )}
    </For>
  );
}

function CommentForm(props: {
  taskID: number;
  parentCommentID?: number;
  compact?: boolean;
  onChange: () => void;
}) {
  const action = mutation();
  const [uploading, setUploading] = createSignal(false);
  const [reset, setReset] = createSignal(0);
  return (
    <form classList={{ "comment-form": true, compact: props.compact }} onSubmit={(event) => {
      event.preventDefault();
      if (uploading()) return;
      const form = event.currentTarget;
      const data = new FormData(form);
      action.run(async () => {
        await post<Comment>(`/api/tasks/${props.taskID}/comments`, {
          content: String(data.get("content") ?? "").trim(),
          parentCommentId: props.parentCommentID,
        });
        setReset((value) => value + 1);
        props.onChange();
      });
    }}>
      <MediaTextarea name="content" required rows={props.compact ? 1 : 3}
        placeholder={props.compact ? "Reply…" : "Add a comment…"} disabled={action.pending()}
        reset={reset()} onUploadingChange={setUploading} />
      <button class="button quiet" disabled={action.pending() || uploading()}>{props.compact ? "Reply" : "Comment"}</button>
      <Show when={action.error()}><span class="form-error">{action.error()}</span></Show>
    </form>
  );
}

function MediaTextarea(props: {
  id?: string;
  name: string;
  initialValue?: string;
  rows: number;
  required?: boolean;
  placeholder?: string;
  disabled?: boolean;
  reset?: number;
  onUploadingChange?: (uploading: boolean) => void;
}) {
  const [value, setValue] = createSignal(props.initialValue ?? "");
  const [uploading, setUploading] = createSignal(false);
  const [progress, setProgress] = createSignal(0);
  const [total, setTotal] = createSignal(0);
  const [error, setError] = createSignal<string>();
  const [dragging, setDragging] = createSignal(false);
  let textarea: HTMLTextAreaElement | undefined;
  let fileInput: HTMLInputElement | undefined;
  let selectionStart = (props.initialValue ?? "").length;
  let selectionEnd = selectionStart;

  createEffect(() => {
    props.reset;
    const initialValue = props.initialValue ?? "";
    setValue(initialValue);
    selectionStart = initialValue.length;
    selectionEnd = initialValue.length;
  });

  const rememberSelection = (target: HTMLTextAreaElement) => {
    selectionStart = target.selectionStart;
    selectionEnd = target.selectionEnd;
  };

  const filesAtSelection = async (files: File[], start: number, end: number) => {
    if (!files.length || uploading()) return;
    setError();
    setTotal(files.length);
    setProgress(0);
    setUploading(true);
    props.onUploadingChange?.(true);
    try {
      for (const file of files) {
        if (file.type && file.type !== "application/octet-stream" && !mediaKind(file.type)) {
          throw new Error(`Unsupported media type: ${file.type}`);
        }
      }
      const markups: string[] = [];
      for (const [index, file] of files.entries()) {
        setProgress(index + 1);
        markups.push(mediaMarkup(await uploadMedia(file)));
      }
      const inserted = insertMediaMarkup(value(), start, end, markups);
      setValue(inserted.value);
      selectionStart = inserted.caret;
      selectionEnd = inserted.caret;
      queueMicrotask(() => {
        textarea?.focus();
        textarea?.setSelectionRange(inserted.caret, inserted.caret);
      });
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setUploading(false);
      props.onUploadingChange?.(false);
    }
  };

  return (
    <div classList={{ "media-textarea": true, dragging: dragging(), uploading: uploading() }}>
      <div class="media-editor">
        <textarea ref={textarea} id={props.id} name={props.name} value={value()} rows={props.rows}
          required={props.required} placeholder={props.placeholder} disabled={props.disabled || uploading()}
          onInput={(event) => {
            setValue(event.currentTarget.value);
            rememberSelection(event.currentTarget);
          }}
          onSelect={(event) => rememberSelection(event.currentTarget)}
          onBlur={(event) => rememberSelection(event.currentTarget)}
          onPaste={(event) => {
            const files = mediaFiles(event.clipboardData);
            if (!files.length) return;
            event.preventDefault();
            void filesAtSelection(files, event.currentTarget.selectionStart, event.currentTarget.selectionEnd);
          }}
          onDragOver={(event) => {
            const transfer = event.dataTransfer;
            if (!transfer || !Array.from(transfer.types).includes("Files")) return;
            event.preventDefault();
            transfer.dropEffect = "copy";
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(event) => {
            setDragging(false);
            const files = mediaFiles(event.dataTransfer);
            if (!files.length) return;
            event.preventDefault();
            void filesAtSelection(files, event.currentTarget.selectionStart, event.currentTarget.selectionEnd);
          }} />
        <div class="media-toolbar">
          <input ref={fileInput} class="media-file-input" type="file" accept={mediaAccept} multiple
            disabled={props.disabled || uploading()} onChange={(event) => {
              const files = Array.from(event.currentTarget.files ?? []);
              event.currentTarget.value = "";
              void filesAtSelection(files, selectionStart, selectionEnd);
            }} />
          <button type="button" class="media-picker" aria-label="Add image or video" title="Add image or video"
            disabled={props.disabled || uploading()} onClick={() => fileInput?.click()}>
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <rect x="3" y="4" width="18" height="16" rx="2" />
              <circle cx="8.5" cy="9" r="1.5" />
              <path d="m4 17 5-5 4 4 2-2 5 5" />
            </svg>
          </button>
          <Show when={uploading()}>
            <small class="upload-status" aria-live="polite">Uploading {progress()} of {total()}…</small>
          </Show>
        </div>
      </div>
      <Show when={error()}><small class="form-error" role="alert">{error()}</small></Show>
    </div>
  );
}

function CommentView() {
  const params = useParams();
  const navigate = useNavigate();
  const [data, { refetch }] = createResource(() => get<CommentDetail>(`/api/comments/${params.comment}`));
  liveRefetch(["comment.deleted", "reaction.updated"], refetch);
  return (
    <div class="page narrow">
      <Load data={data} error={() => data.error}>
        {(value) => {
          const current = () => data() ?? value;
          return <>
            <PageHeader eyebrow={`Task ${params.task}`} title={`Comment ${current().comment.id}`}
              actions={<A class="button" href={`/tasks/${params.task}`}>Back to task</A>} />
            <article class="comment featured">
              <header>
                <strong>{current().comment.author}</strong>
                <time>{date(current().comment.createdAt)}</time>
                <Show when={!current().comment.deletedAt}>
                  <CommentDeleteButton commentID={current().comment.id}
                    onDeleted={() => navigate(`/tasks/${params.task}`)} />
                </Show>
              </header>
              <Markdown content={current().comment.content} />
              <Show when={!current().comment.deletedAt}>
                <ReactionBar targetKind="comment" targetID={current().comment.id}
                  reactions={current().comment.reactions} onChange={refetch} />
              </Show>
            </article>
            <Show when={current().replies.length}>
              <section><SectionTitle title="Direct replies" />
                <div class="comments"><For each={current().replies}>{(reply) => <article class="comment"><header>
                  <strong>{reply.author}</strong><A href={`/tasks/${params.task}/comments/${reply.id}`}>#{reply.id}</A>
                  <time>{date(reply.createdAt)}</time>
                  <CommentDeleteButton commentID={reply.id} onDeleted={refetch} />
                </header><Markdown content={reply.content} />
                  <Show when={!reply.deletedAt}>
                    <ReactionBar targetKind="comment" targetID={reply.id}
                      reactions={reply.reactions} onChange={refetch} />
                  </Show>
                </article>}</For></div>
              </section>
            </Show>
            <ArtifactPanel artifacts={current().artifacts} relationType="comment" relationID={current().comment.id} onChange={refetch} />
          </>;
        }}
      </Load>
    </div>
  );
}

function ArtifactPanel(props: {
  artifacts: Artifact[];
  relationType: string;
  relationID: number;
  onChange: () => void;
}) {
  const action = mutation();
  return (
    <section>
      <SectionTitle title="Artifacts" />
      <Show when={props.artifacts.length} fallback={<Empty>No artifacts attached.</Empty>}>
        <div class="artifacts">
          <For each={props.artifacts}>{(artifact) => <article>
            <span class="artifact-type">{artifact.type}</span>
            <strong>{artifact.name || `Artifact ${artifact.id}`}</strong>
            <Show when={artifact.type === "link"} fallback={<p>{artifact.content}</p>}>
              <a href={artifact.content} target="_blank" rel="noreferrer">{artifact.content}</a>
            </Show>
          </article>}</For>
        </div>
      </Show>
      <form class="artifact-form" onSubmit={(event) => {
        event.preventDefault();
        const form = event.currentTarget;
        const data = new FormData(form);
        action.run(async () => {
          await post<Artifact>("/api/artifacts", {
            name: optional(data.get("name")),
            type: data.get("type"),
            content: String(data.get("content") ?? "").trim(),
            relationType: props.relationType,
            relationId: props.relationID,
          });
          form.reset();
          props.onChange();
        });
      }}>
        <input name="name" placeholder="Name (optional)" />
        <select name="type"><option>text</option><option>link</option><option>image</option><option>document</option></select>
        <textarea name="content" required rows="2" placeholder="Content, URL, or path" />
        <button class="button quiet" disabled={action.pending()}>Attach artifact</button>
        <Show when={action.error()}><span class="form-error">{action.error()}</span></Show>
      </form>
    </section>
  );
}

function Events() {
  const [events, setEvents] = createSignal<Event[]>([]);
  const [selectedEventTypes, setSelectedEventTypes] = createSignal<string[]>([]);
  const [error, setError] = createSignal("");
  const [connected, setConnected] = createSignal(false);
  const [hasOlder, setHasOlder] = createSignal(false);
  const older = mutation();
  const [wireContent, setWireContent] = createSignal<HTMLDivElement>();
  const [olderSentinel, setOlderSentinel] = createSignal<HTMLDivElement>();
  let follower: NewestFollower | undefined;
  let source: EventSource | undefined;
  let continuationFrame: number | undefined;
  const eventTypes = createMemo(() => eventTypeOptions(events()));
  const filteredEvents = createMemo(() => filterEvents(events(), selectedEventTypes()));
  const allEventTypesSelected = createMemo(() => eventTypes().length > 0
    && eventTypes().every((eventType) => selectedEventTypes().includes(eventType)));
  const scheduleOlderContinuation = () => {
    if (continuationFrame != null) window.cancelAnimationFrame(continuationFrame);
    continuationFrame = window.requestAnimationFrame(() => {
      continuationFrame = undefined;
      const sentinel = olderSentinel();
      if (shouldContinueEventPaging({
        hasOlder: hasOlder(),
        pending: older.pending(),
        error: Boolean(older.error()),
        sentinelBounds: sentinel?.getBoundingClientRect(),
        viewportHeight: window.innerHeight,
      })) loadOlder();
    });
  };
  function loadOlder(retry = false) {
    if (!hasOlder() || older.pending() || (older.error() && !retry)) return;
    void older.run(async () => {
      const before = events().at(-1)?.id;
      if (!before) return;
      const page = await get<{ events: Event[] }>(`/api/events?before=${before}&limit=${EVENT_PAGE_SIZE}`);
      setEvents((current) => uniqueByID([...current, ...page.events]));
      setHasOlder(page.events.length === EVENT_PAGE_SIZE);
      scheduleOlderContinuation();
    });
  }
  createEffect(() => {
    const content = wireContent();
    if (!content) return;
    const currentFollower = bindNewestFollower({
      edge: "start",
      viewport: window,
      content,
      anchorRows: () => content.querySelectorAll<HTMLElement>("[data-event-id]"),
    });
    follower = currentFollower;
    onCleanup(() => {
      currentFollower.dispose();
      if (follower === currentFollower) follower = undefined;
    });
  });
  createEffect(() => {
    const sentinel = olderSentinel();
    if (!sentinel) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) loadOlder();
    });
    observer.observe(sentinel);
    onCleanup(() => observer.disconnect());
  });
  onMount(async () => {
    try {
      const initial = await get<{ events: Event[] }>(`/api/events?limit=${EVENT_PAGE_SIZE}`);
      setEvents(initial.events);
      setHasOlder(initial.events.length === EVENT_PAGE_SIZE);
      const after = initial.events[0]?.id ?? 0;
      source = new EventSource(`/api/events/stream?after=${after}`);
      source.onopen = () => setConnected(true);
      source.onerror = () => setConnected(false);
      source.onmessage = (message) => {
        const event = JSON.parse(message.data) as Event;
        follower?.beforePrepend();
        setEvents((current) => [event, ...current.filter((item) => item.id !== event.id)]);
      };
    } catch (caught) {
      setError(errorMessage(caught));
    }
  });
  onCleanup(() => {
    source?.close();
    if (continuationFrame != null) window.cancelAnimationFrame(continuationFrame);
  });
  return (
    <div class="page">
      <PageHeader title="Event wire" description="Every accepted fact, newest first."
        actions={<span classList={{ connection: true, live: connected() }}><span />{connected() ? "Live" : "Connecting"}</span>} />
      <Show when={!error()} fallback={<div class="state">{error()}</div>}>
        <Show when={events().length} fallback={<Empty>The wire is quiet.</Empty>}>
          <EventFilters
            eventTypes={eventTypes()}
            selectedEventTypes={selectedEventTypes()}
            displayedResultCount={filteredEvents().length}
            totalResultCount={events().length}
            allSelected={allEventTypesSelected()}
            onEventTypeChange={(eventType, selected) => setSelectedEventTypes((current) => selected
              ? [...current, eventType]
              : current.filter((value) => value !== eventType))}
            onSelectAll={() => setSelectedEventTypes([...eventTypes()])}
            onUnselectAll={() => setSelectedEventTypes([])}
            onClear={() => setSelectedEventTypes([])}
          />
          <Show when={filteredEvents().length} fallback={<div class="empty event-filter-empty">
            <p>No loaded events match this filter.</p>
            <button type="button" class="button quiet" onClick={() => setSelectedEventTypes([])}>Clear filters</button>
          </div>}>
            <div ref={(element) => setWireContent(element)} class="wire-table">
              <For each={filteredEvents()}>{(event) => <EventRow event={event} expanded />}</For>
            </div>
          </Show>
        </Show>
        <Show when={hasOlder()}>
          <div ref={(element) => setOlderSentinel(element)} class="wire-loader" aria-live="polite">
            <Show when={older.pending()}>Loading {EVENT_PAGE_SIZE} older events…</Show>
            <Show when={older.error()}>
              <span class="form-error">{older.error()}</span>
              <button type="button" class="button quiet" onClick={() => loadOlder(true)}>Retry</button>
            </Show>
          </div>
        </Show>
      </Show>
    </div>
  );
}

function EventFilters(props: {
  eventTypes: string[];
  selectedEventTypes: string[];
  displayedResultCount: number;
  totalResultCount: number;
  allSelected: boolean;
  onEventTypeChange: (eventType: string, selected: boolean) => void;
  onSelectAll: () => void;
  onUnselectAll: () => void;
  onClear: () => void;
}) {
  return (
    <div class="event-filters" aria-label="Event filters">
      <details class="event-filter">
        <summary>Events <span>{props.selectedEventTypes.length} selected</span></summary>
        <div class="event-filter-panel">
          <fieldset>
            <legend class="filter-field-legend">
              <span>Seen event types</span>
              <FilterFieldActions
                selectLabel="Select all event types"
                unselectLabel="Unselect all event types"
                selectDisabled={props.allSelected}
                unselectDisabled={props.selectedEventTypes.length === 0}
                onSelect={props.onSelectAll}
                onUnselect={props.onUnselectAll}
              />
            </legend>
            <div class="event-filter-options" role="group" aria-label="Filter by event type">
              <For each={props.eventTypes}>{(eventType) => <label>
                <input type="checkbox" checked={props.selectedEventTypes.includes(eventType)}
                  onChange={(event) => props.onEventTypeChange(eventType, event.currentTarget.checked)} />
                <span>{eventType}</span>
              </label>}</For>
            </div>
          </fieldset>
          <button type="button" class="button quiet" disabled={props.selectedEventTypes.length === 0}
            onClick={props.onClear}>Clear filters</button>
        </div>
      </details>
      <p class="event-filter-results" role="status">
        Showing {props.displayedResultCount} of {props.totalResultCount} loaded events
      </p>
    </div>
  );
}

function EventRow(props: { event: Event; expanded?: boolean }) {
  return (
    <A href={`/events/${props.event.id}`} classList={{ "event-row": true, expanded: props.expanded }}
      data-event-id={props.expanded ? props.event.id : undefined}>
      <span class="id">#{props.event.id}</span>
      <strong>{props.event.type}</strong>
      <Show when={props.expanded}><code>{compactJSON(props.event.data)}</code></Show>
      <time>{date(props.event.at)}</time>
    </A>
  );
}

function EventView() {
  const params = useParams();
  const [data] = createResource(() => get<Event>(`/api/events/${params.event}`));
  return (
    <div class="page narrow">
      <Load data={data} error={() => data.error}>
        {(event) => <>
          <PageHeader eyebrow={`Event ${event.id}`} title={event.type} actions={<A class="button" href="/events">Back to wire</A>} />
          <dl class="meta"><div><dt>ID</dt><dd>{event.id}</dd></div><div><dt>Received</dt><dd>{date(event.at)}</dd></div></dl>
          <pre class="event-data">{JSON.stringify(event.data, null, 2)}</pre>
        </>}
      </Load>
    </div>
  );
}

function History() {
  const [data, { refetch }] = createResource(async () => {
    const sections = await Promise.all(historyStatuses.map(async (section) => ({
      ...section,
      response: await get<HistoryListResponse>(historyPageURL(section.status, HISTORY_OVERVIEW_LIMIT)),
    })));
    return {
      sections: sections.map(({ response, ...section }) => ({
        ...section, runs: response.history.filter((run) => run.status === section.status),
      })),
      checkpointEventId: Math.min(...sections.map((section) => section.response.checkpointEventId)),
    };
  });
  liveHistoryRows(() => data()?.checkpointEventId, refetch);
  return (
    <div class="page">
      <PageHeader title="Workflow history"
        description="Running, waiting, failed, and completed workflow runs, newest first." />
      <Load data={data} error={() => data.error}>
        {(value) => <div class="history-sections">
          <For each={data()?.sections ?? value.sections}>{(section) => <section class="history-section">
            <SectionTitle title={section.label} href={section.href} />
            <Show when={section.runs.length} fallback={<Empty>No {section.label.toLowerCase()} runs.</Empty>}>
              <div class="rows">
                <For each={section.runs}>{(run) => <HistoryRow run={run} />}</For>
              </div>
            </Show>
          </section>}</For>
        </div>}
      </Load>
    </div>
  );
}

function HistoryRow(props: { run: WorkflowRun }) {
  return (
    <A class="history-row" href={historyRunHref(props.run.id)!}>
      <span class={`run-status ${props.run.status}`}>{props.run.status}</span>
      <span class="run-title"><strong>{props.run.workflowName || `Workflow ${props.run.workflowId}`}</strong>
        <small>Event #{props.run.sourceEventId} · Trigger #{props.run.triggerId}</small></span>
      <span class="id">#{props.run.id}</span>
      <time>{date(props.run.createdAt)}</time>
    </A>
  );
}

function HistoryStatusPage(props: { status: WorkflowRunStatus; label: string }) {
  const [runs, setRuns] = createSignal<WorkflowRun[]>([]);
  const [checkpoint, setCheckpoint] = createSignal<number>();
  const [loaded, setLoaded] = createSignal(false);
  const [refreshing, setRefreshing] = createSignal(false);
  const [latestError, setLatestError] = createSignal("");
  const [hasOlder, setHasOlder] = createSignal(false);
  const [olderPending, setOlderPending] = createSignal(false);
  const [olderError, setOlderError] = createSignal("");
  const [olderSentinel, setOlderSentinel] = createSignal<HTMLDivElement>();
  let generation = 0;

  const replaceLatest = async () => {
    const requestGeneration = ++generation;
    setRefreshing(true);
    setLatestError("");
    setHasOlder(false);
    setOlderPending(false);
    setOlderError("");
    try {
      const response = await get<HistoryListResponse>(historyPageURL(props.status, HISTORY_PAGE_SIZE));
      if (requestGeneration !== generation) return;
      setRuns(response.history.filter((run) => run.status === props.status));
      setHasOlder(response.history.length === HISTORY_PAGE_SIZE);
      setCheckpoint(response.checkpointEventId);
      setLoaded(true);
    } catch (caught) {
      if (requestGeneration === generation) setLatestError(errorMessage(caught));
    } finally {
      if (requestGeneration === generation) setRefreshing(false);
    }
  };

  const loadOlder = () => {
    const cursor = runs().at(-1)?.id;
    if (!cursor || !canLoadHistoryPage({
      hasOlder: hasOlder(), pending: olderPending(), refreshing: refreshing(), error: Boolean(olderError()),
    })) return;
    const request = { generation, cursor };
    setOlderPending(true);
    void get<HistoryListResponse>(historyPageURL(props.status, HISTORY_PAGE_SIZE, cursor))
      .then((response) => {
        if (!historyPageRequestIsCurrent(request, generation, runs().at(-1)?.id)) return;
        setRuns((current) => mergeHistoryRuns(current, response.history, props.status));
        setHasOlder(response.history.length === HISTORY_PAGE_SIZE);
      })
      .catch((caught) => {
        if (request.generation === generation) setOlderError(errorMessage(caught));
      })
      .finally(() => {
        if (request.generation === generation) setOlderPending(false);
      });
  };

  const retryOlder = () => {
    setOlderError("");
    loadOlder();
  };

  createEffect(() => {
    const sentinel = olderSentinel();
    if (!sentinel) return;
    const disconnect = observeHistorySentinel(sentinel, loadOlder);
    onCleanup(disconnect);
  });
  onMount(() => void replaceLatest());
  liveHistoryRows(checkpoint, () => void replaceLatest());

  return (
    <div class="page">
      <PageHeader eyebrow="Workflow history" title={`${props.label} runs`}
        description={`Newest ${props.status} workflow runs first.`}
        actions={<A class="button" href="/history">Back to history</A>} />
      <Show when={loaded()} fallback={<div class="state">
        <Show when={latestError()} fallback="Loading…">
          <span class="form-error">{latestError()}</span>
          <button type="button" class="button quiet" onClick={() => void replaceLatest()}>Retry</button>
        </Show>
      </div>}>
        <Show when={runs().length} fallback={<Empty>No {props.status} runs.</Empty>}>
          <div class="rows"><For each={runs()}>{(run) => <HistoryRow run={run} />}</For></div>
        </Show>
        <Show when={latestError()}>
          <div class="history-loader">
            <span class="form-error">{latestError()}</span>
            <button type="button" class="button quiet" onClick={() => void replaceLatest()}>Retry latest page</button>
          </div>
        </Show>
        <Show when={hasOlder() || olderPending() || olderError()}>
          <div ref={(element) => setOlderSentinel(element)} class="history-loader" aria-live="polite">
            <Show when={olderPending()}>Loading {HISTORY_PAGE_SIZE} older runs…</Show>
            <Show when={olderError()}>
              <span class="form-error">{olderError()}</span>
              <button type="button" class="button quiet" onClick={retryOlder}>Retry</button>
            </Show>
          </div>
        </Show>
      </Show>
    </div>
  );
}

function HistoryView() {
	const params = useParams();
	const [data, { refetch }] = createResource(() => get<HistoryDetail>(`/api/history/${params.item}`));
	const [olderEvents, setOlderEvents] = createSignal<HistoryDetail["events"]>([]);
	const [hasOlder, setHasOlder] = createSignal(false);
	const older = mutation();
	createEffect(() => {
		const latest = data()?.events;
		if (latest && olderEvents().length === 0) setHasOlder(latest.length === PAGE_SIZE);
	});
  const [historyContent, setHistoryContent] = createSignal<HTMLDivElement>();
  createEffect(() => {
    const content = historyContent();
    if (!content) return;
    const follower = bindNewestFollower({ edge: "end", viewport: window, content });
    onCleanup(() => follower.dispose());
  });
  liveRefetch([
    "workflow.run.event", "workflow.run.waiting", "workflow.run.resumed",
    "workflow.run.completed", "workflow.run.failed",
  ], refetch);
  return (
    <div ref={(element) => setHistoryContent(element)} class="page">
      <Load data={data} error={() => data.error}>
		{(value) => {
		  const current = () => {
			const latest = data() ?? value;
			return { ...latest, events: uniqueByID([...olderEvents(), ...latest.events]).sort((left, right) => left.id - right.id) };
		  };
          const resource = () => historyResourceLink(current().run);
          return <>
            <PageHeader eyebrow={`Run ${value.run.id}`}
              title={value.run.workflowName || `Workflow ${value.run.workflowId}`}
              description={`Started from event ${value.run.sourceEventId}`}
              actions={<A class="button" href="/history">Back to history</A>} />
            <div class="run-summary">
              <span class={`run-status ${current().run.status}`}>{current().run.status}</span>
              <A href={`/workflows/${value.run.workflowId}`}>Workflow #{value.run.workflowId}</A>
              <A href={`/events/${value.run.sourceEventId}`}>Event #{value.run.sourceEventId}</A>
              <Show when={resource()}>{(link) =>
                <A href={link().href}>{link().label}</A>}
              </Show>
              <span>Trigger #{value.run.triggerId}</span>
              <time>Started {date(value.run.createdAt)}</time>
              <time>Updated {date(current().run.updatedAt)}</time>
            </div>
            <Show when={phaseGroups(current()).length} fallback={<Empty>
              {current().run.status === "running"
                ? "Waiting for the first workflow event…"
                : current().run.status === "waiting"
                  ? "Waiting for a human response on the task…"
                  : "No workflow events were recorded for this run."}
            </Empty>}>
              <div class="run-phases">
                <For each={phaseGroups(current())}>{([phase, events]) => <section class="run-phase">
                  <header><h2>{phase}</h2><span>{events.length} {events.length === 1 ? "event" : "events"}</span></header>
                  <div class="run-events">
                    <For each={events}>{(event) => <article class="run-event">
                      <header>
                        <span class="artifact-type">{event.type}</span>
                        <strong>{workflowEventTitle(event)}</strong>
                        <Show when={event.backend}><span>{event.backend}</span></Show>
                        <span>seq {event.sequence}</span>
                        <time>{date(event.at)}</time>
                      </header>
                      <Show when={workflowEventMeta(event)}>{(meta) => <p class="run-event-meta">{meta()}</p>}</Show>
                      <Show when={event.message}>{(message) =>
                        <div class="run-event-content"><Markdown content={message()} /></div>}
                      </Show>
                      <Show when={event.error}>{(error) =>
                        <div class="run-event-content step-error"><Markdown content={error()} /></div>}
                      </Show>
                      <Show when={event.result != null}>
                        <div class="run-event-content"><Markdown content={formatResult(event.result)} /></div>
                      </Show>
                    </article>}</For>
                  </div>
                </section>}</For>
              </div>
			</Show>
			<Show when={hasOlder()}>
			  <button class="button quiet" disabled={older.pending()} onClick={() => older.run(async () => {
				const before = current().events[0]?.id;
				if (!before) return;
				const page = await get<HistoryDetail>(`/api/history/${params.item}?before=${before}`);
				setOlderEvents((events) => uniqueByID([...page.events, ...events]));
				setHasOlder(page.events.length === PAGE_SIZE);
			  })}>{older.pending() ? "Loading…" : "Load older run events"}</button>
			</Show>
			<Show when={current().run.output || current().run.error}>
              <section class="run-output"><SectionTitle title={current().run.error ? "Run error" : "Final result"} />
                <div classList={{ "run-event-content": true, "step-error": Boolean(current().run.error) }}>
                  <Markdown content={current().run.error || current().run.output} />
                </div>
              </section>
            </Show>
          </>;
        }}
      </Load>
    </div>
  );
}

function Triggers() {
  const [data] = createResource(async () => {
    const [triggers, workflows] = await Promise.all([
      get<{ triggers: Trigger[] }>("/api/triggers"), get<{ workflows: Workflow[] }>("/api/workflows"),
    ]);
    return { triggers: triggers.triggers, workflows: workflows.workflows };
  });
  const [selectedEventTypes, setSelectedEventTypes] = createSignal<string[]>([]);
  const [selectedWorkflowIDs, setSelectedWorkflowIDs] = createSignal<number[]>([]);
  const eventTypeOptions = createMemo(() => triggerEventTypeOptions(data()?.triggers ?? []));
  const workflowOptions = createMemo(() => triggerWorkflowOptions(
    data()?.triggers ?? [],
    data()?.workflows ?? [],
  ));
  const filteredTriggers = createMemo(() => filterTriggers(data()?.triggers ?? [], {
    eventTypes: selectedEventTypes(),
    workflowIds: selectedWorkflowIDs(),
  }));
  const activeFilterCount = createMemo(() => selectedEventTypes().length + selectedWorkflowIDs().length);
  const displayedResultCount = createMemo(() => filteredTriggers().length);
  const clearFilters = () => {
    setSelectedEventTypes([]);
    setSelectedWorkflowIDs([]);
  };
  return (
    <div class="page">
      <PageHeader title="Triggers" description="Match an event on the wire or a cron tick, then run one workflow when enabled."
        actions={<A class="button primary" href="/triggers/new">New trigger</A>} />
      <Load data={data} error={() => data.error}>
        {(value) => <Show when={value.triggers.length} fallback={<Empty>No triggers configured.</Empty>}>
          <TriggerFilters
            eventTypes={eventTypeOptions()}
            workflows={workflowOptions()}
            selectedEventTypes={selectedEventTypes()}
            selectedWorkflowIDs={selectedWorkflowIDs()}
            activeFilterCount={activeFilterCount()}
            displayedResultCount={displayedResultCount()}
            totalResultCount={value.triggers.length}
            onEventTypeChange={(eventType, selected) => setSelectedEventTypes((current) => selected
              ? [...current, eventType]
              : current.filter((value) => value !== eventType))}
            onWorkflowChange={(workflowID, selected) => setSelectedWorkflowIDs((current) => selected
              ? [...current, workflowID]
              : current.filter((value) => value !== workflowID))}
            onClear={clearFilters}
          />
          <Show when={displayedResultCount()} fallback={<div class="empty trigger-filter-empty">
            <p>No triggers match these filters.</p>
            <button type="button" class="button quiet" onClick={clearFilters}>Clear filters</button>
          </div>}>
            <div class="rows"><For each={filteredTriggers()}>{(trigger) => <A
              classList={{ "trigger-row": true, disabled: !trigger.enabled }} href={`/triggers/${trigger.id}`}>
              <span class="event-chip">{trigger.eventType}</span>
              <strong>{workflowName(trigger.workflowId, value.workflows)}</strong>
              <span class="trigger-schedule">{trigger.schedule || "On event"}</span>
              <span class={`trigger-state ${trigger.enabled ? "enabled" : "disabled"}`}>
                {trigger.enabled ? "Enabled" : "Disabled"}
              </span>
              <span class="id">#{trigger.id}</span>
            </A>}</For></div>
          </Show>
        </Show>}
      </Load>
    </div>
  );
}

function TriggerFilters(props: {
  eventTypes: string[];
  workflows: Array<{ id: number; label: string }>;
  selectedEventTypes: string[];
  selectedWorkflowIDs: number[];
  activeFilterCount: number;
  displayedResultCount: number;
  totalResultCount: number;
  onEventTypeChange: (eventType: string, selected: boolean) => void;
  onWorkflowChange: (workflowID: number, selected: boolean) => void;
  onClear: () => void;
}) {
  return (
    <div class="trigger-filters" aria-label="Trigger filters">
      <details class="trigger-filter">
        <summary>Events <span>{props.selectedEventTypes.length} selected</span></summary>
        <div class="trigger-filter-options" role="group" aria-label="Filter by event type">
          <For each={props.eventTypes}>{(eventType) => <label>
            <input type="checkbox" checked={props.selectedEventTypes.includes(eventType)}
              onChange={(event) => props.onEventTypeChange(eventType, event.currentTarget.checked)} />
            <span>{eventType}</span>
          </label>}</For>
        </div>
      </details>
      <details class="trigger-filter">
        <summary>Workflows <span>{props.selectedWorkflowIDs.length} selected</span></summary>
        <div class="trigger-filter-options" role="group" aria-label="Filter by workflow">
          <For each={props.workflows}>{(workflow) => <label>
            <input type="checkbox" checked={props.selectedWorkflowIDs.includes(workflow.id)}
              onChange={(event) => props.onWorkflowChange(workflow.id, event.currentTarget.checked)} />
            <span>{workflow.label}</span>
          </label>}</For>
        </div>
      </details>
      <button type="button" class="button quiet" disabled={props.activeFilterCount === 0} onClick={props.onClear}>
        Clear filters
      </button>
      <p class="trigger-filter-results" role="status">
        Showing {props.displayedResultCount} of {props.totalResultCount} triggers
      </p>
    </div>
  );
}

function TriggerNew() {
  const navigate = useNavigate();
  const [options] = createResource(triggerOptions);
  const action = mutation();
  return (
    <div class="page narrow">
      <PageHeader eyebrow="Triggers" title="Create a trigger" />
      <Load data={options} error={() => options.error}>
        {(value) => <TriggerForm {...value} pending={action.pending()} error={action.error()} onSave={(body) => action.run(async () => {
          const created = await post<Trigger>("/api/triggers", body);
          navigate(`/triggers/${created.id}`);
        })} />}
      </Load>
    </div>
  );
}

function TriggerView() {
  const params = useParams();
  const navigate = useNavigate();
  const [trigger, { refetch }] = createResource(() => get<Trigger>(`/api/triggers/${params.trigger}`));
  const [options] = createResource(triggerOptions);
  const action = mutation();
  return (
    <div class="page narrow">
      <Load data={trigger} error={() => trigger.error}>
        {(selected) => <>
          <PageHeader eyebrow={`Trigger ${selected.id}`} title={selected.eventType} />
          <Load data={options} error={() => options.error}>
            {(value) => <TriggerForm trigger={selected} {...value} pending={action.pending()} error={action.error()}
              onSave={(body) => action.run(async () => {
                await put<Trigger>(`/api/triggers/${selected.id}`, body);
                await refetch();
              })} />}
          </Load>
          <Meta value={selected} />
          <button class="button danger" onClick={() => action.run(async () => {
            await remove(`/api/triggers/${selected.id}`);
            navigate("/triggers");
          })}>Delete trigger</button>
        </>}
      </Load>
    </div>
  );
}

async function triggerOptions() {
  const [types, workflows] = await Promise.all([
    get<{ eventTypes: string[] }>("/api/events/types"), get<{ workflows: Workflow[] }>("/api/workflows"),
  ]);
  return { eventTypes: types.eventTypes, workflows: workflows.workflows };
}

function TriggerForm(props: {
  trigger?: Trigger;
  eventTypes: string[];
  workflows: Workflow[];
  pending: boolean;
  error?: string;
  onSave: (body: unknown) => void;
}) {
  return (
    <form class="form-panel" onSubmit={(event) => {
      event.preventDefault();
      const data = new FormData(event.currentTarget);
      props.onSave({
        eventType: data.get("eventType"),
        schedule: optional(data.get("schedule")),
        workflowId: Number(data.get("workflowId")),
        enabled: data.has("enabled"),
      });
    }}>
      <label>Event type<select name="eventType" required value={props.trigger?.eventType ?? ""}>
        <option value="" disabled>Select an event</option>
        <For each={props.eventTypes}>{(type) => <option value={type}>{type}</option>}</For>
      </select></label>
      <label>Workflow<select name="workflowId" required value={props.trigger?.workflowId ?? ""}>
        <option value="" disabled>Select a workflow</option>
        <For each={props.workflows}>{(workflow) => <option value={workflow.id}>{workflow.name}</option>}</For>
      </select></label>
      <label>Cron schedule<input name="schedule" placeholder="0 9 * * 1-5" value={props.trigger?.schedule ?? ""} />
        <small>Used only when the event type is cron.</small></label>
      <label class="checkbox-field">
        <input name="enabled" type="checkbox" checked={props.trigger?.enabled ?? true} />
        <span>Enabled<small>Disabled triggers stay visible but admit no new workflow runs.</small></span>
      </label>
      <FormFooter pending={props.pending} error={props.error} label={props.trigger ? "Save trigger" : "Create trigger"} />
    </form>
  );
}

function Workflows() {
  const [data, { refetch }] = createResource(() => get<{ workflows: Workflow[] }>("/api/workflows"));
  const workflows = createMemo(() => sortWorkflowsByUsage(data()?.workflows ?? []));
  liveRefetch(["workflow.run.started"], refetch);
  return (
    <div class="page">
      <PageHeader title="Workflows" description="Discovered by the workflow CLI. Factory-authored files live outside git."
        actions={<A class="button primary" href="/workflows/new">New workflow</A>} />
      <Load data={data} error={() => data.error}>
        {(value) => <Show when={value.workflows.length} fallback={<Empty>No workflows discovered.</Empty>}>
          <div class="card-grid workflows"><For each={workflows()}>{(workflow) => <A class="project-card" href={`/workflows/${workflow.id}`}>
            <span class="id">#{workflow.id} · {workflow.scope || "factory"}</span>
            <h2>{workflow.name}</h2><p>{workflow.description || "No description"}</p>
            <div class="workflow-usage">
              <span class="workflow-usage-item" role="group" title={`Total workflow runs: ${workflow.runCount}`}
                aria-label={`Total workflow runs: ${workflow.runCount}`}>
                <Play aria-hidden="true" />
                <span>{workflow.runCount}</span>
              </span>
              <span class="workflow-usage-item" role="group" title={`Distinct tasks: ${workflow.taskCount}`}
                aria-label={`Distinct tasks: ${workflow.taskCount}`}>
                <ListTodo aria-hidden="true" />
                <span>{workflow.taskCount}</span>
              </span>
            </div>
            <div class="phases"><For each={workflow.phases}>{(phase) => <span>{phase}</span>}</For></div>
          </A>}</For></div>
        </Show>}
      </Load>
    </div>
  );
}

function WorkflowNew() {
  const navigate = useNavigate();
  const action = mutation();
  return (
    <div class="page chat-page">
      <PageHeader eyebrow="Workflow studio" title="Describe the workflow"
        description="The selected harness will generate the dynamic workflow code. There is no manual editor." />
      <form class="composer hero-composer" onSubmit={(event) => {
        event.preventDefault();
        const form = event.currentTarget;
        const data = new FormData(form);
        action.run(async () => {
          const created = await post<Workflow>("/api/workflows", { message: String(data.get("message") ?? "").trim() });
          navigate(`/workflows/${created.id}`);
        });
      }}>
        <textarea name="message" required rows="8" placeholder="Build a workflow that reviews a plan with three independent agents, synthesizes their findings, and returns the blocking issues first." />
        <button class="button primary" disabled={action.pending()}>{action.pending() ? "Starting agent…" : "Start collaborating"}</button>
        <Show when={action.error()}><span class="form-error">{action.error()}</span></Show>
      </form>
    </div>
  );
}

function WorkflowView() {
  const params = useParams();
  const navigate = useNavigate();
  const [data, { refetch }] = createResource(() => get<WorkflowDetail>(`/api/workflows/${params.workflow}`));
  const action = mutation();
  const [conversationViewport, setConversationViewport] = createSignal<HTMLDivElement>();
  const [conversationContent, setConversationContent] = createSignal<HTMLDivElement>();
  createEffect(() => {
    const viewport = conversationViewport();
    const content = conversationContent();
    if (!viewport || !content) return;
    const follower = bindNewestFollower({ edge: "end", viewport, content });
    onCleanup(() => follower.dispose());
  });
  liveRefetch(["comment.created", "workflow.updated", "workflow.authoring.completed", "workflow.authoring.failed"], refetch);
  let sourcePolling: number | undefined;
  onMount(() => {
    sourcePolling = window.setInterval(() => {
      if (workflowConversationWorking(data()?.comments ?? [])) void refetch();
    }, 1000);
  });
  onCleanup(() => window.clearInterval(sourcePolling));
  return (
    <div class="page chat-page">
      <Load data={data} error={() => data.error}>
        {(value) => {
          const current = () => data() ?? value;
          const working = () => workflowConversationWorking(current().comments);
          const source = createMemo(() => current().source);
          const highlightedSource = createMemo(() => highlightWorkflowSource(source()).value);
          return <>
            <PageHeader eyebrow={`Workflow ${value.workflow.id}`} title={value.workflow.name}
              description={value.workflow.description}
              actions={<button class="button danger" onClick={() => action.run(async () => {
                await remove(`/api/workflows/${value.workflow.id}`);
                navigate("/workflows");
              })}>Delete</button>} />
            <div class="workflow-meta">
              <span>{value.workflow.scope || "factory"}</span>
              <Show when={value.workflow.path}><code>{value.workflow.path}</code></Show>
              <Show when={value.workflow.mutating}><span class="event-chip">mutating</span></Show>
            </div>
            <div class="workflow-studio">
              <section class="workflow-chat" aria-label="Workflow conversation">
                <div ref={(element) => setConversationViewport(element)} class="conversation" role="log" aria-live="polite">
                  <div ref={(element) => setConversationContent(element)} class="conversation-content">
                    <For each={current().comments}>{(comment) => {
                      const presentation = workflowCommentPresentation(comment);
                      return <article classList={{
                        message: true,
                        agent: comment.author === "agent",
                        step: presentation.intermediate,
                        error: presentation.error,
                        reasoning: presentation.reasoning,
                      }}>
                        <header>
                          <strong>{presentation.title}</strong>
                          <Show when={presentation.kindLabel}>{(label) => <span>{label()}</span>}</Show>
                          <time>{date(comment.createdAt)}</time>
                        </header>
                        <Show when={presentation.preformatted} fallback={<p>{comment.content}</p>}>
                          <pre>{comment.content}</pre>
                        </Show>
                      </article>;
                    }}</For>
                    <Show when={working()}><article class="message agent working"><header><strong>Agent</strong></header><p>Working on the workflow…</p></article></Show>
                  </div>
                </div>
                <form class="composer" onSubmit={(event) => {
                  event.preventDefault();
                  const form = event.currentTarget;
                  const body = new FormData(form);
                  action.run(async () => {
                    await post<Comment>(`/api/workflows/${value.workflow.id}/comments`, {
                      message: String(body.get("message") ?? "").trim(),
                    });
                    form.reset();
                    await refetch();
                  });
                }}>
                  <textarea name="message" required rows="4" placeholder="Ask the agent to revise, explain, or extend the workflow…" />
                  <button class="button primary" disabled={action.pending()}>Send</button>
                  <Show when={action.error()}><span class="form-error">{action.error()}</span></Show>
                </form>
              </section>
              <section class="source-panel" aria-label="Live workflow source">
                <header>
                  <div><span>Live source</span><strong>{fileName(current().workflow.path)}</strong></div>
                  <span classList={{ "source-status": true, working: working() }}>{working() ? "Updating" : "Current"}</span>
                </header>
                <pre tabIndex={0}>
                  <code class="hljs language-javascript" innerHTML={highlightedSource()} />
                </pre>
              </section>
            </div>
          </>;
        }}
      </Load>
    </div>
  );
}

function FormFooter(props: { pending: boolean; error?: string; label: string; onCancel?: () => void }) {
  return (
    <footer class="form-footer">
      <Show when={props.error}><span class="form-error">{props.error}</span></Show>
      <Show when={props.onCancel}>
        <button type="button" class="button quiet" disabled={props.pending} onClick={props.onCancel}>Cancel</button>
      </Show>
      <button class="button primary" disabled={props.pending}>{props.pending ? "Saving…" : props.label}</button>
    </footer>
  );
}

function mutation() {
  const [pending, setPending] = createSignal(false);
  const [error, setError] = createSignal<string>();
  return {
    pending,
    error,
    run: async (action: () => Promise<void>) => {
      setPending(true);
      setError();
      try {
        await action();
      } catch (caught) {
        setError(errorMessage(caught));
      } finally {
        setPending(false);
      }
    },
  };
}

function liveRefetch(types: string[], refetch: () => unknown) {
  let source: EventSource | undefined;
  onMount(async () => {
    const initial = await get<{ events: Event[] }>("/api/events");
	source = new EventSource(`/api/events/stream?after=${initial.events[0]?.id ?? 0}`);
    source.onmessage = (message) => {
      const event = JSON.parse(message.data) as Event;
      if (types.includes(event.type)) refetch();
    };
  });
  onCleanup(() => source?.close());
}

function liveTaskRows(checkpoint: () => number | undefined, refetch: () => unknown) {
  const types = [
    "task.created", "task.updated", "task.deleted", "comment.created", "comment.deleted",
    "reaction.updated",
    "workflow.run.started", "workflow.run.waiting", "workflow.run.resumed",
    "workflow.run.completed", "workflow.run.failed",
  ];
  let source: EventSource | undefined;
  createEffect(() => {
    const after = checkpoint();
    if (after == null) return;
    source?.close();
    source = new EventSource(`/api/events/stream?after=${after}`);
    source.onmessage = (message) => {
      const event = JSON.parse(message.data) as Event;
      if (types.includes(event.type)) refetch();
    };
  });
  onCleanup(() => source?.close());
}

function liveHistoryRows(checkpoint: () => number | undefined, refresh: () => unknown) {
  const types = [
    "workflow.run.started", "workflow.run.waiting", "workflow.run.resumed",
    "workflow.run.completed", "workflow.run.failed",
  ];
  let source: EventSource | undefined;
  createEffect(() => {
    const after = checkpoint();
    if (after == null) return;
    source?.close();
    source = new EventSource(`/api/events/stream?after=${after}`);
    source.onmessage = (message) => {
      const event = JSON.parse(message.data) as Event;
      if (types.includes(event.type)) refresh();
    };
  });
  onCleanup(() => source?.close());
}

function taskValue(task: Task, field: TaskField): string | number {
  return ({
    id: task.id, createdAt: task.createdAt, updatedAt: task.updatedAt,
    deletedAt: task.deletedAt ?? "", title: task.title, description: task.description ?? "",
    parentTaskId: task.parentTaskId ?? 0, status: task.status, projectId: task.projectId,
  })[field] ?? "";
}

function displayTaskValue(task: Task, field: TaskField, projects: Project[]) {
  if (field === "projectId") return projectName(task.projectId, projects);
  if (field === "parentTaskId") return task.parentTaskId ? `Task ${task.parentTaskId}` : "No parent";
  if (field.endsWith("At")) return taskValue(task, field) ? date(String(taskValue(task, field))) : "Never";
  return String(taskValue(task, field) || "Empty");
}

function compare(left: string | number, right: string | number) {
  if (typeof left === "number" && typeof right === "number") return left - right;
  return String(left).localeCompare(String(right), undefined, { numeric: true, sensitivity: "base" });
}

function sortDirectionLabel(direction: TaskViewPreferences["direction"]) {
  return direction === "desc"
    ? "Sort descending; switch to ascending"
    : "Sort ascending; switch to descending";
}

function uniqueByID<T extends { id: number }>(values: T[]) {
	const seen = new Set<number>();
	return values.filter((value) => {
		if (seen.has(value.id)) return false;
		seen.add(value.id);
		return true;
	});
}

function projectName(id: number, projects: Project[]) {
  return projects.find((project) => project.id === id)?.name ?? `Project ${id}`;
}

function workflowName(id: number, workflows: Workflow[]) {
  return workflows.find((workflow) => workflow.id === id)?.name ?? `Workflow ${id}`;
}

function compactJSON(value: unknown) {
  const text = JSON.stringify(value);
  return text.length > 100 ? `${text.slice(0, 97)}…` : text;
}

function fileName(path: string | undefined) {
  return path?.split("/").at(-1) ?? "workflow.js";
}

function phaseGroups(detail: HistoryDetail) {
  const groups: Array<[string, HistoryDetail["events"]]> = [];
  for (const event of detail.events) {
    const phase = event.phase || "Run";
    const group = groups.at(-1);
    if (group?.[0] === phase) group[1].push(event);
    else groups.push([phase, [event]]);
  }
  return groups;
}

function workflowEventTitle(event: HistoryDetail["events"][number]) {
  if (event.type === "phase.started") return `Entered ${event.phase || "phase"}`;
  if (event.type === "log") return "Workflow log";
  if (event.type === "diagnostic") return "Runtime diagnostic";
  if (event.type.startsWith("runtime.")) return event.type.replace(".", " ");
  const name = event.agentId || event.kind || "Step";
  return `${name} ${event.type.replace("step.", "")}`;
}

function workflowEventMeta(event: HistoryDetail["events"][number]) {
  return [
    event.workflow,
    event.kind,
    event.stepId ? `step ${event.stepId}` : "",
    event.tokens != null ? `${event.tokens} tokens` : "",
    event.concurrency ? `concurrency ${event.concurrency}` : "",
    event.type === "runtime.started" ? `budget ${event.budget ?? "unlimited"}` : "",
  ].filter(Boolean).join(" · ");
}

function formatResult(value: unknown) {
  return typeof value === "string" ? value : `\`\`\`json\n${JSON.stringify(value, null, 2)}\n\`\`\``;
}

function date(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    month: "short", day: "numeric", year: "numeric", hour: "numeric", minute: "2-digit",
  }).format(new Date(value));
}

function slug(value: string) {
  return value.replaceAll(" ", "-");
}

function errorMessage(value: unknown) {
  return value instanceof Error ? value.message : String(value || "Something went wrong.");
}
