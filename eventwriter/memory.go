package eventwriter

import "github.com/jefflinse/continuum"

type EventWriter struct {
	events map[string][]continuum.Event
}

func NewEventWriter(events map[string][]continuum.Event) *EventWriter {
	return &EventWriter{
		events: events,
	}
}

func (s *EventWriter) WriteEvents(events []continuum.Event) error {
	for _, event := range events {
		s.events[event.AggregateID] = append(s.events[event.AggregateID], event)
	}

	return nil
}
