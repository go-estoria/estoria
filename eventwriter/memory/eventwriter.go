package eventwriter

import (
	"log/slog"

	"github.com/jefflinse/continuum"
)

type EventWriter struct {
	events continuum.EventsByAggregateType
}

func NewEventWriter(events continuum.EventsByAggregateType) *EventWriter {
	return &EventWriter{
		events: events,
	}
}

func (s *EventWriter) WriteEvents(events []*continuum.Event) error {
	for _, event := range events {
		if _, ok := s.events[event.AggregateType]; !ok {
			slog.Info("creating event type map", "event_type", event.Data.EventTypeName())
			s.events[event.AggregateType] = make(continuum.EventsByID)
		}

		slog.Info("appending event to event type map", "event_type", event.Data.EventTypeName(), "aggregate_id", event.AggregateID)
		s.events[event.AggregateType][event.AggregateID] = append(
			s.events[event.AggregateType][event.AggregateID],
			event,
		)
	}

	return nil
}
