import type { Comment } from "./types";

export function workflowConversationWorking(comments: Comment[]) {
  const answered = new Set(comments
    .filter((comment) => comment.author === "agent" && comment.final && comment.parentCommentId != null)
    .map((comment) => comment.parentCommentId));
  return comments.some((comment) => comment.author === "user" && !answered.has(comment.id));
}
