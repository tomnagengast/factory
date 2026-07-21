import { A, useParams } from "@solidjs/router";
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
import { errorMessage, get, mutation } from "./api";
import { eventTypeOptions, filterEvents, shouldContinueEventPaging, uniqueByID } from "./events";
import { bindNewestFollower, type NewestFollower } from "./follow-newest";
import type { Event } from "./types";
import { date, Empty, FilterFieldActions, Load, PageHeader } from "./ui";

const EVENT_PAGE_SIZE = 25;

export function Events() {
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

export function EventRow(props: { event: Event; expanded?: boolean }) {
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

export function EventView() {
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

function compactJSON(value: unknown) {
  const text = JSON.stringify(value);
  return text.length > 100 ? `${text.slice(0, 97)}…` : text;
}
