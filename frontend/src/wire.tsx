import { createMemo, createResource, createSignal, For, onCleanup, onMount, Show, type JSX, type Resource } from "solid-js";
import { ActivityHeader, formatTime, InlineError, LoadingRows, resourceState } from "./activity";
import { ActivityChart, type ActivityCount, Pagination } from "./charts";
import { getJSON } from "./http";

function resourceSnapshot<T>(resource: Resource<T>): T | undefined {
  return resource.error ? undefined : resource();
}

type WireEvent = {
  id: string;
  source: string;
  type: string;
  action: string;
  subject?: string;
  attributes?: Record<string, string[]>;
  channels?: string[];
  receivedAt: string;
};

type WireRecord = {
  sequence: number;
  channelSequences?: Record<string, number>;
  event: WireEvent;
};

type WireStatus = {
  total: number;
  dispatched: number;
  pending: number;
  rejectedTotal: number;
};

type WireSnapshot = {
  status: WireStatus;
  retained: number;
  matching: number;
  page: number;
  pageSize: number;
  pageCount: number;
  records: WireRecord[];
  sourceCounts: ActivityCount[];
  typeCounts: ActivityCount[];
  hourCounts: ActivityCount[];
};

type WireEventDetail = {
  record: WireRecord;
  payloadAvailable: boolean;
  payload?: unknown;
};

const activityPageSize = 25;

async function getWire(request: string): Promise<WireSnapshot> {
  return getJSON<WireSnapshot>(request, "Wire request");
}

async function getWireEvent(sequence: number): Promise<WireEventDetail> {
  return getJSON<WireEventDetail>(
    `/api/wire/${sequence}`,
    "Wire event request",
  );
}

