package estoria

import (
	"time"

	"github.com/go-estoria/estoria/typeid"
)

type AggregateSnapshot interface {
	AggregateID() typeid.TypeID
	AggregateVersion() int64
	Data() []byte
}

type EventCountSnapshotPolicy struct {
	N int64
}

func (p EventCountSnapshotPolicy) ShouldSnapshot(_ typeid.TypeID, aggregateVersion int64, _ time.Time) bool {
	return aggregateVersion%p.N == 0
}
