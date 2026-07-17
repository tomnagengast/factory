import { createResource, createSignal, For, onMount, Show, type JSX } from "solid-js";
import { ActivityHeader, formatTime, InlineError, resourceState } from "./activity";
import { Field, saveStateLabel, type SaveState } from "./forms";
import { getJSON, HTTPError, sendJSON } from "./http";

type ProviderSettings = {
  model: string;
  effort: string;
};

type FactorySettings = {
  revision: number;
  updatedAt?: string;
  agents: {
    principal: ProviderSettings & { maxAttempts: number };
    codexChild: ProviderSettings;
    claudeChild: ProviderSettings;
  };
  runtime: {
    maxConcurrentRuns: number;
  };
};

type SettingsSaveResult = {
  snapshot: FactorySettings;
  conflict: boolean;
};


async function getSettings(): Promise<FactorySettings> {
  return getJSON<FactorySettings>("/api/settings", "Settings request");
}

async function saveSettings(
  candidate: FactorySettings,
): Promise<SettingsSaveResult> {
  try {
    return {
      snapshot: await sendJSON<FactorySettings>("/api/settings", "Settings update", {
        method: "PUT",
        body: candidate,
      }),
      conflict: false,
    };
  } catch (error) {
    if (error instanceof HTTPError && error.status === 409) {
      return { snapshot: error.body as FactorySettings, conflict: true };
    }
    throw error;
  }
}

export function SettingsPage(): JSX.Element {
  const [settings] = createResource(getSettings);
  const settingsSnapshot = (): FactorySettings | undefined => settings.error ? undefined : settings();

  onMount(() => {
    document.title = "Settings | Factory";
  });

  return (
    <main class="activity-page settings-page" id="main-content">
      <section class="activity-shell settings-shell" aria-labelledby="settings-title">
        <ActivityHeader
          section="settings"
          state={resourceState(settings.loading, settings.error)}
          label={settings.error ? "Settings unavailable" : "Private configuration"}
        />

        <Show
          when={settingsSnapshot()}
          fallback={
            <div class="settings-loading" aria-live="polite">
              <p class="section-label">Runtime policy</p>
              <h1 class="activity-title compact-title" id="settings-title">
                {settings.error ? "Settings unavailable" : "Opening settings"}
              </h1>
              <Show when={settings.error}>
                <InlineError message="Factory settings could not be loaded." />
              </Show>
            </div>
          }
        >
          {(snapshot) => <SettingsEditor initial={snapshot()} />}
        </Show>
      </section>
    </main>
  );
}

