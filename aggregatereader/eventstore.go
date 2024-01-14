package aggregatereader

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

// EventStoreReader is an AggregateReader that reads aggregates from an event store.
type EventStoreReader struct {
	AggregateType continuum.AggregateType
	EventStore    continuum.EventStore
}

// ReadAggregate reads an aggregate from the event store.
func (r EventStoreReader) ReadAggregate(ctx context.Context, id continuum.AggregateID) (*continuum.Aggregate, error) {
	slog.Default().WithGroup("aggregatereader").Debug("reading aggregate", "id", id)
	events, err := r.EventStore.LoadEvents(ctx, id)
	if err != nil {
		return nil, err
	}

	aggregate := r.AggregateType.NewAggregate(id.ID)

	for _, event := range events {
		if err := aggregate.Data.ApplyEvent(ctx, event.Data()); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}
	}

	return aggregate, nil
}
