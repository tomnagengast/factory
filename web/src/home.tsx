import { A } from "@solidjs/router";
import { createResource, For, Show } from "solid-js";
import { get } from "./api";
import { EventRow } from "./event-views";
import { TaskRow, liveTaskRows } from "./tasks";
import type { Event, Health, Project, TaskListResponse } from "./types";
import { Empty, Load, PageHeader, SectionTitle } from "./ui";

export function Home() {
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

function Metric(props: { label: string; value: number; href: string }) {
  return (
    <A href={props.href} class="metric">
      <span>{props.label}</span>
      <strong>{props.value}</strong>
    </A>
  );
}
