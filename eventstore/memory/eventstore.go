package memory

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/jefflinse/continuum"
)

type EventStore struct {
	Events []continuum.Event

	mu sync.RWMutex
}

func (s *EventStore) LoadEvents(ctx context.Context, aggregateID continuum.AggregateID) ([]continuum.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	events := []continuum.Event{}
	for _, event := range s.Events {
		if event.AggregateID().Equals(aggregateID) {
			slog.Default().WithGroup("eventreader").Debug("reading event", "event_id", event.ID())
			events = append(events, event)
		}
	}

	return events, nil
}

// SaveEvents saves the given events to the event store.
func (s *EventStore) SaveEvents(ctx context.Context, events ...continuum.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// simulate a transaction by adding all or none of the events
	tx := []continuum.Event{}

	for _, event := range events {
		if slices.ContainsFunc(s.Events, func(e continuum.Event) bool {
			return event.ID().Equals(e.ID())
		}) {
			return ErrEventExists{EventID: event.ID()}
		}

		slog.Default().WithGroup("eventwriter").Debug("writing event", "event_id", event.ID())
		tx = append(tx, event)
	}

	s.Events = append(s.Events, tx...)

	return nil
}

// ErrEventExists is returned when attempting to write an event that already exists.
type ErrEventExists struct {
	EventID continuum.EventID
}

// Error returns the error message.
func (e ErrEventExists) Error() string {
	return fmt.Sprintf("event already exists: %s", e.EventID)
}
