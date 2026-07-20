import type { Comment } from "./types";

const kindLabels: Record<Comment["kind"], string> = {
  message: "Agent message",
  reasoning: "Reasoning",
  "tool-use": "Tool use",
  "tool-output": "Tool output",
  error: "Error",
  event: "Harness event",
};

export function workflowConversationWorking(comments: Comment[]) {
  const answered = new Set(comments
    .filter((comment) => comment.author === "agent" && comment.final && comment.parentCommentId != null)
    .map((comment) => comment.parentCommentId));
  return comments.some((comment) => comment.author === "user" && !answered.has(comment.id));
}

export function workflowCommentPresentation(comment: Comment) {
  const intermediate = comment.author === "agent" && !comment.final;
  return {
    intermediate,
    title: intermediate ? (comment.label || kindLabels[comment.kind]) : comment.author === "agent" ? "Agent" : "You",
    kindLabel: intermediate ? kindLabels[comment.kind] : undefined,
    preformatted: intermediate && ["tool-use", "tool-output", "event"].includes(comment.kind),
    error: comment.kind === "error",
    reasoning: comment.kind === "reasoning",
  };
}
