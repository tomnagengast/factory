import type { Event } from "./types";

export type EventPagingState = {
  hasOlder: boolean;
  pending: boolean;
  error: boolean;
  sentinelBounds?: Pick<DOMRectReadOnly, "top" | "bottom">;
  viewportHeight: number;
};

export function eventTypeOptions(events: readonly Event[]) {
  return [...new Set(events.map((event) => event.type))]
    .sort((left, right) => left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" }));
}

export function filterEvents(events: readonly Event[], selectedEventTypes: readonly string[]) {
  if (selectedEventTypes.length === 0) return [...events];
  const selected = new Set(selectedEventTypes);
  return events.filter((event) => selected.has(event.type));
}

export function shouldContinueEventPaging(state: EventPagingState) {
  const bounds = state.sentinelBounds;
  return state.hasOlder
    && !state.pending
    && !state.error
    && bounds !== undefined
    && bounds.bottom > 0
    && bounds.top < state.viewportHeight;
}
