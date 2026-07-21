import { describe, expect, test } from "bun:test";
import { taskStatuses, type Project, type Task } from "./types";
import {
  filterTasks,
  loadTaskViewPreferences,
  parseTaskViewSearchParams,
  saveTaskViewPreferences,
  taskFields,
  taskProjectOptions,
  taskViewDefaults,
  taskViewSearchParams,
  taskViewSearchParamsAreCanonical,
  taskViewSearchParamsHaveOwnedKeys,
  type TaskViewPreferences,
} from "./task-view-preferences";

function task(id: number, status: Task["status"], projectId: number): Task {
  return {
    id,
    title: `Task ${id}`,
    status,
    projectId,
    reactions: [],
    createdAt: "2026-07-20T12:00:00Z",
    updatedAt: "2026-07-20T12:00:00Z",
  };
}

function project(id: number, name: string): Project {
  return {
    id,
    name,
    path: `/tmp/project-${id}`,
    createdAt: "2026-07-20T12:00:00Z",
    updatedAt: "2026-07-20T12:00:00Z",
  };
}

const tasks = [
  task(9, "in progress", 24),
  task(7, "todo", 24),
  task(4, "done", 12),
  task(2, "canceled", 30),
];

function memoryStorage(initial: string | null = null) {
  let value = initial;
  return {
    getItem: () => value,
    setItem: (_key: string, next: string) => { value = next; },
  };
}

describe("task view query preferences", () => {
  test("uses defaults when query parameters are absent", () => {
    expect(parseTaskViewSearchParams({})).toEqual(taskViewDefaults);
    expect(taskViewSearchParams(taskViewDefaults)).toEqual({
      sort: undefined,
      direction: undefined,
      group: undefined,
      status: undefined,
      project: undefined,
    });
  });

  test("round trips a complete non-default view", () => {
    const preferences: TaskViewPreferences = {
      sortField: "status",
      direction: "asc",
      groupField: "projectId",
      statuses: ["todo", "in review"],
      projectIds: [12, 24],
    };

    expect(parseTaskViewSearchParams(taskViewSearchParams(preferences), [12, 24])).toEqual(preferences);
  });

  test("accepts every task field for sorting and grouping", () => {
    for (const [field] of taskFields) {
      expect(parseTaskViewSearchParams({ sort: field, group: field })).toEqual({
        ...taskViewDefaults,
        sortField: field,
        groupField: field,
      });
    }
  });

  test("accepts both directions and no grouping", () => {
    for (const direction of ["asc", "desc"] as const) {
      expect(parseTaskViewSearchParams({ direction })).toEqual({ ...taskViewDefaults, direction });
    }
  });

  test("deduplicates repeated filters and puts them in canonical order", () => {
    const parsed = parseTaskViewSearchParams({
      status: ["done", "in progress", "done", "backlog"],
      project: ["24", "12", "24"],
    });

    expect(parsed.statuses).toEqual(["backlog", "in progress", "done"]);
    expect(parsed.projectIds).toEqual([12, 24]);
    expect(taskViewSearchParams(parsed)).toMatchObject({
      status: ["backlog", "in progress", "done"],
      project: ["12", "24"],
    });
  });

  test("preserves statuses containing spaces", () => {
    expect(parseTaskViewSearchParams({ status: ["in progress", "in review"] }).statuses)
      .toEqual(["in progress", "in review"]);
  });

  test("falls back per scalar field and keeps other valid values", () => {
    expect(parseTaskViewSearchParams({
      sort: "priority",
      direction: "sideways",
      group: "projectId",
      status: "todo",
      project: "24",
      futureOption: "true",
    })).toEqual({
      sortField: "id",
      direction: "desc",
      groupField: "projectId",
      statuses: ["todo"],
      projectIds: [24],
    });
  });

  test("rejects repeated scalar fields", () => {
    expect(parseTaskViewSearchParams({
      sort: ["status", "title"],
      direction: ["asc", "desc"],
      group: ["status", "projectId"],
    })).toEqual(taskViewDefaults);
  });

  test("drops malformed and unknown filter values", () => {
    expect(parseTaskViewSearchParams({
      status: ["todo", "blocked", ""],
      project: ["12", "0", "-1", "1.5", "2e1", "9007199254740992", "nope"],
    })).toEqual({
      ...taskViewDefaults,
      statuses: ["todo"],
      projectIds: [12],
    });
  });

  test("drops unavailable projects after options load", () => {
    expect(parseTaskViewSearchParams({ project: ["12", "24", "42"] }, [12, 42]).projectIds)
      .toEqual([12, 42]);
  });

  test("detects noncanonical defaults, duplicates, invalid values, and order", () => {
    expect(taskViewSearchParamsAreCanonical({}, taskViewSearchParams(taskViewDefaults))).toBe(true);
    expect(taskViewSearchParamsAreCanonical({ sort: "id" }, taskViewSearchParams(taskViewDefaults))).toBe(false);
    expect(taskViewSearchParamsAreCanonical(
      { status: ["todo", "todo"] },
      taskViewSearchParams({ ...taskViewDefaults, statuses: ["todo"], projectIds: [] }),
    )).toBe(false);
    expect(taskViewSearchParamsAreCanonical(
      { status: ["done", "todo"] },
      taskViewSearchParams({ ...taskViewDefaults, statuses: ["todo", "done"], projectIds: [] }),
    )).toBe(false);
  });

  test("distinguishes an explicit task view from a bare route", () => {
    expect(taskViewSearchParamsHaveOwnedKeys({})).toBe(false);
    expect(taskViewSearchParamsHaveOwnedKeys({ unrelated: "kept" })).toBe(false);
    expect(taskViewSearchParamsHaveOwnedKeys({ group: "status" })).toBe(true);
    expect(taskViewSearchParamsHaveOwnedKeys({ group: "" })).toBe(true);
  });
});

