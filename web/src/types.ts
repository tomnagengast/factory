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
  path?: string;
  url?: string;
};

export const taskStatuses = ["backlog", "todo", "in progress", "done", "canceled"] as const;
export type TaskStatus = (typeof taskStatuses)[number];

export type Task = Record & {
  title: string;
  description?: string;
  parentTaskId?: number;
  status: TaskStatus;
  projectId?: number;
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

export type Health = {
  status: string;
  agent: string;
  events: number;
  projects: number;
  tasks: number;
  triggers: number;
  workflows: number;
  commit?: string;
};

export type ProjectDetail = { project: Project; tasks: Task[] };
export type TaskDetail = { task: Task; comments: Comment[]; artifacts: Artifact[] };
export type CommentDetail = { comment: Comment; replies: Comment[]; artifacts: Artifact[] };
export type WorkflowDetail = { workflow: Workflow; comments: Comment[]; artifacts: Artifact[] };
