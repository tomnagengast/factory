import type { JSX } from "solid-js";

type ActivitySection = "home" | "wire" | "tasks" | "agents" | "workflows" | "triggers" | "settings";

const timeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

export function ActivityHeader(props: {
  section: ActivitySection;
  state: "loading" | "ready" | "failed";
  label: string;
}): JSX.Element {
  return (
    <header class="activity-header">
      <a class="brand-link" href="/">
        <span class="mark" aria-hidden="true">
          F
        </span>
        <span>Factory</span>
      </a>
      <nav class="activity-nav" aria-label="Activity sections">
        <a
          classList={{ active: props.section === "home" }}
          aria-current={props.section === "home" ? "page" : undefined}
          href="/home"
        >
          Overview
        </a>
        <a
          classList={{ active: props.section === "wire" }}
          aria-current={props.section === "wire" ? "page" : undefined}
          href="/wire"
        >
          Wire
        </a>
        <a
          classList={{ active: props.section === "tasks" }}
          aria-current={props.section === "tasks" ? "page" : undefined}
          href="/tasks"
        >
          Tasks
        </a>
        <a
          classList={{ active: props.section === "agents" }}
          aria-current={props.section === "agents" ? "page" : undefined}
          href="/agents"
        >
          Agents
        </a>
        <a
          classList={{ active: props.section === "workflows" }}
          aria-current={props.section === "workflows" ? "page" : undefined}
          href="/workflows"
        >
          Workflows
        </a>
        <a
          classList={{ active: props.section === "triggers" }}
          aria-current={props.section === "triggers" ? "page" : undefined}
          href="/triggers"
        >
          Triggers
        </a>
        <a
          classList={{ active: props.section === "settings" }}
          aria-current={props.section === "settings" ? "page" : undefined}
          href="/settings"
        >
          Settings
        </a>
      </nav>
      <div class="listener" aria-live="polite">
        <span
          classList={{
            dot: true,
            ready: props.state === "ready",
            failed: props.state === "failed",
          }}
        />
        <span>{props.label}</span>
      </div>
    </header>
  );
}

export function InlineError(props: { message: string }): JSX.Element {
  return (
    <div class="inline-error" role="alert">
      <strong>Connection failed</strong>
      <span>{props.message}</span>
    </div>
  );
}

export function LoadingRows(): JSX.Element {
  return (
    <div class="loading-rows" aria-label="Loading activity">
      <span />
      <span />
      <span />
    </div>
  );
}

export function runStateLabel(value: string): string {
  return value.replace(/(^|[-_])([a-z])/g, (_, prefix, letter: string) =>
    `${prefix ? " " : ""}${letter.toUpperCase()}`,
  );
}

export function formatTime(value: string | null | undefined): string {
  if (!value) {
    return "No activity yet";
  }
  return timeFormatter.format(new Date(value));
}

export function resourceState(
  loading: boolean,
  error: unknown,
  ready = true,
): "loading" | "ready" | "failed" {
  if (error) {
    return "failed";
  }
  if (loading || !ready) {
    return "loading";
  }
  return "ready";
}
