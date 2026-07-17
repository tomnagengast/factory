package app

import (
	"context"
	"errors"

	"github.com/tomnagengast/factory/internal/policy"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/triggerrouter"
)

// NativeAdmitter preserves the retained task-service result contract while
// serializing native admission with the canonical policy generation and Runs
// journal. A mixed settings/registry/workflow read fails closed instead of
// admitting causation from more than one policy generation.
type NativeAdmitter struct {
	coordinator *policy.Coordinator
	admitter    *runs.Admitter
}

func NewNativeAdmitter(coordinator *policy.Coordinator, admitter *runs.Admitter) (*NativeAdmitter, error) {
	if coordinator == nil || admitter == nil {
		return nil, errors.New("app native admitter: coordinator and canonical admitter are required")
	}
	return &NativeAdmitter{coordinator: coordinator, admitter: admitter}, nil
}

func (a *NativeAdmitter) AdmitNative(value triggerrouter.NativeAdmission) (triggerrouter.Invocation, bool, error) {
	return a.admit(value, "")
}

func (a *NativeAdmitter) AdmitNativeContinuation(value triggerrouter.NativeAdmission, eventKey string) (triggerrouter.Invocation, bool, error) {
	if eventKey == "" {
		return triggerrouter.Invocation{}, false, errors.New("app native admitter: continuation identity is required")
	}
	return a.admit(value, eventKey)
}

func (a *NativeAdmitter) admit(value triggerrouter.NativeAdmission, eventKey string) (triggerrouter.Invocation, bool, error) {
	var admitted runs.Run
	var created bool
	err := a.coordinator.Admit(func(snapshot policy.Snapshot) error {
		settings := snapshot.Settings()
		registry := snapshot.Registry()
		definition, found := snapshot.Workflow(value.Workflow.ID)
		if value.PolicyRevision != settings.Revision || value.RegistryRevision != registry.Revision || !found {
			return triggerrouter.ErrPolicyConflict
		}
		digest, digestErr := policy.WorkflowDigest(definition)
		if digestErr != nil || digest != value.WorkflowDigest {
			return triggerrouter.ErrPolicyConflict
		}
		candidate := runs.NativeAdmission{
			Task: value.Task, Workflow: value.Workflow, WorkflowDigest: value.WorkflowDigest,
			PolicyRevision: value.PolicyRevision, RegistryRevision: value.RegistryRevision,
			PolicyGeneration: snapshot.Generation(), AdmittedAt: value.AdmittedAt.UTC(),
		}
		var err error
		if eventKey == "" {
			admitted, created, err = a.admitter.AdmitNative(candidate)
		} else {
			admitted, created, err = a.admitter.Continue(candidate, eventKey)
		}
		return err
	})
	if err != nil {
		return triggerrouter.Invocation{}, false, err
	}
	return legacyInvocation(admitted, legacyRule(admitted)), created, nil
}

type errorReconciler interface {
	Reconcile(context.Context) error
}

type runReconciler interface {
	Reconcile(context.Context)
}

// TaskReconciler advances the private task outbox before asking the canonical
// Run manager to observe newly admitted work.
type TaskReconciler struct {
	outbox errorReconciler
	runs   runReconciler
}

func NewTaskReconciler(outbox errorReconciler, runManager runReconciler) (*TaskReconciler, error) {
	if outbox == nil || runManager == nil {
		return nil, errors.New("app task reconciler: task outbox and Run manager are required")
	}
	return &TaskReconciler{outbox: outbox, runs: runManager}, nil
}

func (r *TaskReconciler) Reconcile(ctx context.Context) error {
	if err := r.outbox.Reconcile(ctx); err != nil {
		return err
	}
	r.runs.Reconcile(ctx)
	return ctx.Err()
}
