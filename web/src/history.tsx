import { A, useParams } from "@solidjs/router";
import {
  createEffect,
  createMemo,
  createResource,
  createSignal,
  For,
  onCleanup,
  onMount,
  Show,
} from "solid-js";
import { errorMessage, get, liveRefetch, mutation } from "./api";
import { uniqueByID } from "./events";
import { bindNewestFollower } from "./follow-newest";
import {
  HISTORY_OVERVIEW_LIMIT,
  HISTORY_PAGE_SIZE,
  canLoadHistoryPage,
  historyPageRequestIsCurrent,
  historyPageURL,
  historyResourceLink,
  historyRunHref,
  historyStatuses,
  mergeHistoryRuns,
  observeHistorySentinel,
} from "./history-helpers";
import type {
  Event,
  HistoryDetail,
  HistoryListResponse,
  WorkflowRun,
  WorkflowRunStatus,
} from "./types";
import { date, Empty, Load, Markdown, PageHeader, SectionTitle } from "./ui";
import { workflowRunPhases, type WorkflowActivityBlock } from "./workflow-activity";
import { ActivityDisclosure, ActivityNarrative, toggledSet } from "./workflow-activity-view";

const PAGE_SIZE = 200;

export function History() {
  const [data, { refetch }] = createResource(async () => {
    const sections = await Promise.all(historyStatuses.map(async (section) => ({
      ...section,
      response: await get<HistoryListResponse>(historyPageURL(section.status, HISTORY_OVERVIEW_LIMIT)),
    })));
    return {
      sections: sections.map(({ response, ...section }) => ({
        ...section, runs: response.history.filter((run) => run.status === section.status),
      })),
      checkpointEventId: Math.min(...sections.map((section) => section.response.checkpointEventId)),
    };
  });
  liveHistoryRows(() => data()?.checkpointEventId, refetch);
  return (
    <div class="page">
      <PageHeader title="Workflow history"
        description="Running, waiting, failed, and completed workflow runs, newest first." />
      <Load data={data} error={() => data.error}>
        {(value) => <div class="history-sections">
          <For each={data()?.sections ?? value.sections}>{(section) => <section class="history-section">
            <SectionTitle title={section.label} href={section.href} />
            <Show when={section.runs.length} fallback={<Empty>No {section.label.toLowerCase()} runs.</Empty>}>
              <div class="rows">
                <For each={section.runs}>{(run) => <HistoryRow run={run} />}</For>
              </div>
            </Show>
          </section>}</For>
        </div>}
      </Load>
    </div>
  );
}

function HistoryRow(props: { run: WorkflowRun }) {
  return (
    <A class="history-row" href={historyRunHref(props.run.id)!}>
      <span class={`run-status ${props.run.status}`}>{props.run.status}</span>
      <span class="run-title"><strong>{props.run.workflowName || `Workflow ${props.run.workflowId}`}</strong>
        <small>Event #{props.run.sourceEventId} · Trigger #{props.run.triggerId}</small></span>
      <span class="id">#{props.run.id}</span>
      <time>{date(props.run.createdAt)}</time>
    </A>
  );
}

