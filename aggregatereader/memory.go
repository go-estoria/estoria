package aggregatereader

import (
	"context"

	"github.com/jefflinse/continuum"
)

type MemoryReader[D continuum.AggregateData] struct {
	AggreateType continuum.AggregateType[D]
	EventStore   continuum.EventStore
}

func (r MemoryReader[D]) ReadAggregate(ctx context.Context, id continuum.AggregateID) (*continuum.Aggregate[D], error) {
	events, err := r.EventStore.LoadEvents(ctx, id)
	if err != nil {
		return nil, err
	}

	aggregate := r.AggreateType.New()

	for _, event := range events {
		aggregate.Data.ApplyEvent(ctx, event.Data())
	}

	return aggregate, nil
}
