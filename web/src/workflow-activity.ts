import type { Comment, WorkflowRunEvent } from "./types";

export type WorkflowActivityDetail = {
  label: string;
  value: unknown;
  format: "text" | "markdown" | "json";
};

export type WorkflowActivityEntry = {
  id: string;
  title: string;
  kindLabel: string;
  at: string;
  metadata: Array<{ label: string; value: string }>;
  details: WorkflowActivityDetail[];
  observationId?: string;
  failed: boolean;
};

export type WorkflowActivityBlock =
  | {
      kind: "narrative";
      id: string;
      role: "user" | "agent";
      content: string;
      at: string;
      error: boolean;
      entry?: WorkflowActivityEntry;
    }
  | {
      kind: "activity";
      id: string;
      summary: string;
      failed: boolean;
      entries: WorkflowActivityEntry[];
    };

export type WorkflowRunPhase = {
  id: string;
  title: string;
  eventCount: number;
  blocks: WorkflowActivityBlock[];
};

const commentKindLabels: Record<Comment["kind"], string> = {
  message: "Agent message",
  reasoning: "Reasoning",
  "tool-use": "Tool use",
  "tool-output": "Tool output",
  error: "Error",
  event: "Harness event",
};

const activityCommentKinds = new Set<Comment["kind"]>([
  "reasoning", "tool-use", "tool-output", "error", "event",
]);

export function workflowConversationBlocks(comments: Comment[]): WorkflowActivityBlock[] {
  const blocks: WorkflowActivityBlock[] = [];
  let group: Comment[] = [];

  const flush = () => {
    if (!group.length) return;
    blocks.push({
      kind: "activity",
      id: `comment-activity-${group[0].id}`,
      summary: summarizeComments(group),
      failed: group.some((comment) => comment.kind === "error"),
      entries: group.map(commentEntry),
    });
    group = [];
  };

  for (const comment of comments) {
    const activity = comment.author === "agent" && !comment.final && activityCommentKinds.has(comment.kind);
    if (activity) {
      group.push(comment);
      continue;
    }
    flush();
    blocks.push({
      kind: "narrative",
      id: `comment-${comment.id}`,
      role: comment.author,
      content: comment.content,
      at: comment.createdAt,
      error: comment.kind === "error",
    });
  }
  flush();
  return blocks;
}

function commentEntry(comment: Comment): WorkflowActivityEntry {
  return {
    id: `comment-entry-${comment.id}`,
    title: comment.label || commentKindLabels[comment.kind],
    kindLabel: commentKindLabels[comment.kind],
    at: comment.createdAt,
    metadata: [
      { label: "Comment", value: `#${comment.id}` },
      { label: "Kind", value: comment.kind },
    ],
    details: [{ label: "Content", value: comment.content, format: "text" }],
    failed: comment.kind === "error",
  };
}

function summarizeComments(comments: Comment[]): string {
  const counts = new Map<Comment["kind"], number>();
  for (const comment of comments) counts.set(comment.kind, (counts.get(comment.kind) ?? 0) + 1);
  const labels: Array<[Comment["kind"], string, string]> = [
    ["reasoning", "reasoning update", "reasoning updates"],
    ["tool-use", "tool call", "tool calls"],
    ["tool-output", "tool result", "tool results"],
    ["error", "error", "errors"],
    ["event", "harness event", "harness events"],
  ];
  return labels.flatMap(([kind, one, many]) => {
    const count = counts.get(kind) ?? 0;
    return count ? [`${count} ${count === 1 ? one : many}`] : [];
  }).join(", ");
}

type Attempt = { id: string; label: string };
type RunEventContext = { event: WorkflowRunEvent; attempt: Attempt };

export function workflowRunPhases(events: WorkflowRunEvent[]): WorkflowRunPhase[] {
  const sorted = [...events].sort((left, right) => left.sequence - right.sequence || left.id - right.id);
  if (!sorted.length) return [];

  const contexts: RunEventContext[] = [];
  const hasPrefix = !isAttemptBoundary(sorted[0]);
  let attempt: Attempt = {
    id: `attempt-prefix-${sorted[0].id}`,
    label: "Visible attempt",
  };
  let attemptNumber = 0;
  for (const event of sorted) {
    if (isAttemptBoundary(event)) {
      attemptNumber += 1;
      attempt = { id: `attempt-${event.id}`, label: `Attempt ${attemptNumber}` };
    } else if (!hasPrefix && attemptNumber === 0) {
      attempt = { id: `attempt-prefix-${sorted[0].id}`, label: "Visible attempt" };
    }
    contexts.push({ event, attempt });
  }
  const correlation = correlateRunContexts(contexts);

  const phases: WorkflowRunPhase[] = [];
  for (const context of contexts) {
    const title = context.event.phase || "Run";
    const current = phases.at(-1);
    if (current?.title === title) {
      current.eventCount += 1;
      appendRunContext(current.blocks, context, correlation);
    } else {
      const phase: WorkflowRunPhase = {
        id: `run-phase-${context.event.id}`,
        title,
        eventCount: 1,
        blocks: [],
      };
      appendRunContext(phase.blocks, context, correlation);
      phases.push(phase);
    }
  }
  for (const phase of phases) finalizeRunActivityBlocks(phase.blocks, correlation);
  return phases;
}

