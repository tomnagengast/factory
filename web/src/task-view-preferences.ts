import { taskStatuses, type Project, type Task, type TaskStatus } from "./types";

export const taskFields = [
  ["id", "ID"], ["createdAt", "Created at"], ["updatedAt", "Updated at"],
  ["deletedAt", "Deleted at"], ["title", "Title"], ["description", "Description"],
  ["parentTaskId", "Parent task"], ["status", "Status"], ["projectId", "Project"],
] as const;

export type TaskField = (typeof taskFields)[number][0];

export type TaskViewPreferences = {
  sortField: TaskField;
  direction: "asc" | "desc";
  groupField: TaskField | "";
  statuses: TaskStatus[];
  projectIds: number[];
};

export const taskViewDefaults: TaskViewPreferences = {
  sortField: "id",
  direction: "desc",
  groupField: "",
  statuses: [],
  projectIds: [],
};

export type TaskViewSearchParams = Record<string, string | string[] | undefined>;
export type TaskViewSearchUpdate = Record<
  "sort" | "direction" | "group" | "status" | "project",
  string | string[] | undefined
>;

export type TaskProjectOption = {
  id: number;
  label: string;
};

const taskViewQueryKeys = ["sort", "direction", "group", "status", "project"] as const;
const taskFieldValues = new Set<string>(taskFields.map(([field]) => field));
const taskStatusValues = new Set<string>(taskStatuses);

export function parseTaskViewSearchParams(
  searchParams: TaskViewSearchParams,
  availableProjectIds?: Iterable<number>,
): TaskViewPreferences {
  const allowedProjects = availableProjectIds === undefined ? undefined : new Set(availableProjectIds);
  const sort = singleValue(searchParams.sort);
  const direction = singleValue(searchParams.direction);
  const group = singleValue(searchParams.group);
  const statuses = canonicalStatuses(values(searchParams.status).filter(isTaskStatus));
  const projectIds = canonicalProjectIds(values(searchParams.project)
    .map(positiveInteger)
    .filter((id): id is number => id !== undefined && (allowedProjects === undefined || allowedProjects.has(id))));

  return {
    sortField: isTaskField(sort) ? sort : taskViewDefaults.sortField,
    direction: isDirection(direction) ? direction : taskViewDefaults.direction,
    groupField: isTaskField(group) ? group : taskViewDefaults.groupField,
    statuses,
    projectIds,
  };
}

export function taskViewSearchParams(preferences: TaskViewPreferences): TaskViewSearchUpdate {
  const statuses = canonicalStatuses(preferences.statuses);
  const projectIds = canonicalProjectIds(preferences.projectIds);
  return {
    sort: preferences.sortField === taskViewDefaults.sortField ? undefined : preferences.sortField,
    direction: preferences.direction === taskViewDefaults.direction ? undefined : preferences.direction,
    group: preferences.groupField || undefined,
    status: statuses.length ? statuses : undefined,
    project: projectIds.length ? projectIds.map(String) : undefined,
  };
}

export function taskViewSearchParamsAreCanonical(
  searchParams: TaskViewSearchParams,
  canonical: TaskViewSearchUpdate,
): boolean {
  return taskViewQueryKeys.every((key) => {
    const current = values(searchParams[key]);
    const expected = values(canonical[key]);
    return current.length === expected.length && current.every((value, index) => value === expected[index]);
  });
}

export function taskProjectOptions(
  tasks: readonly Pick<Task, "projectId">[],
  projects: readonly Pick<Project, "id" | "name">[],
): TaskProjectOption[] {
  const projectsByID = new Map(projects.map((project) => [project.id, project]));
  const ids = new Set(projects.map((project) => project.id));
  for (const task of tasks) ids.add(task.projectId);
  return [...ids]
    .map((id) => ({ id, label: projectsByID.get(id)?.name ?? `Project ${id}` }))
    .sort((left, right) => {
      const labelOrder = left.label.localeCompare(right.label, undefined, { numeric: true, sensitivity: "base" });
      return labelOrder || left.id - right.id;
    });
}

export function filterTasks<T extends Pick<Task, "status" | "projectId">>(
  tasks: readonly T[],
  preferences: Pick<TaskViewPreferences, "statuses" | "projectIds">,
): T[] {
  return tasks.filter((task) => {
    const statusMatches = preferences.statuses.length === 0 || preferences.statuses.includes(task.status);
    const projectMatches = preferences.projectIds.length === 0 || preferences.projectIds.includes(task.projectId);
    return statusMatches && projectMatches;
  });
}

function values(value: string | string[] | undefined): string[] {
  if (value === undefined) return [];
  return Array.isArray(value) ? value : [value];
}

function singleValue(value: string | string[] | undefined): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function canonicalStatuses(statuses: readonly TaskStatus[]): TaskStatus[] {
  const selected = new Set(statuses);
  return taskStatuses.filter((status) => selected.has(status));
}

function canonicalProjectIds(projectIds: readonly number[]): number[] {
  return [...new Set(projectIds.filter((id) => Number.isSafeInteger(id) && id > 0))]
    .sort((left, right) => left - right);
}

function positiveInteger(value: string): number | undefined {
  if (!/^[1-9]\d*$/.test(value)) return undefined;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : undefined;
}

function isTaskField(value: unknown): value is TaskField {
  return typeof value === "string" && taskFieldValues.has(value);
}

function isDirection(value: unknown): value is TaskViewPreferences["direction"] {
  return value === "asc" || value === "desc";
}

function isTaskStatus(value: string): value is TaskStatus {
  return taskStatusValues.has(value);
}
