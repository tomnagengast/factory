import { A, useNavigate, useParams } from "@solidjs/router";
import { createEffect, createResource, createSignal, For, Show, type JSX } from "solid-js";
import { MoreHorizontal, Reply, Send, Smile, Trash2, X } from "lucide-solid";
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
  compact?: boolean;
}) {
  const action = mutation();
  const targetLabel = () => `${props.targetKind} ${props.targetID}`;
  const reactionButton = (emoji: string, menu = false) => {
    const active = () => props.reactions.includes(emoji);
    const label = () => `${active() ? "Clear" : "Add"} ${emoji} reaction ${active() ? "from" : "to"} ${targetLabel()}`;
    return <button type="button" classList={{ "reaction-button": true, selected: active(), "menu-item": menu }}
      aria-pressed={active()} aria-label={label()} title={label()} disabled={action.pending()}
      onClick={() => action.run(async () => {
        await put(`/api/${props.targetKind}s/${props.targetID}/reactions`, {
          emoji, active: !active(),
        });
        await props.onChange();
      })}>{emoji}</button>;
  };
  return (
    <div classList={{ "reaction-control": true, compact: props.compact }}>
      <Show when={props.compact} fallback={
        <div classList={{ "reaction-bar": true, pending: action.pending() }}
          role="group" aria-label={`Reactions for ${targetLabel()}`}>
          <For each={REACTION_EMOJIS}>{(emoji) => reactionButton(emoji)}</For>
        </div>
      }>
        <div classList={{ "reaction-bar": true, pending: action.pending() }}
          role="group" aria-label={`Reactions for ${targetLabel()}`}>
          <details class="reaction-menu">
            <summary aria-label={`Add a reaction to ${targetLabel()}`} title="Add reaction">
              <Smile aria-hidden="true" />
            </summary>
            <div class="reaction-menu-panel" role="group" aria-label={`Choose a reaction for ${targetLabel()}`}>
              <For each={REACTION_EMOJIS}>{(emoji) => reactionButton(emoji, true)}</For>
            </div>
          </details>
          <For each={props.reactions}>{(emoji) => reactionButton(emoji)}</For>
        </div>
      </Show>
      <Show when={action.error()}><span class="form-error" role="alert">{action.error()}</span></Show>
    </div>
  );
}
export function CommentThread(props: { comments: Comment[]; taskID: number; onChange: () => void }) {
  const roots = () => props.comments.filter((comment) => !comment.parentCommentId);
  const [replyTarget, setReplyTarget] = createSignal<number>();
  createEffect(() => {
    const target = replyTarget();
    if (target && !props.comments.some((comment) => comment.id === target)) setReplyTarget();
  });
  return (
    <div class="comments">
      <Show when={roots().length} fallback={<p class="comment-empty">No comments yet. Start the conversation below.</p>}>
        <div class="comment-thread-list">
          <For each={roots()}>{(root) => <div class="comment-group">
            <CommentBranch comments={props.comments} nodes={[root]} taskID={props.taskID}
              activeReplyID={replyTarget()} onReply={(commentID) =>
                setReplyTarget((current) => current === commentID ? undefined : commentID)}
              onChange={props.onChange} />
          </div>}</For>
        </div>
      </Show>
      <CommentForm taskID={props.taskID} onChange={props.onChange} />
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
        <span>Delete</span>
      </button>
      <Show when={action.error()}><span class="form-error" role="alert">{action.error()}</span></Show>
    </span>
  );
}

function CommentEntry(props: {
  comment: Comment;
  taskID: number;
  activeReplyID?: number;
  featured?: boolean;
  onReply?: (commentID: number) => void;
  onChange: () => unknown;
  onDeleted?: () => unknown;
}) {
  const authorLabel = () => props.comment.author === "agent" ? "Agent" : "User";
  const replying = () => props.activeReplyID === props.comment.id;
  return (
    <article classList={{ "comment-entry": true, featured: props.featured }}>
      <span class={`comment-avatar ${props.comment.author}`} role="img" aria-label={`${authorLabel()} comment`}>
        {props.comment.author === "agent" ? "A" : "U"}
      </span>
      <div class="comment-content">
        <header class="comment-meta">
          <strong>{authorLabel()}</strong>
          <A href={`/tasks/${props.taskID}/comments/${props.comment.id}`}>#{props.comment.id}</A>
          <time datetime={props.comment.createdAt} title={date(props.comment.createdAt)}>
            {relativeDate(props.comment.createdAt)}
          </time>
        </header>
        <div class="comment-body"><Markdown content={props.comment.content} /></div>
        <Show when={!props.comment.deletedAt}>
          <div class="comment-actions">
            <ReactionBar compact targetKind="comment" targetID={props.comment.id}
              reactions={props.comment.reactions} onChange={props.onChange} />
            <Show when={props.onReply}>
              <button type="button" classList={{ "comment-action": true, active: replying() }}
                aria-label={`Reply to comment #${props.comment.id}`} aria-pressed={replying()}
                title="Reply" onClick={() => props.onReply?.(props.comment.id)}>
                <Reply aria-hidden="true" />
              </button>
            </Show>
            <details class="comment-overflow">
              <summary aria-label={`More actions for comment #${props.comment.id}`} title="More actions">
                <MoreHorizontal aria-hidden="true" />
              </summary>
              <div class="comment-overflow-panel">
                <CommentDeleteButton commentID={props.comment.id} onDeleted={props.onDeleted ?? props.onChange} />
              </div>
            </details>
          </div>
          <Show when={replying()}>
            <CommentForm taskID={props.taskID} parentCommentID={props.comment.id}
              onCancel={() => props.onReply?.(props.comment.id)}
              onSubmitted={() => props.onReply?.(props.comment.id)} onChange={props.onChange} />
          </Show>
        </Show>
      </div>
    </article>
  );
}

