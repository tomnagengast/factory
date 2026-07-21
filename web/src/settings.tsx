import { createMemo, createResource, createSignal, For } from "solid-js";
import { get, mutation, put } from "./api";
import type { SettingsDetail } from "./types";
import { FormFooter, Load, PageHeader } from "./ui";

export function SettingsPage() {
  const [data, { refetch }] = createResource(() => get<SettingsDetail>("/api/settings"));
  const action = mutation();
  return (
    <div class="page narrow">
      <PageHeader
        eyebrow="Factory"
        title="Settings"
        description="Harness selection applies to new work. Workflow capacity controls how many triggered runs may execute at once."
      />
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
    </div>
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
      <FormFooter pending={props.pending} error={props.error} label="Save settings" />
    </form>
  );
}