function SettingsEditor(props: { initial: FactorySettings }): JSX.Element {
  const [draft, setDraft] = createSignal(cloneSettings(props.initial));
  const [saveState, setSaveState] = createSignal<SaveState>("idle");
  const [message, setMessage] = createSignal("");

  function update(mutator: (value: FactorySettings) => void): void {
    setDraft((current) => {
      const next = cloneSettings(current);
      mutator(next);
      return next;
    });
    setSaveState("dirty");
    setMessage("Unsaved changes");
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    setSaveState("saving");
    setMessage("Saving revision");
    try {
      const result = await saveSettings(draft());
      setDraft(cloneSettings(result.snapshot));
      if (result.conflict) {
        setSaveState("conflict");
        setMessage("A newer revision was loaded. Review it before saving again.");
        return;
      }
      setSaveState("saved");
      setMessage(`Revision ${result.snapshot.revision} saved`);
    } catch (error) {
      setSaveState("failed");
      setMessage(error instanceof Error ? error.message : "Settings update failed");
    }
  }

  return (
    <>
      <div class="settings-hero">
        <p class="section-label">Runtime policy</p>
        <h1 class="activity-title compact-title" id="settings-title">
          Settings
        </h1>
        <p class="settings-intro">
          Change how new Factory runs begin and which provider settings they inherit.
          Active provider processes keep the snapshot they started with.
        </p>
        <dl class="settings-revision">
          <div>
            <dt>Revision</dt>
            <dd>{draft().revision}</dd>
          </div>
          <div>
            <dt>Last updated</dt>
            <dd>{draft().updatedAt ? formatTime(draft().updatedAt) : "Compiled defaults"}</dd>
          </div>
          <div><dt>Scope</dt><dd>Agents & capacity</dd></div>
        </dl>
      </div>

      <form class="settings-form" onSubmit={submit}>
        <section class="settings-section settings-routing-note" aria-labelledby="trigger-settings-title">
          <div class="settings-section-heading">
            <h2 id="trigger-settings-title">Admission moved to Triggers</h2>
            <p>Legacy trigger fields remain readable for rollback compatibility. Configure new event rules and cron schedules in the dedicated registry.</p>
          </div>
          <a class="secondary-button settings-route-link" href="/triggers">Open triggers</a>
        </section>

        <section class="settings-section settings-routing-note" aria-labelledby="workflow-settings-title">
          <div class="settings-section-heading">
            <h2 id="workflow-settings-title">Workflow authoring has its own workspace</h2>
            <p>Draft and publish Markdown procedures without mixing executable policy into provider configuration.</p>
          </div>
          <a class="secondary-button settings-route-link" href="/workflows">Open workflows</a>
        </section>

        <section class="settings-section" aria-labelledby="agent-settings-title">
          <div class="settings-section-heading">
            <h2 id="agent-settings-title">Agent launches</h2>
            <p>Model values become direct provider arguments. They are never interpreted by a shell.</p>
          </div>
          <div class="agent-settings-grid">
            <ProviderEditor
              title="Principal"
              provider="codex"
              value={draft().agents.principal}
              onChange={(value) => update((next) => { next.agents.principal.model = value.model; next.agents.principal.effort = value.effort; })}
            >
              <Field label="Attempt limit" hint="Includes resumable provider failures.">
                <input
                  type="number"
                  required
                  min="1"
                  max="5"
                  value={draft().agents.principal.maxAttempts}
                  onInput={(event) => update((next) => { next.agents.principal.maxAttempts = event.currentTarget.valueAsNumber; })}
                />
              </Field>
            </ProviderEditor>
            <ProviderEditor
              title="Codex children"
              provider="codex"
              value={draft().agents.codexChild}
              onChange={(value) => update((next) => { next.agents.codexChild = value; })}
            />
            <ProviderEditor
              title="Claude children"
              provider="claude"
              value={draft().agents.claudeChild}
              onChange={(value) => update((next) => { next.agents.claudeChild = value; })}
            />
          </div>
        </section>

        <section class="settings-section capacity-section" aria-labelledby="capacity-settings-title">
          <div class="settings-section-heading">
            <h2 id="capacity-settings-title">Capacity</h2>
            <p>The manager reads this limit at the start of each reconcile pass and never interrupts active runs.</p>
          </div>
          <Field label="Maximum concurrent runs" hint="Allowed range: 1 to 10.">
            <input
              type="number"
              required
              min="1"
              max="10"
              value={draft().runtime.maxConcurrentRuns}
              onInput={(event) => update((next) => { next.runtime.maxConcurrentRuns = event.currentTarget.valueAsNumber; })}
            />
          </Field>
        </section>

        <div class={`settings-save ${saveState()}`}>
          <div aria-live="polite" role={saveState() === "failed" ? "alert" : "status"}>
            <strong>{saveStateLabel(saveState())}</strong>
            <span>{message() || "No unsaved changes"}</span>
          </div>
          <button class="primary-button" type="submit" disabled={saveState() === "saving" || saveState() === "idle" || saveState() === "saved"}>
            {saveState() === "saving" ? "Saving" : "Save settings"}
          </button>
        </div>
      </form>

      <footer class="activity-footer settings-footer">
        <span>Routing, secrets, merge authority, and deployment gates stay locked in code.</span>
        <a class="text-link" href="/agents">View agent runs</a>
      </footer>
    </>
  );
}

function ProviderEditor(props: {
  title: string;
  provider: "codex" | "claude";
  value: ProviderSettings;
  onChange: (value: ProviderSettings) => void;
  children?: JSX.Element;
}): JSX.Element {
  const efforts = (): string[] => props.provider === "codex"
    ? ["low", "medium", "high", "xhigh"]
    : ["low", "medium", "high", "max"];
  return (
    <fieldset class="settings-group provider-editor">
      <legend>{props.title}</legend>
      <Field label="Model" hint="Letters, numbers, dots, slashes, colons, underscores, and hyphens.">
        <input
          required
          maxlength={64}
          pattern="[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}"
          value={props.value.model}
          onInput={(event) => props.onChange({ ...props.value, model: event.currentTarget.value })}
        />
      </Field>
      <Field label="Reasoning effort">
        <select
          value={props.value.effort}
          onChange={(event) => props.onChange({ ...props.value, effort: event.currentTarget.value })}
        >
          <For each={efforts()}>{(effort) => <option value={effort}>{effort}</option>}</For>
        </select>
      </Field>
      {props.children}
    </fieldset>
  );
}

function cloneSettings(value: FactorySettings): FactorySettings {
  return structuredClone(value);
}
