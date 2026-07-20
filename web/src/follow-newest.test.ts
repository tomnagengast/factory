import { describe, expect, test } from "bun:test";
import {
  beginPrependTransaction,
  correctionScrollTop,
  createCorrectionScheduler,
  isAtNewest,
  newestScrollTop,
  preservedScrollTop,
  reduceFollowState,
  type FollowState,
  type ScrollMetrics,
} from "./follow-newest";

class TestFrames {
  private nextID = 1;
  private callbacks = new Map<number, () => void>();
  private cancelled = new Set<number>();

  request = (callback: () => void) => {
    const id = this.nextID++;
    this.callbacks.set(id, callback);
    return id;
  };

  cancel = (id: number) => {
    this.cancelled.add(id);
  };

  active() {
    return [...this.callbacks.keys()].filter((id) => !this.cancelled.has(id));
  }

  run(id: number, force = false) {
    const callback = this.callbacks.get(id);
    if (!callback) throw new Error(`Frame ${id} does not exist.`);
    this.callbacks.delete(id);
    if (force || !this.cancelled.has(id)) callback();
  }

  runNext() {
    const [id] = this.active();
    if (id == null) throw new Error("No active frame exists.");
    this.run(id);
    return id;
  }
}

const viewport = (scrollTop: number, scrollHeight = 1000, clientHeight = 400): ScrollMetrics => ({
  scrollTop,
  scrollHeight,
  clientHeight,
});

describe("newest edge calculations", () => {
  test("recognizes exact and fractional start and end positions", () => {
    expect(isAtNewest(viewport(0), "start")).toBe(true);
    expect(isAtNewest(viewport(1.75), "start")).toBe(true);
    expect(isAtNewest(viewport(2.25), "start")).toBe(false);
    expect(isAtNewest(viewport(600), "end")).toBe(true);
    expect(isAtNewest(viewport(598.25), "end")).toBe(true);
    expect(isAtNewest(viewport(597.75), "end")).toBe(false);
  });

  test("treats empty and non-overflowing content as being at either edge", () => {
    expect(isAtNewest(viewport(0, 0, 400), "start")).toBe(true);
    expect(isAtNewest(viewport(0, 300, 400), "end")).toBe(true);
    expect(isAtNewest(viewport(0, 401.5, 400), "end")).toBe(true);
  });

  test("calculates the direct newest position for both order modes", () => {
    expect(newestScrollTop(viewport(200), "start")).toBe(0);
    expect(newestScrollTop(viewport(200), "end")).toBe(600);
    expect(newestScrollTop(viewport(0, 200, 400), "end")).toBe(0);
  });
});

describe("follow state", () => {
  const initial: FollowState = { following: true, correcting: false };

  test.each(["start", "end"] as const)("pauses and resumes at the %s edge", (edge) => {
    const away = edge === "start" ? viewport(100) : viewport(300);
    const newest = edge === "start" ? viewport(0) : viewport(600);
    const paused = reduceFollowState(initial, { type: "scroll", metrics: away }, edge);
    expect(paused).toEqual({ following: false, correcting: false });
    expect(reduceFollowState(paused, { type: "scroll", metrics: newest }, edge))
      .toEqual({ following: true, correcting: false });
  });

  test("ignores follower-driven scroll events until correction settles", () => {
    const correcting = reduceFollowState(initial, { type: "correction-started" }, "end");
    expect(reduceFollowState(correcting, { type: "scroll", metrics: viewport(100) }, "end"))
      .toEqual({ following: true, correcting: true });
    expect(reduceFollowState(correcting, {
      type: "correction-settled",
      metrics: viewport(600),
    }, "end")).toEqual(initial);
  });

  test("lets user input interrupt a pending correction", () => {
    const correcting = reduceFollowState(initial, { type: "correction-started" }, "end");
    expect(reduceFollowState(correcting, {
      type: "correction-interrupted",
      metrics: viewport(240),
    }, "end")).toEqual({ following: false, correcting: false });
  });

  test("settles an interrupted prepend correction at the current position", () => {
    const correcting = reduceFollowState(initial, { type: "correction-started" }, "start");
    expect(reduceFollowState(correcting, {
      type: "correction-interrupted",
      metrics: viewport(80),
    }, "start")).toEqual({ following: false, correcting: false });
  });

  test("follows from non-overflowing content after it begins overflowing", () => {
    let state = reduceFollowState(initial, {
      type: "scroll",
      metrics: viewport(0, 300, 400),
    }, "end");
    expect(correctionScrollTop(state.following, viewport(0, 700, 400), "end")).toBe(300);
    state = reduceFollowState(state, {
      type: "correction-settled",
      metrics: viewport(300, 700, 400),
    }, "end");
    expect(state.following).toBe(true);
  });

  test("corrects append growth only while following", () => {
    expect(correctionScrollTop(true, viewport(600, 1100, 400), "end")).toBe(700);
    expect(correctionScrollTop(false, viewport(300, 1100, 400), "end")).toBeUndefined();
  });
});

