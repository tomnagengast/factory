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

export const taskStatuses = ["backlog", "todo", "in progress", "done", "canceled"] as const;
export type TaskStatus = (typeof taskStatuses)[number];

export type Task = Record & {
  title: string;
  description?: string;
  parentTaskId?: number;
  status: TaskStatus;
  projectId: number;
};

export type Comment = Record & {
  relationType: string;
  relationId: number;
  parentCommentId?: number;
  author: "user" | "agent";
  content: string;
};

export type Artifact = Record & {
  name?: string;
  type: "text" | "link" | "image" | "document";
  content: string;
  relationType: string;
  relationId: number;
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
};

export type Workflow = Record & {
  name: string;
  description?: string;
  path?: string;
  scope?: string;
  phases: string[];
  mutating: boolean;
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
  status: "running" | "completed" | "failed";
  output?: string;
  error?: string;
};

export type WorkflowRunStep = {
  id: number;
  runId: number;
  createdAt: string;
  updatedAt: string;
  key?: string;
  phase?: string;
  kind: string;
  backend?: string;
  message: string;
  result?: unknown;
  error?: string;
  done: boolean;
};

export type Health = {
  status: string;
  harness: string;
  events: number;
  projects: number;
  tasks: number;
  triggers: number;
  workflows: number;
  commit?: string;
};

export type Settings = {
  harness: string;
  model: string;
  reasoning: string;
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
export type HistoryDetail = { run: WorkflowRun; steps: WorkflowRunStep[] };
export type ProjectDetail = { project: Project; tasks: Task[] };
export type TaskDetail = { task: Task; comments: Comment[]; artifacts: Artifact[] };
export type CommentDetail = { comment: Comment; replies: Comment[]; artifacts: Artifact[] };
export type WorkflowDetail = {
  workflow: Workflow;
  comments: Comment[];
  artifacts: Artifact[];
  source: string;
};
