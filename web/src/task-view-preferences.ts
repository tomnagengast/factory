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
};

export const taskViewDefaults: TaskViewPreferences = {
  sortField: "id",
  direction: "desc",
  groupField: "",
};

type TaskViewStorage = Pick<Storage, "getItem" | "setItem">;

const taskViewStorageKey = "factory.tasks.view";
const taskFieldValues = new Set<string>(taskFields.map(([field]) => field));

export function loadTaskViewPreferences(storage: TaskViewStorage | undefined = browserStorage()): TaskViewPreferences {
  if (!storage) return { ...taskViewDefaults };
  try {
    const stored = storage.getItem(taskViewStorageKey);
    if (stored === null) return { ...taskViewDefaults };
    const value: unknown = JSON.parse(stored);
    if (!isObject(value)) return { ...taskViewDefaults };
    return {
      sortField: isTaskField(value.sortField) ? value.sortField : taskViewDefaults.sortField,
      direction: isDirection(value.direction) ? value.direction : taskViewDefaults.direction,
      groupField: isGroupField(value.groupField) ? value.groupField : taskViewDefaults.groupField,
    };
  } catch {
    return { ...taskViewDefaults };
  }
}

export function saveTaskViewPreferences(
  preferences: TaskViewPreferences,
  storage: TaskViewStorage | undefined = browserStorage(),
): void {
  if (!storage) return;
  try {
    storage.setItem(taskViewStorageKey, JSON.stringify(preferences));
  } catch {
    // Browser storage can be disabled or full. The in-memory view still works.
  }
}

function browserStorage(): TaskViewStorage | undefined {
  try {
    return typeof localStorage === "undefined" ? undefined : localStorage;
  } catch {
    return undefined;
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isTaskField(value: unknown): value is TaskField {
  return typeof value === "string" && taskFieldValues.has(value);
}

function isDirection(value: unknown): value is TaskViewPreferences["direction"] {
  return value === "asc" || value === "desc";
}

function isGroupField(value: unknown): value is TaskViewPreferences["groupField"] {
  return value === "" || isTaskField(value);
}
