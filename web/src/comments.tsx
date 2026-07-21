import { A, useNavigate, useParams } from "@solidjs/router";
import { createEffect, createResource, createSignal, For, Show } from "solid-js";
import { Trash2 } from "lucide-solid";
import {
  errorMessage,
  get,
  liveRefetch,
  mutation,
  optional,
  post,
  put,
  remove,
  uploadMedia,
} from "./api";
import { insertMediaMarkup, mediaAccept, mediaFiles, mediaKind, mediaMarkup } from "./media";
import type { Artifact, Comment, CommentDetail } from "./types";
import { date, Empty, Load, Markdown, PageHeader, SectionTitle } from "./ui";

const REACTION_EMOJIS = ["👍", "👎", "❤️", "🎉", "😂", "👀"] as const;

export function ReactionBar(props: {
  targetKind: "task" | "comment";
  targetID: number;
  reactions: string[];
  onChange: () => unknown;
}) {
  const action = mutation();
  const targetLabel = () => `${props.targetKind} ${props.targetID}`;
  return (
    <div class="reaction-control">
      <div classList={{ "reaction-bar": true, pending: action.pending() }}
        role="group" aria-label={`Reactions for ${targetLabel()}`}>
        <For each={REACTION_EMOJIS}>{(emoji) => {
          const active = () => props.reactions.includes(emoji);
          const label = () => `${active() ? "Clear" : "Add"} ${emoji} reaction ${active() ? "from" : "to"} ${targetLabel()}`;
          return <button type="button" classList={{ "reaction-button": true, selected: active() }}
            aria-pressed={active()} aria-label={label()} title={label()} disabled={action.pending()}
            onClick={() => action.run(async () => {
              await put(`/api/${props.targetKind}s/${props.targetID}/reactions`, {
                emoji, active: !active(),
              });
              await props.onChange();
            })}>{emoji}</button>;
        }}</For>
      </div>
      <Show when={action.error()}><span class="form-error" role="alert">{action.error()}</span></Show>
    </div>
  );
}
export function CommentThread(props: { comments: Comment[]; taskID: number; onChange: () => void }) {
  const roots = () => props.comments.filter((comment) => !comment.parentCommentId);
  return (
    <div class="comments">
      <CommentForm taskID={props.taskID} onChange={props.onChange} />
      <Show when={roots().length} fallback={<Empty>No comments yet.</Empty>}>
        <CommentBranch comments={props.comments} nodes={roots()} taskID={props.taskID} onChange={props.onChange} />
      </Show>
    </div>
  );
}

function CommentDeleteButton(props: { commentID: number; onDeleted: () => unknown }) {
  const action = mutation();
  const label = () => `Delete comment #${props.commentID}`;
  return (
    <span class="comment-delete-control">
      <button type="button" class="comment-delete" aria-label={label()} title="Delete comment"
        disabled={action.pending()} onClick={() => action.run(async () => {
          await remove(`/api/comments/${props.commentID}`);
          await props.onDeleted();
        })}>
        <Trash2 aria-hidden="true" />
      </button>
      <Show when={action.error()}><span class="form-error" role="alert">{action.error()}</span></Show>
    </span>
  );
}

export function CommentBranch(props: {
  comments: Comment[];
  nodes: Comment[];
  taskID: number;
  onChange: () => void;
}) {
  return (
    <For each={props.nodes}>
      {(comment) => (
        <article class="comment">
          <header>
            <strong>{comment.author}</strong>
            <A href={`/tasks/${props.taskID}/comments/${comment.id}`}>#{comment.id}</A>
            <time>{date(comment.createdAt)}</time>
            <CommentDeleteButton commentID={comment.id} onDeleted={props.onChange} />
          </header>
          <Markdown content={comment.content} />
          <Show when={!comment.deletedAt}>
            <ReactionBar targetKind="comment" targetID={comment.id}
              reactions={comment.reactions} onChange={props.onChange} />
          </Show>
          <CommentForm taskID={props.taskID} parentCommentID={comment.id} compact onChange={props.onChange} />
          <div class="replies">
            <CommentBranch
              comments={props.comments}
              nodes={props.comments.filter((candidate) => candidate.parentCommentId === comment.id)}
              taskID={props.taskID}
              onChange={props.onChange}
            />
          </div>
        </article>
      )}
    </For>
  );
}

