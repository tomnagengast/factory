import { describe, expect, test } from "bun:test";
import type { Trigger, Workflow } from "./types";
import { filterTriggers, triggerEventTypeOptions, triggerWorkflowOptions } from "./trigger-helpers";

function trigger(id: number, eventType: string, workflowId: number, enabled = true): Trigger {
  return {
    id,
    eventType,
    workflowId,
    enabled,
    createdAt: "2026-07-20T12:00:00Z",
    updatedAt: "2026-07-20T12:00:00Z",
  };
}

function workflow(id: number, name: string): Workflow {
  return {
    id,
    name,
    runCount: 0,
    taskCount: 0,
    phases: [],
    mutating: false,
    createdAt: "2026-07-20T12:00:00Z",
    updatedAt: "2026-07-20T12:00:00Z",
  };
}

const triggers = [
  trigger(9, "task.updated", 24),
  trigger(7, "task.created", 24, false),
  trigger(4, "release.ready", 12),
  trigger(2, "cron", 30),
];

describe("filterTriggers", () => {
  test("keeps every trigger in its original order with no selections", () => {
    expect(filterTriggers(triggers, { eventTypes: [], workflowIds: [] }).map(({ id }) => id))
      .toEqual([9, 7, 4, 2]);
    expect(triggers.map(({ id }) => id)).toEqual([9, 7, 4, 2]);
  });

  test("uses OR for one or several selected event types", () => {
    expect(filterTriggers(triggers, { eventTypes: ["cron"], workflowIds: [] }).map(({ id }) => id))
      .toEqual([2]);
    expect(filterTriggers(triggers, {
      eventTypes: ["task.created", "task.updated"],
      workflowIds: [],
    }).map(({ id }) => id)).toEqual([9, 7]);
  });

  test("uses OR for one or several selected workflows", () => {
    expect(filterTriggers(triggers, { eventTypes: [], workflowIds: [12] }).map(({ id }) => id))
      .toEqual([4]);
    expect(filterTriggers(triggers, { eventTypes: [], workflowIds: [24, 30] }).map(({ id }) => id))
      .toEqual([9, 7, 2]);
  });

  test("uses AND across event and workflow dimensions", () => {
    expect(filterTriggers(triggers, {
      eventTypes: ["task.created", "release.ready"],
      workflowIds: [24],
    }).map(({ id }) => id)).toEqual([7]);
  });

  test("returns no rows when nothing matches", () => {
    expect(filterTriggers(triggers, { eventTypes: ["task.deleted"], workflowIds: [] })).toEqual([]);
  });

  test("keeps disabled triggers eligible", () => {
    expect(filterTriggers(triggers, { eventTypes: ["task.created"], workflowIds: [] }))
      .toEqual([triggers[1]]);
  });

  test("clearing selections restores the complete original order", () => {
    const filtered = filterTriggers(triggers, { eventTypes: ["cron"], workflowIds: [30] });
    expect(filtered.map(({ id }) => id)).toEqual([2]);
    expect(filterTriggers(triggers, { eventTypes: [], workflowIds: [] }).map(({ id }) => id))
      .toEqual([9, 7, 4, 2]);
  });
});

describe("trigger filter options", () => {
  test("deduplicates and sorts represented event types", () => {
    expect(triggerEventTypeOptions([
      trigger(1, "task.updated", 24),
      trigger(2, "Cron", 12),
      trigger(3, "task.updated", 30),
      trigger(4, "release.ready", 24),
    ])).toEqual(["Cron", "release.ready", "task.updated"]);
  });

  test("deduplicates represented workflows and sorts by label then ID", () => {
    expect(triggerWorkflowOptions([
      trigger(1, "task.updated", 24),
      trigger(2, "task.created", 12),
      trigger(3, "cron", 24),
      trigger(4, "release.ready", 30),
    ], [
      workflow(30, "Review 10"),
      workflow(24, "review 2"),
      workflow(12, "Alpha"),
    ])).toEqual([
      { id: 12, label: "Alpha (#12)" },
      { id: 24, label: "review 2 (#24)" },
      { id: 30, label: "Review 10 (#30)" },
    ]);
  });

  test("keeps duplicate workflow names distinct by ID", () => {
    expect(triggerWorkflowOptions([
      trigger(1, "task.updated", 24),
      trigger(2, "task.created", 12),
    ], [workflow(24, "Review"), workflow(12, "Review")])).toEqual([
      { id: 12, label: "Review (#12)" },
      { id: 24, label: "Review (#24)" },
    ]);
  });

  test("keeps referenced missing workflows as fallback options", () => {
    expect(triggerWorkflowOptions([
      trigger(1, "task.updated", 42),
      trigger(2, "task.created", 24),
    ], [workflow(24, "Review")])).toEqual([
      { id: 24, label: "Review (#24)" },
      { id: 42, label: "Workflow 42" },
    ]);
  });
});
