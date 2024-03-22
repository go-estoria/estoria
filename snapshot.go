package estoria

import (
	"context"
	"time"

	"go.jetpack.io/typeid"
)

type Snapshot interface {
	AggregateID() typeid.AnyID
	AggregateVersion() int64
	Data() []byte
}

type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, aggregateID typeid.AnyID) (Snapshot, error)
}

type SnapshotWriter interface {
	WriteSnapshot(ctx context.Context, snapshot Snapshot) error
}

type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.AnyID, aggregateVersion int64, timestamp time.Time) bool
}

type EventCountSnapshotPolicy struct {
	N int64
}

func (p EventCountSnapshotPolicy) ShouldSnapshot(_ typeid.AnyID, aggregateVersion int64, _ time.Time) bool {
	return aggregateVersion%p.N == 0
}
