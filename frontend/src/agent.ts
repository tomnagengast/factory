export type AgentRun = {
  id: string;
  state: string;
  attempts: number;
  duplicateTriggers: number;
  createdAt: string;
  updatedAt: string;
  startedAt?: string;
  finishedAt?: string;
};

type ReadyCheckpoint = {
  contractVersion: number;
  repository: string;
  pullRequest: number;
  baseBranch: string;
  headBranch: string;
  verifiedHeadOid: string;
  pullRequestUpdatedAt?: string;
  createdAt: string;
  validatedAt?: string;
};

type CompletionValidation = {
  accepted: boolean;
  intent: string;
  blocker?: string;
  state: string;
  reason: string;
  validatedAt: string;
  mergeCommitOid?: string;
  deploymentId?: string;
  deploymentCommit?: string;
};

export type AgentActivityRun = AgentRun & {
  task: {
    source: "factory" | "linear";
    providerId: string;
    identifier: string;
  };
  issueIdentifier: string;
  ready?: ReadyCheckpoint;
  mergeCommitOid?: string;
  lastGitHubCursor?: number;
  lastAuthoritativeRefreshAt?: string;
  nextReconcileAt?: string;
  reconcileFailures?: number;
  resumeCount?: number;
  terminalRejection?: string;
  completion?: CompletionValidation;
};

export function agentRunHref(run: AgentActivityRun): string | undefined {
  if (!run.startedAt) {
    return undefined;
  }
  return `/agents/${encodeURIComponent(run.issueIdentifier)}/${new Date(run.startedAt).getTime()}/run?source=${run.task.source}`;
}
