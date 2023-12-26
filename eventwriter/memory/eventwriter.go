package eventwriter

import (
	"context"

	"github.com/jefflinse/continuum"
)

// EventWriter is an event writer that stores events in memory.
type EventWriter struct {
	events continuum.EventsByAggregateType
}

// New creates a new in-memory EventWriter.
func New(events continuum.EventsByAggregateType) *EventWriter {
	return &EventWriter{
		events: events,
	}
}

// WriteEvents writes the given events to memory.
func (s *EventWriter) WriteEvents(_ context.Context, events []*continuum.Event) error {
	for _, event := range events {
		if _, ok := s.events[event.AggregateType]; !ok {
			s.events[event.AggregateType] = make(continuum.EventsByID)
		}

		s.events[event.AggregateType][event.AggregateID] = append(
			s.events[event.AggregateType][event.AggregateID],
			event,
		)
	}

	return nil
}
