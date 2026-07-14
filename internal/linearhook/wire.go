package linearhook

import (
	"context"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

const WireChannel = "linear-comments"

const (
	attributeDeliveryID      = "deliveryId"
	attributeCommentID       = "commentId"
	attributeIssueID         = "issueId"
	attributeIssueIdentifier = "issueIdentifier"
	attributeParentID        = "parentId"
	attributeURL             = "url"
)

func ToWire(event Event) eventwire.Event {
	attributes := map[string][]string{
		attributeDeliveryID:           {event.DeliveryID},
		attributeCommentID:            {event.CommentID},
		attributeIssueID:              {event.IssueID},
		attributeIssueIdentifier:      {event.IssueIdentifier},
		eventwire.AttributeProducer:   {"linear-webhook"},
		eventwire.AttributeProvenance: {"human"},
	}
	addWireAttribute(attributes, attributeParentID, event.ParentID)
	addWireAttribute(attributes, attributeURL, event.URL)
	return eventwire.Event{
		ID:         "linear:" + event.DeliveryID,
		Source:     eventwire.SourceLinear,
		Type:       "Comment",
		Action:     "create",
		Subject:    event.IssueIdentifier,
		Attributes: attributes,
		Channels:   []string{WireChannel},
		ReceivedAt: event.ReceivedAt,
	}
}

func FromWire(value eventwire.Event) (Event, bool) {
	if value.Source != eventwire.SourceLinear {
		return Event{}, false
	}
	event := Event{
		DeliveryID:      firstWireValue(value.Values(attributeDeliveryID)),
		CommentID:       firstWireValue(value.Values(attributeCommentID)),
		IssueID:         firstWireValue(value.Values(attributeIssueID)),
		IssueIdentifier: firstWireValue(value.Values(attributeIssueIdentifier)),
		ParentID:        firstWireValue(value.Values(attributeParentID)),
		URL:             firstWireValue(value.Values(attributeURL)),
		ReceivedAt:      value.ReceivedAt,
	}
	if event.DeliveryID == "" || event.CommentID == "" || event.IssueID == "" {
		return Event{}, false
	}
	return event, true
}

func ReadWire(path string, filter Filter, after uint64) (Batch, error) {
	if err := filter.Validate(); err != nil {
		return Batch{}, err
	}
	batch, err := eventwire.ReadChannel(path, WireChannel, eventwire.Filter{Source: eventwire.SourceLinear}, after)
	if err != nil {
		return Batch{}, err
	}
	result := Batch{Cursor: batch.Cursor, Events: []Event{}}
	for _, value := range batch.Events {
		event, ok := FromWire(value)
		if ok && filter.matches(event) {
			result.Events = append(result.Events, event)
		}
	}
	return result, nil
}

func WaitWire(ctx context.Context, path string, filter Filter, after uint64, interval time.Duration) (Batch, error) {
	batch, err := eventwire.Wait(ctx, func(cursor uint64) (eventwire.Batch, error) {
		result, readErr := ReadWire(path, filter, cursor)
		return eventwire.Batch{Cursor: result.Cursor, Events: toWireEvents(result.Events)}, readErr
	}, after, interval)
	result := Batch{Cursor: batch.Cursor, Events: []Event{}}
	for _, value := range batch.Events {
		if event, ok := FromWire(value); ok {
			result.Events = append(result.Events, event)
		}
	}
	return result, err
}

func toWireEvents(events []Event) []eventwire.Event {
	values := make([]eventwire.Event, len(events))
	for i := range events {
		values[i] = ToWire(events[i])
	}
	return values
}

func addWireAttribute(attributes map[string][]string, key, value string) {
	if value != "" {
		attributes[key] = []string{value}
	}
}

func firstWireValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
