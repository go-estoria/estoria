package eventreader

import (
	"fmt"

	"github.com/jefflinse/continuum"
)

type EventReader struct {
	events map[string][]continuum.Event
}

func NewEventReader(events map[string][]continuum.Event) *EventReader {
	return &EventReader{
		events: events,
	}
}

func (s *EventReader) ReadEvents(aggregateID string, eventTypes []string, fromVersion int64, toVersion int64) ([]continuum.Event, error) {
	events, ok := s.events[aggregateID]
	if !ok {
		return nil, fmt.Errorf("aggregate not found: %s", aggregateID)
	}

	if fromVersion > 0 {
		events = events[fromVersion:]
	}

	if toVersion > 0 {
		events = events[:toVersion-fromVersion]
	}

	if len(eventTypes) > 0 {
		filteredEvents := []continuum.Event{}
		for _, event := range events {
			for _, eventType := range eventTypes {
				if event.Data.Type() == eventType {
					filteredEvents = append(filteredEvents, event)
					break
				}
			}
		}

		events = filteredEvents
	}

	return events, nil
}
