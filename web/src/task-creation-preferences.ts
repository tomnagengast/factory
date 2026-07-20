import type { Project } from "./types";

type TaskCreationStorage = Pick<Storage, "getItem" | "setItem">;

const lastTaskProjectIDStorageKey = "factory.tasks.lastProjectId";

export function loadLastTaskProjectId(
  storage: TaskCreationStorage | undefined = browserStorage(),
): number | undefined {
  if (!storage) return undefined;
  try {
    const value: unknown = JSON.parse(storage.getItem(lastTaskProjectIDStorageKey) ?? "null");
    return isProjectID(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

export function saveLastTaskProjectId(
  projectID: number,
  storage: TaskCreationStorage | undefined = browserStorage(),
): void {
  if (!storage || !isProjectID(projectID)) return;
  try {
    storage.setItem(lastTaskProjectIDStorageKey, JSON.stringify(projectID));
  } catch {
    // Browser storage can be disabled or full. Task creation still succeeds.
  }
}

export function activeTaskCreationProjectId(
  projects: readonly Pick<Project, "id">[],
  rememberedProjectID: number | undefined,
): number | undefined {
  return projects.some((project) => project.id === rememberedProjectID) ? rememberedProjectID : undefined;
}

function browserStorage(): TaskCreationStorage | undefined {
  try {
    return typeof localStorage === "undefined" ? undefined : localStorage;
  } catch {
    return undefined;
  }
}

function isProjectID(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}