describe("remembered task view", () => {
  test("round trips the complete canonical view", () => {
    const storage = memoryStorage();
    const preferences: TaskViewPreferences = {
      sortField: "updatedAt",
      direction: "asc",
      groupField: "status",
      statuses: ["done", "todo", "done"],
      projectIds: [24, 12, 24],
    };

    saveTaskViewPreferences(preferences, storage);

    expect(loadTaskViewPreferences(storage)).toEqual({
      sortField: "updatedAt",
      direction: "asc",
      groupField: "status",
      statuses: ["todo", "done"],
      projectIds: [12, 24],
    });
  });

  test("restores the former sort and grouping-only storage shape", () => {
    expect(loadTaskViewPreferences(memoryStorage(JSON.stringify({
      sortField: "title", direction: "asc", groupField: "projectId",
    })))).toEqual({
      sortField: "title",
      direction: "asc",
      groupField: "projectId",
      statuses: [],
      projectIds: [],
    });
  });

  test("falls back safely when storage is malformed or unavailable", () => {
    expect(loadTaskViewPreferences(memoryStorage("{"))).toEqual(taskViewDefaults);
    expect(loadTaskViewPreferences()).toEqual(taskViewDefaults);
    expect(loadTaskViewPreferences({
      getItem: () => { throw new Error("storage disabled"); },
      setItem: () => {},
    })).toEqual(taskViewDefaults);
    expect(saveTaskViewPreferences(taskViewDefaults, {
      getItem: () => null,
      setItem: () => { throw new Error("quota exceeded"); },
    })).toBeUndefined();
  });
});

