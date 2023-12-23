package aggregatestore

import (
	"fmt"

	"github.com/jefflinse/continuum"
)

type MemoryAggregateStore[AD continuum.AggregateData] struct {
	EventStore       *continuum.EventStore
	AggregateFactory func() AD
}

func New[AD continuum.AggregateData](eventStore *continuum.EventStore, factory func() AD) *MemoryAggregateStore[AD] {
	return &MemoryAggregateStore[AD]{
		EventStore:       eventStore,
		AggregateFactory: factory,
	}
}

func (s *MemoryAggregateStore[AD]) Create(aggregateID string) (*continuum.Aggregate[AD], error) {
	aggregate := &continuum.Aggregate[AD]{
		ID:            aggregateID,
		Data:          s.AggregateFactory(),
		Events:        make([]*continuum.Event, 0),
		UnsavedEvents: make([]*continuum.Event, 0),
		Version:       0,
	}

	if err := s.Save(aggregate); err != nil {
		return nil, fmt.Errorf("saving aggregate: %w", err)
	}

	return aggregate, nil
}

func (s *MemoryAggregateStore[AD]) Load(aggregateID string) (*continuum.Aggregate[AD], error) {
	aggregateData := s.AggregateFactory()
	aggregateType := aggregateData.AggregateTypeName()
	events, err := s.EventStore.LoadEvents(aggregateType, aggregateID)
	if err != nil {
		return nil, fmt.Errorf("loading events: %w", err)
	}

	if len(events) == 0 {
		return nil, continuum.AggregateNotFoundError[AD]{ID: aggregateID}
	}

	aggregate := &continuum.Aggregate[AD]{
		ID:            aggregateID,
		Data:          aggregateData,
		Events:        events,
		UnsavedEvents: make([]*continuum.Event, 0),
		Version:       events[len(events)-1].Version,
	}

	for _, event := range events {
		if err := aggregate.Apply(event); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}
	}

	return aggregate, nil
}

func (s *MemoryAggregateStore[AD]) Save(a *continuum.Aggregate[AD]) error {
	if err := s.EventStore.SaveEvents(a.UnsavedEvents); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	a.UnsavedEvents = make([]*continuum.Event, 0)

	return nil
}
