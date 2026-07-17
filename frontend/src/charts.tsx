import { For, Show, type JSX } from "solid-js";

export type ActivityCount = { label: string; count: number };

export function ActivityChart(props: {
  title: string;
  subtitle: string;
  items: ActivityCount[];
}): JSX.Element {
  const maximum = (): number => Math.max(1, ...props.items.map((item) => item.count));
  return (
    <article class="activity-chart">
      <header><h2>{props.title}</h2><span>{props.subtitle}</span></header>
      <Show when={props.items.length > 0} fallback={<p class="chart-empty">Waiting for retained activity.</p>}>
        <ol>
          <For each={props.items}>
            {(item) => <li><span>{item.label}</span><progress max={maximum()} value={item.count} aria-label={`${item.label}: ${item.count}`} /><strong>{item.count}</strong></li>}
          </For>
        </ol>
      </Show>
    </article>
  );
}

export function Pagination(props: { page: number; pageCount: number; onChange: (page: number) => void }): JSX.Element {
  const lastPage = (): number => Math.max(1, props.pageCount);
  return (
    <nav class="pagination" aria-label="Event pages">
      <button type="button" disabled={props.page <= 1} onClick={() => props.onChange(props.page - 1)}>Previous</button>
      <span>{props.page} / {lastPage()}</span>
      <button type="button" disabled={props.page >= lastPage()} onClick={() => props.onChange(props.page + 1)}>Next</button>
    </nav>
  );
}