export function CommentForm(props: {
  taskID: number;
  parentCommentID?: number;
  compact?: boolean;
  onChange: () => void;
}) {
  const action = mutation();
  const [uploading, setUploading] = createSignal(false);
  const [reset, setReset] = createSignal(0);
  return (
    <form classList={{ "comment-form": true, compact: props.compact }} onSubmit={(event) => {
      event.preventDefault();
      if (uploading()) return;
      const form = event.currentTarget;
      const data = new FormData(form);
      action.run(async () => {
        await post<Comment>(`/api/tasks/${props.taskID}/comments`, {
          content: String(data.get("content") ?? "").trim(),
          parentCommentId: props.parentCommentID,
        });
        setReset((value) => value + 1);
        props.onChange();
      });
    }}>
      <MediaTextarea name="content" required rows={props.compact ? 1 : 3}
        placeholder={props.compact ? "Reply…" : "Add a comment…"} disabled={action.pending()}
        reset={reset()} onUploadingChange={setUploading} />
      <button class="button quiet" disabled={action.pending() || uploading()}>{props.compact ? "Reply" : "Comment"}</button>
      <Show when={action.error()}><span class="form-error">{action.error()}</span></Show>
    </form>
  );
}

export function MediaTextarea(props: {
  id?: string;
  name: string;
  initialValue?: string;
  rows: number;
  required?: boolean;
  placeholder?: string;
  disabled?: boolean;
  reset?: number;
  onUploadingChange?: (uploading: boolean) => void;
}) {
  const [value, setValue] = createSignal(props.initialValue ?? "");
  const [uploading, setUploading] = createSignal(false);
  const [progress, setProgress] = createSignal(0);
  const [total, setTotal] = createSignal(0);
  const [error, setError] = createSignal<string>();
  const [dragging, setDragging] = createSignal(false);
  let textarea: HTMLTextAreaElement | undefined;
  let fileInput: HTMLInputElement | undefined;
  let selectionStart = (props.initialValue ?? "").length;
  let selectionEnd = selectionStart;

  createEffect(() => {
    props.reset;
    const initialValue = props.initialValue ?? "";
    setValue(initialValue);
    selectionStart = initialValue.length;
    selectionEnd = initialValue.length;
  });

  const rememberSelection = (target: HTMLTextAreaElement) => {
    selectionStart = target.selectionStart;
    selectionEnd = target.selectionEnd;
  };

  const filesAtSelection = async (files: File[], start: number, end: number) => {
    if (!files.length || uploading()) return;
    setError();
    setTotal(files.length);
    setProgress(0);
    setUploading(true);
    props.onUploadingChange?.(true);
    try {
      for (const file of files) {
        if (file.type && file.type !== "application/octet-stream" && !mediaKind(file.type)) {
          throw new Error(`Unsupported media type: ${file.type}`);
        }
      }
      const markups: string[] = [];
      for (const [index, file] of files.entries()) {
        setProgress(index + 1);
        markups.push(mediaMarkup(await uploadMedia(file)));
      }
      const inserted = insertMediaMarkup(value(), start, end, markups);
      setValue(inserted.value);
      selectionStart = inserted.caret;
      selectionEnd = inserted.caret;
      queueMicrotask(() => {
        textarea?.focus();
        textarea?.setSelectionRange(inserted.caret, inserted.caret);
      });
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setUploading(false);
      props.onUploadingChange?.(false);
    }
  };

  return (
    <div classList={{ "media-textarea": true, dragging: dragging(), uploading: uploading() }}>
      <div class="media-editor">
        <textarea ref={textarea} id={props.id} name={props.name} value={value()} rows={props.rows}
          required={props.required} placeholder={props.placeholder} disabled={props.disabled || uploading()}
          onInput={(event) => {
            setValue(event.currentTarget.value);
            rememberSelection(event.currentTarget);
          }}
          onSelect={(event) => rememberSelection(event.currentTarget)}
          onBlur={(event) => rememberSelection(event.currentTarget)}
          onPaste={(event) => {
            const files = mediaFiles(event.clipboardData);
            if (!files.length) return;
            event.preventDefault();
            void filesAtSelection(files, event.currentTarget.selectionStart, event.currentTarget.selectionEnd);
          }}
          onDragOver={(event) => {
            const transfer = event.dataTransfer;
            if (!transfer || !Array.from(transfer.types).includes("Files")) return;
            event.preventDefault();
            transfer.dropEffect = "copy";
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(event) => {
            setDragging(false);
            const files = mediaFiles(event.dataTransfer);
            if (!files.length) return;
            event.preventDefault();
            void filesAtSelection(files, event.currentTarget.selectionStart, event.currentTarget.selectionEnd);
          }} />
        <div class="media-toolbar">
          <input ref={fileInput} class="media-file-input" type="file" accept={mediaAccept} multiple
            disabled={props.disabled || uploading()} onChange={(event) => {
              const files = Array.from(event.currentTarget.files ?? []);
              event.currentTarget.value = "";
              void filesAtSelection(files, selectionStart, selectionEnd);
            }} />
          <button type="button" class="media-picker" aria-label="Add image or video" title="Add image or video"
            disabled={props.disabled || uploading()} onClick={() => fileInput?.click()}>
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <rect x="3" y="4" width="18" height="16" rx="2" />
              <circle cx="8.5" cy="9" r="1.5" />
              <path d="m4 17 5-5 4 4 2-2 5 5" />
            </svg>
          </button>
          <Show when={uploading()}>
            <small class="upload-status" aria-live="polite">Uploading {progress()} of {total()}…</small>
          </Show>
        </div>
      </div>
      <Show when={error()}><small class="form-error" role="alert">{error()}</small></Show>
    </div>
  );
}

export function CommentView() {
  const params = useParams();
  const navigate = useNavigate();
  const [data, { refetch }] = createResource(() => get<CommentDetail>(`/api/comments/${params.comment}`));
  liveRefetch(["comment.deleted", "reaction.updated"], refetch);
  return (
    <div class="page narrow">
      <Load data={data} error={() => data.error}>
        {(value) => {
          const current = () => data() ?? value;
          return <>
            <PageHeader eyebrow={`Task ${params.task}`} title={`Comment ${current().comment.id}`}
              actions={<A class="button" href={`/tasks/${params.task}`}>Back to task</A>} />
            <article class="comment featured">
              <header>
                <strong>{current().comment.author}</strong>
                <time>{date(current().comment.createdAt)}</time>
                <Show when={!current().comment.deletedAt}>
                  <CommentDeleteButton commentID={current().comment.id}
                    onDeleted={() => navigate(`/tasks/${params.task}`)} />
                </Show>
              </header>
              <Markdown content={current().comment.content} />
              <Show when={!current().comment.deletedAt}>
                <ReactionBar targetKind="comment" targetID={current().comment.id}
                  reactions={current().comment.reactions} onChange={refetch} />
              </Show>
            </article>
            <Show when={current().replies.length}>
              <section><SectionTitle title="Direct replies" />
                <div class="comments"><For each={current().replies}>{(reply) => <article class="comment"><header>
                  <strong>{reply.author}</strong><A href={`/tasks/${params.task}/comments/${reply.id}`}>#{reply.id}</A>
                  <time>{date(reply.createdAt)}</time>
                  <CommentDeleteButton commentID={reply.id} onDeleted={refetch} />
                </header><Markdown content={reply.content} />
                  <Show when={!reply.deletedAt}>
                    <ReactionBar targetKind="comment" targetID={reply.id}
                      reactions={reply.reactions} onChange={refetch} />
                  </Show>
                </article>}</For></div>
              </section>
            </Show>
            <ArtifactPanel artifacts={current().artifacts} relationType="comment" relationID={current().comment.id} onChange={refetch} />
          </>;
        }}
      </Load>
    </div>
  );
}

export function ArtifactPanel(props: {
  artifacts: Artifact[];
  relationType: string;
  relationID: number;
  onChange: () => void;
}) {
  const action = mutation();
  return (
    <section>
      <SectionTitle title="Artifacts" />
      <Show when={props.artifacts.length} fallback={<Empty>No artifacts attached.</Empty>}>
        <div class="artifacts">
          <For each={props.artifacts}>{(artifact) => <article>
            <span class="artifact-type">{artifact.type}</span>
            <strong>{artifact.name || `Artifact ${artifact.id}`}</strong>
            <Show when={artifact.type === "link"} fallback={<p>{artifact.content}</p>}>
              <a href={artifact.content} target="_blank" rel="noreferrer">{artifact.content}</a>
            </Show>
          </article>}</For>
        </div>
      </Show>
      <form class="artifact-form" onSubmit={(event) => {
        event.preventDefault();
        const form = event.currentTarget;
        const data = new FormData(form);
        action.run(async () => {
          await post<Artifact>("/api/artifacts", {
            name: optional(data.get("name")),
            type: data.get("type"),
            content: String(data.get("content") ?? "").trim(),
            relationType: props.relationType,
            relationId: props.relationID,
          });
          form.reset();
          props.onChange();
        });
      }}>
        <input name="name" placeholder="Name (optional)" />
        <select name="type"><option>text</option><option>link</option><option>image</option><option>document</option></select>
        <textarea name="content" required rows="2" placeholder="Content, URL, or path" />
        <button class="button quiet" disabled={action.pending()}>Attach artifact</button>
        <Show when={action.error()}><span class="form-error">{action.error()}</span></Show>
      </form>
    </section>
  );
}
