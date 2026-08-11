// Package projection provides the building blocks for maintaining read models
// from an event store: a versioned projection identity, checkpoint persistence
// for resumable progress, and projections that fold events into read-side state.
//
// A checkpoint is not a snapshot. A snapshot (snapshotstore) captures aggregate
// state so hydration can skip already-applied events; a checkpoint records only
// a location — the global position a projection has processed through — and
// carries no state. The distinction matters especially to readers arriving from
// stream-processing systems, where "checkpoint" conventionally names a state
// snapshot.
package projection

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/go-estoria/estoria/eventstore"
)

// An ID identifies one version of a named projection. Checkpoints, cutover,
// and rebuild history are keyed by it: two versions of the same name are
// distinct projections that can process the same events concurrently, each
// under its own checkpoint.
type ID struct {
	// Name identifies the projection independent of version, e.g. "orders".
	Name string

	// Version distinguishes successive rebuilds of the projection's read
	// model. Versions are 1-based.
	Version int
}

// String returns the canonical form, e.g. "orders_v7". It is the single form
// under which a projection is named everywhere: checkpoint keys, log output,
// and the storage suffix a handler uses to derive versioned table, index, or
// collection names.
func (id ID) String() string {
	return id.Name + "_v" + strconv.Itoa(id.Version)
}

// MaxNameLength is the longest allowed projection name. The bound leaves room
// within common identifier limits (PostgreSQL truncates names at 63 bytes) for
// the version suffix and a handler's own prefixing.
const MaxNameLength = 40

// Validate reports whether the ID can serve as a projection identity. The name
// must start with a lowercase letter, contain only lowercase letters, digits,
// and underscores, not end with an underscore, and be at most MaxNameLength
// characters, so that String is always safe as an unquoted storage identifier.
// The version must be positive.
func (id ID) Validate() error {
	switch {
	case id.Name == "":
		return errors.New("projection name is required")
	case len(id.Name) > MaxNameLength:
		return fmt.Errorf("projection name must be at most %d characters", MaxNameLength)
	case id.Name[0] < 'a' || id.Name[0] > 'z':
		return errors.New("projection name must start with a lowercase letter")
	case id.Name[len(id.Name)-1] == '_':
		return errors.New("projection name must not end with '_'")
	case id.Version < 1:
		return errors.New("projection version must be positive")
	}

	for _, c := range id.Name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return fmt.Errorf("projection name must not contain %q", c)
		}
	}

	return nil
}

// An EventHandler handles an individual event.
type EventHandler interface {
	Handle(ctx context.Context, event *eventstore.Event) error
}

// An EventHandlerFunc is a function that handles an event during projection.
type EventHandlerFunc func(ctx context.Context, event *eventstore.Event) error

// Handle implements the EventHandler interface, allowing an EventHandlerFunc to be used as an EventHandler.
func (f EventHandlerFunc) Handle(ctx context.Context, event *eventstore.Event) error {
	return f(ctx, event)
}

// A Checkpoint records a projection's progress through the store's global
// event sequence.
type Checkpoint struct {
	// ProjectionID is the projection the checkpoint belongs to.
	ProjectionID ID

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

// A CheckpointStore persists projection progress. Saves are last-write-wins:
// a save at a position at or below the current one succeeds and overwrites.
// Monotonicity is deliberately not enforced — the only expected writer for an
// ID is that projection's own processor, and a stale write only widens the
// at-least-once redelivery window that projection handlers must tolerate
// anyway.
type CheckpointStore interface {
	// Load returns the projection's checkpoint, or ErrCheckpointNotFound if
	// none has been saved.
	Load(ctx context.Context, id ID) (Checkpoint, error)

	// Save records position as the projection's checkpoint, assigning
	// UpdatedAt even when the position is unchanged.
	Save(ctx context.Context, id ID, position int64) error

	// Delete removes the projection's checkpoint, or reports
	// ErrCheckpointNotFound if none exists.
	Delete(ctx context.Context, id ID) error
}
