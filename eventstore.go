package continuum

import "fmt"

type EventReader interface {
	ReadEvents(aggregateType, aggregateID string, fromVersion, toVersion int64) ([]*Event, error)
}

type EventWriter interface {
	WriteEvents(events []*Event) error
}

type LoadOptions struct {
	FromVersion int64
	ToVersion   int64
}

type EventStore struct {
	Reader EventReader
	Writer EventWriter
}

func (s *EventStore) LoadEvents(aggregateType, aggregateID string, opts ...LoadOptions) ([]*Event, error) {
	if s.Reader == nil {
		return nil, fmt.Errorf("no event reader configured")
	}

	mergedOpts := LoadOptions{}
	for _, opt := range opts {
		if opt.FromVersion > 0 {
			mergedOpts.FromVersion = opt.FromVersion
		}

		if opt.ToVersion > 0 {
			mergedOpts.ToVersion = opt.ToVersion
		}

	}

	if mergedOpts.FromVersion > 0 && mergedOpts.ToVersion > 0 && mergedOpts.FromVersion > mergedOpts.ToVersion {
		return nil, fmt.Errorf("invalid version range: from %d to %d", mergedOpts.FromVersion, mergedOpts.ToVersion)
	}

	events, err := s.Reader.ReadEvents(aggregateType, aggregateID, mergedOpts.FromVersion, mergedOpts.ToVersion)
	if err != nil {
		return nil, fmt.Errorf("reading events: %w", err)
	}

	return events, nil
}

func (s *EventStore) SaveEvents(events []*Event) error {
	if s.Writer == nil {
		return fmt.Errorf("no event writer configured")
	}

	err := s.Writer.WriteEvents(events)
	if err != nil {
		return fmt.Errorf("writing events: %w", err)
	}

	return nil
}
