import { createResource, createSignal, For, onMount, Show, type JSX, type Resource } from "solid-js";
import { ActivityHeader, formatTime, InlineError, resourceState, runStateLabel } from "./activity";
import { Field, saveStateLabel, type SaveState, Toggle } from "./forms";
import { getJSON, HTTPError, sendJSON } from "./http";
import type { WorkflowSummary } from "./workflows";

function resourceSnapshot<T>(resource: Resource<T>): T | undefined {
  return resource.error ? undefined : resource();
}

type TriggerFilter = {
  source?: string;
  type?: string;
  action?: string;
  subject?: string;
  attributes?: Record<string, string>;
};

type TriggerRule = {
  id: string;
  revision: number;
  name: string;
  enabled: boolean;
  filter: TriggerFilter;
  workflowId: string;
  target: { kind: "fixed" | "subject" | "attribute"; value?: string };
  maxHop: number;
  maxOutstanding: number;
  admissionsPerHour: number;
};

type TriggerSchedule = {
  id: string;
  revision: number;
  name: string;
  enabled: boolean;
  cron: string;
  timezone: string;
  subject?: string;
  attributes?: Record<string, string[]>;
};

type TriggerRegistry = {
  schema: number;
  revision: number;
  updatedAt?: string;
  legacyRollbackIncompatible?: boolean;
  rules: TriggerRule[];
  schedules: TriggerSchedule[];
};

type TriggerRuleStatus = {
  ruleId: string;
  outstanding: number;
  admissionsLastHour: number;
};

type TriggerScheduleStatus = {
  scheduleId: string;
  last?: string;
  next?: string;
  skipped: number;
};

type TriggerInvocation = {
  id: string;
  eventId: string;
  ruleId: string;
  ruleRevision: number;
  workflowId: string;
  issueIdentifier: string;
  state: string;
  runId?: string;
  reason?: string;
  updatedAt: string;
};

type TriggerResponse = {
  registry: TriggerRegistry;
  settingsRevision: number;
  workflows: WorkflowSummary[];
  observedSources: string[];
  ruleStatus: TriggerRuleStatus[];
  scheduleStatus: TriggerScheduleStatus[];
  recentInvocations: TriggerInvocation[];
  protectedRoutes: { id: string; name: string; description: string; workflowId?: string; enabled: boolean; protected: boolean }[];
};

type TriggerSaveResult = { snapshot: TriggerResponse; conflict: boolean };
type SubjectFilterMode = "wildcard" | "absent" | "exact";

async function getTriggers(): Promise<TriggerResponse> {
  return getJSON<TriggerResponse>("/api/triggers", "Triggers request");
}

async function saveTriggers(candidate: TriggerRegistry): Promise<TriggerSaveResult> {
  try {
    return {
      snapshot: await sendJSON<TriggerResponse>("/api/triggers", "Trigger update", {
        method: "PUT",
        body: candidate,
      }),
      conflict: false,
    };
  } catch (error) {
    if (error instanceof HTTPError && error.status === 409) {
      return { snapshot: error.body as TriggerResponse, conflict: true };
    }
    throw error;
  }
}

async function saveProtectedFeedback(snapshot: TriggerResponse, workflowId: string): Promise<TriggerResponse> {
  return sendJSON<TriggerResponse>("/api/triggers/protected/linear-feedback", "Protected binding update", {
    method: "PUT",
    body: { expectedPolicyRevision: snapshot.settingsRevision, workflowId },
  });
}

export function TriggersPage(): JSX.Element {
  const [triggers] = createResource(getTriggers);
  const triggerSnapshot = (): TriggerResponse | undefined => resourceSnapshot(triggers);

  onMount(() => {
    document.title = "Triggers | Factory";
  });

  return (
    <main class="activity-page settings-page" id="main-content">
      <section class="activity-shell settings-shell" aria-labelledby="triggers-title">
        <ActivityHeader
          section="triggers"
          state={resourceState(triggers.loading, triggers.error)}
          label={triggers.error ? "Trigger registry unavailable" : "Admission policy"}
        />
        <Show
          when={triggerSnapshot()}
          fallback={
            <div class="settings-loading" aria-live="polite">
              <p class="section-label">Event admission</p>
              <h1 class="activity-title compact-title" id="triggers-title">
                {triggers.error ? "Triggers unavailable" : "Opening registry"}
              </h1>
              <Show when={triggers.error}>
                <InlineError message="The trigger registry could not be loaded." />
              </Show>
            </div>
          }
        >
          {(snapshot) => <TriggersEditor initial={snapshot()} />}
        </Show>
      </section>
    </main>
  );
}

