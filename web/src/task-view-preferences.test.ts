import { describe, expect, test } from "bun:test";
import {
  loadTaskViewPreferences,
  saveTaskViewPreferences,
  taskFields,
  taskViewDefaults,
  type TaskViewPreferences,
} from "./task-view-preferences";

function memoryStorage(initial: string | null = null) {
  let value = initial;
  return {
    getItem: () => value,
    setItem: (_key: string, next: string) => { value = next; },
  };
}

describe("task view preferences", () => {
  test("uses defaults when no preference has been saved", () => {
    expect(loadTaskViewPreferences(memoryStorage())).toEqual(taskViewDefaults);
  });

  test("round trips a complete non-default preference", () => {
    const storage = memoryStorage();
    const preferences: TaskViewPreferences = {
      sortField: "status",
      direction: "asc",
      groupField: "status",
    };

    saveTaskViewPreferences(preferences, storage);

    expect(loadTaskViewPreferences(storage)).toEqual(preferences);
  });

  test("accepts every task field for sorting and grouping", () => {
    for (const [field] of taskFields) {
      expect(loadTaskViewPreferences(memoryStorage(JSON.stringify({
        sortField: field,
        direction: "desc",
        groupField: field,
      })))).toEqual({ sortField: field, direction: "desc", groupField: field });
    }
  });

  test("accepts both directions and no grouping", () => {
    for (const direction of ["asc", "desc"] as const) {
      expect(loadTaskViewPreferences(memoryStorage(JSON.stringify({
        sortField: "id",
        direction,
        groupField: "",
      })))).toEqual({ sortField: "id", direction, groupField: "" });
    }
  });

  test.each([
    ["invalid JSON", "{"],
    ["an array", "[]"],
    ["a string", '"id"'],
    ["a number", "1"],
    ["null", "null"],
  ])("uses defaults for %s", (_name, stored) => {
    expect(loadTaskViewPreferences(memoryStorage(stored))).toEqual(taskViewDefaults);
  });

  test.each([
    ["missing properties", {}],
    ["wrong property types", { sortField: 1, direction: true, groupField: [] }],
    ["removed values", { sortField: "priority", direction: "up", groupField: "assignee" }],
  ])("replaces %s with defaults", (_name, stored) => {
    expect(loadTaskViewPreferences(memoryStorage(JSON.stringify(stored)))).toEqual(taskViewDefaults);
  });

  test("falls back per property and preserves other valid values", () => {
    expect(loadTaskViewPreferences(memoryStorage(JSON.stringify({
      sortField: "status",
      direction: "sideways",
      groupField: "projectId",
      futureOption: true,
    })))).toEqual({
      sortField: "status",
      direction: "desc",
      groupField: "projectId",
    });
  });

  test("uses defaults when storage reads fail", () => {
    expect(loadTaskViewPreferences({
      getItem: () => { throw new Error("storage disabled"); },
      setItem: () => {},
    })).toEqual(taskViewDefaults);
  });

  test("ignores storage write failures", () => {
    expect(saveTaskViewPreferences(taskViewDefaults, {
      getItem: () => null,
      setItem: () => { throw new Error("quota exceeded"); },
    })).toBeUndefined();
  });
});