function appendRunContext(blocks: WorkflowActivityBlock[], context: RunEventContext, correlation: RunCorrelation) {
  if (context.event.type === "log") {
    blocks.push({
      kind: "narrative",
      id: `run-log-${context.event.id}`,
      role: "agent",
      content: context.event.message ?? "",
      at: context.event.at,
      error: false,
      entry: runEntry(context, correlation),
    });
    return;
  }
  const current = blocks.at(-1);
  if (current?.kind === "activity" && !current.summary) {
    current.entries.push(runEntry(context, correlation));
    return;
  }
  blocks.push({
    kind: "activity",
    id: `run-activity-${context.event.id}`,
    summary: "",
    failed: false,
    entries: [runEntry(context, correlation)],
  });
}

function finalizeRunActivityBlocks(blocks: WorkflowActivityBlock[], correlation: RunCorrelation) {
  for (const block of blocks) {
    if (block.kind !== "activity") continue;
    const observations = [...new Set(block.entries.flatMap((entry) => entry.observationId ? [entry.observationId] : []))]
      .flatMap((id) => {
        const observation = correlation.observations.get(id);
        return observation ? [observation] : [];
      });
    block.failed = block.entries.some((entry) => entry.failed);
    block.summary = summarizeRunActivity(block.entries, observations);
  }
}

type StepObservation = {
  id: string;
  kind: string;
  state: "running" | "waiting" | "completed" | "cached" | "failed";
};

type RunCorrelation = {
  observations: Map<string, StepObservation>;
  byEventID: Map<number, string>;
};

function correlateRunContexts(contexts: RunEventContext[]): RunCorrelation {
  const observations = new Map<string, StepObservation>();
  const byEventID = new Map<number, string>();
  for (const context of contexts) {
    const { event, attempt } = context;
    if (event.type === "runtime.suspended") {
      const id = stepObservationBase(event, attempt);
      const observation = observations.get(id);
      if (observation?.state === "running") observation.state = "waiting";
      if (observation) byEventID.set(event.id, observation.id);
      continue;
    }
    if (!event.type.startsWith("step.")) continue;
    const base = stepObservationBase(event, attempt);
    const id = event.type === "step.cached" ? `${base}:cached-${event.id}` : base;
    const state = stepState(event.type);
    const existing = observations.get(id);
    if (existing) existing.state = state;
    else observations.set(id, { id, kind: event.kind || "step", state });
    byEventID.set(event.id, id);
  }
  return { observations, byEventID };
}

const runEntryContexts = new WeakMap<WorkflowActivityEntry, RunEventContext>();

function runEntry(context: RunEventContext, correlation: RunCorrelation): WorkflowActivityEntry {
  const { event, attempt } = context;
  const details: WorkflowActivityDetail[] = [];
  if (event.message != null) details.push({ label: "Message", value: event.message, format: "markdown" });
  if (event.schema != null) details.push({ label: "Schema", value: event.schema, format: "json" });
  if (event.result != null) details.push({
    label: "Result",
    value: event.result,
    format: typeof event.result === "string" ? "markdown" : "json",
  });
  if (event.error != null) details.push({ label: "Error", value: event.error, format: "markdown" });
  details.push({ label: "Raw journal event", value: event.raw, format: "json" });
  const entry: WorkflowActivityEntry = {
    id: `run-entry-${event.id}`,
    title: runEventTitle(event),
    kindLabel: event.type || event.kind || event.agentId || "Runtime event",
    at: event.at,
    metadata: [
      { label: "Attempt", value: attempt.label },
      { label: "Sequence", value: String(event.sequence) },
      { label: "Factory event", value: `#${event.id}` },
      { label: "Run", value: `#${event.runId}` },
      { label: "Recorded", value: event.recordedAt },
    ],
    details,
    observationId: correlation.byEventID.get(event.id),
    failed: event.type.endsWith(".failed") || Boolean(event.error),
  };
  runEntryContexts.set(entry, context);
  return entry;
}

