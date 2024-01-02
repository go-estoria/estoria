package continuum

import "context"

// An EventStore loads and saves events.
type EventStore[E Entity] interface {
	// LoadEvents loads all events for the given aggregate type and ID.
	LoadEvents(ctx context.Context, aggregateType string, aggregateID Identifier, versions VersionSpec) ([]*BasicEvent, error)

	// SaveEvents saves the given events.
	SaveEvents(ctx context.Context, events []*BasicEvent) error
}
