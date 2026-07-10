import {
  createResource,
  For,
  onCleanup,
  onMount,
  Show,
  type JSX,
} from "solid-js";
import { render } from "solid-js/web";
import "./styles.css";

type Health = {
  status: string;
  app: string;
};

type ActivityEvent = {
  type: string;
  action: string;
  receivedAt: string;
};

type ActivitySnapshot = {
  status: string;
  total: number;
  lastReceivedAt: string | null;
  events: ActivityEvent[];
};

const timeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "medium",
});

async function getHealth(): Promise<Health> {
  const response = await fetch("/api/healthz");
  if (!response.ok) {
    throw new Error(`Health check failed with ${response.status}`);
  }
  return response.json() as Promise<Health>;
}

async function getActivity(): Promise<ActivitySnapshot> {
  const response = await fetch("/api/activity", { cache: "no-store" });
  if (!response.ok) {
    throw new Error(`Activity request failed with ${response.status}`);
  }
  return response.json() as Promise<ActivitySnapshot>;
}

function HomePage(): JSX.Element {
  const [health] = createResource(getHealth);

  return (
    <main class="home-page">
      <section class="home-shell" aria-labelledby="factory-title">
        <div class="eyebrow">
          <span class="mark" aria-hidden="true">
            F
          </span>
          <span>nags.cloud</span>
        </div>

        <div class="content">
          <h1 class="home-title" id="factory-title">
            Factory
          </h1>
          <p class="lede">
            The floor is empty. The machinery is warming up. Something useful
            will be built here.
          </p>
        </div>

        <footer class="home-footer">
          <div class="status" aria-live="polite">
            <span
              classList={{
                dot: true,
                ready: health()?.status === "ok",
                failed: Boolean(health.error),
              }}
            />
            <Show
              when={!health.loading}
              fallback={<span>Connecting</span>}
            >
              <span>{health.error ? "Offline" : "Systems online"}</span>
            </Show>
          </div>
          <a class="text-link" href="/activity">
            View activity
          </a>
        </footer>
      </section>
    </main>
  );
}

function ActivityPage(): JSX.Element {
  const [activity, { refetch }] = createResource(getActivity);

  onMount(() => {
    document.title = "Activity | Factory";
    const timer = window.setInterval(() => void refetch(), 5000);
    onCleanup(() => window.clearInterval(timer));
  });

  return (
    <main class="activity-page">
      <section class="activity-shell" aria-labelledby="activity-title">
        <header class="activity-header">
          <a class="brand-link" href="/" aria-label="Factory home">
            <span class="mark" aria-hidden="true">
              F
            </span>
            <span>Factory</span>
          </a>
          <div class="listener" aria-live="polite">
            <span
              classList={{
                dot: true,
                ready: activity()?.status === "listening",
                failed: Boolean(activity.error),
              }}
            />
            <span>
              {listenerLabel(
                activity.loading,
                activity.error,
                Boolean(activity()),
              )}
            </span>
          </div>
        </header>

        <div class="activity-hero">
          <div>
            <p class="section-label">Linear webhook</p>
            <h1 class="activity-title" id="activity-title">
              Activity
            </h1>
          </div>
          <p class="activity-intro">
            Verified delivery metadata from Linear. Payload content stays
            private.
          </p>
        </div>

        <dl class="activity-summary">
          <div>
            <dt>Status</dt>
            <dd>{activity.error ? "Unavailable" : "Listening"}</dd>
          </div>
          <div>
            <dt>Verified deliveries</dt>
            <dd>{activity()?.total ?? 0}</dd>
          </div>
          <div>
            <dt>Last received</dt>
            <dd>{formatTime(activity()?.lastReceivedAt)}</dd>
          </div>
        </dl>

        <section class="event-feed" aria-labelledby="event-feed-title">
          <div class="feed-heading">
            <h2 id="event-feed-title">Recent deliveries</h2>
            <span>Refreshes every 5 seconds</span>
          </div>

          <Show
            when={(activity()?.events.length ?? 0) > 0}
            fallback={
              <div class="empty-state">
                <strong>Waiting for the first signed delivery.</strong>
                <span>Events will appear here as Linear sends them.</span>
                <code>/api/webhooks/linear</code>
              </div>
            }
          >
            <ol class="event-list">
              <For each={activity()?.events ?? []}>
                {(event) => (
                  <li class="event-row">
                    <time datetime={event.receivedAt}>
                      {formatTime(event.receivedAt)}
                    </time>
                    <strong>{event.type}</strong>
                    <span>{event.action}</span>
                  </li>
                )}
              </For>
            </ol>
          </Show>
        </section>

        <footer class="activity-footer">
          <span>Only type, action, and receipt time are retained.</span>
          <a class="text-link" href="/">
            Back to Factory
          </a>
        </footer>
      </section>
    </main>
  );
}

function formatTime(value: string | null | undefined): string {
  if (!value) {
    return "No deliveries yet";
  }
  return timeFormatter.format(new Date(value));
}

function listenerLabel(
  loading: boolean,
  error: unknown,
  hasSnapshot: boolean,
): string {
  if (error) {
    return "Connection error";
  }
  if (loading && !hasSnapshot) {
    return "Connecting";
  }
  return "Listener online";
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("Root element not found");
}

render(
  () =>
    window.location.pathname.replace(/\/+$/, "") === "/activity" ? (
      <ActivityPage />
    ) : (
      <HomePage />
    ),
  root,
);