export function WirePage(): JSX.Element {
  const [page, setPage] = createSignal(1);
  const [source, setSource] = createSignal("");
  const [eventType, setEventType] = createSignal("");
  const [selectedSequence, setSelectedSequence] = createSignal<number>();
  const request = createMemo(() => {
    const query = new URLSearchParams({
      page: String(page()),
      pageSize: String(activityPageSize),
    });
    if (source()) query.set("source", source());
    if (eventType()) query.set("type", eventType());
    return `/api/wire?${query.toString()}`;
  });
  const [activity, { refetch }] = createResource(request, getWire);
  const [eventDetail] = createResource(selectedSequence, getWireEvent);
  const activitySnapshot = (): WireSnapshot | undefined => resourceSnapshot(activity);
  const eventSnapshot = (): WireEventDetail | undefined => resourceSnapshot(eventDetail);

  onMount(() => {
    document.title = "System wire | Factory";
    const timer = window.setInterval(() => void refetch(), 5000);
    onCleanup(() => window.clearInterval(timer));
  });

  function changePage(nextPage: number): void {
    setSelectedSequence(undefined);
    setPage(nextPage);
  }

  function changeFilter(setter: (value: string) => void, value: string): void {
    setter(value);
    setSelectedSequence(undefined);
    setPage(1);
  }

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="wire-title">
        <ActivityHeader
          section="wire"
          state={resourceState(activity.loading, activity.error)}
          label={activity.error ? "Event wire unavailable" : "Private system wire"}
        />

        <div class="activity-hero detail-hero">
          <div>
            <p class="section-label">Authenticated telemetry</p>
            <h1 class="activity-title compact-title" id="wire-title">
              Wire
            </h1>
          </div>
          <p class="activity-intro">
            The journal-backed stream for Linear, GitHub, and Factory events.
            Unknown future event types remain inspectable as normalized records.
          </p>
        </div>

        <dl class="activity-summary detail-summary">
          <div>
            <dt>Retained events</dt>
            <dd>{activitySnapshot()?.retained ?? 0}</dd>
          </div>
          <div>
            <dt>Matching events</dt>
            <dd>{activitySnapshot()?.matching ?? 0}</dd>
          </div>
          <div>
            <dt>Pending dispatch</dt>
            <dd>{activitySnapshot()?.status.pending ?? 0}</dd>
          </div>
          <div>
            <dt>Rejected total</dt>
            <dd>{activitySnapshot()?.status.rejectedTotal ?? 0}</dd>
          </div>
        </dl>

        <form class="wire-filters" aria-label="Wire filters" onSubmit={(event) => event.preventDefault()}>
          <label>
            <span>Source</span>
            <select value={source()} onChange={(event) => changeFilter(setSource, event.currentTarget.value)}>
              <option value="">All sources</option>
              <For each={activitySnapshot()?.sourceCounts ?? []}>
                {(count) => <option value={count.label}>{count.label} ({count.count})</option>}
              </For>
            </select>
          </label>
          <label>
            <span>Event type</span>
            <select value={eventType()} onChange={(event) => changeFilter(setEventType, event.currentTarget.value)}>
              <option value="">All event types</option>
              <For each={activitySnapshot()?.typeCounts ?? []}>
                {(count) => <option value={count.label}>{count.label} ({count.count})</option>}
              </For>
            </select>
          </label>
        </form>

        <Show
          when={!activity.error}
          fallback={<InlineError message="The system wire could not be loaded." />}
        >
          <section class="chart-grid" aria-label="System wire charts">
            <ActivityChart
              title="Events by source"
              subtitle="Current retained window"
              items={activitySnapshot()?.sourceCounts ?? []}
            />
            <ActivityChart
              title="Recent hourly volume"
              subtitle="Up to twelve active UTC hours"
              items={activitySnapshot()?.hourCounts ?? []}
            />
          </section>

          <section class="linear-browser" aria-labelledby="event-browser-title">
            <div class="feed-heading browser-heading">
              <div>
                <h2 id="event-browser-title">Event ledger</h2>
                <span>Select a record to inspect normalized metadata</span>
              </div>
              <Pagination
                page={page()}
                pageCount={activitySnapshot()?.pageCount ?? 0}
                onChange={changePage}
              />
            </div>

            <div class="event-workspace">
              <div class="event-scroll" tabIndex={0} aria-label="System events">
                <Show
                  when={!activity.loading || Boolean(activitySnapshot())}
                  fallback={<LoadingRows />}
                >
                  <Show
                    when={(activitySnapshot()?.records.length ?? 0) > 0}
                    fallback={
                      <div class="empty-state compact">
                        <strong>No events match these filters.</strong>
                        <span>Change the filters or wait for the next journal record.</span>
                      </div>
                    }
                  >
                    <ol class="event-list selectable-events">
                      <For each={activitySnapshot()?.records ?? []}>
                        {(record) => (
                          <li>
                            <button
                              class="event-row event-button"
                              classList={{ selected: selectedSequence() === record.sequence }}
                              type="button"
                              aria-pressed={selectedSequence() === record.sequence}
                              onClick={() => setSelectedSequence(record.sequence)}
                            >
                              <time datetime={record.event.receivedAt}>
                                {formatTime(record.event.receivedAt)}
                              </time>
                              <strong>{record.event.source}</strong>
                              <span>{record.event.type}</span>
                              <i>#{record.sequence} · {record.event.action}</i>
                            </button>
                          </li>
                        )}
                      </For>
                    </ol>
                  </Show>
                </Show>
              </div>

              <aside class="payload-panel" aria-live="polite" aria-labelledby="payload-title">
                <Show
                  when={selectedSequence() !== undefined}
                  fallback={
                    <div class="payload-placeholder">
                      <span class="section-label">Normalized event</span>
                      <strong>Choose a record</strong>
                      <p>Journal metadata and any retained Linear body will open here.</p>
                    </div>
                  }
                >
                  <Show
                    when={!eventDetail.loading}
                    fallback={<div class="payload-placeholder"><strong>Loading payload</strong></div>}
                  >
                    <Show
                      when={eventSnapshot()}
                      fallback={<InlineError message="This event could not be loaded." />}
                    >
                      {(detail) => (
                        <>
                          <div class="payload-heading">
                            <div>
                              <span class="section-label">{detail().record.event.source} · #{detail().record.sequence}</span>
                              <h2 id="payload-title">{detail().record.event.type}</h2>
                            </div>
                            <time datetime={detail().record.event.receivedAt}>
                              {formatTime(detail().record.event.receivedAt)}
                            </time>
                          </div>
                          <dl class="wire-metadata">
                            <div><dt>Action</dt><dd>{detail().record.event.action}</dd></div>
                            <div><dt>Subject</dt><dd>{detail().record.event.subject || "None"}</dd></div>
                            <For each={attributeEntries(detail().record.event.attributes)}>
                              {([key, values]) => <div><dt>{key}</dt><dd>{values.join(", ")}</dd></div>}
                            </For>
                          </dl>
                          <Show
                            when={detail().payloadAvailable}
                            fallback={
                              <div class="payload-unavailable">
                                <strong>Payload not retained</strong>
                                <p>Only available Linear bodies are attached to normalized records.</p>
                              </div>
                            }
                          >
                            <pre class="payload-code" tabIndex={0}>
                              <code>{formatPayload(detail().payload)}</code>
                            </pre>
                          </Show>
                        </>
                      )}
                    </Show>
                  </Show>
                </Show>
              </aside>
            </div>
          </section>
        </Show>

        <footer class="activity-footer">
          <span>Normalized records are journal authority; payloads remain private sidecars.</span>
          <a class="text-link" href="/home">
            Back to home
          </a>
        </footer>
      </section>
    </main>
  );
}


function attributeEntries(attributes: Record<string, string[]> | undefined): [string, string[]][] {
  return Object.entries(attributes ?? {}).sort(([left], [right]) => left.localeCompare(right));
}

function formatPayload(value: unknown): string {
  return JSON.stringify(value, null, 2) ?? "null";
}
