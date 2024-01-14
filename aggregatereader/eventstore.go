package aggregatereader

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

type EventStoreReader[D continuum.AggregateData] struct {
	AggregateType continuum.AggregateType[D]
	EventStore    continuum.EventStore
}

func (r EventStoreReader[D]) ReadAggregate(ctx context.Context, id continuum.AggregateID) (*continuum.Aggregate[D], error) {
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