describe("prepend transactions", () => {
  test("follows a prepend at the top", () => {
    expect(beginPrependTransaction(undefined, true)).toEqual({ kind: "follow" });
  });

  test("preserves a stable row while away", () => {
    const anchor = { id: "42", top: 120 };
    expect(beginPrependTransaction(undefined, false, anchor)).toEqual({ kind: "preserve", anchor });
    expect(preservedScrollTop(500, anchor, 168)).toBe(548);
  });

  test("keeps the first snapshot when prepends coalesce", () => {
    const first = beginPrependTransaction(undefined, false, { id: "42", top: 120 });
    expect(beginPrependTransaction(first, false, { id: "43", top: 160 })).toBe(first);
  });

  test("does not guess when the stable row is missing", () => {
    const anchor = { id: "42", top: 120 };
    expect(preservedScrollTop(500, anchor)).toBeUndefined();
    expect(beginPrependTransaction(undefined, false)).toEqual({ kind: "preserve", anchor: undefined });
  });

  test("does not correct unrelated height changes while paused", () => {
    expect(correctionScrollTop(false, viewport(240, 1400, 400), "start")).toBeUndefined();
  });
});

describe("correction scheduler", () => {
  test("keeps the original apply deadline while requests coalesce", () => {
    const frames = new TestFrames();
    const applied: number[] = [];
    let latestHeight = 100;
    const scheduler = createCorrectionScheduler(
      frames,
      (generation) => applied.push(generation, latestHeight),
      () => {},
    );

    scheduler.request();
    const [firstFrame] = frames.active();
    latestHeight = 200;
    scheduler.request();
    expect(frames.active()).toEqual([firstFrame]);

    frames.runNext();
    const [secondFrame] = frames.active();
    latestHeight = 300;
    scheduler.request();
    expect(frames.active()).toEqual([secondFrame]);

    frames.runNext();
    expect(applied).toEqual([3, 300]);
  });

  test("ignores stale settlement callbacks after a newer request", () => {
    const frames = new TestFrames();
    const applied: number[] = [];
    const settled: number[] = [];
    const scheduler = createCorrectionScheduler(
      frames,
      (generation) => applied.push(generation),
      (generation) => settled.push(generation),
    );

    scheduler.request();
    frames.runNext();
    frames.runNext();
    const firstSettleFrame = frames.runNext();
    const [staleSettleFrame] = frames.active();

    scheduler.request();
    frames.run(staleSettleFrame, true);
    expect(settled).toEqual([]);

    frames.runNext();
    frames.runNext();
    frames.runNext();
    frames.runNext();
    expect(firstSettleFrame).toBeLessThan(staleSettleFrame);
    expect(applied).toEqual([1, 2]);
    expect(settled).toEqual([2]);
  });

  test("invalidates a cancelled apply slot even if its callback still runs", () => {
    const frames = new TestFrames();
    const applied: number[] = [];
    const scheduler = createCorrectionScheduler(
      frames,
      (generation) => applied.push(generation),
      () => {},
    );

    scheduler.request();
    const [staleApplyFrame] = frames.active();
    scheduler.cancel();
    scheduler.request();
    frames.run(staleApplyFrame, true);
    frames.runNext();
    frames.runNext();

    expect(applied).toEqual([3]);
  });
});