export function HistoryStatusPage(props: { status: WorkflowRunStatus; label: string }) {
  const [runs, setRuns] = createSignal<WorkflowRun[]>([]);
  const [checkpoint, setCheckpoint] = createSignal<number>();
  const [loaded, setLoaded] = createSignal(false);
  const [refreshing, setRefreshing] = createSignal(false);
  const [latestError, setLatestError] = createSignal("");
  const [hasOlder, setHasOlder] = createSignal(false);
  const [olderPending, setOlderPending] = createSignal(false);
  const [olderError, setOlderError] = createSignal("");
  const [olderSentinel, setOlderSentinel] = createSignal<HTMLDivElement>();
  let generation = 0;

  const replaceLatest = async () => {
    const requestGeneration = ++generation;
    setRefreshing(true);
    setLatestError("");
    setHasOlder(false);
    setOlderPending(false);
    setOlderError("");
    try {
      const response = await get<HistoryListResponse>(historyPageURL(props.status, HISTORY_PAGE_SIZE));
      if (requestGeneration !== generation) return;
      setRuns(response.history.filter((run) => run.status === props.status));
      setHasOlder(response.history.length === HISTORY_PAGE_SIZE);
      setCheckpoint(response.checkpointEventId);
      setLoaded(true);
    } catch (caught) {
      if (requestGeneration === generation) setLatestError(errorMessage(caught));
    } finally {
      if (requestGeneration === generation) setRefreshing(false);
    }
  };

  const loadOlder = () => {
    const cursor = runs().at(-1)?.id;
    if (!cursor || !canLoadHistoryPage({
      hasOlder: hasOlder(), pending: olderPending(), refreshing: refreshing(), error: Boolean(olderError()),
    })) return;
    const request = { generation, cursor };
    setOlderPending(true);
    void get<HistoryListResponse>(historyPageURL(props.status, HISTORY_PAGE_SIZE, cursor))
      .then((response) => {
        if (!historyPageRequestIsCurrent(request, generation, runs().at(-1)?.id)) return;
        setRuns((current) => mergeHistoryRuns(current, response.history, props.status));
        setHasOlder(response.history.length === HISTORY_PAGE_SIZE);
      })
      .catch((caught) => {
        if (request.generation === generation) setOlderError(errorMessage(caught));
      })
      .finally(() => {
        if (request.generation === generation) setOlderPending(false);
      });
  };

  const retryOlder = () => {
    setOlderError("");
    loadOlder();
  };

  createEffect(() => {
    const sentinel = olderSentinel();
    if (!sentinel) return;
    const disconnect = observeHistorySentinel(sentinel, loadOlder);
    onCleanup(disconnect);
  });
  onMount(() => void replaceLatest());
  liveHistoryRows(checkpoint, () => void replaceLatest());

  return (
    <div class="page">
      <PageHeader eyebrow="Workflow history" title={`${props.label} runs`}
        description={`Newest ${props.status} workflow runs first.`}
        actions={<A class="button" href="/history">Back to history</A>} />
      <Show when={loaded()} fallback={<div class="state">
        <Show when={latestError()} fallback="Loading…">
          <span class="form-error">{latestError()}</span>
          <button type="button" class="button quiet" onClick={() => void replaceLatest()}>Retry</button>
        </Show>
      </div>}>
        <Show when={runs().length} fallback={<Empty>No {props.status} runs.</Empty>}>
          <div class="rows"><For each={runs()}>{(run) => <HistoryRow run={run} />}</For></div>
        </Show>
        <Show when={latestError()}>
          <div class="history-loader">
            <span class="form-error">{latestError()}</span>
            <button type="button" class="button quiet" onClick={() => void replaceLatest()}>Retry latest page</button>
          </div>
        </Show>
        <Show when={hasOlder() || olderPending() || olderError()}>
          <div ref={(element) => setOlderSentinel(element)} class="history-loader" aria-live="polite">
            <Show when={olderPending()}>Loading {HISTORY_PAGE_SIZE} older runs…</Show>
            <Show when={olderError()}>
              <span class="form-error">{olderError()}</span>
              <button type="button" class="button quiet" onClick={retryOlder}>Retry</button>
            </Show>
          </div>
        </Show>
      </Show>
    </div>
  );
}

