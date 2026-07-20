import { describe, expect, test } from "bun:test";
import {
  activeTaskCreationProjectId,
  loadLastTaskProjectId,
  saveLastTaskProjectId,
} from "./task-creation-preferences";

function memoryStorage(initial: string | null = null) {
  let value = initial;
  return {
    getItem: () => value,
    setItem: (_key: string, next: string) => { value = next; },
  };
}

describe("task creation preferences", () => {
  test("returns no project when no preference has been saved", () => {
    expect(loadLastTaskProjectId(memoryStorage())).toBeUndefined();
  });

  test("round trips a positive project ID", () => {
    const storage = memoryStorage();

    saveLastTaskProjectId(17, storage);

    expect(loadLastTaskProjectId(storage)).toBe(17);
  });

  test.each([
    ["malformed JSON", "{"],
    ["zero", "0"],
    ["a negative number", "-2"],
    ["a fractional number", "2.5"],
    ["a non-numeric value", '"project"'],
  ])("rejects %s", (_name, stored) => {
    expect(loadLastTaskProjectId(memoryStorage(stored))).toBeUndefined();
  });

  test("returns no project when browser storage is unavailable", () => {
    expect(loadLastTaskProjectId()).toBeUndefined();
  });

  test("returns no project when storage reads fail", () => {
    expect(loadLastTaskProjectId({
      getItem: () => { throw new Error("storage disabled"); },
      setItem: () => {},
    })).toBeUndefined();
  });

  test("ignores storage write failures", () => {
    expect(saveLastTaskProjectId(17, {
      getItem: () => null,
      setItem: () => { throw new Error("quota exceeded"); },
    })).toBeUndefined();
  });

  test("ignores invalid project IDs when saving", () => {
    const storage = memoryStorage();

    saveLastTaskProjectId(0, storage);

    expect(loadLastTaskProjectId(storage)).toBeUndefined();
  });

  test("restores a remembered active project", () => {
    expect(activeTaskCreationProjectId([{ id: 12 }, { id: 17 }], 17)).toBe(17);
  });

  test("returns no project when the remembered project is not active", () => {
    expect(activeTaskCreationProjectId([{ id: 12 }], 17)).toBeUndefined();
  });

  test("saves, reloads, and resolves an active project", () => {
    const storage = memoryStorage();
    saveLastTaskProjectId(17, storage);

    const remembered = loadLastTaskProjectId(storage);

    expect(activeTaskCreationProjectId([{ id: 12 }, { id: 17 }], remembered)).toBe(17);
  });
});
