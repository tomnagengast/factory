import { describe, expect, test } from "bun:test";
import {
  HISTORY_OVERVIEW_LIMIT,
  HISTORY_PAGE_SIZE,
  canLoadHistoryPage,
  historyPageRequestIsCurrent,
  historyPageURL,
  historyResourceLink,
  historyRunHref,
  historyStatuses,
  mergeHistoryRuns,
  observeHistorySentinel,
} from "./history";
import type { WorkflowRun } from "./types";

describe("workflow history routes", () => {
  test("keeps canonical status sections in display order", () => {
    expect(historyStatuses).toEqual([
      { status: "running", label: "Running", href: "/history/running" },
      { status: "waiting", label: "Waiting", href: "/history/waiting" },
      { status: "failed", label: "Failed", href: "/history/failed" },
      { status: "completed", label: "Completed", href: "/history/completed" },
    ]);
    expect(historyStatuses.some((section) => section.href.includes("done"))).toBe(false);
  });

  test("builds bounded overview and status-page URLs", () => {
    expect(historyPageURL("running", HISTORY_OVERVIEW_LIMIT))
      .toBe("/api/history?status=running&limit=5");
    expect(historyPageURL("completed", HISTORY_PAGE_SIZE))
      .toBe("/api/history?status=completed&limit=25");
    expect(historyPageURL("failed", HISTORY_PAGE_SIZE, 175))
      .toBe("/api/history?status=failed&limit=25&before=175");
  });

  test.each([1, 42, 9700])("links run %d to numeric detail", (id) => {
    expect(historyRunHref(id)).toBe(`/history/${id}`);
  });

  test.each([0, -1, 1.5, Number.NaN])("rejects invalid run ID %s", (id) => {
    expect(historyRunHref(id)).toBeUndefined();
  });
});

describe("workflow history pagination", () => {
  const run = (id: number, status: WorkflowRun["status"] = "running") => ({
    id, status, createdAt: "2026-07-20T00:00:00Z", updatedAt: "2026-07-20T00:00:00Z",
    triggerId: id, workflowId: 1, workflowName: "test", workflowPhases: [], sourceEventId: id - 1,
  }) satisfies WorkflowRun;

  test("merges pages newest first without duplicate run IDs", () => {
    expect(mergeHistoryRuns(
      [run(30), run(29), run(28)],
      [run(28), run(27), run(26), run(25, "failed")],
      "running",
    ).map((item) => item.id))
      .toEqual([30, 29, 28, 27, 26]);
  });

  test("invalidates an older cursor after a live page replacement", () => {
    const request = { generation: 3, cursor: 26 };
    expect(historyPageRequestIsCurrent(request, 3, 26)).toBe(true);
    expect(historyPageRequestIsCurrent(request, 4, 26)).toBe(false);
    expect(historyPageRequestIsCurrent(request, 3, 25)).toBe(false);
  });

  test("loads once on intersection and suppresses pending or ended loads", () => {
    let callback: IntersectionObserverCallback | undefined;
    let disconnected = false;
    class MockObserver {
      constructor(selected: IntersectionObserverCallback) { callback = selected; }
      observe(_target: Element) {}
      disconnect() { disconnected = true; }
    }
    const state = { hasOlder: true, pending: false, refreshing: false, error: false };
    let loads = 0;
    const disconnect = observeHistorySentinel({} as Element, () => {
      if (!canLoadHistoryPage(state)) return;
      loads++;
      state.pending = true;
    }, MockObserver);
    const intersect = () => callback?.(
      [{ isIntersecting: true } as IntersectionObserverEntry],
      {} as IntersectionObserver,
    );

    intersect();
    intersect();
    expect(loads).toBe(1);
    state.pending = false;
    state.hasOlder = false;
    intersect();
    expect(loads).toBe(1);
    disconnect();
    expect(disconnected).toBe(true);
  });
});

describe("historyResourceLink", () => {
  test("links a task-triggered run to its task", () => {
    expect(historyResourceLink({ taskId: 2415 })).toEqual({
      href: "/tasks/2415",
      label: "Task #2415",
    });
  });

  test("omits a link when the run has no task", () => {
    expect(historyResourceLink({})).toBeUndefined();
  });

  test.each([0, -1, 1.5, Number.NaN])("omits a link for invalid task ID %s", (taskId) => {
    expect(historyResourceLink({ taskId })).toBeUndefined();
  });
});
