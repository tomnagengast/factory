import { A, useNavigate, useParams, useSearchParams } from "@solidjs/router";
import {
  createEffect,
  createMemo,
  createResource,
  createSignal,
  For,
  onCleanup,
  Show,
} from "solid-js";
import { ArrowDownWideNarrow, ArrowUpNarrowWide } from "lucide-solid";
import { get, liveRefetch, mutation, optional, optionalID, post, put, remove } from "./api";
import { ArtifactPanel, CommentThread, MediaTextarea, ReactionBar } from "./comments";
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
import {
  taskStatuses,
  type Event,
  type Project,
  type SettingsDetail,
  type Task,
  type TaskDetail,
  type TaskListResponse,
  type TaskStatus,
  type TaskSummary,
  type TaskWorkflowRun,
} from "./types";
import {
  date,
  Empty,
  FilterFieldActions,
  FormFooter,
  Load,
  Markdown,
  Meta,
  PageHeader,
  SectionTitle,
} from "./ui";
import { formatWorkflowRunDuration } from "./workflow-run-duration";

export function Tasks() {
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

export function TaskRow(props: { task: TaskSummary; projects: Project[] }) {
  return (
    <A href={`/tasks/${props.task.id}`} class="task-row">
      <span class="task-row-status" role="group" aria-label={`Status: ${props.task.status}`}
        title={`Status: ${props.task.status}`}>
        <TaskStatusIcon status={props.task.status} />
      </span>
      <span class="task-title">
        <strong>{props.task.title}</strong>
        <small>
          <span>{projectName(props.task.projectId, props.projects)}</span>
          <span class="task-id">#{props.task.id}</span>
        </small>
      </span>
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

export function TaskNew() {
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

export function TaskView() {
  const params = useParams();
  const navigate = useNavigate();
  const [data, { refetch }] = createResource(() => get<TaskDetail>(`/api/tasks/${params.task}`));
  const [settings, { refetch: refetchSettings }] = createResource(() => get<SettingsDetail>("/api/settings"));
  liveTaskRows(() => data()?.checkpointEventId, refetch);
  liveRefetch(["settings.updated"], refetchSettings);
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
      <Load data={settings} error={() => settings.error}>
        {(settingsValue) => <Load data={data} error={() => data.error}>
          {(value) => {
            const current = () => data() ?? value;
            const currentSettings = () => settings() ?? settingsValue;
            return (
          <>
            <PageHeader eyebrow={`Task ${current().task.id}`} title={<Markdown content={current().task.title} inline />}
              actions={<Show when={!editing()}>
                <button class="button" onClick={() => setEditing(true)}>Edit task</button>
              </Show>} />
            <TaskProperties task={current().task} projects={options()?.projects ?? []} tasks={options()?.tasks ?? []} />
            <Show when={!current().task.deletedAt}>
              <ReactionBar targetKind="task" targetID={current().task.id}
                reactions={current().task.reactions}
                configuredEmojis={currentSettings().settings.reactionEmojis} onChange={refetch} />
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
                <CommentThread comments={current().comments} taskID={current().task.id}
                  configuredEmojis={currentSettings().settings.reactionEmojis} onChange={refetch} />
              </section>
              <ArtifactPanel artifacts={current().artifacts} relationType="task" relationID={current().task.id} onChange={refetch} />
            </div>
          </>
          );
          }}
        </Load>}
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

export function liveTaskRows(checkpoint: () => number | undefined, refetch: () => unknown) {
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

function projectName(id: number, projects: Project[]) {
  return projects.find((project) => project.id === id)?.name ?? `Project ${id}`;
}

function slug(value: string) {
  return value.replaceAll(" ", "-");
}
