package memoryeventstore

import (
	"fmt"

	"github.com/jefflinse/continuum"
	memoryeventreader "github.com/jefflinse/continuum/eventreader/memory"
	memoryeventwriter "github.com/jefflinse/continuum/eventwriter/memory"
)

type EventStore struct {
	reader continuum.EventReader
	writer continuum.EventWriter
}

func NewEventStore() *EventStore {
	events := make(continuum.EventMap)
	return &EventStore{
		reader: memoryeventreader.NewEventReader(events),
		writer: memoryeventwriter.NewEventWriter(events),
	}
}

func (s *EventStore) LoadEvents(aggregateType, aggregateID string, fromVersion, toVersion int64) ([]continuum.Event, error) {
	events, err := s.reader.ReadEvents(aggregateType, aggregateID, fromVersion, toVersion)
	if err != nil {
		return nil, fmt.Errorf("reading events: %w", err)
	}

	return events, nil
}

func (s *EventStore) SaveEvents(events []continuum.Event) error {
	err := s.writer.WriteEvents(events)
	if err != nil {
		return fmt.Errorf("writing events: %w", err)
	}

	return nil
}
