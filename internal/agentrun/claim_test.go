package agentrun

import (
	"time"

	"github.com/tomnagengast/factory/internal/workflow"
)

func testInitialClaim(trigger Trigger) InitialClaim {
	pinned := workflow.Pin(workflow.Default(time.Time{}))
	digest, err := pinned.Digest()
	if err != nil {
		panic(err)
	}
	return InitialClaim{Trigger: trigger, Workflow: ResolvedWorkflowCandidate(pinned, digest, 7)}
}
