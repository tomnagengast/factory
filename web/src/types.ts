export type Record = {
  id: number;
  createdAt: string;
  updatedAt: string;
  deletedAt?: string;
};

export type Project = Record & {
  name: string;
  description?: string;
  repo?: string;
  path: string;
  url?: string;
};

export const taskStatuses = ["backlog", "todo", "in progress", "in review", "done", "canceled"] as const;
export type TaskStatus = (typeof taskStatuses)[number];

export type Task = Record & {
  title: string;
  description?: string;
  parentTaskId?: number;
  status: TaskStatus;
  projectId: number;
  reactions: string[];
};

export type TaskWorkflowRun = {
  runId: number;
  triggerId: number;
  workflowId: number;
  workflowName: string;
  status: "running" | "waiting" | "completed" | "failed";
};

export type TaskSummary = Task & {
  commentCount: number;
  workflowRuns: TaskWorkflowRun[];
};

export type TaskListResponse = {
  checkpointEventId: number;
  tasks: TaskSummary[];
};

export type Comment = Record & {
  relationType: string;
  relationId: number;
  parentCommentId?: number;
  author: "user" | "agent";
  kind: "message" | "reasoning" | "tool-use" | "tool-output" | "error" | "event";
  label?: string;
  final: boolean;
  content: string;
  reactions: string[];
};

export type Artifact = Record & {
  name?: string;
  type: "text" | "link" | "image" | "document";
  content: string;
  relationType: string;
  relationId: number;
};

export type MediaUpload = Record & {
  name: string;
  contentType: string;
  size: number;
  sha256: string;
  url: string;
};

export type Event = {
  id: number;
  type: string;
  at: string;
  data: unknown;
};

export type Trigger = Record & {
  eventType: string;
  schedule?: string;
  workflowId: number;
  enabled: boolean;
};

export type Workflow = Record & {
  name: string;
  description?: string;
  path?: string;
  scope?: string;
  phases: string[];
  mutating: boolean;
  runCount: number;
  taskCount: number;
};

export type WorkflowRun = {
  id: number;
  createdAt: string;
  updatedAt: string;
  triggerId: number;
  workflowId: number;
  workflowName: string;
  workflowPhases: string[];
  sourceEventId: number;
  taskId?: number;
  status: "running" | "waiting" | "completed" | "failed";
  waitingGate?: {
    workflow: string;
    phase?: string;
    stepId: number;
    key: string;
    agentId?: string;
    message: string;
    schema?: unknown;
  };
  gateCommentId?: number;
  responseCommentId?: number;
  output?: string;
  error?: string;
};

export type WorkflowRunEvent = {
  id: number;
  runId: number;
  recordedAt: string;
  sequence: number;
  at: string;
  type: string;
  workflow: string;
  stepId?: number;
  key?: string;
  phase?: string;
  agentId?: string;
  backend?: string;
  kind?: string;
  message?: string;
  schema?: unknown;
  result?: unknown;
  error?: string;
  tokens?: number;
  concurrency?: number;
  budget?: number;
};

export type Health = {
  status: string;
  harness: string;
  workflowCapacity: number;
  events: number;
  projects: number;
  tasks: number;
  triggers: number;
  workflowRunning: number;
  checkpointEventId: number;
  commit?: string;
};

export type Settings = {
  harness: string;
  model: string;
  reasoning: string;
  workflowCapacity: number;
};

export type ModelOption = {
  id: string;
  reasoning: string[];
  defaultReasoning: string;
};

export type HarnessOption = {
  id: string;
  name: string;
  models: ModelOption[];
};

export type SettingsDetail = { settings: Settings; harnesses: HarnessOption[] };
export type HistoryDetail = { run: WorkflowRun; events: WorkflowRunEvent[] };
export type ProjectDetail = { project: Project; tasks: TaskSummary[]; checkpointEventId: number };
export type TaskDetail = { task: Task; comments: Comment[]; artifacts: Artifact[] };
export type CommentDetail = { comment: Comment; replies: Comment[]; artifacts: Artifact[] };
export type WorkflowDetail = {
  workflow: Workflow;
  comments: Comment[];
  artifacts: Artifact[];
  source: string;
};
