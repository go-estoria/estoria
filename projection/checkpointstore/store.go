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
	// ErrCheckpointNotFound if none exists. Deletes of the same ID may run
	// concurrently — independent retirement repairs overlap, and nothing
	// serializes them across processes — and every overlapping caller must
	// receive success or ErrCheckpointNotFound.
	Delete(ctx context.Context, id projection.ID) error
}

// A TransactionalStore is a Store that can open a transaction in which a
// checkpoint save and a handler's own writes commit together. A processor
// discovers the capability by type assertion; a store without it keeps the
// two-write behavior. The transaction is on the store's own database by
// construction, and the store's plain Save, Load, and Delete never consult a
// context's transaction.
type TransactionalStore interface {
	Store

	// Begin opens a transaction.
	Begin(ctx context.Context) (Transaction, error)
}

// A Transaction is one open transaction on a TransactionalStore's database.
// Context returns a context derived from the one passed to Begin that carries
// the transaction in a form only the store's own package can read back. Save
// records the checkpoint inside the transaction with the same last-write-wins
// contract as Store.Save. Commit ends the transaction; an error from it does
// not say whether the server committed. Rollback is cleanup-safe: it returns
// nil once the transaction has ended for any reason, so a deferred Rollback is
// always correct.
type Transaction interface {
	// Context returns the context carrying the transaction.
	Context() context.Context

	// Save records position as the projection's checkpoint within the
	// transaction, assigning UpdatedAt even when the position is unchanged.
	Save(ctx context.Context, id projection.ID, position int64) error

	// Commit makes the transaction's changes durable and ends it.
	Commit(ctx context.Context) error

	// Rollback discards the transaction's changes and ends it.
	Rollback(ctx context.Context) error
}
