import { createMemo, createResource, For, onMount, Show, type JSX, type Resource } from "solid-js";
import { ActivityHeader, formatTime, InlineError, LoadingRows, resourceState, runStateLabel, shortOID } from "./activity";
import { agentRunHref, type AgentActivityRun } from "./agent";
import { ActivityChart, type ActivityCount } from "./charts";
import { getJSON } from "./http";
import { usePolling } from "./poll";

function resourceSnapshot<T>(resource: Resource<T>): T | undefined {
  return resource.error ? undefined : resource();
}

type AgentActivitySnapshot = {
  total: number;
  active: number;
  runs: AgentActivityRun[];
};

async function getAgentActivity(): Promise<AgentActivitySnapshot> {
  return getJSON<AgentActivitySnapshot>(
    "/api/agents",
    "Agent activity request",
  );
}


export function AgentActivityPage(): JSX.Element {
  const [activity, { refetch }] = createResource(getAgentActivity);
  const activitySnapshot = (): AgentActivitySnapshot | undefined => resourceSnapshot(activity);
  const stateCounts = createMemo(() => countRunStates(activitySnapshot()?.runs ?? []));

  onMount(() => {
    document.title = "Agent activity | Factory";
  });
  usePolling(() => void refetch(), 5000);

  return (
    <main class="activity-page" id="main-content">
      <section class="activity-shell" aria-labelledby="agents-title">
        <ActivityHeader
          section="agents"
          state={resourceState(activity.loading, activity.error)}
          label={activity.error ? "Run store unavailable" : "Private run index"}
        />

        <div class="activity-hero detail-hero">
          <div>
            <p class="section-label">Autonomous delivery</p>
            <h1 class="activity-title compact-title" id="agents-title">
              Agents
            </h1>
          </div>
          <p class="activity-intro">
            Every retained Factory loop, addressed by its task identifier and start
            time. Enter a run to observe the live session or durable result.
          </p>
        </div>

        <dl class="activity-summary detail-summary">
          <div>
            <dt>Total runs</dt>
            <dd>{activitySnapshot()?.total ?? 0}</dd>
          </div>
          <div>
            <dt>Active loops</dt>
            <dd>{activitySnapshot()?.active ?? 0}</dd>
          </div>
          <div>
            <dt>Terminal runs</dt>
            <dd>{Math.max(0, (activitySnapshot()?.runs.length ?? 0) - (activitySnapshot()?.active ?? 0))}</dd>
          </div>
        </dl>

        <Show
          when={!activity.error}
          fallback={<InlineError message="Agent activity could not be loaded." />}
        >
          <section class="agent-overview-grid">
            <ActivityChart
              title="Runs by state"
              subtitle="Current retained window"
              items={stateCounts()}
            />
            <div class="run-pulse" aria-label="Current run status">
              <span class="section-label">Live capacity</span>
              <strong>{activitySnapshot()?.active ?? 0}</strong>
              <p>
                {(activitySnapshot()?.active ?? 0) === 1 ? "loop is" : "loops are"} active across the
                Factory runner.
              </p>
            </div>
          </section>

          <section class="run-feed dedicated-run-feed" aria-labelledby="run-feed-title">
            <div class="feed-heading">
              <h2 id="run-feed-title">Run ledger</h2>
              <span>Task context is authenticated</span>
            </div>

            <Show
              when={!activity.loading || Boolean(activitySnapshot())}
              fallback={<LoadingRows />}
            >
              <Show
                when={(activitySnapshot()?.runs.length ?? 0) > 0}
                fallback={
                  <div class="empty-state compact">
                    <strong>No agent run has been claimed.</strong>
                    <span>Apply the Factory label to a Linear issue.</span>
                  </div>
                }
              >
                <ol class="run-list private-run-list">
                  <For each={activitySnapshot()?.runs ?? []}>
                    {(run) => (
                      <li class="run-row private-run-row">
                        <Show
                          when={agentRunHref(run)}
                          fallback={<span class="run-link issue-link pending">{run.issueIdentifier}</span>}
                        >
                          {(href) => <a class="run-link issue-link" href={href()}>{run.issueIdentifier}</a>}
                        </Show>
                        <strong class={`run-state ${run.state}`}>
                          {runStateLabel(run.state)}
                        </strong>
                        <span title={runLifecycleDetail(run)}>
                          {runLifecycleDetail(run)}
                        </span>
                        <time datetime={run.lastAuthoritativeRefreshAt ?? run.startedAt ?? run.createdAt}>
                          {run.lastAuthoritativeRefreshAt ? "REFRESH " : "START "}
                          {formatTime(run.lastAuthoritativeRefreshAt ?? run.startedAt ?? run.createdAt)}
                        </time>
                      </li>
                    )}
                  </For>
                </ol>
              </Show>
            </Show>
          </section>
        </Show>

        <footer class="activity-footer">
          <span>Live pane output is authenticated, redacted, and read-only.</span>
          <a class="text-link" href="/home">
            Back to home
          </a>
        </footer>
      </section>
    </main>
  );
}

function countRunStates(runs: AgentActivityRun[]): ActivityCount[] {
  const counts = new Map<string, number>();
  for (const run of runs) {
    const label = runStateLabel(run.state);
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([label, count]) => ({ label, count }))
    .sort((left, right) => right.count - left.count || left.label.localeCompare(right.label));
}

function runLifecycleDetail(run: AgentActivityRun): string {
  const details: string[] = [];
  if (run.ready) {
    details.push(`PR #${run.ready.pullRequest}`);
    details.push(`HEAD ${shortOID(run.ready.verifiedHeadOid)}`);
  }
  if (run.mergeCommitOid) {
    details.push(`MERGE ${shortOID(run.mergeCommitOid)}`);
  }
  if (run.nextReconcileAt) {
    details.push(`NEXT ${formatTime(run.nextReconcileAt)}`);
  }
  if (run.resumeCount) {
    details.push(`RESUMES ${run.resumeCount}`);
  }
  if (run.reconcileFailures) {
    details.push(`REFRESH FAILURES ${run.reconcileFailures}`);
  }
  if (run.completion?.deploymentId) {
    details.push(`DEPLOY ${run.completion.deploymentId}`);
  }
  if (run.terminalRejection) {
    details.push(`REJECTED ${run.terminalRejection}`);
  }
  if (details.length === 0) {
    return run.attempts ? `ATTEMPT ${run.attempts}` : "QUEUED";
  }
  return details.join(" · ");
}
