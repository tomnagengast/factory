import { For, Show } from "solid-js";
import type {
  WorkflowActivityBlock,
  WorkflowActivityDetail,
  WorkflowActivityEntry,
} from "./workflow-activity";
import { Markdown } from "./ui";

export function ActivityDisclosure(props: {
  block: Extract<WorkflowActivityBlock, { kind: "activity" }>;
  expanded: boolean;
  entryExpanded: (id: string) => boolean;
  onToggle: () => void;
  onEntryToggle: (id: string) => void;
}) {
  const panelID = () => `${props.block.id}-details`;
  return (
    <section classList={{ "activity-group": true, failed: props.block.failed }}>
      <button type="button" class="activity-summary" aria-expanded={props.expanded}
        aria-controls={panelID()} onClick={props.onToggle}>
        <span classList={{ "activity-chevron": true, expanded: props.expanded }} aria-hidden="true">›</span>
        <span>{props.block.summary}</span>
        <Show when={props.block.failed}><span class="activity-failure">Failure</span></Show>
      </button>
      <Show when={props.expanded}>
        <div id={panelID()} class="activity-entries">
          <For each={props.block.entries}>{(entry) =>
            <ActivityEntryDisclosure entry={entry} expanded={props.entryExpanded(entry.id)}
              onToggle={() => props.onEntryToggle(entry.id)} />}
          </For>
        </div>
      </Show>
    </section>
  );
}

function ActivityEntryDisclosure(props: {
  entry: WorkflowActivityEntry;
  expanded: boolean;
  onToggle: () => void;
}) {
  const panelID = () => `${props.entry.id}-details`;
  return (
    <div classList={{ "activity-entry": true, failed: props.entry.failed }}>
      <button type="button" class="activity-entry-summary" aria-expanded={props.expanded}
        aria-controls={panelID()} onClick={props.onToggle}>
        <span classList={{ "activity-chevron": true, expanded: props.expanded }} aria-hidden="true">›</span>
        <span class="activity-kind">{props.entry.kindLabel}</span>
        <strong>{props.entry.title}</strong>
        <time>{shortTime(props.entry.at)}</time>
      </button>
      <Show when={props.expanded}>
        <div id={panelID()} class="activity-entry-details">
          <dl class="activity-metadata">
            <For each={props.entry.metadata}>{(item) => <div><dt>{item.label}</dt><dd>{item.value}</dd></div>}</For>
          </dl>
          <For each={props.entry.details}>{(detail) => <ActivityDetail detail={detail} />}</For>
        </div>
      </Show>
    </div>
  );
}

function ActivityDetail(props: { detail: WorkflowActivityDetail }) {
  return (
    <section class="activity-detail">
      <h4>{props.detail.label}</h4>
      <Show when={props.detail.format === "markdown" && typeof props.detail.value === "string"}
        fallback={<pre classList={{ "activity-data": true, json: props.detail.format === "json" }}>{props.detail.format === "json"
          ? formatJSON(props.detail.value)
          : String(props.detail.value ?? "")}</pre>}>
        <Markdown content={String(props.detail.value)} />
      </Show>
    </section>
  );
}

export function ActivityNarrative(props: {
  block: Extract<WorkflowActivityBlock, { kind: "narrative" }>;
  entryExpanded: (id: string) => boolean;
  onEntryToggle: (id: string) => void;
}) {
  return (
    <article classList={{
      "workflow-narrative": true,
      user: props.block.role === "user",
      agent: props.block.role === "agent",
      error: props.block.error,
    }}>
      <Show when={props.block.role === "agent"} fallback={<p>{props.block.content}</p>}>
        <Markdown content={props.block.content} />
      </Show>
      <time>{shortTime(props.block.at)}</time>
      <Show when={props.block.entry}>{(entry) =>
        <div class="narrative-event-detail">
          <ActivityEntryDisclosure entry={entry()} expanded={props.entryExpanded(entry().id)}
            onToggle={() => props.onEntryToggle(entry().id)} />
        </div>}
      </Show>
    </article>
  );
}

export function toggledSet(current: Set<string>, id: string) {
  const next = new Set(current);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  return next;
}

function formatJSON(value: unknown) {
  try {
    return JSON.stringify(value, null, 2) ?? String(value ?? "");
  } catch {
    return String(value ?? "");
  }
}

function shortTime(value: string) {
  return new Intl.DateTimeFormat(undefined, { timeStyle: "short" }).format(new Date(value));
}
