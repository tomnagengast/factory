import { A, useNavigate, useParams } from "@solidjs/router";
import { createMemo, createResource, createSignal, For, Show } from "solid-js";
import { get, mutation, optional, post, put, remove } from "./api";
import { filterTriggers, triggerEventTypeOptions, triggerWorkflowOptions } from "./trigger-helpers";
import type { Trigger, Workflow } from "./types";
import { Empty, FormFooter, Load, Meta, PageHeader } from "./ui";

export function Triggers() {
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

export function TriggerNew() {
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

export function TriggerView() {
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

function workflowName(id: number, workflows: Workflow[]) {
  return workflows.find((workflow) => workflow.id === id)?.name ?? `Workflow ${id}`;
}
