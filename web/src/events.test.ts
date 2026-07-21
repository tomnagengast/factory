import { describe, expect, test } from "bun:test";
import type { Event } from "./types";
import { eventTypeOptions, filterEvents, shouldContinueEventPaging } from "./events";

function event(id: number, type: string): Event {
  return { id, type, at: "2026-07-21T12:00:00Z", data: { id } };
}

const newest = [
  event(9, "task.updated"),
  event(8, "release.ready"),
  event(7, "task.created"),
  event(6, "task.updated"),
];

describe("event filter options", () => {
  test("deduplicates and naturally sorts represented event types", () => {
    expect(eventTypeOptions([
      event(1, "task.updated"),
      event(2, "Ingress.10"),
      event(3, "task.updated"),
      event(4, "ingress.2"),
      event(5, "release.ready"),
    ])).toEqual(["ingress.2", "Ingress.10", "release.ready", "task.updated"]);
  });

  test("adds types seen in older pages and live events without mutating the wire", () => {
    const raw = [...newest];
    const afterOlder = [...raw, event(5, "cron")];
    const afterLive = [event(10, "deploy.completed"), ...afterOlder];

    expect(eventTypeOptions(afterOlder)).toEqual(["cron", "release.ready", "task.created", "task.updated"]);
    expect(eventTypeOptions(afterLive)).toEqual([
      "cron", "deploy.completed", "release.ready", "task.created", "task.updated",
    ]);
    expect(raw).toEqual(newest);
  });
});

describe("filterEvents", () => {
  test("keeps every event in newest-first source order with no selection", () => {
    expect(filterEvents(newest, []).map(({ id }) => id)).toEqual([9, 8, 7, 6]);
    expect(newest.map(({ id }) => id)).toEqual([9, 8, 7, 6]);
  });

  test("uses OR semantics for one or several selected types", () => {
    expect(filterEvents(newest, ["release.ready"]).map(({ id }) => id)).toEqual([8]);
    expect(filterEvents(newest, ["task.created", "task.updated"]).map(({ id }) => id))
      .toEqual([9, 7, 6]);
  });

  test("recomputes older appends in raw order", () => {
    const withOlder = [...newest, event(5, "task.created"), event(4, "release.ready")];
    expect(filterEvents(withOlder, ["task.created"]).map(({ id }) => id)).toEqual([7, 5]);
    expect(withOlder.map(({ id }) => id)).toEqual([9, 8, 7, 6, 5, 4]);
  });

  test("includes matching live prepends and excludes unselected new types", () => {
    const matching = [event(11, "task.updated"), ...newest];
    const unselected = [event(12, "deploy.completed"), ...matching];

    expect(filterEvents(matching, ["task.updated"]).map(({ id }) => id)).toEqual([11, 9, 6]);
    expect(filterEvents(unselected, ["task.updated"]).map(({ id }) => id)).toEqual([11, 9, 6]);
    expect(eventTypeOptions(unselected)).toContain("deploy.completed");
    expect(unselected.map(({ id }) => id)).toEqual([12, 11, 9, 8, 7, 6]);
  });
});

describe("event paging continuation", () => {
  const visibleSentinel = { top: 300, bottom: 356 };
  const eligible = {
    hasOlder: true,
    pending: false,
    error: false,
    sentinelBounds: visibleSentinel,
    viewportHeight: 700,
  };

  test("continues across consecutive raw pages with no selected matches", () => {
    const selected = ["release.ready"];
    const pageOne = [event(5, "task.updated"), event(4, "task.created")];
    const pageTwo = [event(3, "workflow.run.started"), event(2, "task.deleted")];
    const pageThree = [event(1, "release.ready")];
    let raw = [...newest.filter((item) => item.type !== "release.ready")];

    raw = [...raw, ...pageOne];
    expect(filterEvents(raw, selected)).toEqual([]);
    expect(shouldContinueEventPaging(eligible)).toBe(true);

    raw = [...raw, ...pageTwo];
    expect(filterEvents(raw, selected)).toEqual([]);
    expect(shouldContinueEventPaging(eligible)).toBe(true);

    raw = [...raw, ...pageThree];
    expect(filterEvents(raw, selected).map(({ id }) => id)).toEqual([1]);
  });

  test("stops outside the viewport, at history end, while pending, or after an error", () => {
    expect(shouldContinueEventPaging({ ...eligible, sentinelBounds: { top: 700, bottom: 756 } })).toBe(false);
    expect(shouldContinueEventPaging({ ...eligible, sentinelBounds: { top: -56, bottom: 0 } })).toBe(false);
    expect(shouldContinueEventPaging({ ...eligible, hasOlder: false })).toBe(false);
    expect(shouldContinueEventPaging({ ...eligible, pending: true })).toBe(false);
    expect(shouldContinueEventPaging({ ...eligible, error: true })).toBe(false);
    expect(shouldContinueEventPaging({ ...eligible, sentinelBounds: undefined })).toBe(false);
  });
});
