package aggregatestore

import "github.com/jefflinse/continuum"

type MemoryAggregateStore struct {
	Aggregates map[string]map[string]continuum.Aggregate
}

func (s *MemoryAggregateStore) Load(aggregateeType, id string) (continuum.Aggregate, error) {
	return s.Aggregates[aggregateeType][id], nil
}

func (s *MemoryAggregateStore) Save(a continuum.Aggregate) error {
	if _, ok := s.Aggregates[a.Type]; !ok {
		s.Aggregates[a.Type] = make(map[string]continuum.Aggregate)
	}

	s.Aggregates[a.Type][a.ID] = a

	return nil
}
