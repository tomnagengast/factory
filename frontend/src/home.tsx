import {
  createResource,
  onCleanup,
  onMount,
  Show,
  type JSX,
  type Resource,
} from "solid-js";
import {
  ActivityHeader,
  formatTime,
  resourceState,
  shortOID,
} from "./activity";
import type { AgentRun } from "./agent";
import { getJSON } from "./http";

type Health = {
  status: string;
  app: string;
  commit: string;
  tree: string;
  buildId: string;
  deploymentId: string;
  contractVersion: string;
  startedAt: string;
};

type ActivityEvent = {
  type: string;
  action: string;
  receivedAt: string;
};

type AgentRunSnapshot = {
  total: number;
  active: number;
  runs: AgentRun[];
};

type ActivitySnapshot = {
  status: string;
  total: number;
  lastReceivedAt: string | null;
  events: ActivityEvent[];
  agentRuns: AgentRunSnapshot;
};






const refreshIntervalMs = 2000;



async function getHealth(): Promise<Health> {
  return getJSON<Health>("/api/healthz", "Health check");
}

async function getActivity(): Promise<ActivitySnapshot> {
  return getJSON<ActivitySnapshot>("/api/home", "Home request");
}





function resourceSnapshot<T>(resource: Resource<T>): T | undefined {
  return resource.error ? undefined : resource();
}

export function HomePage(): JSX.Element {
  const [health] = createResource(getHealth);
  const healthSnapshot = (): Health | undefined => resourceSnapshot(health);

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
                ready: healthSnapshot()?.status === "ok",
                failed: Boolean(health.error),
              }}
            />
            <Show when={!health.loading} fallback={<span>Connecting</span>}>
              <span>
                {health.error
                  ? "Offline"
                  : `Systems online · ${shortOID(healthSnapshot()?.commit)} · contract ${healthSnapshot()?.contractVersion}`}
              </span>
            </Show>
          </div>
          <a class="text-link" href="/home">
            View activity
          </a>
        </footer>
      </section>
    </main>
  );
}

export function ActivityPage(): JSX.Element {
  const [activity, { refetch }] = createResource(getActivity);
  const activitySnapshot = (): ActivitySnapshot | undefined => resourceSnapshot(activity);

  onMount(() => {
    document.title = "Home | Factory";
    const timer = window.setInterval(() => void refetch(), refreshIntervalMs);
    onCleanup(() => window.clearInterval(timer));
  });

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="activity-title">
        <ActivityHeader
          section="home"
          state={resourceState(activity.loading, activity.error)}
          label={listenerLabel(activity.loading, activity.error, Boolean(activitySnapshot()))}
        />

        <div class="activity-hero overview-hero">
          <div>
            <p class="section-label">Factory telemetry</p>
            <h1 class="activity-title" id="activity-title">
              Home
            </h1>
          </div>
          <p class="activity-intro">
            A quiet overview of webhook traffic and autonomous issue loops. Open
            a dedicated workspace when you need the underlying detail.
          </p>
        </div>

        <dl class="activity-summary overview-summary">
          <div>
            <dt>Status</dt>
            <dd>{activity.error ? "Unavailable" : "Listening"}</dd>
          </div>
          <div>
            <dt>Verified deliveries</dt>
            <dd>{activitySnapshot()?.total ?? 0}</dd>
          </div>
          <div>
            <dt>Last received</dt>
            <dd>{formatTime(activitySnapshot()?.lastReceivedAt)}</dd>
          </div>
          <div>
            <dt>Agent runs</dt>
            <dd>{activitySnapshot()?.agentRuns.total ?? 0}</dd>
          </div>
          <div>
            <dt>Active loops</dt>
            <dd>{activitySnapshot()?.agentRuns.active ?? 0}</dd>
          </div>
        </dl>

        <section class="activity-destinations" aria-label="Detailed activity">
          <a class="destination destination-linear" href="/wire">
            <span class="destination-index">01 / Wire</span>
            <strong>Inspect the system event wire</strong>
            <p>
              Filter every retained source and event type, then open normalized
              metadata and available Linear payloads.
            </p>
            <span class="destination-meta">
              {activitySnapshot()?.events.filter((event) => !event.type.startsWith("github/"))
                .length ?? 0} recent events
            </span>
          </a>
          <a class="destination destination-agents" href="/agents">
            <span class="destination-index">02 / Agents</span>
            <strong>Follow autonomous work</strong>
            <p>
              Review loop state by task, then enter the authenticated live tmux
              observer for a specific run.
            </p>
            <span class="destination-meta">
              {activitySnapshot()?.agentRuns.active ?? 0} active now
            </span>
          </a>
        </section>

        <footer class="activity-footer">
          <span>Detailed activity requires operator authentication.</span>
          <a class="text-link" href="/">
            Back to Factory
          </a>
        </footer>
      </section>
    </main>
  );
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
