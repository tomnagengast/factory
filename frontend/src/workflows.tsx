import { createResource, createSignal, For, onCleanup, onMount, Show, type JSX, type Resource } from "solid-js";
import { ActivityHeader, InlineError, resourceState } from "./activity";
import { Field, Toggle } from "./forms";
import { getJSON, HTTPError, sendJSON } from "./http";

function resourceSnapshot<T>(resource: Resource<T>): T | undefined {
  return resource.error ? undefined : resource();
}

export type WorkflowSummary = {
  id: string;
  revision: number;
  name: string;
  enabled: boolean;
};

type WorkflowDefinition = WorkflowSummary & {
  markdown: string;
  updatedAt?: string;
};

type WorkflowDraft = {
  workflowId: string;
  revision: number;
  baseWorkflowRevision: number;
  name: string;
  enabled: boolean;
  markdown: string;
  updatedAt?: string;
};

type WorkflowReference = {
  kind: "protected" | "rule";
  id: string;
  name: string;
  enabled: boolean;
};

type WorkflowDocument = {
  workflowId: string;
  published?: WorkflowDefinition;
  draft: WorkflowDraft;
  savedDraft: boolean;
  draftConflict?: boolean;
  references: WorkflowReference[];
};

type WorkflowsResponse = {
  policyRevision: number;
  draftAvailable: boolean;
  draftError?: string;
  workflows: WorkflowDocument[];
};

async function getWorkflows(): Promise<WorkflowsResponse> {
  return getJSON<WorkflowsResponse>("/api/workflows", "Workflows request");
}

async function createWorkflowDraft(): Promise<WorkflowDraft> {
  return workflowRequest<WorkflowDraft>("/api/workflow-drafts", "POST");
}

async function saveWorkflowDraft(draft: WorkflowDraft): Promise<WorkflowDraft> {
  return workflowRequest<WorkflowDraft>(`/api/workflow-drafts/${encodeURIComponent(draft.workflowId)}`, "PUT", {
    expectedDraftRevision: draft.revision,
    expectedWorkflowRevision: draft.baseWorkflowRevision,
    name: draft.name,
    enabled: draft.enabled,
    markdown: draft.markdown,
  });
}

async function discardWorkflowDraft(draft: WorkflowDraft): Promise<void> {
  await workflowRequest<void>(`/api/workflow-drafts/${encodeURIComponent(draft.workflowId)}`, "DELETE", {
    expectedDraftRevision: draft.revision,
    expectedWorkflowRevision: draft.baseWorkflowRevision,
  });
}

async function publishWorkflowDraft(draft: WorkflowDraft, policyRevision: number): Promise<void> {
  await workflowRequest<void>(`/api/workflow-drafts/${encodeURIComponent(draft.workflowId)}/publish`, "POST", {
    expectedDraftRevision: draft.revision,
    expectedWorkflowRevision: draft.baseWorkflowRevision,
    expectedPolicyRevision: policyRevision,
  });
}

async function deletePublishedWorkflow(document: WorkflowDocument, policyRevision: number): Promise<void> {
  if (!document.published) return;
  await workflowRequest<void>(`/api/workflows/${encodeURIComponent(document.workflowId)}`, "DELETE", {
    expectedWorkflowRevision: document.published.revision,
    expectedPolicyRevision: policyRevision,
  });
}

class WorkflowConflict extends Error {
  constructor(readonly snapshot: WorkflowsResponse) {
    super("A newer workflow revision is available");
  }
}

async function workflowRequest<T>(url: string, method: string, body?: unknown): Promise<T> {
  try {
    return await sendJSON<T>(url, "Workflow request", { method, body });
  } catch (error) {
    if (error instanceof HTTPError && error.status === 409) {
      throw new WorkflowConflict(error.body as WorkflowsResponse);
    }
    throw error;
  }
}


type WorkflowEditorState = "published" | "unpublished" | "dirty" | "saving" | "conflict" | "failed" | "invalid";

export function WorkflowsPage(): JSX.Element {
  const [workflows] = createResource(getWorkflows);
  const workflowSnapshot = (): WorkflowsResponse | undefined => resourceSnapshot(workflows);

  onMount(() => {
    document.title = "Workflows | Factory";
  });

  return (
    <main class="activity-page settings-page" id="main-content">
      <section class="activity-shell settings-shell" aria-labelledby="workflows-title">
        <ActivityHeader
          section="workflows"
          state={resourceState(workflows.loading, workflows.error)}
          label={workflows.error ? "Workflow policy unavailable" : "Markdown workflow policy"}
        />
        <Show
          when={workflowSnapshot()}
          fallback={
            <div class="settings-loading" aria-live="polite">
              <p class="section-label">Procedural policy</p>
              <h1 class="activity-title compact-title" id="workflows-title">
                {workflows.error ? "Workflows unavailable" : "Opening workflows"}
              </h1>
              <Show when={workflows.error}><InlineError message="Published workflows could not be loaded." /></Show>
            </div>
          }
        >
          {(snapshot) => <WorkflowsEditor initial={snapshot()} />}
        </Show>
      </section>
    </main>
  );
}

