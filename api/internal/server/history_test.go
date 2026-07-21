package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/tomnagengast/factory/api/internal/state"
)

func TestWorkflowHistoryFiltersPagesAndMovesMembership(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	workflow, _ := wire.Publish(state.WorkflowDiscovered, state.WorkflowData{Name: "history-test"})
	newRun := func(triggerID int64) (state.WorkflowRunData, int64) {
		t.Helper()
		source, err := wire.Publish("history.source", map[string]int64{"triggerId": triggerID})
		if err != nil {
			t.Fatal(err)
		}
		claim := state.WorkflowRunData{
			TriggerID: triggerID, WorkflowID: workflow.ID,
			WorkflowName: "history-test", SourceEventID: source.ID,
		}
		started, err := wire.Publish(state.WorkflowRunStarted, claim)
		if err != nil {
			t.Fatal(err)
		}
		return claim, started.ID
	}

	_, oldestRunning := newRun(1)
	_, newestRunning := newRun(2)
	waitingClaim, waitingID := newRun(3)
	if _, err := wire.Publish(state.WorkflowRunWaiting, state.WorkflowRunStateData{RunID: waitingID}); err != nil {
		t.Fatal(err)
	}
	completedClaim, completedID := newRun(4)
	if _, err := wire.Publish(state.WorkflowRunCompleted, completedClaim); err != nil {
		t.Fatal(err)
	}
	failedClaim, failedID := newRun(5)
	if _, err := wire.Publish(state.WorkflowRunFailed, failedClaim); err != nil {
		t.Fatal(err)
	}

	handler := testServer(t, wire).Handler()
	decode := func(path string) historyCollectionResponse {
		t.Helper()
		response := requestJSON(t, handler, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, response.Code, response.Body)
		}
		var result historyCollectionResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	running := decode("/api/history?status=running&limit=1")
	checkpoint := wire.LastID()
	if running.CheckpointEventID != checkpoint || len(running.History) != 1 || running.History[0].ID != newestRunning {
		t.Fatalf("running page = %#v, checkpoint = %d", running.History, running.CheckpointEventID)
	}
	older := decode(fmt.Sprintf("/api/history?status=running&limit=1&before=%d", newestRunning))
	if len(older.History) != 1 || older.History[0].ID != oldestRunning {
		t.Fatalf("older running page = %#v", older.History)
	}
	for status, id := range map[string]int64{
		"waiting": waitingID, "failed": failedID, "completed": completedID,
	} {
		result := decode("/api/history?status=" + status + "&limit=5")
		if len(result.History) != 1 || result.History[0].ID != id || result.History[0].Status != status {
			t.Fatalf("%s history = %#v", status, result.History)
		}
	}

	for _, status := range []string{"done", "Running", "unknown"} {
		response := requestJSON(t, handler, http.MethodGet, "/api/history?status="+status, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status %q response = %d %s", status, response.Code, response.Body)
		}
	}
	if bare := decode("/api/history?limit=10"); len(bare.History) != 5 {
		t.Fatalf("bare history = %#v", bare.History)
	}
	detail := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/history/%d", completedID), "")
	if detail.Code != http.StatusOK {
		t.Fatalf("numeric detail = %d %s", detail.Code, detail.Body)
	}

	if _, err := wire.Publish(state.WorkflowRunResumed, state.WorkflowRunStateData{RunID: waitingID}); err != nil {
		t.Fatal(err)
	}
	if waiting := decode("/api/history?status=waiting&limit=5"); len(waiting.History) != 0 {
		t.Fatalf("resumed run remained waiting: %#v", waiting.History)
	}
	running = decode("/api/history?status=running&limit=5")
	if len(running.History) != 3 || running.History[0].ID != waitingID {
		t.Fatalf("resumed run did not move to running: %#v", running.History)
	}
	if _, err := wire.Publish(state.WorkflowRunCompleted, waitingClaim); err != nil {
		t.Fatal(err)
	}
	completed := decode("/api/history?status=completed&limit=5")
	if len(completed.History) != 2 || completed.History[0].ID != completedID || completed.History[1].ID != waitingID {
		t.Fatalf("completed membership = %#v", completed.History)
	}
}

type historyCollectionResponse struct {
	History           []state.WorkflowRun `json:"history"`
	CheckpointEventID int64               `json:"checkpointEventId"`
}
