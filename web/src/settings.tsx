import { createEffect, createMemo, createResource, createSignal, For } from "solid-js";
import { get, mutation, put } from "./api";
import { parseReactionEmojis, reactionEmojisText } from "./reactions";
import type { CredentialEntryStatus, CredentialStatus, SettingsDetail } from "./types";
import { FormFooter, Load, PageHeader, SectionTitle } from "./ui";

export function SettingsPage() {
  const [data, { refetch }] = createResource(() => get<SettingsDetail>("/api/settings"));
  const [credentials, { refetch: refetchCredentials }] = createResource(() => get<CredentialStatus>("/api/credentials"));
  const action = mutation();
  const credentialAction = mutation();
  return (
    <div class="page narrow">
      <PageHeader
        eyebrow="Factory"
        title="Settings"
        description="Choose the agent harness, credentials, workflow capacity, and canned reactions used across tasks and comments."
      />
      <div class="settings-sections">
        <section>
          <SectionTitle title="Agent defaults" />
          <Load data={data} error={() => data.error}>
            {(value) => (
              <SettingsForm
                detail={value}
                pending={action.pending()}
                error={action.error()}
                onSave={(body) => action.run(async () => {
                  await put("/api/settings", body);
                  await refetch();
                })}
              />
            )}
          </Load>
        </section>
        <section>
          <SectionTitle title="API credentials" />
          <Load data={credentials} error={() => credentials.error}>
            {(value) => (
              <CredentialForm status={value} pending={credentialAction.pending()} error={credentialAction.error()}
                onSave={async (body) => {
                  let saved = false;
                  await credentialAction.run(async () => {
                    await put("/api/credentials", body);
                    await refetchCredentials();
                    saved = true;
                  });
                  return saved;
                }} />
            )}
          </Load>
        </section>
      </div>
    </div>
  );
}

function CredentialForm(props: {
  status: CredentialStatus;
  pending: boolean;
  error?: string;
  onSave: (body: unknown) => Promise<boolean>;
}) {
  const [openAIAPIKey, setOpenAIAPIKey] = createSignal("");
  const [anthropicAPIKey, setAnthropicAPIKey] = createSignal("");
  const describe = (status: CredentialEntryStatus) => {
    if (status.source === "saved") return "A saved API key will be used for new processes.";
    if (status.source === "environment") return "The API key from the server environment will be used.";
    return "No API key is configured. An existing CLI login may still work.";
  };
  return (
    <form class="form-panel" onSubmit={async (event) => {
      event.preventDefault();
      const body: { openaiApiKey?: string; anthropicApiKey?: string } = {};
      if (openAIAPIKey()) body.openaiApiKey = openAIAPIKey();
      if (anthropicAPIKey()) body.anthropicApiKey = anthropicAPIKey();
      if (await props.onSave(body)) {
        setOpenAIAPIKey("");
        setAnthropicAPIKey("");
      }
    }}>
      <label>OpenAI API key<input name="openaiApiKey" type="password" autocomplete="off"
        value={openAIAPIKey()} onInput={(event) => setOpenAIAPIKey(event.currentTarget.value)}
        placeholder={props.status.codex.configured ? "Enter a replacement key" : "Enter an API key"} />
        <small>{describe(props.status.codex)}</small>
      </label>
      <label>Anthropic API key<input name="anthropicApiKey" type="password" autocomplete="off"
        value={anthropicAPIKey()} onInput={(event) => setAnthropicAPIKey(event.currentTarget.value)}
        placeholder={props.status.claude.configured ? "Enter a replacement key" : "Enter an API key"} />
        <small>{describe(props.status.claude)}</small>
      </label>
      <p class="form-note">Keys are stored outside the event wire and are never returned by the API. Blank fields keep their current value.</p>
      <FormFooter pending={props.pending} error={props.error} label="Save credentials" />
    </form>
  );
}

function SettingsForm(props: {
  detail: SettingsDetail;
  pending: boolean;
  error?: string;
  onSave: (body: unknown) => void;
}) {
  const [harness, setHarness] = createSignal(props.detail.settings.harness);
  const [model, setModel] = createSignal(props.detail.settings.model);
  const [reasoning, setReasoning] = createSignal(props.detail.settings.reasoning);
  const [workflowCapacity, setWorkflowCapacity] = createSignal(props.detail.settings.workflowCapacity);
  const [reactionEmojis, setReactionEmojis] = createSignal(reactionEmojisText(props.detail.settings.reactionEmojis));
  createEffect(() => setReactionEmojis(reactionEmojisText(props.detail.settings.reactionEmojis)));
  const selectedHarness = createMemo(() =>
    props.detail.harnesses.find((option) => option.id === harness()) ?? props.detail.harnesses[0]);
  const selectedModel = createMemo(() =>
    selectedHarness()?.models.find((option) => option.id === model()) ?? selectedHarness()?.models[0]);
  const changeHarness = (value: string) => {
    const option = props.detail.harnesses.find((candidate) => candidate.id === value)!;
    setHarness(value);
    setModel(option.models[0].id);
    setReasoning(option.models[0].defaultReasoning);
  };
  const changeModel = (value: string) => {
    const option = selectedHarness()!.models.find((candidate) => candidate.id === value)!;
    setModel(value);
    setReasoning(option.defaultReasoning);
  };
  return (
    <form class="form-panel" onSubmit={(event) => {
      event.preventDefault();
      props.onSave({
        harness: harness(), model: model(), reasoning: reasoning(),
        workflowCapacity: workflowCapacity(),
        reactionEmojis: parseReactionEmojis(reactionEmojis()),
      });
    }}>
      <label>Harness<select name="harness" value={harness()}
        onChange={(event) => changeHarness(event.currentTarget.value)}>
        <For each={props.detail.harnesses}>{(option) => <option value={option.id}>{option.name}</option>}</For>
      </select></label>
      <label>Model<select name="model" value={model()}
        onChange={(event) => changeModel(event.currentTarget.value)}>
        <For each={selectedHarness()?.models}>{(option) => <option value={option.id}>{option.id}</option>}</For>
      </select></label>
      <label>Reasoning level<select name="reasoning" value={reasoning()}
        onChange={(event) => setReasoning(event.currentTarget.value)}>
        <For each={selectedModel()?.reasoning}>{(level) => <option value={level}>{level}</option>}</For>
      </select></label>
      <label>Workflow capacity<input name="workflowCapacity" type="number" min="0" max="10" step="1" required
        value={workflowCapacity()}
        onInput={(event) => setWorkflowCapacity(event.currentTarget.valueAsNumber)} />
        <small>Maximum triggered workflow runs at once. Set to 0 to pause new runs.</small>
      </label>
      <label>Canned reactions<textarea class="reaction-emojis-input" name="reactionEmojis" rows="6" required
        value={reactionEmojis()} onInput={(event) => setReactionEmojis(event.currentTarget.value)} />
        <small>Enter one value per line. Order controls reaction choices across tasks and comments.</small>
      </label>
      <FormFooter pending={props.pending} error={props.error} label="Save settings" />
    </form>
  );
}
