package aggregatestore

import "github.com/jefflinse/continuum"

type MemoryAggregateStore struct {
	Aggregates continuum.AggregateMap
}

func (s *MemoryAggregateStore) Load(aggregateType, id string) (continuum.Aggregate, error) {
	aggregates, ok := s.Aggregates[aggregateType]
	if !ok {
		return continuum.Aggregate{}, continuum.AggregateNotFoundError{
			Type: aggregateType,
			ID:   id,
		}
	}

	aggregate, ok := aggregates[id]
	if !ok {
		return continuum.Aggregate{}, continuum.AggregateNotFoundError{
			Type: aggregateType,
			ID:   id,
		}
	}

	return aggregate, nil
}

func (s *MemoryAggregateStore) Save(a continuum.Aggregate) error {
	if _, ok := s.Aggregates[a.Type]; !ok {
		s.Aggregates[a.Type] = make(map[string]continuum.Aggregate)
	}

	s.Aggregates[a.Type][a.ID] = a

	return nil
}
