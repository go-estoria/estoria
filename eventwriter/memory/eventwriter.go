package eventwriter

import (
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
			s.events[event.AggregateType] = make(continuum.EventsByID)
		}

		s.events[event.AggregateType][event.AggregateID] = append(
			s.events[event.AggregateType][event.AggregateID],
			event,
		)
	}

	return nil
}
