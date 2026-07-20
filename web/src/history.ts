import type { WorkflowRun } from "./types";

export function historyResourceLink(run: Pick<WorkflowRun, "taskId">) {
  const taskID = run.taskId;
  if (typeof taskID !== "number" || !Number.isInteger(taskID) || taskID < 1) return undefined;
  return { href: `/tasks/${taskID}`, label: `Task #${taskID}` };
}
