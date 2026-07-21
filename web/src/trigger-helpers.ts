import type { Trigger, Workflow } from "./types";

export type TriggerFilters = {
  eventTypes: string[];
  workflowIds: number[];
};

export type TriggerWorkflowOption = {
  id: number;
  label: string;
};

export function filterTriggers(triggers: readonly Trigger[], filters: TriggerFilters) {
  return triggers.filter((trigger) => {
    const eventMatches = filters.eventTypes.length === 0 || filters.eventTypes.includes(trigger.eventType);
    const workflowMatches = filters.workflowIds.length === 0 || filters.workflowIds.includes(trigger.workflowId);
    return eventMatches && workflowMatches;
  });
}

export function triggerEventTypeOptions(triggers: readonly Trigger[]) {
  return [...new Set(triggers.map((trigger) => trigger.eventType))]
    .sort((left, right) => left.localeCompare(right, undefined, { numeric: true, sensitivity: "base" }));
}

export function triggerWorkflowOptions(
  triggers: readonly Trigger[],
  workflows: readonly Workflow[],
): TriggerWorkflowOption[] {
  const workflowsByID = new Map(workflows.map((workflow) => [workflow.id, workflow]));
  return [...new Set(triggers.map((trigger) => trigger.workflowId))]
    .map((id) => ({
      id,
      label: workflowsByID.has(id) ? `${workflowsByID.get(id)!.name} (#${id})` : `Workflow ${id}`,
    }))
    .sort((left, right) => {
      const labelOrder = left.label.localeCompare(right.label, undefined, { numeric: true, sensitivity: "base" });
      return labelOrder || left.id - right.id;
    });
}
