package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHumanResultUsesPlainCommentWithoutSchema(t *testing.T) {
	result, err := humanResult("Please fix the timeout first.", nil)
	if err != nil || string(result) != `"Please fix the timeout first."` {
		t.Fatalf("plain result = %s, %v", result, err)
	}
}

func TestHumanResultValidatesStructuredComment(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"additionalProperties":false,
		"required":["approved","blockers"],
		"properties":{
			"approved":{"type":"boolean"},
			"blockers":{"type":"array","items":{"type":"string"}}
		}
	}`)
	result, err := humanResult(`{"approved":false,"blockers":["Fix the timeout."]}`, schema)
	if err != nil || string(result) != `{"approved":false,"blockers":["Fix the timeout."]}` {
		t.Fatalf("structured result = %s, %v", result, err)
	}
	for _, response := range []string{
		`not json`,
		`{"approved":false}`,
		`{"approved":"no","blockers":[]}`,
		`{"approved":true,"blockers":[],"extra":1}`,
	} {
		if _, err := humanResult(response, schema); err == nil {
			t.Fatalf("invalid response accepted: %s", response)
		}
	}
}

func TestHumanResultReportsMissingRequiredField(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"required":["decision"],
		"properties":{"decision":{"type":"string"}}
	}`)
	_, err := humanResult(`{}`, schema)
	if err == nil || !strings.Contains(err.Error(), "response.decision is required") {
		t.Fatalf("missing field error = %v", err)
	}
}
