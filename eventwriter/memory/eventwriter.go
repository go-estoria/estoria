package eventwriter

import "github.com/jefflinse/continuum"

type EventWriter struct {
	events continuum.EventMap
}

func NewEventWriter(events continuum.EventMap) *EventWriter {
	return &EventWriter{
		events: events,
	}
}

func (s *EventWriter) WriteEvents(events []*continuum.Event) error {
	for _, event := range events {
		if _, ok := s.events[event.Data.EventTypeName()]; !ok {
			s.events[event.Data.EventTypeName()] = make(map[string][]*continuum.Event)
		}

		s.events[event.Data.EventTypeName()][event.AggregateID] = append(
			s.events[event.Data.EventTypeName()][event.AggregateID],
			event,
		)
	}

	return nil
}
