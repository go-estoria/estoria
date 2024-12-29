package snapshotstore

import (
	"errors"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

// An AggregateSnapshot is a snapshot of an aggregate at a specific version.
type AggregateSnapshot struct {
	AggregateID      typeid.UUID
	AggregateVersion int64
	Timestamp        time.Time
	Data             []byte
}

// An EventCountSnapshotPolicy takes a snapshot every N events.
// If N is 0, no snapshots are taken.
type EventCountSnapshotPolicy struct {
	N int64
}

func (p EventCountSnapshotPolicy) ShouldSnapshot(_ typeid.UUID, aggregateVersion int64, _ time.Time) bool {
	return p.N > 0 && aggregateVersion%p.N == 0
}

type ReadSnapshotOptions struct {
	MaxVersion int64
}

// A MaxSnapshotsRetentionPolicy retains the last N snapshots.
type MaxSnapshotsRetentionPolicy struct {
	N int64
}

func (p MaxSnapshotsRetentionPolicy) ShouldRetain(_ *AggregateSnapshot, snapshotIndex, totalSnapshots int64) bool {
	return p.N == 0 || snapshotIndex >= totalSnapshots-p.N
}

type MinAggregateVersionRetentionPolicy struct {
	MinVersion int64
}

func (p MinAggregateVersionRetentionPolicy) ShouldRetain(snap *AggregateSnapshot, _, _ int64) bool {
	return snap.AggregateVersion >= p.MinVersion
}

var ErrSnapshotNotFound = errors.New("snapshot not found")
