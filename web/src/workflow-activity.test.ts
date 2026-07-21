import { describe, expect, test } from "bun:test";
import type { Comment, WorkflowRunEvent } from "./types";
import {
  workflowConversationBlocks,
  workflowRunPhases,
  type WorkflowActivityBlock,
  type WorkflowActivityEntry,
} from "./workflow-activity";

function comment(id: number, author: Comment["author"], values: Partial<Comment> = {}): Comment {
  return {
    id,
    createdAt: `2026-07-19T12:00:${String(id).padStart(2, "0")}Z`,
    updatedAt: `2026-07-19T12:00:${String(id).padStart(2, "0")}Z`,
    relationType: "workflow",
    relationId: 1,
    author,
    kind: "message",
    final: author === "agent",
    content: `content ${id}`,
    reactions: [],
    ...values,
  };
}

function runEvent(id: number, values: Partial<WorkflowRunEvent> = {}): WorkflowRunEvent {
  const event = {
    id,
    runId: 30,
    recordedAt: `2026-07-19T12:00:${String(id).padStart(2, "0")}Z`,
    sequence: id,
    at: `2026-07-19T12:00:${String(id).padStart(2, "0")}Z`,
    type: "diagnostic",
    workflow: "review",
    raw: {},
    ...values,
  };
  return { ...event, raw: Object.keys(event.raw).length ? event.raw : {
    sequence: event.sequence,
    at: event.at,
    type: event.type,
    workflow: event.workflow,
    phase: event.phase,
    stepId: event.stepId,
    key: event.key,
    agentId: event.agentId,
    kind: event.kind,
    message: event.message,
    result: event.result,
    error: event.error,
  } };
}

function activities(blocks: WorkflowActivityBlock[]) {
  return blocks.filter((block) => block.kind === "activity");
}

function allEntries(blocks: WorkflowActivityBlock[]) {
  return blocks.flatMap((block) => block.kind === "activity"
    ? block.entries
    : block.entry ? [block.entry] : []);
}

function metadata(entry: WorkflowActivityEntry, label: string) {
  return entry.metadata.find((item) => item.label === label)?.value;
}

describe("workflowConversationBlocks", () => {
  test("separates narrative and consecutive activity without guessing tool pairs", () => {
    const long = "result\n".repeat(1000);
    const comments = [
      comment(1, "user", { final: false, content: "Build it" }),
      comment(2, "agent", { final: false, kind: "reasoning", label: "codex" }),
      comment(3, "agent", { final: false, kind: "tool-use", label: "Bash" }),
      comment(4, "agent", { final: false, kind: "tool-use", label: "Bash" }),
      comment(5, "agent", { final: false, kind: "tool-output", label: "Bash", content: long }),
      comment(6, "agent", { final: false, kind: "message", content: "I found the issue." }),
      comment(7, "agent", { final: false, kind: "event", label: "future-event" }),
      comment(8, "agent", { final: false, kind: "error", content: "tool failed" }),
      comment(9, "agent", { final: true, kind: "error", content: "Could not finish" }),
    ];

    const blocks = workflowConversationBlocks(comments);
    expect(blocks.map((block) => block.kind)).toEqual([
      "narrative", "activity", "narrative", "activity", "narrative",
    ]);
    expect(blocks[0]).toMatchObject({ role: "user", content: "Build it" });
    expect(blocks[2]).toMatchObject({ role: "agent", content: "I found the issue.", error: false });
    expect(blocks[4]).toMatchObject({ role: "agent", content: "Could not finish", error: true });

    const groups = activities(blocks);
    expect(groups[0].summary).toBe("1 reasoning update, 2 tool calls, 1 tool result");
    expect(groups[0].entries.map((entry) => entry.title)).toEqual(["codex", "Bash", "Bash", "Bash"]);
    expect(groups[0].entries.map((entry) => entry.kindLabel)).toEqual([
      "Reasoning", "Tool use", "Tool use", "Tool output",
    ]);
    expect(groups[0].entries[3].details[0].value).toBe(long);
    expect(groups[1]).toMatchObject({ summary: "1 error, 1 harness event", failed: true });
    expect(groups[1].entries.map((entry) => entry.title)).toEqual(["future-event", "Error"]);

    const represented = blocks.flatMap((block) => block.kind === "activity"
      ? block.entries.map((entry) => Number(entry.id.split("-").at(-1)))
      : [Number(block.id.split("-").at(-1))]);
    expect(represented).toEqual(comments.map((value) => value.id));
  });

  test("keeps existing group IDs stable when live comments append", () => {
    const initial = [
      comment(1, "user", { final: false }),
      comment(2, "agent", { final: false, kind: "reasoning" }),
      comment(3, "agent", { final: false, kind: "tool-use" }),
    ];
    const before = workflowConversationBlocks(initial);
    const after = workflowConversationBlocks([...initial, comment(4, "agent", { final: true })]);
    expect(before[1].id).toBe("comment-activity-2");
    expect(after[1].id).toBe(before[1].id);
  });
});

