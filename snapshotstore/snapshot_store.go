package snapshotstore

import (
	"context"
	"errors"
	"time"

	"github.com/go-estoria/estoria/typeid"
)

// An AggregateSnapshot is a snapshot of an aggregate at a specific version.
type AggregateSnapshot struct {
	AggregateID      typeid.ID
	AggregateVersion int64
	Timestamp        time.Time
	Data             []byte

	// DataContentType is the MIME content type of Data, declared by the codec
	// that produced the bytes. Stores round-trip it verbatim, including an
	// empty one: the default lives with the codec layer, never with storage.
	// An empty value on a read snapshot means the snapshot was written before
	// its writer declared content types.
	DataContentType string
}

// A SnapshotReader reads snapshots.
type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, aggregateID typeid.ID, opts ReadSnapshotOptions) (*AggregateSnapshot, error)
}

// A SnapshotWriter writes snapshots.
type SnapshotWriter interface {
	WriteSnapshot(ctx context.Context, snap *AggregateSnapshot) error
}

// A SnapshotStore reads and writes snapshots.
type SnapshotStore interface {
	SnapshotReader
	SnapshotWriter
}

// An EventCountSnapshotPolicy takes a snapshot every N events.
// If N is 0, no snapshots are taken.
type EventCountSnapshotPolicy struct {
	N int64
}

func (p EventCountSnapshotPolicy) ShouldSnapshot(_ typeid.ID, aggregateVersion int64, _ time.Time) bool {
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
