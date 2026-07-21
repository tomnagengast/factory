package deployment

import (
	"os"

	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/store"
)

type Recorder struct {
	store    *store.Store
	identity state.ReleaseIdentity
}

func FromEnvironment() state.ReleaseIdentity {
	return state.ReleaseIdentity{
		Commit:          os.Getenv("FACTORY_RELEASE_COMMIT"),
		Tree:            os.Getenv("FACTORY_RELEASE_TREE"),
		BuildID:         os.Getenv("FACTORY_RELEASE_BUILD"),
		DeploymentID:    os.Getenv("FACTORY_RELEASE_DEPLOYMENT"),
		ContractVersion: os.Getenv("FACTORY_RELEASE_CONTRACT"),
	}
}

func NewRecorder(eventStore *store.Store, identity state.ReleaseIdentity) *Recorder {
	return &Recorder{store: eventStore, identity: identity}
}

func (r *Recorder) Started() error {
	return r.append(state.DeploymentStarted, 0, "")
}

func (r *Recorder) Quiescing(workflowActive int) error {
	return r.append(state.DeploymentQuiescing, workflowActive, "")
}

func (r *Recorder) Quiesced(workflowActive int) error {
	return r.append(state.DeploymentQuiesced, workflowActive, "")
}

func (r *Recorder) Resumed(reason string, workflowActive int) error {
	return r.append(state.DeploymentResumed, workflowActive, reason)
}

func (r *Recorder) append(eventType string, workflowActive int, reason string) error {
	_, err := r.store.Append(eventType, Data(r.identity, workflowActive, reason))
	return err
}

func Data(identity state.ReleaseIdentity, workflowActive int, reason string) state.DeploymentData {
	return state.DeploymentData{
		Commit: identity.Commit, Tree: identity.Tree, BuildID: identity.BuildID,
		DeploymentID: identity.DeploymentID, ContractVersion: identity.ContractVersion,
		WorkflowActive: workflowActive, Reason: reason,
	}
}
