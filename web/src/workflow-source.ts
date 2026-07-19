import hljs from "highlight.js/lib/common";

export const workflowSourcePlaceholder = "// Waiting for the agent to write the workflow file.";

export function highlightWorkflowSource(source: string) {
  return hljs.highlight(source || workflowSourcePlaceholder, {
    language: "javascript",
    ignoreIllegals: true,
  });
}
