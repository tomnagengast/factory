package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

type providerActivity interface {
	StagePayload(string, []byte) error
}

type providerPublisher interface {
	Publish(context.Context, eventwire.Event) (eventwire.Record, bool, error)
}

// ProviderAgentStarter turns provider coordination into an ordinary canonical
// Linear-label admission. The deterministic private payload is staged before
// publication so retained activity projection can never acknowledge a missing
// corpus entry.
type ProviderAgentStarter struct {
	coordinator projectsetup.ProviderCoordinator
	activity    providerActivity
	publisher   providerPublisher
	actorID     string
	label       string
	now         func() time.Time
}

func NewProviderAgentStarter(
	coordinator projectsetup.ProviderCoordinator,
	activity providerActivity,
	publisher providerPublisher,
	actorID, label string,
	now func() time.Time,
) (*ProviderAgentStarter, error) {
	if coordinator == nil || activity == nil || publisher == nil || actorID == "" || label == "" || now == nil {
		return nil, errors.New("app provider starter: coordinator, event authorities, actor, label, and clock are required")
	}
	return &ProviderAgentStarter{
		coordinator: coordinator, activity: activity, publisher: publisher,
		actorID: actorID, label: triggerregistry.CanonicalFold(label), now: now,
	}, nil
}

func (s *ProviderAgentStarter) Start(ctx context.Context, spec projectsetup.Spec) error {
	issue, err := s.coordinator.Ensure(ctx, spec)
	if err != nil {
		return err
	}
	if issue.ID == "" || issue.Identifier == "" {
		return errors.New("app provider starter: coordinator returned an incomplete issue")
	}
	digest := sha256.Sum256([]byte(spec.ProjectID + "\x00" + spec.Repository + "\x00" + spec.CloudURL))
	deliveryID := "project-provider:" + issue.ID + ":" + hex.EncodeToString(digest[:8])
	payload, err := json.Marshal(map[string]string{
		"deliveryId": deliveryID, "issueId": issue.ID, "issueIdentifier": issue.Identifier,
	})
	if err != nil {
		return err
	}
	if err := s.activity.StagePayload(deliveryID, payload); err != nil {
		return err
	}
	event := eventwire.Event{
		ID: "linear:" + deliveryID, Source: eventwire.SourceLinear, Type: "Issue", Action: "update", Subject: issue.Identifier,
		Attributes: map[string][]string{
			"deliveryId":                        {deliveryID},
			"issueIdentifier":                   {issue.Identifier},
			triggerregistry.AttributeActorID:    {s.actorID},
			triggerregistry.AttributeAddedLabel: {s.label},
			eventwire.AttributeProducer:         {"project-provider"},
			eventwire.AttributeProvenance:       {"factory"},
		},
		ReceivedAt: s.now().UTC(),
	}
	_, _, err = s.publisher.Publish(ctx, event)
	return err
}
