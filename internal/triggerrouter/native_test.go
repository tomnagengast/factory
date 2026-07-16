package triggerrouter

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/workflow"
)

func TestAdmitNativeIsDurableAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pinned := workflow.Pin(workflow.Default(time.Time{}))
	digest, err := pinned.Digest()
	if err != nil {
		t.Fatal(err)
	}
	task := taskmodel.TaskRef{Source: taskmodel.SourceFactory, ProviderID: "task-0123456789abcdef", Identifier: "FAC-1"}
	admission := NativeAdmission{Task: task, Workflow: pinned, WorkflowDigest: digest, PolicyRevision: 7, RegistryRevision: 3, AdmittedAt: time.Now().UTC()}
	invocation, created, err := store.AdmitNative(admission)
	if err != nil || !created || !NativeInvocationMatches(invocation, task, digest) {
		t.Fatalf("admit: invocation=%#v created=%t err=%v", invocation, created, err)
	}
	repeated, created, err := store.AdmitNative(admission)
	if err != nil || created || repeated.ID != invocation.ID {
		t.Fatalf("repeat: invocation=%#v created=%t err=%v", repeated, created, err)
	}
	feedback, created, err := store.AdmitNativeContinuation(admission, "message:msg-0123456789abcdef")
	if err != nil || !created || !NativeFeedbackInvocation(feedback) {
		t.Fatalf("feedback invocation=%#v created=%t err=%v", feedback, created, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	durable, found := reopened.Invocation(invocation.ID)
	if !found || !NativeInvocationMatches(durable, task, digest) {
		t.Fatalf("durable invocation = %#v found=%t", durable, found)
	}
}
