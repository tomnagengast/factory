/**
 * @typedef {{
 *   sequence: number;
 *   id: string;
 *   type: string;
 *   at: string;
 *   taskId?: string;
 *   runId?: string;
 *   data: unknown;
 * }} EventRecord
 */

/**
 * @typedef {{
 *   sequence: number;
 *   stream: string;
 *   text: string;
 *   at: string;
 * }} AgentOutput
 */

/**
 * @typedef {{
 *   id: string;
 *   prompt: string;
 *   status: "queued" | "running" | "completed" | "failed";
 *   runId?: string;
 *   submittedAt: string;
 *   startedAt?: string;
 *   finishedAt?: string;
 *   error?: string;
 *   output: AgentOutput[];
 * }} Task
 */

const app = document.querySelector("#app");
if (!(app instanceof HTMLElement)) throw new Error("Factory app root is missing");

app.innerHTML = `
  <header class="masthead">
    <a class="brand" href="/" aria-label="Factory home">
      <span class="brand-mark">F</span>
      <span>Factory</span>
    </a>
    <div class="connection" aria-live="polite">
      <span class="connection-dot"></span>
      <span class="connection-label">Connecting</span>
    </div>
  </header>
  <section class="hero">
    <div>
      <p class="eyebrow">One wire · One worker</p>
      <h1>Give an agent<br>a job.</h1>
    </div>
    <p class="hero-copy">
      A prompt enters the event wire, one agent picks it up, and every output
      and state transition comes back through the same path.
    </p>
  </section>
  <section class="workspace">
    <div class="work-column">
      <form class="task-form">
        <label for="prompt">New task</label>
        <textarea
          id="prompt"
          name="prompt"
          rows="5"
          placeholder="Describe what you want the agent to do…"
          required
        ></textarea>
        <div class="form-footer">
          <span>Runs with unrestricted access in the configured workspace.</span>
          <button type="submit">Run task <span aria-hidden="true">→</span></button>
        </div>
        <p class="form-error" role="alert"></p>
      </form>
      <div class="task-heading">
        <div>
          <p class="eyebrow">Agent loop</p>
          <h2>Tasks</h2>
        </div>
        <dl class="counts">
          <div><dt>Queued</dt><dd data-count="queued">0</dd></div>
          <div><dt>Running</dt><dd data-count="running">0</dd></div>
          <div><dt>Finished</dt><dd data-count="finished">0</dd></div>
        </dl>
      </div>
      <div class="tasks" aria-live="polite"></div>
    </div>
    <aside class="wire-column">
      <div class="wire-heading">
        <div>
          <p class="eyebrow">Append-only</p>
          <h2>Event wire</h2>
        </div>
        <span class="event-total">0 events</span>
      </div>
      <ol class="events" aria-live="polite"></ol>
    </aside>
  </section>
  <footer>
    <span>Trusted environment demonstrator</span>
    <span>Prompts and output are stored in plain text</span>
  </footer>
`;

/** @type {Task[]} */
let tasks = [];
/** @type {EventRecord[]} */
let events = [];
/** @type {number | undefined} */
let refreshTimer;

/**
 * @template T
 * @param {string} path
 * @param {RequestInit} [options]
 * @returns {Promise<T>}
 */
async function request(path, options) {
  const response = await fetch(path, options);
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message.trim() || `${response.status} ${response.statusText}`);
  }
  return response.json();
}

/**
 * @template {Element} T
 * @param {string} selector
 * @param {new (...arguments_: any[]) => T} constructor
 * @returns {T}
 */
function required(selector, constructor) {
  const node = document.querySelector(selector);
  if (!(node instanceof constructor)) throw new Error(`Factory interface is missing ${selector}`);
  return node;
}

const form = required(".task-form", HTMLFormElement);
const prompt = required("#prompt", HTMLTextAreaElement);
const formError = required(".form-error", HTMLElement);
const taskList = required(".tasks", HTMLElement);
const eventList = required(".events", HTMLOListElement);
const eventTotal = required(".event-total", HTMLElement);
const connection = required(".connection", HTMLElement);
const connectionLabel = required(".connection-label", HTMLElement);
const submit = required(".task-form button", HTMLButtonElement);

/**
 * @template {keyof HTMLElementTagNameMap} K
 * @param {K} tag
 * @param {string} [className]
 * @param {string} [text]
 * @returns {HTMLElementTagNameMap[K]}
 */
function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

/** @param {string | undefined} value */
function formatTime(value) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(value));
}

/** @param {string | undefined} value */
function shortID(value) {
  if (!value) return "waiting";
  return value.length > 14 ? value.slice(0, 14) : value;
}

/** @param {EventRecord} event */
function eventDetail(event) {
  const data = event.data;
  if (typeof data !== "object" || data === null) return event.runId || event.taskId || "";
  if (event.type === "task.submitted" && "prompt" in data && typeof data.prompt === "string") {
    return data.prompt;
  }
  if (event.type === "agent.output" && "text" in data && typeof data.text === "string") {
    return data.text;
  }
  if (event.type === "run.failed" && "error" in data && typeof data.error === "string") {
    return data.error;
  }
  return event.runId || event.taskId || "";
}

