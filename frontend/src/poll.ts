import { onCleanup, onMount } from "solid-js";

export function usePolling(
  refresh: () => void,
  intervalMs: number,
  enabled: () => boolean = () => true,
): void {
  onMount(() => {
    const timer = window.setInterval(() => {
      if (enabled()) refresh();
    }, intervalMs);
    onCleanup(() => window.clearInterval(timer));
  });
}
