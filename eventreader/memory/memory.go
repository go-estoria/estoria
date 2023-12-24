package memory

import (
	"fmt"

	"github.com/jefflinse/continuum"
)

type EventReader struct {
	events continuum.EventsByAggregateType
}

func NewEventReader(events continuum.EventsByAggregateType) *EventReader {
	return &EventReader{
		events: events,
	}
}

func (s *EventReader) ReadEvents(aggregateType string, aggregateID continuum.Identifier, fromVersion int64, toVersion int64) ([]*continuum.Event, error) {
	if _, ok := s.events[aggregateType]; !ok {
		return nil, fmt.Errorf("aggregate type not found: %s", aggregateType)
	}

	events, ok := s.events[aggregateType][aggregateID]
	if !ok {
		return nil, fmt.Errorf("aggregate not found: %s", aggregateID)
	}

	if fromVersion > 0 {
		events = events[fromVersion:]
	}

	if toVersion > 0 {
		events = events[:toVersion-fromVersion]
	}

	return events, nil
}
