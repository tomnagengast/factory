import type { Workflow } from "./types";

export function sortWorkflowsByUsage(workflows: readonly Workflow[]) {
  return [...workflows].sort((left, right) => {
    if (left.runCount !== right.runCount) return right.runCount - left.runCount;
    const nameOrder = left.name.localeCompare(right.name, undefined, {
      numeric: true,
      sensitivity: "base",
    });
    return nameOrder || left.id - right.id;
  });
}
