package aggregatereader

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

// An AggregateCreator is used by the AggregateReader to create new aggregates
// before populating them with events from the event store.
type AggregateCreator interface {
	NewAggregate(id continuum.Identifier) *continuum.Aggregate
}

// An EventLoader is used by the AggregateReader to load events from an event store.
type EventLoader interface {
	LoadEvents(ctx context.Context, id continuum.AggregateID) ([]continuum.Event, error)
}

// EventStoreReader is an AggregateReader that reads aggregates from an event store.
type EventStoreReader struct {
	EventStore EventLoader
}

// ReadAggregate reads an aggregate from the event store.
func (r EventStoreReader) ReadAggregate(ctx context.Context, id continuum.AggregateID) (*continuum.Aggregate, error) {
	slog.Default().WithGroup("aggregatereader").Debug("reading aggregate", "id", id)
	events, err := r.EventStore.LoadEvents(ctx, id)
	if err != nil {
		return nil, err
	}

	aggregate := id.NewAggregate()

	for _, event := range events {
		if err := aggregate.Apply(ctx, event); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}
	}

	return aggregate, nil
}