function renderEvents() {
  eventList.replaceChildren();
  eventTotal.textContent = `${events.length} ${events.length === 1 ? "event" : "events"}`;

  if (events.length === 0) {
    const empty = element("li", "empty-state");
    empty.append(
      element("strong", "", "The wire is quiet."),
      element("span", "", "Submit a task to create the first event."),
    );
    eventList.append(empty);
    return;
  }

  for (const event of [...events].reverse()) {
    const row = element("li", `event event-${event.type.replace(".", "-")}`);
    const sequence = element("span", "event-sequence", String(event.sequence).padStart(4, "0"));
    const body = element("div", "event-body");
    const heading = element("div", "event-title");
    heading.append(
      element("strong", "", event.type),
      element("time", "", formatTime(event.at)),
    );
    body.append(heading, element("p", "", eventDetail(event)));
    row.append(sequence, body);
    eventList.append(row);
  }
}

function renderTasks() {
  taskList.replaceChildren();

  const counts = {
    queued: tasks.filter((task) => task.status === "queued").length,
    running: tasks.filter((task) => task.status === "running").length,
    finished: tasks.filter((task) => task.status === "completed" || task.status === "failed").length,
  };
  for (const [name, count] of Object.entries(counts)) {
    const target = document.querySelector(`[data-count="${name}"]`);
    if (target instanceof HTMLElement) target.textContent = String(count);
  }

  if (tasks.length === 0) {
    const empty = element("div", "empty-state task-empty");
    empty.append(
      element("strong", "", "No tasks yet."),
      element("span", "", "The first prompt will appear here as soon as it reaches the wire."),
    );
    taskList.append(empty);
    return;
  }

  for (const task of [...tasks].reverse()) {
    const article = element("article", `task task-${task.status}`);
    const header = element("header", "task-card-heading");
    header.append(
      element("span", "task-status", task.status),
      element("span", "task-id", shortID(task.id)),
    );

    const meta = element("dl", "task-meta");
    const metaValues = [
      ["Submitted", formatTime(task.submittedAt)],
      ["Run", shortID(task.runId)],
      ["Finished", formatTime(task.finishedAt)],
    ];
    for (const [label, value] of metaValues) {
      const item = element("div");
      item.append(element("dt", "", label), element("dd", "", value));
      meta.append(item);
    }
    article.append(header, element("h3", "", task.prompt), meta);

    if (task.output.length > 0) {
      const log = element("div", "agent-log");
      for (const output of task.output) {
        const line = element("div", `agent-line stream-${output.stream}`);
        line.append(
          element("span", "line-stream", output.stream),
          element("pre", "", output.text),
        );
        log.append(line);
      }
      article.append(log);
    }
    if (task.error) article.append(element("p", "task-error", task.error));
    taskList.append(article);
  }
}

async function refreshTasks() {
  /** @type {{tasks: Task[]}} */
  const snapshot = await request("/api/tasks");
  tasks = snapshot.tasks;
  renderTasks();
}

function scheduleTaskRefresh() {
  if (refreshTimer !== undefined) return;
  refreshTimer = window.setTimeout(() => {
    refreshTimer = undefined;
    void refreshTasks().catch((error) => {
      connection.classList.add("disconnected");
      connectionLabel.textContent = error instanceof Error ? error.message : "Task refresh failed";
    });
  }, 80);
}

/** @param {number} after */
function connect(after) {
  const source = new EventSource(`/api/events/stream?after=${after}`);
  source.onopen = () => {
    connection.classList.remove("disconnected");
    connectionLabel.textContent = "Wire connected";
  };
  source.onmessage = (message) => {
    /** @type {EventRecord} */
    const event = JSON.parse(message.data);
    if (!events.some((existing) => existing.sequence === event.sequence)) {
      events.push(event);
      events.sort((left, right) => left.sequence - right.sequence);
      renderEvents();
      scheduleTaskRefresh();
    }
  };
  source.onerror = () => {
    connection.classList.add("disconnected");
    connectionLabel.textContent = "Reconnecting";
  };
}

form.addEventListener("submit", async (submitEvent) => {
  submitEvent.preventDefault();
  const value = prompt.value.trim();
  if (!value) return;

  submit.disabled = true;
  formError.textContent = "";
  try {
    await request("/api/tasks", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt: value }),
    });
    prompt.value = "";
    prompt.focus();
    await refreshTasks();
  } catch (error) {
    formError.textContent = error instanceof Error ? error.message : "Task submission failed";
  } finally {
    submit.disabled = false;
  }
});

async function start() {
  try {
    /** @type {[{tasks: Task[]}, {events: EventRecord[]}]} */
    const [taskSnapshot, eventSnapshot] = await Promise.all([
      request("/api/tasks"),
      request("/api/events"),
    ]);
    tasks = taskSnapshot.tasks;
    events = eventSnapshot.events;
    renderTasks();
    renderEvents();
    connect(events.at(-1)?.sequence ?? 0);
  } catch (error) {
    connection.classList.add("disconnected");
    connectionLabel.textContent = error instanceof Error ? error.message : "Factory unavailable";
    renderTasks();
    renderEvents();
  }
}

void start();