describe("workflowRunPhases", () => {
  test("keeps contiguous phases, narrative logs, typed summaries, and raw facts", () => {
    const extension = { future: { nested: [1, 2, 3] } };
    const events = [
      runEvent(1, { type: "runtime.started", backend: "codex", concurrency: 2, budget: 500 }),
      runEvent(2, { type: "phase.started", phase: "Review" }),
      runEvent(3, { type: "log", phase: "Review", message: "## Reviewing\nThe change" }),
      runEvent(4, { type: "step.started", phase: "Review", stepId: 1, key: "a", agentId: "A", kind: "agent" }),
      runEvent(5, { type: "step.started", phase: "Review", stepId: 2, key: "b", agentId: "B", kind: "agent" }),
      runEvent(6, { type: "step.completed", phase: "Review", stepId: 2, key: "b", agentId: "B", kind: "agent", result: "B done" }),
      runEvent(7, { type: "step.completed", phase: "Review", stepId: 1, key: "a", agentId: "A", kind: "agent", result: "A done" }),
      runEvent(8, { type: "diagnostic", phase: "Review", message: "advisory" }),
      runEvent(9, {
        type: "runtime.failed", phase: "Review", error: "boom",
        raw: { sequence: 9, type: "runtime.failed", error: "boom", errorCode: "backend-failed", extension },
      }),
      runEvent(10, { type: "phase.started", phase: "Finish" }),
      runEvent(11, { type: "mystery.observed", phase: "Review", kind: "future", agentId: "unknown" }),
    ];
    const phases = workflowRunPhases([...events].reverse());

    expect(phases.map((phase) => [phase.title, phase.eventCount])).toEqual([
      ["Run", 1], ["Review", 8], ["Finish", 1], ["Review", 1],
    ]);
    expect(phases[1].blocks.map((block) => block.kind)).toEqual(["activity", "narrative", "activity"]);
    expect(phases[1].blocks[1]).toMatchObject({ content: "## Reviewing\nThe change" });
    const reviewActivity = activities(phases[1].blocks)[1];
    expect(reviewActivity.summary).toBe("2 agents, 2 completed, 1 runtime update, 1 diagnostic, 1 failure");
    expect(reviewActivity.failed).toBeTrue();
    expect(reviewActivity.entries.map((entry) => Number(metadata(entry, "Sequence")))).toEqual([4, 5, 6, 7, 8, 9]);
    expect(reviewActivity.entries[0].observationId).toBe(reviewActivity.entries[3].observationId);
    expect(reviewActivity.entries[1].observationId).toBe(reviewActivity.entries[2].observationId);

    const failure = reviewActivity.entries.at(-1)!;
    const raw = failure.details.find((detail) => detail.label === "Raw journal event")?.value;
    expect(raw).toEqual({
      sequence: 9, type: "runtime.failed", error: "boom", errorCode: "backend-failed", extension,
    });
    expect(metadata(failure, "Factory event")).toBe("#9");
    expect(metadata(failure, "Run")).toBe("#30");
    expect(activities(phases[3].blocks)[0]).toMatchObject({ summary: "1 other update", failed: false });

    const represented = phases.flatMap((phase) => allEntries(phase.blocks))
      .map((entry) => Number(metadata(entry, "Factory event")?.slice(1)));
    expect(represented).toEqual(events.map((event) => event.id));
  });

  test("scopes human-gate correlation to runtime attempts when step IDs are reused", () => {
    const events = [
      runEvent(1, { type: "runtime.started", phase: "Review" }),
      runEvent(2, { type: "step.started", phase: "Review", stepId: 1, key: "gate-key", agentId: "review", backend: "human", kind: "gate" }),
      runEvent(3, { type: "runtime.suspended", phase: "Review", stepId: 1, key: "gate-key", agentId: "review", backend: "human", kind: "gate" }),
      runEvent(4, { type: "step.completed", phase: "Review", stepId: 1, key: "gate-key", agentId: "review", backend: "human", kind: "gate", result: "approved" }),
      runEvent(5, { type: "runtime.resumed", phase: "Review" }),
      runEvent(6, { type: "step.cached", phase: "Review", stepId: 1, key: "gate-key", agentId: "review", backend: "human", kind: "gate", result: "approved" }),
    ];
    const phases = workflowRunPhases(events);
    const group = activities(phases[0].blocks)[0];
    const entries = group.entries;

    expect(group.summary).toBe("2 gates, 1 completed, 1 cached, 2 runtime updates");
    expect(entries[1].observationId).toBe(entries[3].observationId);
    expect(entries[2].observationId).toBe(entries[1].observationId);
    expect(entries[5].observationId).not.toBe(entries[1].observationId);
    expect(metadata(entries[3], "Attempt")).toBe("Attempt 1");
    expect(metadata(entries[5], "Attempt")).toBe("Attempt 2");
    expect(entries.map((entry) => Number(metadata(entry, "Sequence")))).toEqual([1, 2, 3, 4, 5, 6]);

    const changed = workflowRunPhases(events.map((event) => event.id === 6
      ? { ...event, key: "changed-key", message: "changed call" }
      : event));
    expect(activities(changed[0].blocks)[0].entries[1].observationId).toBe(entries[1].observationId);
    expect(activities(changed[0].blocks)[0].summary).toBe(group.summary);
  });

  test("summarizes host actions, nested workflows, gates, and unknown updates", () => {
    const events = [
      runEvent(1, { type: "runtime.started", phase: "Execute" }),
      runEvent(2, { type: "step.started", phase: "Execute", stepId: 1, key: "action", agentId: "git", backend: "host", kind: "action" }),
      runEvent(3, { type: "step.completed", phase: "Execute", stepId: 1, key: "action", agentId: "git", backend: "host", kind: "action", result: { exitCode: 0 } }),
      runEvent(4, { type: "step.cached", phase: "Execute", stepId: 2, key: "nested", agentId: "verify", kind: "workflow", result: "verified" }),
      runEvent(5, { type: "step.failed", phase: "Execute", stepId: 3, key: "gate", agentId: "approval", kind: "gate", error: "rejected" }),
      runEvent(6, { type: "runtime.completed", phase: "Execute", result: "done" }),
      runEvent(7, { type: "provider.extension", phase: "Execute", kind: "future" }),
    ];
    const group = activities(workflowRunPhases(events)[0].blocks)[0];

    expect(group.summary).toBe("1 gate, 1 host action, 1 nested workflow, 1 completed, 1 cached, 1 failed, 2 runtime updates, 1 other update");
    expect(group.entries.map((entry) => entry.title)).toContain("git host action completed");
    expect(group.entries.map((entry) => entry.title)).toContain("verify nested workflow cached");
    expect(group.entries.map((entry) => entry.title)).toContain("approval gate failed");
    expect(group.entries.at(-1)).toMatchObject({ title: "provider.extension", kindLabel: "provider.extension" });
  });

  test("keeps step correlation across an intervening narrative log", () => {
    const phases = workflowRunPhases([
      runEvent(1, { type: "runtime.started", phase: "Review" }),
      runEvent(2, { type: "step.started", phase: "Review", stepId: 1, key: "review", agentId: "reviewer", kind: "agent" }),
      runEvent(3, { type: "log", phase: "Review", message: "Still reviewing" }),
      runEvent(4, { type: "step.completed", phase: "Review", stepId: 1, key: "review", agentId: "reviewer", kind: "agent", result: "done" }),
    ]);
    const groups = activities(phases[0].blocks);

    expect(phases[0].blocks.map((block) => block.kind)).toEqual(["activity", "narrative", "activity"]);
    expect(groups[0].entries[1].observationId).toBe(groups[1].entries[0].observationId);
    expect(groups[0].summary).toBe("1 agent, 1 completed, 1 runtime update");
    expect(groups[1].summary).toBe("1 agent, 1 completed");
  });

  test("uses a synthetic attempt for a visible page prefix and keeps live group IDs stable", () => {
    const long = "x".repeat(20_000);
    const events = [
      runEvent(20, { sequence: 20, type: "step.completed", phase: "Review", stepId: 4, kind: "action", result: long }),
      runEvent(21, { sequence: 21, type: "diagnostic", phase: "Review" }),
    ];
    const first = workflowRunPhases(events);
    const replay = workflowRunPhases(events);
    const appended = workflowRunPhases([...events, runEvent(22, { sequence: 22, type: "log", phase: "Review", message: "done" })]);
    const entry = activities(first[0].blocks)[0].entries[0];

    expect(metadata(entry, "Attempt")).toBe("Visible attempt");
    expect(entry.details.find((detail) => detail.label === "Result")?.value).toBe(long);
    expect(replay).toEqual(first);
    expect(appended[0].blocks[0].id).toBe(first[0].blocks[0].id);
  });
});