function TriggersEditor(props: { initial: TriggerResponse }): JSX.Element {
  const [response, setResponse] = createSignal(structuredClone(props.initial));
  const [draft, setDraft] = createSignal(structuredClone(props.initial.registry));
  const [saveState, setSaveState] = createSignal<SaveState>("idle");
  const [message, setMessage] = createSignal("");
  const [pendingDelete, setPendingDelete] = createSignal("");
  const [broadConfirmed, setBroadConfirmed] = createSignal(false);
  const [protectedWorkflow, setProtectedWorkflow] = createSignal(
    props.initial.protectedRoutes.find((route) => route.id === "linear-feedback")?.workflowId ?? "",
  );
  const [protectedState, setProtectedState] = createSignal<SaveState>("idle");
  const [protectedMessage, setProtectedMessage] = createSignal("");
  const enabledWorkflows = (): WorkflowSummary[] =>
    response().workflows.filter((workflow) => workflow.enabled);

  function update(mutator: (registry: TriggerRegistry) => void): void {
    setDraft((current) => {
      const next = structuredClone(current);
      mutator(next);
      return next;
    });
    setSaveState("dirty");
    setMessage("Unsaved admission changes");
    setBroadConfirmed(false);
  }

  function addRule(source?: TriggerRule): void {
    update((next) => {
      const id = uniqueTriggerID("rule", [...next.rules.map((rule) => rule.id), ...next.schedules.map((schedule) => schedule.id)]);
      const workflow = enabledWorkflows()[0];
      next.rules.push(source ? {
        ...structuredClone(source),
        id,
        revision: 0,
        name: `${source.name} copy`,
      } : {
        id,
        revision: 0,
        name: "New event rule",
        enabled: false,
        filter: { attributes: {} },
        workflowId: workflow?.id ?? "",
        target: { kind: "subject" },
        maxHop: 4,
        maxOutstanding: 10,
        admissionsPerHour: 120,
      });
    });
  }

  function addSchedule(): void {
    update((next) => {
      const id = uniqueTriggerID("schedule", [...next.rules.map((rule) => rule.id), ...next.schedules.map((schedule) => schedule.id)]);
      next.schedules.push({
        id,
        revision: 0,
        name: "New schedule",
        enabled: false,
        cron: "0 8 * * *",
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
        attributes: {},
      });
    });
  }

  function remove(kind: "rule" | "schedule", id: string): void {
    const key = `${kind}:${id}`;
    if (pendingDelete() !== key) {
      setPendingDelete(key);
      return;
    }
    update((next) => {
      if (kind === "rule") {
        next.rules = next.rules.filter((rule) => rule.id !== id);
      } else {
        next.schedules = next.schedules.filter((schedule) => schedule.id !== id);
      }
    });
    setPendingDelete("");
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const problem = validateTriggerDraft(draft(), enabledWorkflows());
    if (problem) {
      setSaveState("failed");
      setMessage(problem);
      return;
    }
    const broadRules = draft().rules.filter((rule) => rule.enabled && broadTriggerRule(rule));
    if (broadRules.length > 0 && !broadConfirmed()) {
      setBroadConfirmed(true);
      setSaveState("dirty");
      setMessage(`Review broad scope for ${broadRules.map((rule) => rule.name).join(", ")}, then save again.`);
      return;
    }
    setSaveState("saving");
    setMessage("Saving one coordinated registry revision");
    try {
      const result = await saveTriggers(draft());
      setResponse(structuredClone(result.snapshot));
      setDraft(structuredClone(result.snapshot.registry));
      setBroadConfirmed(false);
      if (result.conflict) {
        setSaveState("conflict");
        setMessage("A newer registry revision was loaded. Review it before saving again.");
      } else {
        setSaveState("saved");
        setMessage(`Revision ${result.snapshot.registry.revision} saved`);
      }
    } catch (error) {
      setSaveState("failed");
      setMessage(error instanceof Error ? error.message : "Trigger update failed");
    }
  }

  const ruleStatus = (id: string): TriggerRuleStatus | undefined =>
    response().ruleStatus.find((status) => status.ruleId === id);
  const scheduleStatus = (id: string): TriggerScheduleStatus | undefined =>
    response().scheduleStatus.find((status) => status.scheduleId === id);

  async function updateProtectedFeedback(): Promise<void> {
    setProtectedState("saving");
    setProtectedMessage("Updating protected policy binding");
    try {
      const next = await saveProtectedFeedback(response(), protectedWorkflow());
      setResponse(structuredClone(next));
      setProtectedWorkflow(next.protectedRoutes.find((route) => route.id === "linear-feedback")?.workflowId ?? "");
      setProtectedState("saved");
      setProtectedMessage(`Policy revision ${next.settingsRevision} saved`);
    } catch (error) {
      setProtectedState(error instanceof HTTPError && error.status === 409 ? "conflict" : "failed");
      setProtectedMessage(error instanceof Error ? error.message : "Protected binding update failed");
    }
  }

  return (
    <>
      <div class="settings-hero trigger-hero">
        <p class="section-label">Event admission</p>
        <h1 class="activity-title compact-title" id="triggers-title">Triggers</h1>
        <p class="settings-intro">
          Match normalized wire events to pinned workflows. Every matching rule creates its own serialized invocation; schedules only produce events.
        </p>
        <dl class="settings-revision trigger-revision">
          <div><dt>Registry revision</dt><dd>{draft().revision}</dd></div>
          <div><dt>Configured rules</dt><dd>{draft().rules.length} / 32</dd></div>
          <div><dt>Schedules</dt><dd>{draft().schedules.length} / 32</dd></div>
          <div><dt>Compatibility</dt><dd>{draft().legacyRollbackIncompatible ? "Forward only" : "Legacy readable"}</dd></div>
        </dl>
      </div>

      <form class="settings-form trigger-form" onSubmit={submit}>
        <section class="settings-section" aria-labelledby="rules-title">
          <div class="settings-section-heading workflow-heading">
            <div>
              <h2 id="rules-title">Event rules</h2>
              <p>Omitted fields are wildcards. Exact attributes use AND semantics, and all matching rules fire in stable ID order.</p>
            </div>
            <button class="secondary-button" type="button" disabled={draft().rules.length >= 32} onClick={() => addRule()}>
              Add rule
            </button>
          </div>
          <Show when={draft().rules.length > 0} fallback={<TriggerEmpty title="No configured rules" detail="Add a disabled rule, define its scope, then enable it when the preview is right." />}>
            <div class="trigger-editor-list">
              <For each={draft().rules}>
                {(rule, index) => (
                  <RuleEditor
                    rule={rule}
                    status={ruleStatus(rule.id)}
                    workflows={enabledWorkflows()}
                    observedSources={response().observedSources}
                    pendingDelete={pendingDelete() === `rule:${rule.id}`}
                    onChange={(mutator) => update((next) => mutator(next.rules[index()]))}
                    onClone={() => addRule(rule)}
                    onRemove={() => remove("rule", rule.id)}
                  />
                )}
              </For>
            </div>
          </Show>
        </section>

        <section class="settings-section" aria-labelledby="schedules-title">
          <div class="settings-section-heading workflow-heading">
            <div>
              <h2 id="schedules-title">Cron schedules</h2>
              <p>Schedules emit <code>factory / cron / due</code>. They never select a workflow directly.</p>
            </div>
            <button class="secondary-button" type="button" disabled={draft().schedules.length >= 32} onClick={addSchedule}>
              Add schedule
            </button>
          </div>
          <Show when={draft().schedules.length > 0} fallback={<TriggerEmpty title="No schedules" detail="Add a five-field cron schedule when Factory should place a due event on the wire." />}>
            <div class="trigger-editor-list">
              <For each={draft().schedules}>
                {(schedule, index) => (
                  <ScheduleEditor
                    schedule={schedule}
                    status={scheduleStatus(schedule.id)}
                    pendingDelete={pendingDelete() === `schedule:${schedule.id}`}
                    onChange={(mutator) => update((next) => mutator(next.schedules[index()]))}
                    onRemove={() => remove("schedule", schedule.id)}
                  />
                )}
              </For>
            </div>
          </Show>
        </section>

        <section class="settings-section protected-section" aria-labelledby="protected-title">
          <div class="settings-section-heading">
            <h2 id="protected-title">Protected lifecycle routes</h2>
            <p>Configured rules are additive. Protected routes stay enabled; the feedback route can select any enabled published workflow.</p>
          </div>
          <div class="protected-route-list">
            <For each={response().protectedRoutes}>
              {(route) => (
                <article>
                  <span>Protected · always enabled</span>
                  <h3>{route.name}</h3>
                  <p>{route.description}</p>
                  <Show when={route.id === "linear-feedback"}>
                    <div class="protected-binding">
                      <Field label="Workflow">
                        <select value={protectedWorkflow()} onChange={(event) => { setProtectedWorkflow(event.currentTarget.value); setProtectedState("dirty"); setProtectedMessage("Binding change not saved"); }}>
                          <For each={enabledWorkflows()}>{(workflow) => <option value={workflow.id}>{workflow.name} · r{workflow.revision}</option>}</For>
                        </select>
                      </Field>
                      <button class="secondary-button" type="button" disabled={["idle", "saving", "saved"].includes(protectedState())} onClick={() => void updateProtectedFeedback()}>
                        {protectedState() === "saving" ? "Saving" : "Update binding"}
                      </button>
                      <small classList={{ failed: ["failed", "conflict"].includes(protectedState()) }} aria-live="polite">{protectedMessage()}</small>
                    </div>
                  </Show>
                </article>
              )}
            </For>
          </div>
        </section>

        <section class="settings-section" aria-labelledby="recent-title">
          <div class="settings-section-heading">
            <h2 id="recent-title">Recent routing outcomes</h2>
            <p>Safe routing summaries only. Payloads, commands, paths, and pinned workflow files stay private.</p>
          </div>
          <Show when={response().recentInvocations.length > 0} fallback={<TriggerEmpty title="No invocation has been admitted" detail="Matching events and visible suppression outcomes will appear here." />}>
            <div class="invocation-ledger" tabIndex={0}>
              <For each={response().recentInvocations}>
                {(invocation) => (
                  <article>
                    <strong>{invocation.issueIdentifier || invocation.eventId}</strong>
                    <span>{invocation.ruleId} · r{invocation.ruleRevision}</span>
                    <i class={`run-state ${invocation.state}`}>{runStateLabel(invocation.state)}</i>
                    <time datetime={invocation.updatedAt}>{formatTime(invocation.updatedAt)}</time>
                  </article>
                )}
              </For>
            </div>
          </Show>
        </section>

        <div class={`settings-save ${saveState()}`}>
          <div aria-live="polite" role={saveState() === "failed" ? "alert" : "status"}>
            <strong>{saveStateLabel(saveState())}</strong>
            <span>{message() || "No unsaved changes"}</span>
          </div>
          <button class="primary-button" type="submit" disabled={["idle", "saving", "saved"].includes(saveState())}>
            {triggerSaveButtonLabel(saveState(), broadConfirmed())}
          </button>
        </div>
      </form>

      <footer class="activity-footer settings-footer">
        <span>Every save is optimistic, coordinated, and revisioned.</span>
        <a class="text-link" href="/wire">Inspect the wire</a>
      </footer>
    </>
  );
}