function WorkflowsEditor(props: { initial: WorkflowsResponse }): JSX.Element {
  const [catalog, setCatalog] = createSignal(structuredClone(props.initial));
  const [selectedID, setSelectedID] = createSignal(props.initial.workflows[0]?.workflowId ?? "");
  const initialDocument = props.initial.workflows[0];
  const [local, setLocal] = createSignal<WorkflowDraft>(structuredClone(initialDocument?.draft ?? {
    workflowId: "", revision: 0, baseWorkflowRevision: 0, name: "", enabled: false, markdown: "",
  }));
  const [editorState, setEditorState] = createSignal<WorkflowEditorState>(workflowDocumentState(initialDocument));
  const [message, setMessage] = createSignal("Published policy is unchanged");
  const [localUnacknowledged, setLocalUnacknowledged] = createSignal(false);
  let saveTimer: number | undefined;
  let saving = false;
  let saveQueued = false;

  const selectedDocument = (): WorkflowDocument | undefined =>
    catalog().workflows.find((document) => document.workflowId === selectedID());

  onMount(() => {
    const warn = (event: BeforeUnloadEvent): void => {
      if (!localUnacknowledged()) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    onCleanup(() => {
      window.removeEventListener("beforeunload", warn);
      if (saveTimer !== undefined) window.clearTimeout(saveTimer);
    });
  });

  function selectWorkflow(id: string): void {
    if (id === selectedID()) return;
    if (localUnacknowledged() && !window.confirm("Discard local edits that have not reached the draft store?")) return;
    const document = catalog().workflows.find((candidate) => candidate.workflowId === id);
    if (!document) return;
    if (saveTimer !== undefined) window.clearTimeout(saveTimer);
    setSelectedID(id);
    setLocal(structuredClone(document.draft));
    setLocalUnacknowledged(false);
    setEditorState(workflowDocumentState(document));
    setMessage(document.savedDraft ? "Saved draft loaded" : "Editing the published revision");
  }

  function edit(mutator: (draft: WorkflowDraft) => void): void {
    setLocal((current) => {
      const next = structuredClone(current);
      mutator(next);
      return next;
    });
    setLocalUnacknowledged(true);
    const problem = validateWorkflowDraft(local());
    if (problem) {
      setEditorState("invalid");
      setMessage(problem);
      return;
    }
    setEditorState("dirty");
    setMessage("Local edits will autosave shortly");
    if (saveTimer !== undefined) window.clearTimeout(saveTimer);
    saveTimer = window.setTimeout(() => void autosave(), 700);
  }

  async function autosave(): Promise<void> {
    if (saving) {
      saveQueued = true;
      return;
    }
    const problem = validateWorkflowDraft(local());
    if (problem) {
      setEditorState("invalid");
      setMessage(problem);
      return;
    }
    saving = true;
    const captured = structuredClone(local());
    setEditorState("saving");
    setMessage("Saving private draft");
    try {
      const saved = await saveWorkflowDraft(captured);
      setCatalog((current) => ({
        ...current,
        workflows: current.workflows.map((document) => document.workflowId === saved.workflowId
          ? { ...document, draft: structuredClone(saved), savedDraft: true, draftConflict: false }
          : document),
      }));
      const unchanged = workflowEditableEqual(local(), captured);
      setLocal((current) => ({ ...current, revision: saved.revision, baseWorkflowRevision: saved.baseWorkflowRevision, updatedAt: saved.updatedAt }));
      if (unchanged) {
        setLocalUnacknowledged(false);
        const published = selectedDocument()?.published;
        const current = { ...saved };
        setEditorState(published && workflowPublishedEqual(published, current) ? "published" : "unpublished");
        setMessage(published && workflowPublishedEqual(published, current) ? "Draft matches the published revision" : "Draft saved · publication required");
      } else {
        saveQueued = true;
      }
    } catch (error) {
      if (error instanceof WorkflowConflict) {
        setCatalog(structuredClone(error.snapshot));
        setEditorState("conflict");
        setMessage("A newer server revision exists. Local text has been preserved.");
      } else {
        setEditorState("failed");
        setMessage(error instanceof Error ? error.message : "Draft autosave failed");
      }
    } finally {
      saving = false;
      if (saveQueued) {
        saveQueued = false;
        void autosave();
      }
    }
  }

  async function refresh(preferredID = selectedID()): Promise<void> {
    const next = await getWorkflows();
    setCatalog(structuredClone(next));
    const document = next.workflows.find((candidate) => candidate.workflowId === preferredID) ?? next.workflows[0];
    setSelectedID(document?.workflowId ?? "");
    setLocal(structuredClone(document?.draft ?? { workflowId: "", revision: 0, baseWorkflowRevision: 0, name: "", enabled: false, markdown: "" }));
    setLocalUnacknowledged(false);
    setEditorState(workflowDocumentState(document));
  }

  async function createDraft(): Promise<void> {
    try {
      const created = await createWorkflowDraft();
      await refresh(created.workflowId);
      setMessage("Disabled draft created");
    } catch (error) {
      setEditorState("failed");
      setMessage(error instanceof Error ? error.message : "Workflow creation failed");
    }
  }

  async function publish(): Promise<void> {
    if (localUnacknowledged() || !selectedDocument()?.savedDraft) return;
    setEditorState("saving");
    setMessage("Publishing the exact saved draft");
    try {
      await publishWorkflowDraft(local(), catalog().policyRevision);
      await refresh(local().workflowId);
      setEditorState("published");
      setMessage("Published for later admissions");
    } catch (error) {
      if (error instanceof WorkflowConflict) setCatalog(structuredClone(error.snapshot));
      setEditorState(error instanceof WorkflowConflict ? "conflict" : "failed");
      setMessage(error instanceof Error ? error.message : "Workflow publish failed");
    }
  }

  async function discard(): Promise<void> {
    const document = selectedDocument();
    if (!document) return;
    if (localUnacknowledged() && !window.confirm("Discard local edits and the saved draft?")) return;
    try {
      if (document.savedDraft) await discardWorkflowDraft(local());
      await refresh(document.workflowId);
      setMessage(document.published ? "Draft discarded · published revision restored" : "Draft discarded");
    } catch (error) {
      if (error instanceof WorkflowConflict) setCatalog(structuredClone(error.snapshot));
      setEditorState(error instanceof WorkflowConflict ? "conflict" : "failed");
      setMessage(error instanceof Error ? error.message : "Draft discard failed");
    }
  }

  async function duplicateLocal(): Promise<void> {
    try {
      const created = await createWorkflowDraft();
      const copied = await saveWorkflowDraft({ ...created, name: `${local().name} copy`, enabled: false, markdown: local().markdown });
      await refresh(copied.workflowId);
      setMessage("Local text duplicated into a disabled draft");
    } catch (error) {
      setEditorState("failed");
      setMessage(error instanceof Error ? error.message : "Workflow duplication failed");
    }
  }

  async function deleteWorkflow(): Promise<void> {
    const document = selectedDocument();
    if (!document?.published || !window.confirm(`Delete published workflow ${document.published.name}?`)) return;
    try {
      await deletePublishedWorkflow(document, catalog().policyRevision);
      await refresh("");
      setMessage("Published workflow deleted");
    } catch (error) {
      if (error instanceof WorkflowConflict) setCatalog(structuredClone(error.snapshot));
      setEditorState(error instanceof WorkflowConflict ? "conflict" : "failed");
      setMessage(error instanceof Error ? error.message : "Workflow deletion failed");
    }
  }

  return (
    <>
      <div class="settings-hero workflow-hero">
        <p class="section-label">Procedural policy</p>
        <h1 class="activity-title compact-title" id="workflows-title">Workflows</h1>
        <p class="settings-intro">Write procedures as Markdown notes. Drafts autosave privately; only an explicit publish changes later Factory admissions.</p>
        <dl class="settings-revision workflow-revision">
          <div><dt>Policy revision</dt><dd>{catalog().policyRevision}</dd></div>
          <div><dt>Documents</dt><dd>{catalog().workflows.length} / 8</dd></div>
          <div><dt>Authoring</dt><dd>{catalog().draftAvailable ? "Available" : "Read only"}</dd></div>
        </dl>
      </div>

      <Show when={catalog().draftError}><InlineError message={catalog().draftError ?? "Draft store unavailable"} /></Show>
      <div class="workflow-workspace">
        <aside class="workflow-index" aria-label="Workflow documents">
          <div class="workflow-index-heading">
            <strong>Documents</strong>
            <button class="text-button" type="button" disabled={!catalog().draftAvailable || catalog().workflows.length >= 8} onClick={() => void createDraft()}>New</button>
          </div>
          <For each={catalog().workflows}>
            {(document) => (
              <button type="button" classList={{ selected: document.workflowId === selectedID() }} onClick={() => selectWorkflow(document.workflowId)}>
                <strong>{document.draft.name || document.published?.name || "Untitled"}</strong>
                <span>{document.published ? `Published r${document.published.revision}` : "Draft only"}</span>
                <i>{document.draftConflict ? "Conflict" : document.savedDraft ? "Saved draft" : "Published"}</i>
              </button>
            )}
          </For>
        </aside>

        <Show when={selectedDocument()} fallback={<div class="workflow-empty"><strong>No workflow document</strong><p>Create a disabled draft to begin.</p></div>}>
          {(document) => (
            <section class="workflow-note" aria-labelledby="workflow-note-title">
              <header class="workflow-note-header">
                <div>
                  <span class="workflow-id">{document().workflowId}</span>
                  <h2 id="workflow-note-title">{local().name || "Untitled workflow"}</h2>
                </div>
                <Toggle checked={local().enabled} compact label={local().enabled ? "Enabled on publish" : "Disabled on publish"} onChange={(enabled) => edit((draft) => { draft.enabled = enabled; })} />
              </header>
              <div class="workflow-note-meta">
                <Field label="Name"><input maxlength={80} value={local().name} onInput={(event) => edit((draft) => { draft.name = event.currentTarget.value; })} /></Field>
                <Field label="Stable ID" hint="IDs are server assigned and immutable."><input readOnly value={local().workflowId} /></Field>
              </div>
              <label class="workflow-markdown-field">
                <span>Markdown procedure</span>
                <textarea spellcheck={false} value={local().markdown} onInput={(event) => edit((draft) => { draft.markdown = event.currentTarget.value; })} />
                <small>{new TextEncoder().encode(local().markdown).length.toLocaleString()} / 131,072 bytes</small>
              </label>
              <Show when={document().references.length > 0}>
                <div class="workflow-references"><strong>Live references</strong><For each={document().references}>{(reference) => <span>{reference.kind === "protected" ? "Protected" : reference.enabled ? "Enabled rule" : "Disabled rule"} · {reference.name}</span>}</For></div>
              </Show>
              <div class={`workflow-editor-status ${editorState()}`}>
                <div aria-live="polite" role={["failed", "conflict", "invalid"].includes(editorState()) ? "alert" : "status"}>
                  <strong>{workflowStateLabel(editorState())}</strong><span>{message()}</span>
                </div>
                <div class="workflow-note-actions">
                  <Show when={editorState() === "conflict"}>
                    <button class="text-button" type="button" onClick={() => void refresh(local().workflowId)}>Reload server</button>
                    <button class="text-button" type="button" onClick={() => void duplicateLocal()}>Duplicate local text</button>
                  </Show>
                  <Show when={editorState() === "failed"}><button class="text-button" type="button" onClick={() => void autosave()}>Retry save</button></Show>
                  <button class="text-button" type="button" disabled={!catalog().draftAvailable || (!document().savedDraft && !localUnacknowledged())} onClick={() => void discard()}>Discard draft</button>
                  <button class="text-button danger-button" type="button" disabled={!document().published || document().references.length > 0} onClick={() => void deleteWorkflow()}>Delete published</button>
                  <button class="primary-button" type="button" disabled={!document().savedDraft || localUnacknowledged() || ["saving", "dirty", "invalid", "conflict", "published"].includes(editorState())} onClick={() => void publish()}>Publish saved draft</button>
                </div>
              </div>
            </section>
          )}
        </Show>
      </div>
      <footer class="activity-footer settings-footer"><span>Current Runs retain the workflow revision they admitted.</span><a class="text-link" href="/triggers">Manage trigger bindings</a></footer>
    </>
  );
}

function workflowDocumentState(document: WorkflowDocument | undefined): WorkflowEditorState {
  if (!document) return "published";
  if (document.draftConflict) return "conflict";
  if (!document.savedDraft || (document.published && workflowPublishedEqual(document.published, document.draft))) return "published";
  return "unpublished";
}

function workflowEditableEqual(left: WorkflowDraft, right: WorkflowDraft): boolean {
  return left.name === right.name && left.enabled === right.enabled && left.markdown === right.markdown;
}

function workflowPublishedEqual(published: WorkflowDefinition, draft: WorkflowDraft): boolean {
  return published.name === draft.name && published.enabled === draft.enabled && published.markdown === draft.markdown;
}

function validateWorkflowDraft(draft: WorkflowDraft): string | undefined {
  if (!draft.name || draft.name !== draft.name.trim() || new TextEncoder().encode(draft.name).length > 80) return "Name must be trimmed and at most 80 bytes";
  const size = new TextEncoder().encode(draft.markdown).length;
  if (!draft.markdown.trim()) return "Markdown cannot be blank";
  if (size > 131072) return "Markdown exceeds 131,072 bytes";
  if (draft.markdown.includes("\0")) return "Markdown cannot contain NUL";
  return undefined;
}

function workflowStateLabel(state: WorkflowEditorState): string {
  switch (state) {
    case "dirty": return "Local edits";
    case "saving": return "Saving draft";
    case "unpublished": return "Unpublished changes";
    case "conflict": return "Draft conflict";
    case "failed": return "Autosave failed";
    case "invalid": return "Validation required";
    default: return "Published";
  }
}
