package continuum

import "context"

// An EventStore loads and saves events.
type EventStore interface {
	// LoadEvents loads all events for the given aggregate type and ID.
	LoadEvents(ctx context.Context, aggregateType string, aggregateID Identifier, fromVersion, toVersion int64) ([]*Event, error)

	// SaveEvents saves the given events.
	SaveEvents(ctx context.Context, events []*Event) error
}