function RuleEditor(props: {
  rule: TriggerRule;
  status?: TriggerRuleStatus;
  workflows: WorkflowSummary[];
  observedSources: string[];
  pendingDelete: boolean;
  onChange: (mutator: (rule: TriggerRule) => void) => void;
  onClone: () => void;
  onRemove: () => void;
}): JSX.Element {
  const [subjectSelection, setSubjectSelection] = createSignal(subjectFilterMode(props.rule.filter.subject));
  return (
    <article class="trigger-editor" aria-labelledby={`rule-${props.rule.id}`}>
      <header class="trigger-editor-header">
        <div>
          <span class="workflow-id">{props.rule.id} · revision {props.rule.revision || "new"}</span>
          <h3 id={`rule-${props.rule.id}`}>{props.rule.name || "Untitled rule"}</h3>
          <p class="scope-summary">{ruleScopeSummary(props.rule)}</p>
        </div>
        <div class="trigger-card-actions">
          <Toggle checked={props.rule.enabled} compact label={props.rule.enabled ? "Enabled" : "Disabled"} onChange={(enabled) => props.onChange((rule) => { rule.enabled = enabled; })} />
          <button class="text-button" type="button" onClick={props.onClone}>Clone</button>
          <button class="text-button danger-button" type="button" onClick={props.onRemove}>{props.pendingDelete ? "Confirm removal" : "Remove"}</button>
        </div>
      </header>
      <dl class="trigger-card-status">
        <div><dt>Outstanding</dt><dd>{props.status?.outstanding ?? 0} / {props.rule.maxOutstanding}</dd></div>
        <div><dt>Last hour</dt><dd>{props.status?.admissionsLastHour ?? 0} / {props.rule.admissionsPerHour}</dd></div>
        <div><dt>Hop ceiling</dt><dd>{props.rule.maxHop}</dd></div>
      </dl>
      <div class="trigger-field-grid identity-fields">
        <Field label="Stable ID" hint={props.rule.revision ? "IDs cannot change after creation." : "Lowercase letters, numbers, and hyphens."}>
          <input required readOnly={props.rule.revision > 0} pattern="[a-z0-9][a-z0-9-]{0,47}" maxlength={48} value={props.rule.id} onInput={(event) => props.onChange((rule) => { rule.id = event.currentTarget.value; })} />
        </Field>
        <Field label="Rule name"><input required maxlength={80} value={props.rule.name} onInput={(event) => props.onChange((rule) => { rule.name = event.currentTarget.value; })} /></Field>
      </div>
      <fieldset class="trigger-subsection">
        <legend>Exact event filter</legend>
        <div class="trigger-field-grid filter-fields">
          <Field label="Source" hint="Leave empty for any source.">
            <input list={`observed-trigger-sources-${props.rule.id}`} maxlength={48} pattern="[a-z0-9-]*" value={props.rule.filter.source ?? ""} onInput={(event) => props.onChange((rule) => { rule.filter.source = optional(event.currentTarget.value); })} />
          </Field>
          <datalist id={`observed-trigger-sources-${props.rule.id}`}><For each={props.observedSources}>{(source) => <option value={source} />}</For></datalist>
          <Field label="Type"><input maxlength={256} value={props.rule.filter.type ?? ""} onInput={(event) => props.onChange((rule) => { rule.filter.type = optional(event.currentTarget.value); })} /></Field>
          <Field label="Action"><input maxlength={256} value={props.rule.filter.action ?? ""} onInput={(event) => props.onChange((rule) => { rule.filter.action = optional(event.currentTarget.value); })} /></Field>
          <Field label="Subject mode">
            <select value={subjectSelection()} onChange={(event) => {
              const mode = event.currentTarget.value as SubjectFilterMode;
              setSubjectSelection(mode);
              props.onChange((rule) => { rule.filter.subject = subjectFilterValue(mode); });
            }}>
              <option value="wildcard">Any subject</option><option value="absent">Subject absent</option><option value="exact">Exact subject</option>
            </select>
          </Field>
          <Show when={subjectSelection() === "exact"}>
            <Field label="Exact subject"><input required maxlength={256} value={props.rule.filter.subject ?? ""} onInput={(event) => props.onChange((rule) => { rule.filter.subject = event.currentTarget.value; })} /></Field>
          </Show>
        </div>
        <ExactAttributeEditor values={props.rule.filter.attributes ?? {}} onChange={(attributes) => props.onChange((rule) => { rule.filter.attributes = attributes; })} />
      </fieldset>
      <fieldset class="trigger-subsection">
        <legend>Workflow and target</legend>
        <div class="trigger-field-grid target-fields">
          <Field label="Workflow"><select required value={props.rule.workflowId} onChange={(event) => props.onChange((rule) => { rule.workflowId = event.currentTarget.value; })}><For each={props.workflows}>{(workflow) => <option value={workflow.id}>{workflow.name}</option>}</For></select></Field>
          <Field label="Issue target">
            <select value={props.rule.target.kind} onChange={(event) => props.onChange((rule) => { rule.target = { kind: event.currentTarget.value as TriggerRule["target"]["kind"] }; })}>
              <option value="subject">Event subject</option><option value="attribute">One event attribute</option><option value="fixed">Fixed Linear issue</option>
            </select>
          </Field>
          <Show when={props.rule.target.kind !== "subject"}>
            <Field label={props.rule.target.kind === "fixed" ? "Linear issue" : "Attribute key"}>
              <input required maxlength={256} placeholder={props.rule.target.kind === "fixed" ? "ENG-40" : "issueIdentifier"} value={props.rule.target.value ?? ""} onInput={(event) => props.onChange((rule) => { rule.target.value = event.currentTarget.value; })} />
            </Field>
          </Show>
        </div>
      </fieldset>
      <fieldset class="trigger-subsection">
        <legend>Admission limits</legend>
        <div class="trigger-field-grid limit-fields">
          <Field label="Maximum hop"><input required type="number" min="1" max="8" value={props.rule.maxHop} onInput={(event) => props.onChange((rule) => { rule.maxHop = event.currentTarget.valueAsNumber; })} /></Field>
          <Field label="Outstanding"><input required type="number" min="1" max="100" value={props.rule.maxOutstanding} onInput={(event) => props.onChange((rule) => { rule.maxOutstanding = event.currentTarget.valueAsNumber; })} /></Field>
          <Field label="Admissions / hour"><input required type="number" min="1" max="10000" value={props.rule.admissionsPerHour} onInput={(event) => props.onChange((rule) => { rule.admissionsPerHour = event.currentTarget.valueAsNumber; })} /></Field>
        </div>
      </fieldset>
    </article>
  );
}

