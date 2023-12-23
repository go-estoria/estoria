package aggregatestore

import (
	"fmt"

	"github.com/jefflinse/continuum"
)

type MemoryAggregateStore[AD continuum.AggregateData] struct {
	Aggregates continuum.AggregatesByID[AD]
}

func NewAggregateStore[AD continuum.AggregateData](eventStore *continuum.EventStore) *MemoryAggregateStore[AD] {
	return &MemoryAggregateStore[AD]{
		Aggregates: make(continuum.AggregatesByID[AD]),
	}
}

func (s *MemoryAggregateStore[AD]) Create(aggregateID string) (*continuum.Aggregate[AD], error) {
	aggregate := &continuum.Aggregate[AD]{
		ID:            aggregateID,
		Data:          *new(AD),
		Events:        make([]*continuum.Event, 0),
		UnsavedEvents: make([]*continuum.Event, 0),
		Version:       1,
	}

	if err := s.Save(aggregate); err != nil {
		return nil, fmt.Errorf("saving aggregate: %w", err)
	}

	return aggregate, nil
}

func (s *MemoryAggregateStore[AD]) Load(aggregateID string) (*continuum.Aggregate[AD], error) {
	aggregate, ok := s.Aggregates[aggregateID]
	if !ok {
		return nil, continuum.AggregateNotFoundError[AD]{
			ID: aggregateID,
		}
	}

	return aggregate, nil
}

func (s *MemoryAggregateStore[AD]) Save(a *continuum.Aggregate[AD]) error {
	s.Aggregates[a.ID] = a
	return nil
}
