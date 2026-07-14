package triggerscheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

type Registry interface {
	Snapshot() triggerregistry.Snapshot
}

type Scheduler struct {
	registry  Registry
	cursors   *Store
	publisher agentrun.EventPublisher
	logger    *slog.Logger
	now       func() time.Time
}

type Status struct {
	ScheduleID string     `json:"scheduleId"`
	Last       *time.Time `json:"last,omitempty"`
	Next       *time.Time `json:"next,omitempty"`
	Skipped    uint64     `json:"skipped"`
}

func New(registry Registry, cursors *Store, publisher agentrun.EventPublisher, logger *slog.Logger, now func() time.Time) (*Scheduler, error) {
	if registry == nil || cursors == nil || publisher == nil || logger == nil || now == nil {
		return nil, fmt.Errorf("trigger scheduler: dependencies are required")
	}
	return &Scheduler{registry: registry, cursors: cursors, publisher: publisher, logger: logger, now: now}, nil
}

func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.Tick(ctx, s.now()); err != nil && ctx.Err() == nil {
			s.logger.Warn("publish scheduled trigger", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	snapshot := s.registry.Snapshot()
	schedules := slices.Clone(snapshot.Schedules)
	slices.SortFunc(schedules, func(left, right triggerregistry.Schedule) int { return strings.Compare(left.ID, right.ID) })
	for _, schedule := range schedules {
		if !schedule.Enabled {
			continue
		}
		if err := s.tickSchedule(ctx, schedule, now.UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) tickSchedule(ctx context.Context, schedule triggerregistry.Schedule, now time.Time) error {
	cursor, found := s.cursors.Cursor(schedule.ID)
	if !found || cursor.ScheduleRevision != schedule.Revision {
		return s.cursors.Advance(Cursor{ScheduleID: schedule.ID, ScheduleRevision: schedule.Revision, LastScheduledAt: now})
	}
	parsed, err := parser.Parse(schedule.Cron)
	if err != nil {
		return fmt.Errorf("trigger scheduler: parse %s: %w", schedule.ID, err)
	}
	location, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return fmt.Errorf("trigger scheduler: load %s timezone: %w", schedule.ID, err)
	}
	next := parsed.Next(cursor.LastScheduledAt.In(location)).UTC()
	if next.After(now) {
		return nil
	}
	oldest := next
	last := next
	skipped := uint64(0)
	for {
		candidate := parsed.Next(last.In(location)).UTC()
		if candidate.After(now) {
			break
		}
		last = candidate
		skipped++
	}
	event := scheduledEvent(schedule, oldest, now)
	if _, err := s.publisher.PublishBatch(ctx, []eventwire.Event{event}); err != nil {
		return fmt.Errorf("trigger scheduler: publish %s: %w", schedule.ID, err)
	}
	cursor.LastScheduledAt = last
	cursor.Skipped += skipped
	return s.cursors.Advance(cursor)
}

func (s *Scheduler) Statuses(now time.Time) []Status {
	var statuses []Status
	for _, schedule := range s.registry.Snapshot().Schedules {
		status := Status{ScheduleID: schedule.ID}
		cursor, found := s.cursors.Cursor(schedule.ID)
		if found && cursor.ScheduleRevision == schedule.Revision {
			last := cursor.LastScheduledAt
			status.Last = &last
			status.Skipped = cursor.Skipped
			if schedule.Enabled {
				if parsed, err := parser.Parse(schedule.Cron); err == nil {
					if location, err := time.LoadLocation(schedule.Timezone); err == nil {
						next := parsed.Next(maxTime(cursor.LastScheduledAt, now.UTC()).In(location)).UTC()
						status.Next = &next
					}
				}
			}
		}
		statuses = append(statuses, status)
	}
	slices.SortFunc(statuses, func(left, right Status) int { return strings.Compare(left.ScheduleID, right.ScheduleID) })
	return statuses
}

func scheduledEvent(schedule triggerregistry.Schedule, scheduledAt, receivedAt time.Time) eventwire.Event {
	attributes := make(map[string][]string, len(schedule.Attributes)+3)
	for key, values := range schedule.Attributes {
		attributes[key] = slices.Clone(values)
	}
	attributes[triggerregistry.AttributeScheduleID] = []string{schedule.ID}
	attributes[triggerregistry.AttributeScheduleRev] = []string{strconv.FormatUint(schedule.Revision, 10)}
	attributes[triggerregistry.AttributeScheduledAt] = []string{scheduledAt.Format(time.RFC3339)}
	digest := sha256.Sum256([]byte("factory-cron-v1\x00" + schedule.ID + "\x00" + strconv.FormatUint(schedule.Revision, 10) + "\x00" + scheduledAt.Format(time.RFC3339Nano)))
	return eventwire.Event{
		ID: "factory:cron:" + hex.EncodeToString(digest[:]), Source: eventwire.SourceFactory,
		Type: "cron", Action: "due", Subject: schedule.Subject, Attributes: attributes, ReceivedAt: receivedAt,
	}
}

func maxTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}