export function HistoryView() {
	const params = useParams();
	const [data, { refetch }] = createResource(() => get<HistoryDetail>(`/api/history/${params.item}`));
	const [olderEvents, setOlderEvents] = createSignal<HistoryDetail["events"]>([]);
	const [hasOlder, setHasOlder] = createSignal(false);
	const [expandedGroups, setExpandedGroups] = createSignal(new Set<string>());
	const [expandedEntries, setExpandedEntries] = createSignal(new Set<string>());
	const older = mutation();
	const toggleGroup = (id: string) => setExpandedGroups((current) => toggledSet(current, id));
	const toggleEntry = (id: string) => setExpandedEntries((current) => toggledSet(current, id));
	createEffect(() => {
		const latest = data()?.events;
		if (latest && olderEvents().length === 0) setHasOlder(latest.length === PAGE_SIZE);
	});
  const [historyContent, setHistoryContent] = createSignal<HTMLDivElement>();
  createEffect(() => {
    const content = historyContent();
    if (!content) return;
    const follower = bindNewestFollower({ edge: "end", viewport: window, content });
    onCleanup(() => follower.dispose());
  });
  liveRefetch([
    "workflow.run.event", "workflow.run.waiting", "workflow.run.resumed",
    "workflow.run.completed", "workflow.run.failed",
  ], refetch);
  return (
    <div ref={(element) => setHistoryContent(element)} class="page">
      <Load data={data} error={() => data.error}>
		{(value) => {
		  const current = () => {
			const latest = data() ?? value;
			return { ...latest, events: uniqueByID([...olderEvents(), ...latest.events]).sort((left, right) => left.id - right.id) };
		  };
          const phases = createMemo(() => workflowRunPhases(current().events));
          const resource = () => historyResourceLink(current().run);
          return <>
            <PageHeader eyebrow={`Run ${value.run.id}`}
              title={value.run.workflowName || `Workflow ${value.run.workflowId}`}
              description={`Started from event ${value.run.sourceEventId}`}
              actions={<A class="button" href="/history">Back to history</A>} />
            <div class="run-summary">
              <span class={`run-status ${current().run.status}`}>{current().run.status}</span>
              <A href={`/workflows/${value.run.workflowId}`}>Workflow #{value.run.workflowId}</A>
              <A href={`/events/${value.run.sourceEventId}`}>Event #{value.run.sourceEventId}</A>
              <Show when={resource()}>{(link) =>
                <A href={link().href}>{link().label}</A>}
              </Show>
              <span>Trigger #{value.run.triggerId}</span>
              <time>Started {date(value.run.createdAt)}</time>
              <time>Updated {date(current().run.updatedAt)}</time>
            </div>
            <Show when={phases().length} fallback={<Empty>
              {current().run.status === "running"
                ? "Waiting for the first workflow event…"
                : current().run.status === "waiting"
                  ? "Waiting for a human response on the task…"
                  : "No workflow events were recorded for this run."}
            </Empty>}>
              <div class="run-phases">
                <For each={phases()}>{(phase) => <section class="run-phase">
                  <header><h2>{phase.title}</h2><span>{phase.eventCount} {phase.eventCount === 1 ? "event" : "events"}</span></header>
                  <div class="run-events">
                    <For each={phase.blocks}>{(block) => <Show when={block.kind === "activity"}
                      fallback={<ActivityNarrative
                        block={block as Extract<WorkflowActivityBlock, { kind: "narrative" }>}
                        entryExpanded={(id) => expandedEntries().has(id)} onEntryToggle={toggleEntry} />}>
                      <ActivityDisclosure block={block as Extract<WorkflowActivityBlock, { kind: "activity" }>}
                        expanded={expandedGroups().has(block.id)} entryExpanded={(id) => expandedEntries().has(id)}
                        onToggle={() => toggleGroup(block.id)} onEntryToggle={toggleEntry} />
                    </Show>}</For>
                  </div>
                </section>}</For>
              </div>
			</Show>
			<Show when={hasOlder()}>
			  <button class="button quiet" disabled={older.pending()} onClick={() => older.run(async () => {
				const before = current().events[0]?.id;
				if (!before) return;
				const page = await get<HistoryDetail>(`/api/history/${params.item}?before=${before}`);
				setOlderEvents((events) => uniqueByID([...page.events, ...events]));
				setHasOlder(page.events.length === PAGE_SIZE);
			  })}>{older.pending() ? "Loading…" : "Load older run events"}</button>
			</Show>
			<Show when={current().run.output || current().run.error}>
              <section class="run-output"><SectionTitle title={current().run.error ? "Run error" : "Final result"} />
                <div classList={{ "run-event-content": true, "step-error": Boolean(current().run.error) }}>
                  <Markdown content={current().run.error || current().run.output} />
                </div>
              </section>
            </Show>
          </>;
        }}
      </Load>
    </div>
  );
}

function liveHistoryRows(checkpoint: () => number | undefined, refresh: () => unknown) {
  const types = [
    "workflow.run.started", "workflow.run.waiting", "workflow.run.resumed",
    "workflow.run.completed", "workflow.run.failed",
  ];
  let source: EventSource | undefined;
  createEffect(() => {
    const after = checkpoint();
    if (after == null) return;
    source?.close();
    source = new EventSource(`/api/events/stream?after=${after}`);
    source.onmessage = (message) => {
      const event = JSON.parse(message.data) as Event;
      if (types.includes(event.type)) refresh();
    };
  });
  onCleanup(() => source?.close());
}