describe("filterTasks", () => {
  test("keeps every task in source order with no selections", () => {
    expect(filterTasks(tasks, { statuses: [], projectIds: [] }).map(({ id }) => id)).toEqual([9, 7, 4, 2]);
    expect(tasks.map(({ id }) => id)).toEqual([9, 7, 4, 2]);
  });

  test("uses OR for one or several selected statuses", () => {
    expect(filterTasks(tasks, { statuses: ["done"], projectIds: [] }).map(({ id }) => id)).toEqual([4]);
    expect(filterTasks(tasks, { statuses: ["todo", "in progress"], projectIds: [] }).map(({ id }) => id))
      .toEqual([9, 7]);
  });

  test("uses OR for one or several selected projects", () => {
    expect(filterTasks(tasks, { statuses: [], projectIds: [12] }).map(({ id }) => id)).toEqual([4]);
    expect(filterTasks(tasks, { statuses: [], projectIds: [24, 30] }).map(({ id }) => id))
      .toEqual([9, 7, 2]);
  });

  test("uses AND across status and project filters", () => {
    expect(filterTasks(tasks, { statuses: ["todo", "done"], projectIds: [24] }).map(({ id }) => id))
      .toEqual([7]);
  });

  test("returns no tasks when nothing matches and clearing restores all tasks", () => {
    expect(filterTasks(tasks, { statuses: ["backlog"], projectIds: [] })).toEqual([]);
    expect(filterTasks(tasks, { statuses: [], projectIds: [] }).map(({ id }) => id)).toEqual([9, 7, 4, 2]);
  });
});

describe("task filter bulk states", () => {
  test("every status remains canonical through query and storage and includes all represented statuses", () => {
    const preferences: TaskViewPreferences = {
      ...taskViewDefaults,
      statuses: [...taskStatuses],
      projectIds: [],
    };
    const storage = memoryStorage();

    expect(filterTasks(tasks, preferences).map(({ id }) => id)).toEqual([9, 7, 4, 2]);
    expect(taskViewSearchParams(preferences).status).toEqual([...taskStatuses]);
    expect(parseTaskViewSearchParams(taskViewSearchParams(preferences)).statuses).toEqual([...taskStatuses]);
    saveTaskViewPreferences(preferences, storage);
    expect(loadTaskViewPreferences(storage).statuses).toEqual([...taskStatuses]);
  });

  test("every current project remains canonical and includes all represented projects", () => {
    const preferences: TaskViewPreferences = {
      ...taskViewDefaults,
      statuses: [],
      projectIds: [30, 12, 24],
    };

    expect(filterTasks(tasks, preferences).map(({ id }) => id)).toEqual([9, 7, 4, 2]);
    expect(taskViewSearchParams(preferences).project).toEqual(["12", "24", "30"]);
    expect(parseTaskViewSearchParams(taskViewSearchParams(preferences), [12, 24, 30]).projectIds)
      .toEqual([12, 24, 30]);
  });

  test("clearing either complete selection restores that dimension to unrestricted", () => {
    expect(filterTasks(tasks, { statuses: [], projectIds: [24] }).map(({ id }) => id)).toEqual([9, 7]);
    expect(filterTasks(tasks, { statuses: ["todo"], projectIds: [] }).map(({ id }) => id)).toEqual([7]);
    expect(filterTasks(tasks, { statuses: [], projectIds: [] }).map(({ id }) => id)).toEqual([9, 7, 4, 2]);
  });

  test("a full selection in one dimension still combines with a partial selection in the other", () => {
    expect(filterTasks(tasks, { statuses: [...taskStatuses], projectIds: [24] }).map(({ id }) => id))
      .toEqual([9, 7]);
    expect(filterTasks(tasks, { statuses: ["done"], projectIds: [12, 24, 30] }).map(({ id }) => id))
      .toEqual([4]);
  });
});

describe("task project options", () => {
  test("includes active, unused, duplicate-name, and represented missing projects", () => {
    expect(taskProjectOptions([
      task(1, "todo", 24),
      task(2, "done", 42),
    ], [
      project(30, "Unused"),
      project(24, "Review 10"),
      project(12, "review 2"),
      project(8, "Review 10"),
    ])).toEqual([
      { id: 42, label: "Project 42" },
      { id: 12, label: "review 2" },
      { id: 8, label: "Review 10" },
      { id: 24, label: "Review 10" },
      { id: 30, label: "Unused" },
    ]);
  });
});