function ScheduleEditor(props: {
  schedule: TriggerSchedule;
  status?: TriggerScheduleStatus;
  pendingDelete: boolean;
  onChange: (mutator: (schedule: TriggerSchedule) => void) => void;
  onRemove: () => void;
}): JSX.Element {
  return (
    <article class="trigger-editor schedule-editor" aria-labelledby={`schedule-${props.schedule.id}`}>
      <header class="trigger-editor-header">
        <div><span class="workflow-id">{props.schedule.id} · revision {props.schedule.revision || "new"}</span><h3 id={`schedule-${props.schedule.id}`}>{props.schedule.name || "Untitled schedule"}</h3><p class="scope-summary">factory / cron / due · {props.schedule.timezone}</p></div>
        <div class="trigger-card-actions">
          <Toggle checked={props.schedule.enabled} compact label={props.schedule.enabled ? "Enabled" : "Disabled"} onChange={(enabled) => props.onChange((schedule) => { schedule.enabled = enabled; })} />
          <button class="text-button danger-button" type="button" onClick={props.onRemove}>{props.pendingDelete ? "Confirm removal" : "Remove"}</button>
        </div>
      </header>
      <dl class="trigger-card-status">
        <div><dt>Last due</dt><dd>{props.status?.last ? formatTime(props.status.last) : "Not emitted"}</dd></div>
        <div><dt>Next due</dt><dd>{props.status?.next ? formatTime(props.status.next) : "Inactive"}</dd></div>
        <div><dt>Skipped</dt><dd>{props.status?.skipped ?? 0}</dd></div>
      </dl>
      <div class="trigger-field-grid identity-fields">
        <Field label="Stable ID"><input required readOnly={props.schedule.revision > 0} pattern="[a-z0-9][a-z0-9-]{0,47}" maxlength={48} value={props.schedule.id} onInput={(event) => props.onChange((schedule) => { schedule.id = event.currentTarget.value; })} /></Field>
        <Field label="Schedule name"><input required maxlength={80} value={props.schedule.name} onInput={(event) => props.onChange((schedule) => { schedule.name = event.currentTarget.value; })} /></Field>
      </div>
      <div class="trigger-field-grid schedule-fields">
        <Field label="Five-field cron" hint="Minute hour day-of-month month day-of-week. No descriptors or embedded timezone."><input required maxlength={128} value={props.schedule.cron} onInput={(event) => props.onChange((schedule) => { schedule.cron = event.currentTarget.value; })} /></Field>
        <Field label="IANA timezone"><input required maxlength={128} placeholder="America/Los_Angeles" value={props.schedule.timezone} onInput={(event) => props.onChange((schedule) => { schedule.timezone = event.currentTarget.value; })} /></Field>
        <Field label="Optional subject"><input maxlength={256} value={props.schedule.subject ?? ""} onInput={(event) => props.onChange((schedule) => { schedule.subject = optional(event.currentTarget.value); })} /></Field>
      </div>
      <ContextAttributeEditor values={props.schedule.attributes ?? {}} onChange={(attributes) => props.onChange((schedule) => { schedule.attributes = attributes; })} />
    </article>
  );
}

