export function formatWorkflowRunDuration(start: string | number, end: string | number) {
  const elapsedMilliseconds = Math.max(0, new Date(end).getTime() - new Date(start).getTime());
  const elapsedSeconds = Math.floor(elapsedMilliseconds / 1000);

  if (elapsedSeconds < 60) return `${elapsedSeconds}s`;

  const elapsedMinutes = Math.floor(elapsedSeconds / 60);
  if (elapsedMinutes < 60) return `${elapsedMinutes}m ${elapsedSeconds % 60}s`;

  const elapsedHours = Math.floor(elapsedMinutes / 60);
  if (elapsedHours < 24) return `${elapsedHours}h ${elapsedMinutes % 60}m`;

  const elapsedDays = Math.floor(elapsedHours / 24);
  return `${elapsedDays}d ${elapsedHours % 24}h`;
}
