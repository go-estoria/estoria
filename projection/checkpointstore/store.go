// Package checkpointstore provides persistence for projection progress: the
// global position a projection has processed through, keyed by versioned
// projection ID.
//
// A checkpoint is not a snapshot. A snapshot (snapshotstore) captures aggregate
// state so hydration can skip already-applied events; a checkpoint records only
// a location — the global position a projection has processed through — and
// carries no state. The distinction matters especially to readers arriving from
// stream-processing systems, where "checkpoint" conventionally names a state
// snapshot.
package checkpointstore

import (
	"context"
	"errors"
	"time"

	"github.com/go-estoria/estoria/projection"
)

// A Checkpoint records a projection's progress through the store's global
// event sequence.
type Checkpoint struct {
	// ProjectionID is the projection the checkpoint belongs to.
	ProjectionID projection.ID

	// Position is the global position of the last successfully handled event.
	// Resuming means reading with eventstore.ReadAllOptions{AfterPosition: Position}.
	Position int64

	// UpdatedAt is when the checkpoint was last saved, assigned by the store.
	// A save at an unchanged position refreshes it, so its recency doubles as
	// a liveness signal for the processor that owns the checkpoint.
	UpdatedAt time.Time
}

// ErrCheckpointNotFound indicates that a projection has no saved checkpoint.
var ErrCheckpointNotFound = errors.New("checkpoint not found")

// A Store persists projection progress. Saves are last-write-wins: a save at
// a position at or below the current one succeeds and overwrites. Monotonicity
// is deliberately not enforced — the only expected writer for an ID is that
// projection's own processor, and a stale write only widens the at-least-once
// redelivery window that projection handlers must tolerate anyway.
type Store interface {
	// Load returns the projection's checkpoint, or ErrCheckpointNotFound if
	// none has been saved.
	Load(ctx context.Context, id projection.ID) (Checkpoint, error)

	// Save records position as the projection's checkpoint, assigning
	// UpdatedAt even when the position is unchanged.
	Save(ctx context.Context, id projection.ID, position int64) error

	// Delete removes the projection's checkpoint, or reports
	// ErrCheckpointNotFound if none exists.
	Delete(ctx context.Context, id projection.ID) error
}
