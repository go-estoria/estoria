package estoria

import (
	"encoding/json"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

type AggregateSnapshot struct {
	AggregateID      typeid.TypeID
	AggregateVersion int64
	Data             []byte
}

type AggregateSnapshotMarshaler interface {
	MarshalSnapshot(snapshot *AggregateSnapshot) ([]byte, error)
	UnmarshalSnapshot(data []byte, dest *AggregateSnapshot) error
}

type JSONAggregateSnapshotMarshaler struct{}

func (m JSONAggregateSnapshotMarshaler) MarshalSnapshot(snapshot *AggregateSnapshot) ([]byte, error) {
	return json.Marshal(snapshot)
}

func (m JSONAggregateSnapshotMarshaler) UnmarshalSnapshot(data []byte, dest *AggregateSnapshot) error {
	return json.Unmarshal(data, dest)
}

type EventCountSnapshotPolicy struct {
	N int64
}

func (p EventCountSnapshotPolicy) ShouldSnapshot(_ typeid.TypeID, aggregateVersion int64, _ time.Time) bool {
	return aggregateVersion%p.N == 0
}
