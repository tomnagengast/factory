import { Route, Router } from "@solidjs/router";
import { CommentView } from "./comments";
import { Events, EventView } from "./event-views";
import { History, HistoryStatusPage, HistoryView } from "./history";
import { Home } from "./home";
import { ProjectNew, Projects, ProjectView } from "./projects";
import { SettingsPage } from "./settings";
import { TaskNew, Tasks, TaskView } from "./tasks";
import { TriggerNew, Triggers, TriggerView } from "./triggers";
import { Shell } from "./ui";
import { WorkflowNew, Workflows, WorkflowView } from "./workflows";

export function App() {
  return (
    <Router root={Shell}>
      <Route path="/" component={Home} />
      <Route path="/projects" component={Projects} />
      <Route path="/projects/new" component={ProjectNew} />
      <Route path="/projects/:project" component={ProjectView} />
      <Route path="/tasks" component={Tasks} />
      <Route path="/tasks/new" component={TaskNew} />
      <Route path="/tasks/:task" component={TaskView} />
      <Route path="/tasks/:task/comments/:comment" component={CommentView} />
      <Route path="/events" component={Events} />
      <Route path="/events/:event" component={EventView} />
      <Route path="/triggers" component={Triggers} />
      <Route path="/triggers/new" component={TriggerNew} />
      <Route path="/triggers/:trigger" component={TriggerView} />
      <Route path="/workflows" component={Workflows} />
      <Route path="/workflows/new" component={WorkflowNew} />
      <Route path="/workflows/:workflow" component={WorkflowView} />
      <Route path="/history" component={History} />
      <Route path="/history/running" component={() => <HistoryStatusPage status="running" label="Running" />} />
      <Route path="/history/waiting" component={() => <HistoryStatusPage status="waiting" label="Waiting" />} />
      <Route path="/history/failed" component={() => <HistoryStatusPage status="failed" label="Failed" />} />
      <Route path="/history/completed" component={() => <HistoryStatusPage status="completed" label="Completed" />} />
      <Route path="/history/:item" component={HistoryView} />
      <Route path="/settings" component={SettingsPage} />
    </Router>
  );
}
