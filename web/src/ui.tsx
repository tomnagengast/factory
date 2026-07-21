import { A } from "@solidjs/router";
import { createEffect, createMemo, For, Show, type JSX } from "solid-js";
import hljs from "highlight.js/lib/common";
import "highlight.js/styles/github-dark.css";
import { errorMessage } from "./api";
import { renderMarkdown } from "./markdown";

export function Shell(props: { children?: JSX.Element }) {
  const links = [
    ["/", "Overview"],
    ["/projects", "Projects"],
    ["/tasks", "Tasks"],
    ["/events", "Event wire"],
    ["/triggers", "Triggers"],
    ["/workflows", "Workflows"],
    ["/history", "History"],
    ["/settings", "Settings"],
  ];
  return (
    <div class="shell">
      <aside class="rail">
        <A href="/" class="brand" aria-label="Factory overview">
          <span class="brand-mark">F</span>
          <span>
            <strong>Factory</strong>
            <small>one wire / bounded runs</small>
          </span>
        </A>
        <nav aria-label="Primary navigation">
          <For each={links}>
            {([href, label]) => (
              <A href={href} end={href === "/"} activeClass="active">
                {label}
              </A>
            )}
          </For>
        </nav>
        <div class="rail-status">
          <span class="pulse" aria-hidden="true" />
          Coordinator connected
        </div>
      </aside>
      <main>{props.children}</main>
    </div>
  );
}

export function PageHeader(props: { eyebrow?: string; title: JSX.Element; description?: string; actions?: JSX.Element }) {
  return (
    <header class="page-header">
      <div>
        <Show when={props.eyebrow}>
          <p class="eyebrow">{props.eyebrow}</p>
        </Show>
        <h1>{props.title}</h1>
        <Show when={props.description}>
          <p>{props.description}</p>
        </Show>
      </div>
      <Show when={props.actions}>
        <div class="header-actions">{props.actions}</div>
      </Show>
    </header>
  );
}

export function Load<T>(props: {
  data: () => T | undefined;
  error: () => unknown;
  children: (value: T) => JSX.Element;
}) {
  return (
    <Show
      when={props.data()}
      fallback={
        <div class="state">
          <Show when={props.error()} fallback="Loading…">
            {errorMessage(props.error())}
          </Show>
        </div>
      }
    >
      {(value) => props.children(value())}
    </Show>
  );
}

export function Empty(props: { children: JSX.Element }) {
  return <div class="empty">{props.children}</div>;
}

export function Markdown(props: { content?: string; inline?: boolean }) {
  let body: HTMLDivElement | undefined;
  const html = createMemo(() => renderMarkdown(props.content ?? "", props.inline));
  createEffect(() => {
    html();
    if (!props.inline) queueMicrotask(() =>
      body?.querySelectorAll<HTMLElement>("pre code").forEach((code) => hljs.highlightElement(code)));
  });
  return props.inline
    ? <span class="markdown inline" innerHTML={html()} />
    : <div ref={body} class="markdown" innerHTML={html()} />;
}

export function Meta(props: { value: { id: number; createdAt: string; updatedAt: string; deletedAt?: string } }) {
  return (
    <dl class="meta">
      <div><dt>ID</dt><dd>{props.value.id}</dd></div>
      <div><dt>Created</dt><dd>{date(props.value.createdAt)}</dd></div>
      <div><dt>Updated</dt><dd>{date(props.value.updatedAt)}</dd></div>
      <div><dt>Deleted</dt><dd>{props.value.deletedAt ? date(props.value.deletedAt) : "No"}</dd></div>
    </dl>
  );
}

export function SectionTitle(props: { title: string; href?: string }) {
  return (
    <header class="section-title">
      <h2>{props.title}</h2>
      <Show when={props.href}><A href={props.href!}>View all</A></Show>
    </header>
  );
}

export function FilterFieldActions(props: {
  selectLabel: string;
  unselectLabel: string;
  selectDisabled: boolean;
  unselectDisabled: boolean;
  onSelect: () => void;
  onUnselect: () => void;
}) {
  return (
    <span class="filter-field-actions">
      <button type="button" aria-label={props.selectLabel} disabled={props.selectDisabled}
        onClick={props.onSelect}>Select all</button>
      <button type="button" aria-label={props.unselectLabel} disabled={props.unselectDisabled}
        onClick={props.onUnselect}>Unselect all</button>
    </span>
  );
}

export function FormFooter(props: { pending: boolean; error?: string; label: string; onCancel?: () => void }) {
  return (
    <footer class="form-footer">
      <Show when={props.error}><span class="form-error">{props.error}</span></Show>
      <Show when={props.onCancel}>
        <button type="button" class="button quiet" disabled={props.pending} onClick={props.onCancel}>Cancel</button>
      </Show>
      <button class="button primary" disabled={props.pending}>{props.pending ? "Saving…" : props.label}</button>
    </footer>
  );
}

export function date(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    month: "short", day: "numeric", year: "numeric", hour: "numeric", minute: "2-digit",
  }).format(new Date(value));
}
