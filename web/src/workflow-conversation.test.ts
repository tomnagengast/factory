import { describe, expect, test } from "bun:test";
import type { Comment } from "./types";
import { workflowConversationWorking } from "./workflow-conversation";

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