function ExactAttributeEditor(props: { values: Record<string, string>; onChange: (values: Record<string, string>) => void }): JSX.Element {
  const entries = (): [string, string][] => Object.entries(props.values);
  return (
    <div class="attribute-editor">
      <div class="attribute-heading"><strong>Attribute membership</strong><button class="text-button" type="button" disabled={entries().length >= 32} onClick={() => props.onChange({ ...props.values, [uniqueAttributeKey(props.values)]: "" })}>Add attribute</button></div>
      <For each={entries()}>{([key, value], index) => <div class="attribute-row"><input aria-label="Attribute key" maxlength={256} value={key} onInput={(event) => props.onChange(renameAttribute(props.values, key, event.currentTarget.value))} /><input aria-label="Required attribute value" maxlength={256} value={value} onInput={(event) => props.onChange({ ...props.values, [key]: event.currentTarget.value })} /><button type="button" aria-label={`Remove attribute ${index() + 1}`} onClick={() => props.onChange(withoutAttribute(props.values, key))}>Remove</button></div>}</For>
    </div>
  );
}

function ContextAttributeEditor(props: { values: Record<string, string[]>; onChange: (values: Record<string, string[]>) => void }): JSX.Element {
  const entries = (): [string, string[]][] => Object.entries(props.values);
  return (
    <div class="attribute-editor">
      <div class="attribute-heading"><strong>Event context</strong><button class="text-button" type="button" disabled={entries().length >= 32} onClick={() => props.onChange({ ...props.values, [uniqueAttributeKey(props.values)]: [""] })}>Add context</button></div>
      <For each={entries()}>{([key, values], index) => <div class="attribute-row"><input aria-label="Context key" maxlength={256} value={key} onInput={(event) => props.onChange(renameAttribute(props.values, key, event.currentTarget.value))} /><input aria-label="Comma-separated context values" maxlength={512} value={values.join(", ")} onInput={(event) => props.onChange({ ...props.values, [key]: event.currentTarget.value.split(",").map((value) => value.trim()) })} /><button type="button" aria-label={`Remove context ${index() + 1}`} onClick={() => props.onChange(withoutAttribute(props.values, key))}>Remove</button></div>}</For>
    </div>
  );
}

