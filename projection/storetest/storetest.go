// Package storetest provides an acceptance suite that every
// projection.CheckpointStore implementation is expected to pass.
//
// A backend wires it up with a single test:
//
//	func TestCheckpointStore_AcceptanceTest(t *testing.T) {
//		storetest.RunCheckpointStoreSuite(t, func(t *testing.T) projection.CheckpointStore {
//			return newStoreForTest(t)
//		})
//	}
package storetest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/gofrs/uuid/v5"
)

// NewStoreFunc returns a checkpoint store for one clause of the acceptance suite to use.
//
// Implementations may return the same store on every call. Every clause uses freshly
// generated projection IDs and asserts only on those IDs, so sharing one store across
// clauses is safe and avoids standing up a backend per clause.
type NewStoreFunc func(t *testing.T) projection.CheckpointStore

// RunCheckpointStoreSuite runs the checkpoint store acceptance suite against stores
// returned by newStore, reporting each clause as its own named subtest.
//
// The suite does not call t.Parallel; parallelism is the caller's to choose.
func RunCheckpointStoreSuite(t *testing.T, newStore NewStoreFunc) {
	t.Helper()

	for _, clause := range []struct {
		name string
		run  func(t *testing.T, store projection.CheckpointStore)
	}{
		{"reports ErrCheckpointNotFound for a projection that was never checkpointed", clauseNeverCheckpointed},
		{"round-trips a checkpoint and assigns UpdatedAt", clauseRoundTripsCheckpoint},
		{"keeps checkpoints for distinct projection IDs separate", clauseSeparatesIDs},
		{"overwrites on save, including to a lower position", clauseLastSaveWins},
		{"refreshes UpdatedAt on a save at an unchanged position", clauseRefreshesUpdatedAt},
		{"deletes a checkpoint", clauseDeletesCheckpoint},
		{"reports ErrCheckpointNotFound deleting a checkpoint that does not exist", clauseDeleteAbsent},
	} {
		t.Run(clause.name, func(t *testing.T) {
			clause.run(t, newStore(t))
		})
	}
}

// clauseNeverCheckpointed pins the sentinel a processor checks on cold start: a
// projection with no history reports ErrCheckpointNotFound, which means "replay from
// the beginning", not "fail".
func clauseNeverCheckpointed(t *testing.T, store projection.CheckpointStore) {
	if _, err := store.Load(t.Context(), newProjectionID()); !errors.Is(err, projection.ErrCheckpointNotFound) {
		t.Errorf("want ErrCheckpointNotFound loading a checkpoint that was never saved, got %v", err)
	}
}

// clauseRoundTripsCheckpoint pins the base guarantee: the ID and position read back
// exactly as saved, and the store assigned UpdatedAt.
func clauseRoundTripsCheckpoint(t *testing.T, store projection.CheckpointStore) {
	id := newProjectionID()
	saveCheckpoint(t, store, id, 42)

	checkpoint := loadCheckpoint(t, store, id)

	if checkpoint.ProjectionID != id {
		t.Errorf("want projection ID %s, got %s", id, checkpoint.ProjectionID)
	}

	if checkpoint.Position != 42 {
		t.Errorf("want position 42, got %d", checkpoint.Position)
	}

	if checkpoint.UpdatedAt.IsZero() {
		t.Error("want an assigned UpdatedAt, got the zero time")
	}
}

// clauseSeparatesIDs pins that checkpoints are keyed by the full ID. Independence
// between versions of one name is what lets an old and a new version of a projection
// tail the same stream concurrently during a rebuild.
func clauseSeparatesIDs(t *testing.T, store projection.CheckpointStore) {
	v1 := newProjectionID()
	v2 := projection.ID{Name: v1.Name, Version: v1.Version + 1}
	other := newProjectionID()

	saveCheckpoint(t, store, v1, 10)
	saveCheckpoint(t, store, v2, 3)
	saveCheckpoint(t, store, other, 99)

	if got := loadCheckpoint(t, store, v1).Position; got != 10 {
		t.Errorf("want position 10 for %s, got %d", v1, got)
	}

	if got := loadCheckpoint(t, store, v2).Position; got != 3 {
		t.Errorf("want position 3 for %s, got %d", v2, got)
	}

	if got := loadCheckpoint(t, store, other).Position; got != 99 {
		t.Errorf("want position 99 for %s, got %d", other, got)
	}
}

