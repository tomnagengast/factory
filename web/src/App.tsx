import { A, Route, Router, useNavigate, useParams } from "@solidjs/router";
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
import { get, optional, optionalID, post, put, remove } from "./api";
import {
  taskStatuses,
  type Artifact,
  type Comment,
  type CommentDetail,
  type Event,
  type Health,
  type HistoryDetail,
  type Project,
  type ProjectDetail,
  type SettingsDetail,
  type Task,
  type TaskDetail,
  type TaskStatus,
  type Trigger,
  type Workflow,
  type WorkflowDetail,
  type WorkflowRun,
} from "./types";

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
            <small>one wire / one loop</small>
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
          Agent loop connected
        </div>
      </aside>
      <main>{props.children}</main>
    </div>
  );
}

function PageHeader(props: { eyebrow?: string; title: string; description?: string; actions?: JSX.Element }) {
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

function Home() {
  const [data] = createResource(async () => {
    const [health, projects, tasks, events] = await Promise.all([
      get<Health>("/api/health"),
      get<{ projects: Project[] }>("/api/projects"),
      get<{ tasks: Task[] }>("/api/tasks"),
      get<{ events: Event[] }>("/api/events"),
    ]);
    return {
      health,
      projects: projects.projects.slice(0, 4),
      tasks: tasks.tasks.slice(0, 5),
      events: events.events.slice(-6).reverse(),
    };
  });
  return (
    <div class="page">
      <PageHeader
        eyebrow="Trusted environment demonstrator"
        title="Factory overview"
        description="Projects and tasks enter one observable wire. The selected harness handles workflow authoring and triggered runs sequentially."
      />
      <Load data={data} error={() => data.error}>
        {(value) => (
          <>
            <section class="metrics" aria-label="Factory totals">
              <Metric label="Projects" value={value.health.projects} href="/projects" />
              <Metric label="Tasks" value={value.health.tasks} href="/tasks" />
              <Metric label="Events" value={value.health.events} href="/events" />
              <Metric label="Workflows" value={value.health.workflows} href="/workflows" />
            </section>
            <div class="split">
              <section>
                <SectionTitle title="Recent tasks" href="/tasks" />
                <Show when={value.tasks.length} fallback={<Empty>No tasks yet.</Empty>}>
                  <div class="rows">
                    <For each={value.tasks}>{(task) => <TaskRow task={task} projects={value.projects} />}</For>
                  </div>
                </Show>
              </section>
              <section>
                <SectionTitle title="Latest on the wire" href="/events" />
                <Show when={value.events.length} fallback={<Empty>The wire is quiet.</Empty>}>
                  <div class="wire-list">
                    <For each={value.events}>{(event) => <EventRow event={event} />}</For>
                  </div>
                </Show>
              </section>
            </div>
          </>
        )}
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
        description="This selection applies to new workflow conversations and triggered workflow runs."
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
      props.onSave({ harness: harness(), model: model(), reasoning: reasoning() });
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

const taskFields = [
  ["id", "ID"], ["createdAt", "Created at"], ["updatedAt", "Updated at"],
  ["deletedAt", "Deleted at"], ["title", "Title"], ["description", "Description"],
  ["parentTaskId", "Parent task"], ["status", "Status"], ["projectId", "Project"],
] as const;

function Tasks() {
  const [data] = createResource(async () => {
    const [tasks, projects] = await Promise.all([
      get<{ tasks: Task[] }>("/api/tasks"), get<{ projects: Project[] }>("/api/projects"),
    ]);
    return { tasks: tasks.tasks, projects: projects.projects };
  });
  const [sortField, setSortField] = createSignal("id");
  const [direction, setDirection] = createSignal<"asc" | "desc">("desc");
  const [groupField, setGroupField] = createSignal("");
  const groups = createMemo(() => {
    const value = data();
    if (!value) return [] as Array<[string, Task[]]>;
    const sorted = [...value.tasks].sort((left, right) => {
      const result = compare(taskValue(left, sortField()), taskValue(right, sortField()));
      return direction() === "desc" ? -result : result;
    });
    if (!groupField()) return [["", sorted]] as Array<[string, Task[]]>;
    const grouped = new Map<string, Task[]>();
    for (const task of sorted) {
      const key = displayTaskValue(task, groupField(), value.projects);
      grouped.set(key, [...(grouped.get(key) ?? []), task]);
    }
    return [...grouped.entries()];
  });
  return (
    <div class="page">
      <PageHeader
        title="Tasks"
        description="Ungrouped and newest first by default. Change either control without changing the underlying wire."
        actions={<A class="button primary" href="/tasks/new">New task</A>}
      />
      <div class="controls">
        <label>Sort by<select value={sortField()} onChange={(event) => setSortField(event.currentTarget.value)}>
          <For each={taskFields}>{(field) => <option value={field[0]}>{field[1]}</option>}</For>
        </select></label>
        <label>Direction<select value={direction()} onChange={(event) => setDirection(event.currentTarget.value as "asc" | "desc")}>
          <option value="desc">Descending</option><option value="asc">Ascending</option>
        </select></label>
        <label>Group by<select value={groupField()} onChange={(event) => setGroupField(event.currentTarget.value)}>
          <option value="">No grouping</option>
          <For each={taskFields}>{(field) => <option value={field[0]}>{field[1]}</option>}</For>
        </select></label>
      </div>
      <Load data={data} error={() => data.error}>
        {(value) => (
          <Show when={value.tasks.length} fallback={<Empty>No tasks yet.</Empty>}>
            <For each={groups()}>
              {([label, tasks]) => (
                <section class="task-group">
                  <Show when={label}><h2>{label}<span>{tasks.length}</span></h2></Show>
                  <div class="rows"><For each={tasks}>{(task) => <TaskRow task={task} projects={value.projects} />}</For></div>
                </section>
              )}
            </For>
          </Show>
        )}
      </Load>
    </div>
  );
}

function TaskRow(props: { task: Task; projects: Project[] }) {
  return (
    <A href={`/tasks/${props.task.id}`} class="task-row">
      <span class={`status ${slug(props.task.status)}`}>{props.task.status}</span>
      <span class="task-title"><strong>{props.task.title}</strong><small>{projectName(props.task.projectId, props.projects)}</small></span>
      <span class="id">#{props.task.id}</span>
      <time>{date(props.task.updatedAt)}</time>
    </A>
  );
}

function TaskNew() {
  const navigate = useNavigate();
  const [options] = createResource(async () => {
    const [projects, tasks] = await Promise.all([
      get<{ projects: Project[] }>("/api/projects"), get<{ tasks: Task[] }>("/api/tasks"),
    ]);
    return { projects: projects.projects, tasks: tasks.tasks };
  });
  const action = mutation();
  return (
    <div class="page narrow">
      <PageHeader eyebrow="Tasks" title="Create a task" />
      <Load data={options} error={() => options.error}>
        {(value) => <TaskForm projects={value.projects} tasks={value.tasks} pending={action.pending()} error={action.error()}
          onSave={(body) => action.run(async () => {
            const created = await post<Task>("/api/tasks", body);
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
  const [options] = createResource(async () => {
    const [projects, tasks] = await Promise.all([
      get<{ projects: Project[] }>("/api/projects"), get<{ tasks: Task[] }>("/api/tasks"),
    ]);
    return { projects: projects.projects, tasks: tasks.tasks };
  });
  const action = mutation();
  return (
    <div class="page">
      <Load data={data} error={() => data.error}>
        {(value) => (
          <>
            <PageHeader eyebrow={`Task ${value.task.id}`} title={value.task.title} />
            <div class="detail-grid">
              <Show when={options()}>
                {(available) => <TaskForm task={value.task} projects={available().projects} tasks={available().tasks}
                  pending={action.pending()} error={action.error()} onSave={(body) => action.run(async () => {
                    await put<Task>(`/api/tasks/${value.task.id}`, body);
                    await refetch();
                  })} />}
              </Show>
              <aside class="side-detail">
                <Meta value={value.task} />
                <button class="button danger" onClick={() => action.run(async () => {
                  await remove(`/api/tasks/${value.task.id}`);
                  navigate("/tasks");
                })}>Delete task</button>
              </aside>
            </div>
            <div class="split lower">
              <section>
                <SectionTitle title="Comments" />
                <CommentThread comments={value.comments} taskID={value.task.id} onChange={refetch} />
              </section>
              <ArtifactPanel artifacts={value.artifacts} relationType="task" relationID={value.task.id} onChange={refetch} />
            </div>
          </>
        )}
      </Load>
    </div>
  );
}

function TaskForm(props: {
  task?: Task;
  projects: Project[];
  tasks: Task[];
  pending: boolean;
  error?: string;
  onSave: (body: unknown) => void;
}) {
  return (
    <form class="form-panel" onSubmit={(event) => {
      event.preventDefault();
      const data = new FormData(event.currentTarget);
      props.onSave({
        title: String(data.get("title") ?? "").trim(),
        description: optional(data.get("description")),
        parentTaskId: optionalID(data.get("parentTaskId")),
        status: data.get("status") as TaskStatus,
        projectId: Number(data.get("projectId")),
      });
    }}>
      <label>Title<input name="title" required value={props.task?.title ?? ""} /></label>
      <label>Description<textarea name="description" rows="5">{props.task?.description ?? ""}</textarea></label>
      <div class="field-pair">
        <label>Status<select name="status" value={props.task?.status ?? "backlog"}>
          <For each={taskStatuses}>{(status) => <option value={status}>{status}</option>}</For>
        </select></label>
        <label>Project<select name="projectId" required value={props.task?.projectId ?? ""}>
          <option value="" disabled>Select a project</option>
          <For each={props.projects}>{(project) => <option value={project.id}>{project.name}</option>}</For>
        </select></label>
      </div>
      <label>Parent task<select name="parentTaskId" value={props.task?.parentTaskId ?? ""}>
        <option value="">No parent</option>
        <For each={props.tasks.filter((task) => task.id !== props.task?.id)}>
          {(task) => <option value={task.id}>#{task.id} {task.title}</option>}
        </For>
      </select></label>
      <FormFooter pending={props.pending} error={props.error} label={props.task ? "Save task" : "Create task"} />
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
          </header>
          <p>{comment.content}</p>
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
  return (
    <form classList={{ "comment-form": true, compact: props.compact }} onSubmit={(event) => {
      event.preventDefault();
      const form = event.currentTarget;
      const data = new FormData(form);
      action.run(async () => {
        await post<Comment>(`/api/tasks/${props.taskID}/comments`, {
          content: String(data.get("content") ?? "").trim(),
          parentCommentId: props.parentCommentID,
        });
        form.reset();
        props.onChange();
      });
    }}>
      <textarea name="content" required rows={props.compact ? 1 : 3} placeholder={props.compact ? "Reply…" : "Add a comment…"} />
      <button class="button quiet" disabled={action.pending()}>{props.compact ? "Reply" : "Comment"}</button>
      <Show when={action.error()}><span class="form-error">{action.error()}</span></Show>
    </form>
  );
}

function CommentView() {
  const params = useParams();
  const [data, { refetch }] = createResource(() => get<CommentDetail>(`/api/comments/${params.comment}`));
  return (
    <div class="page narrow">
      <Load data={data} error={() => data.error}>
        {(value) => (
          <>
            <PageHeader eyebrow={`Task ${params.task}`} title={`Comment ${value.comment.id}`}
              actions={<A class="button" href={`/tasks/${params.task}`}>Back to task</A>} />
            <article class="comment featured">
              <header><strong>{value.comment.author}</strong><time>{date(value.comment.createdAt)}</time></header>
              <p>{value.comment.content}</p>
            </article>
            <Show when={value.replies.length}>
              <section><SectionTitle title="Direct replies" />
                <div class="comments"><For each={value.replies}>{(reply) => <article class="comment"><header>
                  <strong>{reply.author}</strong><A href={`/tasks/${params.task}/comments/${reply.id}`}>#{reply.id}</A>
                  <time>{date(reply.createdAt)}</time></header><p>{reply.content}</p></article>}</For></div>
              </section>
            </Show>
            <ArtifactPanel artifacts={value.artifacts} relationType="comment" relationID={value.comment.id} onChange={refetch} />
          </>
        )}
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
  const [error, setError] = createSignal("");
  const [connected, setConnected] = createSignal(false);
  let source: EventSource | undefined;
  onMount(async () => {
    try {
      const initial = await get<{ events: Event[] }>("/api/events");
      setEvents(initial.events);
      const after = initial.events.at(-1)?.id ?? 0;
      source = new EventSource(`/api/events/stream?after=${after}`);
      source.onopen = () => setConnected(true);
      source.onerror = () => setConnected(false);
      source.onmessage = (message) => {
        const event = JSON.parse(message.data) as Event;
        setEvents((current) => [...current.filter((item) => item.id !== event.id), event]);
      };
    } catch (caught) {
      setError(errorMessage(caught));
    }
  });
  onCleanup(() => source?.close());
  return (
    <div class="page">
      <PageHeader title="Event wire" description="Every accepted fact, newest first."
        actions={<span classList={{ connection: true, live: connected() }}><span />{connected() ? "Live" : "Connecting"}</span>} />
      <Show when={!error()} fallback={<div class="state">{error()}</div>}>
        <Show when={events().length} fallback={<Empty>The wire is quiet.</Empty>}>
          <div class="wire-table">
            <For each={[...events()].reverse()}>{(event) => <EventRow event={event} expanded />}</For>
          </div>
        </Show>
      </Show>
    </div>
  );
}

function EventRow(props: { event: Event; expanded?: boolean }) {
  return (
    <A href={`/events/${props.event.id}`} classList={{ "event-row": true, expanded: props.expanded }}>
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
  const [data, { refetch }] = createResource(() => get<{ history: WorkflowRun[] }>("/api/history"));
  liveRefetch(["workflow.run.started", "workflow.run.event", "workflow.run.completed", "workflow.run.failed"], refetch);
  return (
    <div class="page">
      <PageHeader title="Workflow history"
        description="Live and completed workflow runs, newest first." />
      <Load data={data} error={() => data.error}>
        {(value) => <Show when={value.history.length} fallback={<Empty>No workflows have run yet.</Empty>}>
          <div class="rows">
            <For each={value.history}>{(run) => <A class="history-row" href={`/history/${run.id}`}>
              <span class={`run-status ${run.status}`}>{run.status}</span>
              <span class="run-title"><strong>{run.workflowName || `Workflow ${run.workflowId}`}</strong>
                <small>Event #{run.sourceEventId} · Trigger #{run.triggerId}</small></span>
              <span class="id">#{run.id}</span>
              <time>{date(run.createdAt)}</time>
            </A>}</For>
          </div>
        </Show>}
      </Load>
    </div>
  );
}

function HistoryView() {
  const params = useParams();
  const [data, { refetch }] = createResource(() => get<HistoryDetail>(`/api/history/${params.item}`));
  liveRefetch(["workflow.run.event", "workflow.run.completed", "workflow.run.failed"], refetch);
  return (
    <div class="page">
      <Load data={data} error={() => data.error}>
        {(value) => {
          const current = () => data() ?? value;
          return <>
            <PageHeader eyebrow={`Run ${value.run.id}`}
              title={value.run.workflowName || `Workflow ${value.run.workflowId}`}
              description={`Started from event ${value.run.sourceEventId}`}
              actions={<A class="button" href="/history">Back to history</A>} />
            <div class="run-summary">
              <span class={`run-status ${current().run.status}`}>{current().run.status}</span>
              <A href={`/workflows/${value.run.workflowId}`}>Workflow #{value.run.workflowId}</A>
              <A href={`/events/${value.run.sourceEventId}`}>Event #{value.run.sourceEventId}</A>
              <span>Trigger #{value.run.triggerId}</span>
              <time>Started {date(value.run.createdAt)}</time>
              <time>Updated {date(current().run.updatedAt)}</time>
            </div>
            <Show when={phaseGroups(current()).length} fallback={<Empty>
              {current().run.status === "running" ? "Waiting for the first workflow event…" : "No workflow events were recorded for this run."}
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
                      <Show when={event.message}><pre>{event.message}</pre></Show>
                      <Show when={event.error}><pre class="step-error">{event.error}</pre></Show>
                      <Show when={event.result != null}><pre>{formatResult(event.result)}</pre></Show>
                    </article>}</For>
                  </div>
                </section>}</For>
              </div>
            </Show>
            <Show when={current().run.output || current().run.error}>
              <section class="run-output"><SectionTitle title={current().run.error ? "Run error" : "Final result"} />
                <pre classList={{ "step-error": Boolean(current().run.error) }}>
                  {current().run.error || current().run.output}
                </pre>
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
  return (
    <div class="page">
      <PageHeader title="Triggers" description="Match an event on the wire or a cron tick, then enqueue one workflow."
        actions={<A class="button primary" href="/triggers/new">New trigger</A>} />
      <Load data={data} error={() => data.error}>
        {(value) => <Show when={value.triggers.length} fallback={<Empty>No triggers configured.</Empty>}>
          <div class="rows"><For each={value.triggers}>{(trigger) => <A class="trigger-row" href={`/triggers/${trigger.id}`}>
            <span class="event-chip">{trigger.eventType}</span>
            <strong>{workflowName(trigger.workflowId, value.workflows)}</strong>
            <span>{trigger.schedule || "On event"}</span><span class="id">#{trigger.id}</span>
          </A>}</For></div>
        </Show>}
      </Load>
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
      <FormFooter pending={props.pending} error={props.error} label={props.trigger ? "Save trigger" : "Create trigger"} />
    </form>
  );
}

function Workflows() {
  const [data] = createResource(() => get<{ workflows: Workflow[] }>("/api/workflows"));
  return (
    <div class="page">
      <PageHeader title="Workflows" description="Discovered by the workflow CLI. Factory-authored files live outside git."
        actions={<A class="button primary" href="/workflows/new">New workflow</A>} />
      <Load data={data} error={() => data.error}>
        {(value) => <Show when={value.workflows.length} fallback={<Empty>No workflows discovered.</Empty>}>
          <div class="card-grid workflows"><For each={value.workflows}>{(workflow) => <A class="project-card" href={`/workflows/${workflow.id}`}>
            <span class="id">#{workflow.id} · {workflow.scope || "factory"}</span>
            <h2>{workflow.name}</h2><p>{workflow.description || "No description"}</p>
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
  liveRefetch(["comment.created", "workflow.updated", "workflow.authoring.completed", "workflow.authoring.failed"], refetch);
  let sourcePolling: number | undefined;
  onMount(() => {
    sourcePolling = window.setInterval(() => {
      if (data()?.comments.at(-1)?.author === "user") void refetch();
    }, 1000);
  });
  onCleanup(() => window.clearInterval(sourcePolling));
  return (
    <div class="page chat-page">
      <Load data={data} error={() => data.error}>
        {(value) => {
          const current = () => data() ?? value;
          const working = () => current().comments.at(-1)?.author === "user";
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
                <div class="conversation" role="log" aria-live="polite">
                  <For each={current().comments}>{(comment) => <article classList={{ message: true, agent: comment.author === "agent" }}>
                    <header><strong>{comment.author === "agent" ? "Agent" : "You"}</strong><time>{date(comment.createdAt)}</time></header>
                    <p>{comment.content}</p>
                  </article>}</For>
                  <Show when={working()}><article class="message agent working"><header><strong>Agent</strong></header><p>Working on the workflow…</p></article></Show>
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
                <pre tabIndex={0}><code>{current().source || "// Waiting for the agent to write the workflow file."}</code></pre>
              </section>
            </div>
          </>;
        }}
      </Load>
    </div>
  );
}

function FormFooter(props: { pending: boolean; error?: string; label: string }) {
  return (
    <footer class="form-footer">
      <Show when={props.error}><span class="form-error">{props.error}</span></Show>
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
    source = new EventSource(`/api/events/stream?after=${initial.events.at(-1)?.id ?? 0}`);
    source.onmessage = (message) => {
      const event = JSON.parse(message.data) as Event;
      if (types.includes(event.type)) refetch();
    };
  });
  onCleanup(() => source?.close());
}

function taskValue(task: Task, field: string): string | number {
  return ({
    id: task.id, createdAt: task.createdAt, updatedAt: task.updatedAt,
    deletedAt: task.deletedAt ?? "", title: task.title, description: task.description ?? "",
    parentTaskId: task.parentTaskId ?? 0, status: task.status, projectId: task.projectId,
  })[field] ?? "";
}

function displayTaskValue(task: Task, field: string, projects: Project[]) {
  if (field === "projectId") return projectName(task.projectId, projects);
  if (field === "parentTaskId") return task.parentTaskId ? `Task ${task.parentTaskId}` : "No parent";
  if (field.endsWith("At")) return taskValue(task, field) ? date(String(taskValue(task, field))) : "Never";
  return String(taskValue(task, field) || "Empty");
}

function compare(left: string | number, right: string | number) {
  if (typeof left === "number" && typeof right === "number") return left - right;
  return String(left).localeCompare(String(right), undefined, { numeric: true, sensitivity: "base" });
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
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
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
