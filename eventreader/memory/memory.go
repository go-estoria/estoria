package memory

import (
	"context"
	"fmt"

	"github.com/jefflinse/continuum"
)

// An EventReader reads events from memory.
type EventReader struct {
	events continuum.EventsByAggregateType
}

// New creates a new in-memory EventReader.
func New(events continuum.EventsByAggregateType) *EventReader {
	return &EventReader{
		events: events,
	}
}

// ReadEvents reads events for the given aggregate type and ID.
func (s *EventReader) ReadEvents(_ context.Context, aggregateType string, aggregateID continuum.Identifier, versions continuum.VersionSpec) ([]*continuum.BasicEvent, error) {
	if _, ok := s.events[aggregateType]; !ok {
		return nil, fmt.Errorf("aggregate type not found: %s", aggregateType)
	}

	events, ok := s.events[aggregateType][aggregateID]
	if !ok {
		return nil, fmt.Errorf("aggregate not found: %s", aggregateID)
	}

	if versions.FromVersion > 0 {
		events = events[versions.FromVersion:]
	}

	if versions.ToVersion > 0 {
		events = events[:versions.ToVersion-versions.FromVersion]
	}

	return events, nil
}
