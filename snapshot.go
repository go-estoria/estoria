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

type EventCountSnapshotPolicy struct {
	N int64
}

func (p EventCountSnapshotPolicy) ShouldSnapshot(_ typeid.AnyID, aggregateVersion int64, _ time.Time) bool {
	return aggregateVersion%p.N == 0
}
