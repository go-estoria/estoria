package estoria

import (
	"time"

	"go.jetpack.io/typeid"
)

type Snapshot interface {
	AggregateID() typeid.AnyID
	AggregateVersion() int64
	Data() []byte
}

type snapshot struct {
	aggregateID      typeid.AnyID
	aggregateVersion int64
	data             []byte
}

func (s *snapshot) AggregateID() typeid.AnyID {
	return s.aggregateID
}

func (s *snapshot) AggregateVersion() int64 {
	return s.aggregateVersion
}

func (s *snapshot) Data() []byte {
	return s.data
}

type EventCountSnapshotPolicy struct {
	N int64
}

func (p EventCountSnapshotPolicy) ShouldSnapshot(_ typeid.AnyID, aggregateVersion int64, _ time.Time) bool {
	return aggregateVersion%p.N == 0
}
