import { createSignal, onCleanup, onMount } from "solid-js";
import type { Event, MediaUpload } from "./types";

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: init?.body ? { "Content-Type": "application/json", ...init.headers } : init?.headers,
  });
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as { error?: string } | null;
    throw new Error(body?.error ?? `${response.status} ${response.statusText}`);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export const get = <T,>(path: string) => request<T>(path);
export const post = <T,>(path: string, body: unknown) =>
  request<T>(path, { method: "POST", body: JSON.stringify(body) });
export const put = <T,>(path: string, body: unknown) =>
  request<T>(path, { method: "PUT", body: JSON.stringify(body) });
export const remove = (path: string) => request<void>(path, { method: "DELETE" });

export async function uploadMedia(file: File): Promise<MediaUpload> {
  const body = new FormData();
  body.append("file", file, file.name);
  const response = await fetch("/api/media", { method: "POST", body });
  if (!response.ok) {
    const error = (await response.json().catch(() => null)) as { error?: string } | null;
    throw new Error(error?.error ?? `${response.status} ${response.statusText}`);
  }
  return response.json() as Promise<MediaUpload>;
}

export function optional(value: FormDataEntryValue | null) {
  const text = String(value ?? "").trim();
  return text || undefined;
}

export function optionalID(value: FormDataEntryValue | null) {
  const text = optional(value);
  return text ? Number(text) : undefined;
}

export function mutation() {
  const [pending, setPending] = createSignal(false);
  const [error, setError] = createSignal<string>();
  return {
    pending,
    error,
    run: async (action: () => Promise<void>) => {
      setPending(true);
      setError();
      try {
        await action();
      } catch (caught) {
        setError(errorMessage(caught));
      } finally {
        setPending(false);
      }
    },
  };
}

export function liveRefetch(types: string[], refetch: () => unknown) {
  let source: EventSource | undefined;
  onMount(async () => {
    const initial = await get<{ events: Event[] }>("/api/events");
    source = new EventSource(`/api/events/stream?after=${initial.events[0]?.id ?? 0}`);
    source.onmessage = (message) => {
      const event = JSON.parse(message.data) as Event;
      if (types.includes(event.type)) refetch();
    };
  });
  onCleanup(() => source?.close());
}

export function errorMessage(value: unknown) {
  return value instanceof Error ? value.message : String(value || "Something went wrong.");
}
