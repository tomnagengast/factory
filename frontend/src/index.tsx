import { render } from "solid-js/web";
import {
  ActivityPage,
  HomePage,
} from "./home";
import { AgentPage, getAgentByReference } from "./agent-detail";
import { AgentActivityPage } from "./agent-activity";
import { SettingsPage } from "./settings";
import { LinearTaskDetailPage, NativeTaskDetailPage, TasksPage } from "./tasks";
import { TriggersPage } from "./triggers";
import { WorkflowsPage } from "./workflows";
import { WirePage } from "./wire";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("Root element not found");

const path = window.location.pathname;
const agentRoute = /^\/agents\/([^/]+)\/(\d+)\/run$/.exec(path);
const taskRoute = /^\/tasks\/(factory|linear)\/([^/]+)$/.exec(path);

render(() => {
  switch (path) {
    case "/": return <HomePage />;
    case "/home": return <ActivityPage />;
    case "/wire": return <WirePage />;
    case "/agents": return <AgentActivityPage />;
    case "/tasks": return <TasksPage />;
    case "/settings": return <SettingsPage />;
    case "/workflows": return <WorkflowsPage />;
    case "/triggers": return <TriggersPage />;
  }
  if (agentRoute) {
    const requestedSource = new URLSearchParams(window.location.search).get("source");
    const source = requestedSource === "factory" || requestedSource === "linear"
      ? requestedSource
      : undefined;
    return <AgentPage load={() => getAgentByReference(agentRoute[1], agentRoute[2], source)} />;
  }
  if (taskRoute) {
    const id = decodeURIComponent(taskRoute[2]);
    return taskRoute[1] === "factory"
      ? <NativeTaskDetailPage id={id} />
      : <LinearTaskDetailPage id={id} />;
  }
  return <main class="home-page"><section class="home-shell"><h1>Not found</h1></section></main>;
}, root);
