package aggregatestore

import (
	"fmt"
	"log/slog"

	"github.com/jefflinse/continuum"
	"github.com/jefflinse/continuum/eventstore"
)

type AggregateStore[E continuum.Entity] struct {
	EventStore *eventstore.EventStore
	NewEntity  func(id string) E
}

func New[E continuum.Entity](eventStore *eventstore.EventStore, entityFactory func(id string) E) *AggregateStore[E] {
	return &AggregateStore[E]{
		EventStore: eventStore,
		NewEntity:  entityFactory,
	}
}

func (s *AggregateStore[E]) Create(aggregateID string) (*continuum.Aggregate[E], error) {
	aggregate := &continuum.Aggregate[E]{
		Data:          s.NewEntity(aggregateID),
		Events:        make([]*continuum.Event, 0),
		UnsavedEvents: make([]*continuum.Event, 0),
		Version:       0,
	}

	return aggregate, nil
}

func (s *AggregateStore[E]) Load(aggregateID string) (*continuum.Aggregate[E], error) {
	aggregate, err := s.Create(aggregateID)
	events, err := s.EventStore.LoadEvents(aggregate.TypeName(), aggregate.ID())
	if err != nil {
		return nil, fmt.Errorf("loading events: %w", err)
	}

	if len(events) == 0 {
		return nil, continuum.AggregateNotFoundError[E]{ID: aggregateID}
	}

	aggregate.Events = events

	slog.Info("loaded aggregate", "aggregate_id", aggregate.ID, "aggregate_type", aggregate.TypeName(), "events", len(aggregate.Events))

	for _, event := range aggregate.Events {
		if err := aggregate.Apply(event); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}
	}

	return aggregate, nil
}

func (s *AggregateStore[E]) Save(a *continuum.Aggregate[E]) error {
	slog.Info("saving aggregate", "aggregate_id", a.ID, "aggregate_type", a.TypeName(), "events", len(a.UnsavedEvents))
	if err := s.EventStore.SaveEvents(a.UnsavedEvents); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	a.Events = append(a.Events, a.UnsavedEvents...)
	a.UnsavedEvents = make([]*continuum.Event, 0)

	return nil
}
