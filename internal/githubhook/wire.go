package githubhook

import (
	"context"
	"strconv"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

const WireChannel = "github"

const (
	attributeDeliveryID  = "deliveryId"
	attributeRepository  = "repository"
	attributePullRequest = "pullRequest"
	attributeHeadBranch  = "headBranch"
	attributeHeadSHA     = "headSha"
	attributeStatus      = "status"
	attributeConclusion  = "conclusion"
	attributeURL         = "url"
)

func ToWire(event Event) eventwire.Event {
	attributes := map[string][]string{
		attributeDeliveryID:           {event.DeliveryID},
		attributeRepository:           {event.Repository},
		eventwire.AttributeProducer:   {"github-webhook"},
		eventwire.AttributeProvenance: {"github"},
	}
	for _, number := range event.PullRequests {
		attributes[attributePullRequest] = append(attributes[attributePullRequest], strconv.Itoa(number))
	}
	addAttribute(attributes, attributeHeadBranch, event.HeadBranch)
	addAttribute(attributes, attributeHeadSHA, event.HeadSHA)
	addAttribute(attributes, attributeStatus, event.Status)
	addAttribute(attributes, attributeConclusion, event.Conclusion)
	addAttribute(attributes, attributeURL, event.URL)
	return eventwire.Event{
		ID:         "github:" + event.DeliveryID,
		Source:     eventwire.SourceGitHub,
		Type:       event.Type,
		Action:     event.Action,
		Subject:    event.Repository,
		Attributes: attributes,
		Channels:   []string{WireChannel},
		ReceivedAt: event.ReceivedAt,
	}
}

func FromWire(value eventwire.Event) (Event, bool) {
	deliveryID := first(value.Values(attributeDeliveryID))
	if value.Source != eventwire.SourceGitHub || deliveryID == "" {
		return Event{}, false
	}
	event := Event{
		DeliveryID: deliveryID,
		Type:       value.Type,
		Action:     value.Action,
		Repository: first(value.Values(attributeRepository)),
		HeadBranch: first(value.Values(attributeHeadBranch)),
		HeadSHA:    first(value.Values(attributeHeadSHA)),
		Status:     first(value.Values(attributeStatus)),
		Conclusion: first(value.Values(attributeConclusion)),
		URL:        first(value.Values(attributeURL)),
		ReceivedAt: value.ReceivedAt,
	}
	if event.DeliveryID == "" || event.Repository == "" {
		return Event{}, false
	}
	for _, value := range value.Values(attributePullRequest) {
		number, err := strconv.Atoi(value)
		if err != nil || number < 1 {
			return Event{}, false
		}
		event.PullRequests = append(event.PullRequests, number)
	}
	return event, true
}

func ReadWire(path string, filter Filter, after uint64) (Batch, error) {
	if err := filter.Validate(); err != nil {
		return Batch{}, err
	}
	batch, err := eventwire.ReadChannel(path, WireChannel, eventwire.Filter{
		Source: eventwire.SourceGitHub,
		Attributes: map[string]string{
			attributeRepository: filter.Repository,
		},
	}, after)
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

func addAttribute(attributes map[string][]string, key, value string) {
	if value != "" {
		attributes[key] = []string{value}
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