function TriggerEmpty(props: { title: string; detail: string }): JSX.Element {
  return <div class="empty-state compact trigger-empty"><strong>{props.title}</strong><span>{props.detail}</span></div>;
}

function uniqueTriggerID(prefix: string, ids: string[]): string {
  const existing = new Set(ids);
  let sequence = 1;
  while (existing.has(`${prefix}-${sequence}`)) sequence += 1;
  return `${prefix}-${sequence}`;
}

function uniqueAttributeKey(values: Record<string, unknown>): string {
  let sequence = 1;
  while (`attribute-${sequence}` in values) sequence += 1;
  return `attribute-${sequence}`;
}

function renameAttribute<T>(values: Record<string, T>, oldKey: string, newKey: string): Record<string, T> {
  const next: Record<string, T> = {};
  for (const [key, value] of Object.entries(values) as [string, T][]) next[key === oldKey ? newKey : key] = value;
  return next;
}

function withoutAttribute<T>(values: Record<string, T>, removed: string): Record<string, T> {
  return Object.fromEntries(Object.entries(values).filter(([key]) => key !== removed)) as Record<string, T>;
}

function optional(value: string): string | undefined {
  return value === "" ? undefined : value;
}

function subjectFilterMode(subject: string | undefined): SubjectFilterMode {
  if (subject === undefined) {
    return "wildcard";
  }
  if (subject === "") {
    return "absent";
  }
  return "exact";
}

