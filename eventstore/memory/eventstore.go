package memory

import (
	"context"
	"fmt"
	"log/slog"
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

func (s *EventStore) SaveEvent(ctx context.Context, event continuum.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range s.Events {
		if eventID := event.ID(); e.ID().Equals(eventID) {
			return ErrEventExists{
				EventID: eventID,
			}
		}
	}

	slog.Default().WithGroup("eventwriter").Debug("writing event", "event_id", event.ID())

	s.Events = append(s.Events, event)
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