export function CommentBranch(props: {
  comments: Comment[];
  nodes: Comment[];
  taskID: number;
  activeReplyID?: number;
  onReply: (commentID: number) => void;
  onChange: () => unknown;
}) {
  return (
    <For each={props.nodes}>
      {(comment) => {
        const replies = () => props.comments.filter((candidate) => candidate.parentCommentId === comment.id);
        return <div class="comment-branch">
          <CommentEntry comment={comment} taskID={props.taskID} activeReplyID={props.activeReplyID}
            onReply={props.onReply} onChange={props.onChange} />
          <Show when={replies().length}>
            <div class="comment-children">
              <CommentBranch comments={props.comments} nodes={replies()} taskID={props.taskID}
                activeReplyID={props.activeReplyID} onReply={props.onReply} onChange={props.onChange} />
            </div>
          </Show>
        </div>;
      }}
    </For>
  );
}

export function CommentForm(props: {
  taskID: number;
  parentCommentID?: number;
  onCancel?: () => void;
  onSubmitted?: () => void;
  onChange: () => unknown;
}) {
  const action = mutation();
  const [uploading, setUploading] = createSignal(false);
  const [reset, setReset] = createSignal(0);
  const reply = () => props.parentCommentID != null;
  return (
    <form classList={{ "comment-form": true, reply: reply(), root: !reply() }} onSubmit={(event) => {
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
        await props.onChange();
        props.onSubmitted?.();
      });
    }}>
      <MediaTextarea name="content" required rows={reply() ? 1 : 2}
        placeholder={reply() ? "Write a reply…" : "Add a comment…"} disabled={action.pending()}
        reset={reset()} onUploadingChange={setUploading}
        toolbarActions={<div class="composer-toolbar-actions">
          <Show when={props.onCancel}>
            <button type="button" class="composer-action" aria-label="Cancel reply" title="Cancel reply"
              disabled={action.pending() || uploading()} onClick={props.onCancel}>
              <X aria-hidden="true" />
            </button>
          </Show>
          <button type="submit" class="composer-action send"
            aria-label={action.pending() ? "Sending comment" : reply() ? "Send reply" : "Send comment"}
            title={reply() ? "Send reply" : "Send comment"} disabled={action.pending() || uploading()}>
            <Send aria-hidden="true" />
          </button>
        </div>} />
      <Show when={action.error()}><span class="form-error" role="alert">{action.error()}</span></Show>
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
  toolbarActions?: JSX.Element;
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
          {props.toolbarActions}
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
            <div class="comment-group featured">
              <CommentEntry featured comment={current().comment} taskID={Number(params.task)} onChange={refetch}
                onDeleted={() => navigate(`/tasks/${params.task}`)} />
            </div>
            <Show when={current().replies.length}>
              <section><SectionTitle title="Direct replies" />
                <div class="comment-group comment-detail-replies">
                  <For each={current().replies}>{(reply) => <CommentEntry comment={reply}
                    taskID={Number(params.task)} onChange={refetch} />}</For>
                </div>
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

function relativeDate(value: string) {
  const elapsedSeconds = Math.round((new Date(value).getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  const intervals: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ["year", 60 * 60 * 24 * 365],
    ["month", 60 * 60 * 24 * 30],
    ["week", 60 * 60 * 24 * 7],
    ["day", 60 * 60 * 24],
    ["hour", 60 * 60],
    ["minute", 60],
  ];
  for (const [unit, seconds] of intervals) {
    if (Math.abs(elapsedSeconds) >= seconds) return formatter.format(Math.round(elapsedSeconds / seconds), unit);
  }
  return formatter.format(0, "second");
}
