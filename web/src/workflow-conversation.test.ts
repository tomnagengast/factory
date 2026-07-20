import { describe, expect, test } from "bun:test";
import type { Comment } from "./types";
import { workflowCommentPresentation, workflowConversationWorking } from "./workflow-conversation";

function comment(id: number, author: Comment["author"], values: Partial<Comment> = {}): Comment {
  return {
    id,
    createdAt: "2026-07-19T12:00:00Z",
    updatedAt: "2026-07-19T12:00:00Z",
    relationType: "workflow",
    relationId: 1,
    author,
    kind: "message",
    final: author === "agent",
    content: "content",
    reactions: [],
    ...values,
  };
}

describe("workflowConversationWorking", () => {
  test("is idle without a user request", () => {
    expect(workflowConversationWorking([])).toBeFalse();
  });

  test("stays working through intermediate agent steps", () => {
    expect(workflowConversationWorking([
      comment(1, "user", { final: false }),
      comment(2, "agent", { parentCommentId: 1, kind: "reasoning", final: false }),
      comment(3, "agent", { parentCommentId: 1, kind: "tool-output", final: false }),
    ])).toBeTrue();
  });

  test("stops after a final success or error", () => {
    for (const kind of ["message", "error"] as const) {
      expect(workflowConversationWorking([
        comment(1, "user", { final: false }),
        comment(2, "agent", { parentCommentId: 1, kind, final: true }),
      ])).toBeFalse();
    }
  });

  test("remains working when a later request is queued", () => {
    expect(workflowConversationWorking([
      comment(1, "user", { final: false }),
      comment(2, "agent", { parentCommentId: 1, final: true }),
      comment(3, "user", { final: false }),
      comment(4, "agent", { parentCommentId: 3, kind: "reasoning", final: false }),
    ])).toBeTrue();
  });
});

describe("workflowCommentPresentation", () => {
  test("classifies every intermediate step kind", () => {
    const expected = {
      message: ["Agent message", false],
      reasoning: ["Reasoning", false],
      "tool-use": ["Tool use", true],
      "tool-output": ["Tool output", true],
      error: ["Error", false],
      event: ["Harness event", true],
    } as const;
    for (const kind of Object.keys(expected) as Comment["kind"][]) {
      const presentation = workflowCommentPresentation(comment(2, "agent", { kind, final: false }));
      expect(presentation.intermediate).toBeTrue();
      expect(presentation.title).toBe(expected[kind][0]);
      expect(presentation.kindLabel).toBe(expected[kind][0]);
      expect(presentation.preformatted).toBe(expected[kind][1]);
      expect(presentation.error).toBe(kind === "error");
      expect(presentation.reasoning).toBe(kind === "reasoning");
    }
  });

  test("uses harness and tool labels while retaining the normalized kind", () => {
    for (const label of ["codex", "Bash"]) {
      const presentation = workflowCommentPresentation(comment(2, "agent", {
        kind: label === "codex" ? "reasoning" : "tool-use",
        label,
        final: false,
      }));
      expect(presentation.title).toBe(label);
      expect(presentation.kindLabel).toBe(label === "codex" ? "Reasoning" : "Tool use");
    }
  });

  test("labels final agent responses and user messages as conversation bubbles", () => {
    expect(workflowCommentPresentation(comment(2, "agent", {
      kind: "error", label: "claude", final: true,
    }))).toEqual({
      intermediate: false,
      title: "Agent",
      kindLabel: undefined,
      preformatted: false,
      error: true,
      reasoning: false,
    });
    expect(workflowCommentPresentation(comment(1, "user", { final: false })).title).toBe("You");
  });
});
