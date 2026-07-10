import { createResource, Show, type JSX } from "solid-js";
import { render } from "solid-js/web";
import "./styles.css";

type Health = {
  status: string;
  app: string;
};

async function getHealth(): Promise<Health> {
  const response = await fetch("/api/healthz");
  if (!response.ok) {
    throw new Error(`Health check failed with ${response.status}`);
  }
  return response.json() as Promise<Health>;
}

function App(): JSX.Element {
  const [health] = createResource(getHealth);

  return (
    <main>
      <section class="shell" aria-labelledby="factory-title">
        <div class="eyebrow">
          <span class="mark" aria-hidden="true">
            F
          </span>
          <span>nags.cloud</span>
        </div>

        <div class="content">
          <h1 id="factory-title">Factory</h1>
          <p class="lede">
            The floor is empty. The machinery is warming up. Something useful
            will be built here.
          </p>
        </div>

        <footer>
          <div class="status" aria-live="polite">
            <span
              classList={{
                dot: true,
                ready: health()?.status === "ok",
                failed: Boolean(health.error),
              }}
            />
            <Show
              when={!health.loading}
              fallback={<span>Connecting</span>}
            >
              <span>{health.error ? "Offline" : "Systems online"}</span>
            </Show>
          </div>
        </footer>
      </section>
    </main>
  );
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("Root element not found");
}

render(() => <App />, root);
