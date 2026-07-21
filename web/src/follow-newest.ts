export type NewestEdge = "start" | "end";

export type ScrollMetrics = {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
};

export type FollowState = {
  following: boolean;
  correcting: boolean;
};

export type StableAnchor = {
  id: string;
  top: number;
};

export type PrependTransaction =
  | { kind: "follow" }
  | { kind: "preserve"; anchor?: StableAnchor };

export const newestEdgeTolerance = 2;

export function isAtNewest(
  metrics: ScrollMetrics,
  edge: NewestEdge,
  tolerance = newestEdgeTolerance,
) {
  if (metrics.scrollHeight <= metrics.clientHeight + tolerance) return true;
  if (edge === "start") return metrics.scrollTop <= tolerance;
  return metrics.scrollHeight - metrics.clientHeight - metrics.scrollTop <= tolerance;
}

export function newestScrollTop(metrics: ScrollMetrics, edge: NewestEdge) {
  return edge === "start" ? 0 : Math.max(0, metrics.scrollHeight - metrics.clientHeight);
}

export function correctionScrollTop(
  following: boolean,
  metrics: ScrollMetrics,
  edge: NewestEdge,
) {
  return following ? newestScrollTop(metrics, edge) : undefined;
}

export type FollowAction =
  | { type: "scroll"; metrics: ScrollMetrics }
  | { type: "correction-started" }
  | { type: "correction-settled"; metrics: ScrollMetrics }
  | { type: "correction-interrupted"; metrics: ScrollMetrics };

export function reduceFollowState(
  state: FollowState,
  action: FollowAction,
  edge: NewestEdge,
): FollowState {
  switch (action.type) {
    case "scroll":
      return state.correcting
        ? state
        : { following: isAtNewest(action.metrics, edge), correcting: false };
    case "correction-started":
      return { ...state, correcting: true };
    case "correction-settled":
    case "correction-interrupted":
      return { following: isAtNewest(action.metrics, edge), correcting: false };
  }
}

export function beginPrependTransaction(
  current: PrependTransaction | undefined,
  following: boolean,
  anchor?: StableAnchor,
): PrependTransaction {
  return current ?? (following ? { kind: "follow" } : { kind: "preserve", anchor });
}

export function preservedScrollTop(scrollTop: number, anchor: StableAnchor, currentTop?: number) {
  return currentTop == null ? undefined : scrollTop + currentTop - anchor.top;
}

export type NewestFollower = {
  beforePrepend(): void;
  dispose(): void;
};

type FrameClock = Pick<Window, "requestAnimationFrame" | "cancelAnimationFrame">;

export type CorrectionScheduler = {
  request(): void;
  cancel(): void;
};

export function createCorrectionScheduler(
  clock: FrameClock,
  apply: (generation: number) => void,
  settle: (generation: number) => void,
): CorrectionScheduler {
  type FrameSlot = { frame?: number };
  let generation = 0;
  let applySlot: FrameSlot | undefined;
  let settleSlot: (FrameSlot & { generation: number }) | undefined;

  const cancelSlot = (slot: FrameSlot | undefined) => {
    if (slot?.frame != null) clock.cancelAnimationFrame(slot.frame);
  };

  const scheduleSettlement = (appliedGeneration: number) => {
    const slot: FrameSlot & { generation: number } = { generation: appliedGeneration };
    settleSlot = slot;
    slot.frame = clock.requestAnimationFrame(() => {
      if (settleSlot !== slot || generation !== appliedGeneration) return;
      slot.frame = clock.requestAnimationFrame(() => {
        if (settleSlot !== slot || generation !== appliedGeneration) return;
        settleSlot = undefined;
        settle(appliedGeneration);
      });
    });
  };

  return {
    request() {
      generation += 1;
      cancelSlot(settleSlot);
      settleSlot = undefined;
      if (applySlot) return;

      const slot: FrameSlot = {};
      applySlot = slot;
      slot.frame = clock.requestAnimationFrame(() => {
        if (applySlot !== slot) return;
        slot.frame = clock.requestAnimationFrame(() => {
          if (applySlot !== slot) return;
          applySlot = undefined;
          const appliedGeneration = generation;
          apply(appliedGeneration);
          if (generation === appliedGeneration && !applySlot) {
            scheduleSettlement(appliedGeneration);
          }
        });
      });
    },
    cancel() {
      generation += 1;
      cancelSlot(applySlot);
      cancelSlot(settleSlot);
      applySlot = undefined;
      settleSlot = undefined;
    },
  };
}

type NewestFollowerOptions = {
  edge: NewestEdge;
  viewport: HTMLElement | Window;
  content: HTMLElement;
  anchorRows?: () => Iterable<HTMLElement>;
};

function scrollElement(viewport: HTMLElement | Window) {
  if (viewport instanceof Window) {
    const element = viewport.document.scrollingElement;
    if (!(element instanceof HTMLElement)) throw new Error("Document has no scrolling element.");
    return element;
  }
  return viewport;
}

function metricsFor(element: HTMLElement): ScrollMetrics {
  return {
    scrollTop: element.scrollTop,
    scrollHeight: element.scrollHeight,
    clientHeight: element.clientHeight,
  };
}