function subjectFilterValue(mode: SubjectFilterMode): string | undefined {
  if (mode === "wildcard") {
    return undefined;
  }
  if (mode === "absent") {
    return "";
  }
  return "ENG-40";
}

function triggerSaveButtonLabel(state: SaveState, broadConfirmed: boolean): string {
  if (broadConfirmed) {
    return "Confirm broad scope";
  }
  if (state === "saving") {
    return "Saving";
  }
  return "Save registry";
}

function broadTriggerRule(rule: TriggerRule): boolean {
  const filter = rule.filter;
  return (!filter.source && !filter.type && !filter.action && filter.subject === undefined && Object.keys(filter.attributes ?? {}).length === 0) ||
    filter.source === "telemetry" || filter.type === "telemetry" || filter.type === "lifecycle" || filter.type === "service" ||
    filter.type === "agent-record" || filter.type === "agent-run";
}

function ruleScopeSummary(rule: TriggerRule): string {
  const parts = [rule.filter.source || "any source", rule.filter.type || "any type", rule.filter.action || "any action"];
  if (rule.filter.subject !== undefined) parts.push(rule.filter.subject === "" ? "subject absent" : `subject ${rule.filter.subject}`);
  const attributes = Object.keys(rule.filter.attributes ?? {}).length;
  if (attributes) parts.push(`${attributes} attribute ${attributes === 1 ? "match" : "matches"}`);
  return parts.join(" / ");
}

