import { describe, expect, test } from "bun:test";
import type { Workflow } from "./types";
import { sortWorkflowsByUsage } from "./workflow-helpers";

function workflow(id: number, name: string, runCount: number): Workflow {
  return {
    id,
    name,
    runCount,
    taskCount: 0,
    phases: [],
    mutating: false,
    createdAt: "2026-07-19T12:00:00Z",
    updatedAt: "2026-07-19T12:00:00Z",
  };
}

describe("sortWorkflowsByUsage", () => {
  test("sorts by runs descending, then name and ID ascending", () => {
    const workflows = [
      workflow(5, "Zero", 0),
      workflow(4, "Review 10", 3),
      workflow(3, "review 2", 3),
      workflow(2, "Alpha", 5),
      workflow(1, "Alpha", 5),
    ];

    expect(sortWorkflowsByUsage(workflows).map(({ id }) => id)).toEqual([1, 2, 3, 4, 5]);
    expect(workflows.map(({ id }) => id)).toEqual([5, 4, 3, 2, 1]);
  });
});
