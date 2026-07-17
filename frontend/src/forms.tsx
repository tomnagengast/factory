import type { JSX } from "solid-js";

export type SaveState =
  | "idle"
  | "dirty"
  | "saving"
  | "saved"
  | "conflict"
  | "failed";

export function Field(props: { label: string; hint?: string; children: JSX.Element }): JSX.Element {
  return (
    <label class="settings-field">
      <span>{props.label}</span>
      {props.children}
      {props.hint ? <small>{props.hint}</small> : null}
    </label>
  );
}

export function Toggle(props: {
  checked: boolean;
  disabled?: boolean;
  compact?: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}): JSX.Element {
  return (
    <label classList={{ "settings-toggle": true, compact: Boolean(props.compact) }}>
      <input
        type="checkbox"
        checked={props.checked}
        disabled={props.disabled}
        onChange={(event) => props.onChange(event.currentTarget.checked)}
      />
      <span>{props.label}</span>
    </label>
  );
}

export function saveStateLabel(state: SaveState): string {
  switch (state) {
    case "dirty": return "Ready to save";
    case "saving": return "Saving";
    case "saved": return "Saved";
    case "conflict": return "Newer revision loaded";
    case "failed": return "Save failed";
    default: return "Current revision";
  }
}
