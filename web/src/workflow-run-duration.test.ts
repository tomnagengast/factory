import { describe, expect, test } from "bun:test";
import { formatWorkflowRunDuration } from "./workflow-run-duration";

describe("formatWorkflowRunDuration", () => {
  test("shows seconds for runs shorter than a minute", () => {
    expect(formatWorkflowRunDuration("2026-07-20T12:00:00Z", "2026-07-20T12:00:42Z")).toBe("42s");
  });

  test("shows minutes and seconds for runs shorter than an hour", () => {
    expect(formatWorkflowRunDuration("2026-07-20T12:00:00Z", "2026-07-20T12:12:34Z")).toBe("12m 34s");
  });

  test("shows hours and minutes for runs shorter than a day", () => {
    expect(formatWorkflowRunDuration("2026-07-20T12:00:00Z", "2026-07-20T15:05:40Z")).toBe("3h 5m");
  });

  test("shows days and hours for longer runs", () => {
    expect(formatWorkflowRunDuration("2026-07-18T12:00:00Z", "2026-07-20T15:05:40Z")).toBe("2d 3h");
  });

  test("clamps timestamps before the run start", () => {
    expect(formatWorkflowRunDuration("2026-07-20T12:00:01Z", "2026-07-20T12:00:00Z")).toBe("0s");
  });
});
