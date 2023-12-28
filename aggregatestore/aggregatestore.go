package aggregatestore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
)

// An EntityFactory creates new entities.
type EntityFactory[E continuum.Entity] func(id continuum.Identifier) E

// An AggregateStore utilizes an EventStore to load and save aggregates.
type AggregateStore[E continuum.Entity] struct {
	EventStore continuum.EventStore
	NewEntity  EntityFactory[E]
}

var _ continuum.AggregateStore[continuum.Entity] = (*AggregateStore[continuum.Entity])(nil)

// New creates a new AggregateStore.
func New[E continuum.Entity](eventStore continuum.EventStore, entityFactory EntityFactory[E]) *AggregateStore[E] {
	return &AggregateStore[E]{
		EventStore: eventStore,
		NewEntity:  entityFactory,
	}
}

// Create creates a new aggregate with the given ID.
func (s *AggregateStore[E]) Create(aggregateID continuum.Identifier) (*continuum.Aggregate[E], error) {
	aggregate := &continuum.Aggregate[E]{
		Entity:        s.NewEntity(aggregateID),
		UnsavedEvents: make([]*continuum.BasicEvent, 0),
		Version:       0,
	}

	return aggregate, nil
}

// Load loads an aggregate with the given ID.
func (s *AggregateStore[E]) Load(ctx context.Context, aggregateID continuum.Identifier) (*continuum.Aggregate[E], error) {
	aggregate, err := s.Create(aggregateID)
	events, err := s.EventStore.LoadEvents(ctx, aggregate.TypeName(), aggregate.ID(), 0, 0)
	if err != nil {
		return nil, fmt.Errorf("loading events: %w", err)
	}

	if len(events) == 0 {
		return nil, continuum.AggregateNotFoundError[E]{ID: aggregateID}
	}

	if err := aggregate.Apply(ctx, events...); err != nil {
		return nil, fmt.Errorf("applying event: %w", err)
	}

	slog.Info("loaded aggregate", "aggregate_id", aggregate.ID, "aggregate_type", aggregate.TypeName(), "events", len(events))

	return aggregate, nil
}

// Save saves the given aggregate.
func (s *AggregateStore[E]) Save(ctx context.Context, a *continuum.Aggregate[E]) error {
	slog.Info("saving aggregate", "aggregate_id", a.ID, "aggregate_type", a.TypeName(), "events", len(a.UnsavedEvents))
	if err := s.EventStore.SaveEvents(ctx, a.UnsavedEvents); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	a.UnsavedEvents = make([]*continuum.BasicEvent, 0)

	return nil
}
