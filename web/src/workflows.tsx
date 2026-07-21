import { A, useNavigate, useParams } from "@solidjs/router";
import {
  createEffect,
  createMemo,
  createResource,
  createSignal,
  For,
  onCleanup,
  onMount,
  Show,
} from "solid-js";
import { ListTodo, Play } from "lucide-solid";
import { get, liveRefetch, mutation, post, remove } from "./api";
import { bindNewestFollower } from "./follow-newest";
import type { Comment, Workflow, WorkflowDetail } from "./types";
import { Empty, Load, PageHeader } from "./ui";
import { workflowConversationBlocks, type WorkflowActivityBlock } from "./workflow-activity";
import { ActivityDisclosure, ActivityNarrative, toggledSet } from "./workflow-activity-view";
import { workflowConversationWorking } from "./workflow-conversation";
import { sortWorkflowsByUsage } from "./workflow-helpers";
import { highlightWorkflowSource } from "./workflow-source";

export function Workflows() {
  const [data, { refetch }] = createResource(() => get<{ workflows: Workflow[] }>("/api/workflows"));
  const workflows = createMemo(() => sortWorkflowsByUsage(data()?.workflows ?? []));
  liveRefetch(["workflow.run.started"], refetch);
  return (
    <div class="page">
      <PageHeader title="Workflows" description="Discovered by the workflow CLI. Factory-authored files live outside git."
        actions={<A class="button primary" href="/workflows/new">New workflow</A>} />
      <Load data={data} error={() => data.error}>
        {(value) => <Show when={value.workflows.length} fallback={<Empty>No workflows discovered.</Empty>}>
          <div class="resource-list"><For each={workflows()}>{(workflow) => <A class="resource-row" href={`/workflows/${workflow.id}`}>
            <div class="resource-copy">
              <h2>{workflow.name}</h2>
              <p>{workflow.description || "No description"}</p>
            </div>
            <div class="resource-details">
              <span class="id">#{workflow.id} · {workflow.scope || "factory"}</span>
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
            </div>
          </A>}</For></div>
        </Show>}
      </Load>
    </div>
  );
}

export function WorkflowNew() {
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

export function WorkflowView() {
  const params = useParams();
  const navigate = useNavigate();
  const [data, { refetch }] = createResource(() => get<WorkflowDetail>(`/api/workflows/${params.workflow}`));
  const action = mutation();
  const [conversationViewport, setConversationViewport] = createSignal<HTMLDivElement>();
  const [conversationContent, setConversationContent] = createSignal<HTMLDivElement>();
  const [expandedGroups, setExpandedGroups] = createSignal(new Set<string>());
  const [expandedEntries, setExpandedEntries] = createSignal(new Set<string>());
  const toggleGroup = (id: string) => setExpandedGroups((current) => toggledSet(current, id));
  const toggleEntry = (id: string) => setExpandedEntries((current) => toggledSet(current, id));
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
          const blocks = createMemo(() => workflowConversationBlocks(current().comments));
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
                    <For each={blocks()}>{(block) => <Show when={block.kind === "activity"}
                      fallback={<ActivityNarrative
                        block={block as Extract<WorkflowActivityBlock, { kind: "narrative" }>}
                        entryExpanded={(id) => expandedEntries().has(id)} onEntryToggle={toggleEntry} />}>
                      <ActivityDisclosure block={block as Extract<WorkflowActivityBlock, { kind: "activity" }>}
                        expanded={expandedGroups().has(block.id)} entryExpanded={(id) => expandedEntries().has(id)}
                        onToggle={() => toggleGroup(block.id)} onEntryToggle={toggleEntry} />
                    </Show>}</For>
                    <Show when={working()}><article class="workflow-narrative agent working">
                      <p>Working on the workflow<span aria-hidden="true" /></p>
                    </article></Show>
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

function fileName(path: string | undefined) {
  return path?.split("/").at(-1) ?? "workflow.js";
}