function entryContext(entry: WorkflowActivityEntry) {
  return runEntryContexts.get(entry);
}

function isAttemptBoundary(event: WorkflowRunEvent) {
  return event.type === "runtime.started" || event.type === "runtime.resumed";
}

function stepObservationBase(event: WorkflowRunEvent, attempt: Attempt) {
  const step = event.stepId == null ? `event-${event.id}` : String(event.stepId);
  return [attempt.id, event.workflow, step, event.key ?? ""].join(":");
}

function stepState(type: string): StepObservation["state"] {
  if (type === "step.cached") return "cached";
  if (type === "step.completed") return "completed";
  if (type === "step.failed") return "failed";
  return "running";
}

function summarizeRunActivity(entries: WorkflowActivityEntry[], observations: StepObservation[]) {
  const parts: string[] = [];
  const kinds = countBy(observations, (observation) => observation.kind);
  for (const [kind, one, many] of [
    ["agent", "agent", "agents"],
    ["gate", "gate", "gates"],
    ["action", "host action", "host actions"],
    ["workflow", "nested workflow", "nested workflows"],
    ["step", "step", "steps"],
  ] as const) {
    const count = kinds.get(kind) ?? 0;
    if (count) parts.push(`${count} ${count === 1 ? one : many}`);
  }
  const knownKinds = new Set(["agent", "gate", "action", "workflow", "step"]);
  const otherSteps = observations.filter((observation) => !knownKinds.has(observation.kind)).length;
  if (otherSteps) parts.push(`${otherSteps} other ${otherSteps === 1 ? "step" : "steps"}`);

  const states = countBy(observations, (observation) => observation.state);
  for (const [state, label] of [
    ["completed", "completed"], ["cached", "cached"], ["failed", "failed"],
    ["waiting", "waiting"], ["running", "running"],
  ] as const) {
    const count = states.get(state) ?? 0;
    if (count) parts.push(`${count} ${label}`);
  }

  const contexts = entries.flatMap((entry) => {
    const context = entryContext(entry);
    return context ? [context] : [];
  });
  const nonStep = contexts.filter(({ event }) =>
    !event.type.startsWith("step.") && event.type !== "runtime.suspended");
  const updateKinds = countBy(nonStep, ({ event }) => {
    if (event.type.startsWith("runtime.")) return "runtime update";
    if (event.type === "phase.started") return "phase update";
    if (event.type === "diagnostic") return "diagnostic";
    return "other update";
  });
  for (const label of ["runtime update", "phase update", "diagnostic", "other update"]) {
    const count = updateKinds.get(label) ?? 0;
    if (count) parts.push(`${count} ${count === 1 ? label : pluralUpdate(label)}`);
  }
  const nonStepFailures = nonStep.filter(({ event }) => event.type.endsWith(".failed") || Boolean(event.error)).length;
  if (nonStepFailures) parts.push(`${nonStepFailures} ${nonStepFailures === 1 ? "failure" : "failures"}`);
  if (!parts.length) return `${entries.length} ${entries.length === 1 ? "activity update" : "activity updates"}`;
  return parts.join(", ");
}

function countBy<T>(values: T[], key: (value: T) => string) {
  const counts = new Map<string, number>();
  for (const value of values) {
    const name = key(value);
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }
  return counts;
}

function pluralUpdate(label: string) {
  return label === "diagnostic" ? "diagnostics" : `${label}s`;
}

function runEventTitle(event: WorkflowRunEvent) {
  if (event.type === "phase.started") return `Entered ${event.phase || "phase"}`;
  if (event.type === "diagnostic") return "Runtime diagnostic";
  if (event.type === "runtime.started") return "Runtime started";
  if (event.type === "runtime.resumed") return "Runtime resumed";
  if (event.type === "runtime.suspended") return `${event.agentId || "Human gate"} waiting for response`;
  if (event.type === "runtime.completed") return "Runtime completed";
  if (event.type === "runtime.failed") return "Runtime failed";
  if (event.type === "log") return "Workflow log";
  if (event.type.startsWith("step.")) {
    const action = event.type.slice("step.".length);
    const kind = event.kind === "action"
      ? "host action"
      : event.kind === "workflow"
        ? "nested workflow"
        : event.kind || "step";
    return `${event.agentId || capitalize(kind)} ${kind} ${action}`;
  }
  return event.type || event.kind || event.agentId || "Runtime event";
}

function capitalize(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
