import { describe, expect, test } from "bun:test";
import { historyResourceLink } from "./history";

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