function validateTriggerDraft(registry: TriggerRegistry, workflows: WorkflowSummary[]): string | undefined {
  const ids = new Set<string>();
  const idPattern = /^[a-z0-9][a-z0-9-]{0,47}$/;
  const workflowIDs = new Set(workflows.map((workflow) => workflow.id));
  for (const rule of registry.rules) {
    if (!idPattern.test(rule.id) || ids.has(rule.id)) return `Rule ID ${rule.id || "(empty)"} is invalid or duplicated.`;
    ids.add(rule.id);
    if (!rule.name.trim() || !workflowIDs.has(rule.workflowId)) return `Rule ${rule.id} needs a name and enabled workflow.`;
    if (rule.target.kind !== "subject" && !rule.target.value?.trim()) return `Rule ${rule.id} needs a target value.`;
    if (Object.keys(rule.filter.attributes ?? {}).some((key) => !key.trim())) return `Rule ${rule.id} has an empty attribute key.`;
  }
  for (const schedule of registry.schedules) {
    if (!idPattern.test(schedule.id) || ids.has(schedule.id)) return `Schedule ID ${schedule.id || "(empty)"} is invalid or duplicated.`;
    ids.add(schedule.id);
    if (!schedule.name.trim() || schedule.cron.trim().split(/\s+/).length !== 5 || schedule.cron.startsWith("@") || /(?:CRON_)?TZ=/.test(schedule.cron)) return `Schedule ${schedule.id} needs a name and standard five-field cron.`;
    try { new Intl.DateTimeFormat("en-US", { timeZone: schedule.timezone }).format(); } catch { return `Schedule ${schedule.id} has an invalid IANA timezone.`; }
    if (Object.keys(schedule.attributes ?? {}).some((key) => !key.trim())) return `Schedule ${schedule.id} has an empty context key.`;
  }
  return undefined;
}