// clauseLastSaveWins pins that monotonicity is deliberately not enforced: a save
// below the current position succeeds and overwrites. Restarting a torn-down rebuild
// of the same version legitimately rewinds, and a stale writer only widens the
// at-least-once redelivery window that projection handlers tolerate anyway. A store
// that "helpfully" rejects lower positions breaks the restart path.
func clauseLastSaveWins(t *testing.T, store projection.CheckpointStore) {
	id := newProjectionID()

	saveCheckpoint(t, store, id, 10)
	saveCheckpoint(t, store, id, 5)

	if got := loadCheckpoint(t, store, id).Position; got != 5 {
		t.Errorf("want the later save's position 5, got %d", got)
	}
}

// clauseRefreshesUpdatedAt pins the liveness mechanic: a processor idling at the
// stream head re-saves its unchanged position so UpdatedAt doubles as a heartbeat.
// A store that skips the write when nothing changed makes a healthy idle processor
// indistinguishable from a dead one.
func clauseRefreshesUpdatedAt(t *testing.T, store projection.CheckpointStore) {
	id := newProjectionID()

	saveCheckpoint(t, store, id, 7)
	first := loadCheckpoint(t, store, id).UpdatedAt

	// Backends that persist timestamps at millisecond precision need daylight
	// between the saves for strictly-after to be observable.
	time.Sleep(20 * time.Millisecond)

	saveCheckpoint(t, store, id, 7)
	second := loadCheckpoint(t, store, id).UpdatedAt

	if !second.After(first) {
		t.Errorf("want UpdatedAt refreshed by a save at an unchanged position, got %s then %s", first, second)
	}
}

// clauseDeletesCheckpoint pins removal: after a delete, the projection reads as never
// checkpointed.
func clauseDeletesCheckpoint(t *testing.T, store projection.CheckpointStore) {
	id := newProjectionID()
	saveCheckpoint(t, store, id, 12)

	if err := store.Delete(t.Context(), id); err != nil {
		t.Fatalf("deleting checkpoint: %v", err)
	}

	if _, err := store.Load(t.Context(), id); !errors.Is(err, projection.ErrCheckpointNotFound) {
		t.Errorf("want ErrCheckpointNotFound loading a deleted checkpoint, got %v", err)
	}
}

// clauseDeleteAbsent pins that deleting an absent checkpoint is reported, mirroring
// how stream deletion reports ErrStreamNotFound. A caller cleaning up a retired
// projection can distinguish "cleaned" from "was already gone", or errors.Is past
// the difference when it does not care.
func clauseDeleteAbsent(t *testing.T, store projection.CheckpointStore) {
	if err := store.Delete(t.Context(), newProjectionID()); !errors.Is(err, projection.ErrCheckpointNotFound) {
		t.Errorf("want ErrCheckpointNotFound deleting a checkpoint that does not exist, got %v", err)
	}
}

// newProjectionID returns a projection ID unique to one clause, so clauses sharing a
// store cannot observe each other's writes. The generated name is valid per
// projection.ID.Validate, modeling correct usage even though stores do not validate.
func newProjectionID() projection.ID {
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]

	return projection.ID{Name: "storetest_" + suffix, Version: 1}
}

func saveCheckpoint(t *testing.T, store projection.CheckpointStore, id projection.ID, position int64) {
	t.Helper()

	if err := store.Save(t.Context(), id, position); err != nil {
		t.Fatalf("saving checkpoint for %s at position %d: %v", id, position, err)
	}
}

func loadCheckpoint(t *testing.T, store projection.CheckpointStore, id projection.ID) projection.Checkpoint {
	t.Helper()

	checkpoint, err := store.Load(t.Context(), id)
	if err != nil {
		t.Fatalf("loading checkpoint for %s: %v", id, err)
	}

	return checkpoint
}
