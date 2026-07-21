import { A, useNavigate, useParams } from "@solidjs/router";
import { createResource, For, Show } from "solid-js";
import { get, mutation, optional, post, put, remove } from "./api";
import { TaskRow, liveTaskRows } from "./tasks";
import type { Project, ProjectDetail } from "./types";
import { date, Empty, FormFooter, Load, Meta, PageHeader, SectionTitle } from "./ui";

export function Projects() {
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
export function ProjectNew() {
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

export function ProjectView() {
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