function firstVisibleAnchor(rows: Iterable<HTMLElement>, viewportHeight: number): StableAnchor | undefined {
  for (const row of rows) {
    const id = row.dataset.eventId;
    const bounds = row.getBoundingClientRect();
    if (id && bounds.bottom > 0 && bounds.top < viewportHeight) return { id, top: bounds.top };
  }
}

const scrollingKeys = new Set([
  "ArrowDown", "ArrowUp", "End", "Home", "PageDown", "PageUp", " ",
]);

export function bindNewestFollower(options: NewestFollowerOptions): NewestFollower {
  const scroller = scrollElement(options.viewport);
  const scrollTarget: HTMLElement | Window = options.viewport;
  let state: FollowState = { following: true, correcting: false };
  let transaction: PrependTransaction | undefined;
  let disposed = false;

  const currentMetrics = () => metricsFor(scroller);

  const applyCorrection = () => {
    if (disposed) return;

    const pending = transaction;
    transaction = undefined;
    let nextScrollTop: number | undefined;
    const metrics = currentMetrics();

    if (pending?.kind === "follow") {
      nextScrollTop = newestScrollTop(metrics, options.edge);
    } else if (pending?.kind === "preserve" && pending.anchor) {
      const row = [...(options.anchorRows?.() ?? [])]
        .find((candidate) => candidate.dataset.eventId === pending.anchor?.id);
      nextScrollTop = preservedScrollTop(
        metrics.scrollTop,
        pending.anchor,
        row?.getBoundingClientRect().top,
      );
    } else if (!pending) {
      nextScrollTop = correctionScrollTop(state.following, metrics, options.edge);
    }

    if (nextScrollTop != null) scroller.scrollTop = nextScrollTop;
  };

  const correctionScheduler = createCorrectionScheduler(
    window,
    applyCorrection,
    () => {
      if (disposed) return;
      state = reduceFollowState(
        state,
        { type: "correction-settled", metrics: currentMetrics() },
        options.edge,
      );
    },
  );

  const scheduleCorrection = () => {
    if (disposed) return;
    state = reduceFollowState(state, { type: "correction-started" }, options.edge);
    correctionScheduler.request();
  };

  const onScroll = () => {
    if (transaction) return;
    state = reduceFollowState(state, { type: "scroll", metrics: currentMetrics() }, options.edge);
  };

  const correctionPending = () => transaction != null || state.correcting;

  const interruptCorrection = () => {
    if (!correctionPending()) return;
    correctionScheduler.cancel();
    transaction = undefined;
    state = reduceFollowState(
      state,
      { type: "correction-interrupted", metrics: currentMetrics() },
      options.edge,
    );
  };

  const onKeyDown = (event: Event) => {
    if (event instanceof KeyboardEvent && scrollingKeys.has(event.key)) interruptCorrection();
  };

  const onContentResize = () => {
    if (state.following || transaction) scheduleCorrection();
  };

  const onViewportResize = () => {
    if (!state.following && !transaction && !state.correcting) {
      state = reduceFollowState(state, { type: "scroll", metrics: currentMetrics() }, options.edge);
    }
    if (state.following || transaction) scheduleCorrection();
  };

  scrollTarget.addEventListener("scroll", onScroll, { passive: true });
  scrollTarget.addEventListener("wheel", interruptCorrection, { capture: true, passive: true });
  scrollTarget.addEventListener("touchmove", interruptCorrection, { capture: true, passive: true });
  scrollTarget.addEventListener("pointerdown", interruptCorrection, { capture: true, passive: true });
  scrollTarget.addEventListener("keydown", onKeyDown, true);

  const contentObserver = new ResizeObserver(onContentResize);
  contentObserver.observe(options.content);
  const viewportObserver = options.viewport instanceof Window
    ? undefined
    : new ResizeObserver(onViewportResize);
  if (viewportObserver) viewportObserver.observe(options.viewport as HTMLElement);
  else options.viewport.addEventListener("resize", onViewportResize, { passive: true });

  scheduleCorrection();

  return {
    beforePrepend() {
      if (transaction) return;
      const metrics = currentMetrics();
      const following = isAtNewest(metrics, options.edge);
      state = { following, correcting: state.correcting };
      const anchor = following || !options.anchorRows
        ? undefined
        : firstVisibleAnchor(options.anchorRows(), metrics.clientHeight);
      transaction = beginPrependTransaction(transaction, following, anchor);
      scheduleCorrection();
    },
    dispose() {
      disposed = true;
      correctionScheduler.cancel();
      contentObserver.disconnect();
      viewportObserver?.disconnect();
      scrollTarget.removeEventListener("scroll", onScroll);
      scrollTarget.removeEventListener("wheel", interruptCorrection, true);
      scrollTarget.removeEventListener("touchmove", interruptCorrection, true);
      scrollTarget.removeEventListener("pointerdown", interruptCorrection, true);
      scrollTarget.removeEventListener("keydown", onKeyDown, true);
      if (options.viewport instanceof Window) options.viewport.removeEventListener("resize", onViewportResize);
    },
  };
}
