package server

import (
	"time"

	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/workflow"
)

func testInitialClaim(trigger agentrun.Trigger) agentrun.InitialClaim {
	pinned := workflow.Pin(workflow.Default(time.Time{}))
	digest, err := pinned.Digest()
	if err != nil {
		panic(err)
	}
	return agentrun.InitialClaim{Trigger: trigger, Workflow: agentrun.ResolvedWorkflowCandidate(pinned, digest, 7)}
}
