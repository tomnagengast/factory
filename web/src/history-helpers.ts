import type { WorkflowRun, WorkflowRunStatus } from "./types";

export const HISTORY_OVERVIEW_LIMIT = 5;
export const HISTORY_PAGE_SIZE = 25;

export const historyStatuses = [
  { status: "running", label: "Running", href: "/history/running" },
  { status: "waiting", label: "Waiting", href: "/history/waiting" },
  { status: "retrying", label: "Retrying", href: "/history/retrying" },
  { status: "failed", label: "Failed", href: "/history/failed" },
  { status: "completed", label: "Completed", href: "/history/completed" },
] as const satisfies ReadonlyArray<{
  status: WorkflowRunStatus;
  label: string;
  href: string;
}>;

export function historyPageURL(status: WorkflowRunStatus, limit: number, before?: number) {
  const query = new URLSearchParams({ status, limit: String(limit) });
  if (before != null) query.set("before", String(before));
  return `/api/history?${query}`;
}

export function historyRunHref(id: number) {
  if (!Number.isInteger(id) || id < 1) return undefined;
  return `/history/${id}`;
}

export function mergeHistoryRuns(
  current: WorkflowRun[],
  next: WorkflowRun[],
  status: WorkflowRunStatus,
) {
  const runs = new Map<number, WorkflowRun>();
  for (const run of [...current, ...next]) {
    if (run.status === status) runs.set(run.id, run);
  }
  return [...runs.values()].sort((left, right) => right.id - left.id);
}

export type HistoryPageRequest = { generation: number; cursor: number };

export function historyPageRequestIsCurrent(
  request: HistoryPageRequest,
  generation: number,
  cursor: number | undefined,
) {
  return request.generation === generation && request.cursor === cursor;
}

export function canLoadHistoryPage(state: {
  hasOlder: boolean;
  pending: boolean;
  refreshing: boolean;
  error: boolean;
}) {
  return state.hasOlder && !state.pending && !state.refreshing && !state.error;
}

type HistoryObserver = Pick<IntersectionObserver, "observe" | "disconnect">;
type HistoryObserverConstructor = new (callback: IntersectionObserverCallback) => HistoryObserver;

export function observeHistorySentinel(
  target: Element,
  onIntersect: () => void,
  Observer: HistoryObserverConstructor = IntersectionObserver,
) {
  const observer = new Observer((entries) => {
    if (entries.some((entry) => entry.isIntersecting)) onIntersect();
  });
  observer.observe(target);
  return () => observer.disconnect();
}

export function historyResourceLink(run: Pick<WorkflowRun, "taskId">) {
  const taskID = run.taskId;
  if (typeof taskID !== "number" || !Number.isInteger(taskID) || taskID < 1) return undefined;
  return { href: `/tasks/${taskID}`, label: `Task #${taskID}` };
}
