// Package storetest provides acceptance suites that checkpointstore
// implementations are expected to pass: one for every checkpointstore.Store,
// and one more for stores that also implement checkpointstore.TransactionalStore.
//
// A backend wires them up with a test each:
//
//	func TestCheckpointStore_AcceptanceTest(t *testing.T) {
//		storetest.RunCheckpointStoreSuite(t, func(t *testing.T) checkpointstore.Store {
//			return newStoreForTest(t)
//		})
//	}
//
//	func TestCheckpointStore_TransactionalAcceptanceTest(t *testing.T) {
//		storetest.RunTransactionalStoreSuite(t, func(t *testing.T) checkpointstore.TransactionalStore {
//			return newStoreForTest(t)
//		})
//	}
package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	"github.com/gofrs/uuid/v5"
)

// NewStoreFunc returns a checkpoint store for one clause of the acceptance suite to use.
//
// Implementations may return the same store on every call. Every clause uses freshly
// generated projection IDs and asserts only on those IDs, so sharing one store across
// clauses is safe and avoids standing up a backend per clause.
type NewStoreFunc func(t *testing.T) checkpointstore.Store

// RunCheckpointStoreSuite runs the checkpoint store acceptance suite against stores
// returned by newStore, reporting each clause as its own named subtest.
//
// The suite does not call t.Parallel; parallelism is the caller's to choose.
func RunCheckpointStoreSuite(t *testing.T, newStore NewStoreFunc) {
	t.Helper()

	for _, clause := range []struct {
		name string
		run  func(t *testing.T, store checkpointstore.Store)
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

// RunTransactionalStoreSuite runs the transactional checkpoint store acceptance suite
// against stores returned by newStore, reporting each clause as its own named subtest.
// It complements RunCheckpointStoreSuite and does not repeat it.
//
// The suite does not call t.Parallel; parallelism is the caller's to choose. Every
// clause ends its transaction, so a backend that serializes transactions is safe to
// share across clauses. Rollback after a failed Commit is not covered, because no
// backend-independent way to make Commit fail exists; backends test it themselves.
func RunTransactionalStoreSuite(t *testing.T, newStore func(t *testing.T) checkpointstore.TransactionalStore) {
	t.Helper()

	for _, clause := range []struct {
		name string
		run  func(t *testing.T, store checkpointstore.TransactionalStore)
	}{
		{"leaves no checkpoint for a save in a rolled-back transaction", clauseRollbackDiscardsSave},
		{"makes a save in a committed transaction visible through Load", clauseCommitPublishesSave},
		{"hides an uncommitted save from Load outside the transaction", clauseUncommittedSaveIsInvisible},
		{"reports an error on a second Commit", clauseSecondCommitFails},
		{"returns nil from Rollback after Commit and keeps the committed checkpoint", clauseRollbackAfterCommit},
		{"returns nil from Rollback after Rollback", clauseRollbackAfterRollback},
	} {
		t.Run(clause.name, func(t *testing.T) {
			clause.run(t, newStore(t))
		})
	}
}

// clauseNeverCheckpointed pins the sentinel a processor checks on cold start: a
// projection with no history reports ErrCheckpointNotFound, which means "replay from
// the beginning", not "fail".
func clauseNeverCheckpointed(t *testing.T, store checkpointstore.Store) {
	if _, err := store.Load(t.Context(), newProjectionID()); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want ErrCheckpointNotFound loading a checkpoint that was never saved, got %v", err)
	}
}

// clauseRoundTripsCheckpoint pins the base guarantee: the ID and position read back
// exactly as saved, and the store assigned UpdatedAt.
func clauseRoundTripsCheckpoint(t *testing.T, store checkpointstore.Store) {
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
func clauseSeparatesIDs(t *testing.T, store checkpointstore.Store) {
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
func clauseLastSaveWins(t *testing.T, store checkpointstore.Store) {
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
func clauseRefreshesUpdatedAt(t *testing.T, store checkpointstore.Store) {
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
func clauseDeletesCheckpoint(t *testing.T, store checkpointstore.Store) {
	id := newProjectionID()
	saveCheckpoint(t, store, id, 12)

	if err := store.Delete(t.Context(), id); err != nil {
		t.Fatalf("deleting checkpoint: %v", err)
	}

	if _, err := store.Load(t.Context(), id); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want ErrCheckpointNotFound loading a deleted checkpoint, got %v", err)
	}
}

// clauseDeleteAbsent pins that deleting an absent checkpoint is reported, mirroring
// how stream deletion reports ErrStreamNotFound. A caller cleaning up a retired
// projection can distinguish "cleaned" from "was already gone", or errors.Is past
// the difference when it does not care.
func clauseDeleteAbsent(t *testing.T, store checkpointstore.Store) {
	if err := store.Delete(t.Context(), newProjectionID()); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want ErrCheckpointNotFound deleting a checkpoint that does not exist, got %v", err)
	}
}

// clauseRollbackDiscardsSave pins that a rolled-back window persists nothing: neither a
// first checkpoint nor an overwrite of an existing one.
func clauseRollbackDiscardsSave(t *testing.T, store checkpointstore.TransactionalStore) {
	fresh := newProjectionID()
	existing := newProjectionID()
	saveCheckpoint(t, store, existing, 3)

	tx := beginTransaction(t, store)
	transactionSave(t, tx, fresh)
	transactionSave(t, tx, existing)

	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back: %v", err)
	}

	if _, err := store.Load(t.Context(), fresh); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want ErrCheckpointNotFound loading a checkpoint saved only in a rolled-back transaction, got %v", err)
	}

	if got := loadCheckpoint(t, store, existing).Position; got != 3 {
		t.Errorf("want the existing checkpoint's position 3 after rollback, got %d", got)
	}
}

// clauseCommitPublishesSave pins that a committed window's checkpoint is the store's
// checkpoint: readable through the plain Load with the store-assigned UpdatedAt.
func clauseCommitPublishesSave(t *testing.T, store checkpointstore.TransactionalStore) {
	id := newProjectionID()

	tx := beginTransaction(t, store)
	transactionSave(t, tx, id)

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("committing: %v", err)
	}

	checkpoint := loadCheckpoint(t, store, id)
	if checkpoint.Position != 8 {
		t.Errorf("want position 8, got %d", checkpoint.Position)
	}

	if checkpoint.UpdatedAt.IsZero() {
		t.Error("want an assigned UpdatedAt, got the zero time")
	}
}

// clauseUncommittedSaveIsInvisible pins isolation: the plain Load, which runs outside
// the transaction, sees the committed state until Commit and the new state after.
func clauseUncommittedSaveIsInvisible(t *testing.T, store checkpointstore.TransactionalStore) {
	fresh := newProjectionID()
	existing := newProjectionID()
	saveCheckpoint(t, store, existing, 3)

	tx := beginTransaction(t, store)
	transactionSave(t, tx, fresh)
	transactionSave(t, tx, existing)

	if _, err := store.Load(t.Context(), fresh); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want ErrCheckpointNotFound loading an uncommitted first checkpoint, got %v", err)
	}

	if got := loadCheckpoint(t, store, existing).Position; got != 3 {
		t.Errorf("want the committed position 3 before Commit, got %d", got)
	}

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("committing: %v", err)
	}

	if got := loadCheckpoint(t, store, fresh).Position; got != 8 {
		t.Errorf("want position 8 after Commit, got %d", got)
	}

	if got := loadCheckpoint(t, store, existing).Position; got != 8 {
		t.Errorf("want position 8 after Commit, got %d", got)
	}
}

// clauseSecondCommitFails pins that Commit ends the transaction: committing again is an
// error rather than a silent success, so a caller cannot mistake a no-op for durability.
func clauseSecondCommitFails(t *testing.T, store checkpointstore.TransactionalStore) {
	tx := beginTransaction(t, store)
	transactionSave(t, tx, newProjectionID())

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("committing: %v", err)
	}

	if err := tx.Commit(t.Context()); err == nil {
		t.Error("want an error from a second Commit, got nil")
	}
}

// clauseRollbackAfterCommit pins the cleanup-safe contract that lets a processor defer
// Rollback unconditionally: it succeeds once the transaction has committed, and it
// does not undo the commit.
func clauseRollbackAfterCommit(t *testing.T, store checkpointstore.TransactionalStore) {
	id := newProjectionID()

	tx := beginTransaction(t, store)
	transactionSave(t, tx, id)

	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("committing: %v", err)
	}

	if err := tx.Rollback(t.Context()); err != nil {
		t.Errorf("want nil from Rollback after Commit, got %v", err)
	}

	if got := loadCheckpoint(t, store, id).Position; got != 8 {
		t.Errorf("want the committed position 8 to survive Rollback, got %d", got)
	}
}

// clauseRollbackAfterRollback pins that Rollback is idempotent.
func clauseRollbackAfterRollback(t *testing.T, store checkpointstore.TransactionalStore) {
	tx := beginTransaction(t, store)

	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back: %v", err)
	}

	if err := tx.Rollback(t.Context()); err != nil {
		t.Errorf("want nil from Rollback after Rollback, got %v", err)
	}
}

// newProjectionID returns a projection ID unique to one clause, so clauses sharing a
// store cannot observe each other's writes. The generated name is valid per
// projection.ID.Validate, modeling correct usage even though stores do not validate.
func newProjectionID() projection.ID {
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]

	return projection.ID{Name: "storetest_" + suffix, Version: 1}
}

func saveCheckpoint(t *testing.T, store checkpointstore.Store, id projection.ID, position int64) {
	t.Helper()

	if err := store.Save(t.Context(), id, position); err != nil {
		t.Fatalf("saving checkpoint for %s at position %d: %v", id, position, err)
	}
}

func loadCheckpoint(t *testing.T, store checkpointstore.Store, id projection.ID) checkpointstore.Checkpoint {
	t.Helper()

	checkpoint, err := store.Load(t.Context(), id)
	if err != nil {
		t.Fatalf("loading checkpoint for %s: %v", id, err)
	}

	return checkpoint
}

// beginTransaction opens a transaction and arranges for it to be rolled back when the
// clause ends, so a clause that fails partway cannot strand a transaction that blocks
// the clauses after it.
func beginTransaction(t *testing.T, store checkpointstore.TransactionalStore) checkpointstore.Transaction {
	t.Helper()

	tx, err := store.Begin(t.Context())
	if err != nil {
		t.Fatalf("beginning transaction: %v", err)
	}

	t.Cleanup(func() {
		if err := tx.Rollback(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("rolling back at cleanup: %v", err)
		}
	})

	return tx
}

// transactionSave saves position 8, the position every transactional clause asserts on,
// which differs from the committed position 3 that clauses seed.
func transactionSave(t *testing.T, tx checkpointstore.Transaction, id projection.ID) {
	t.Helper()

	if err := tx.Save(t.Context(), id, 8); err != nil {
		t.Fatalf("saving checkpoint for %s at position 8 in transaction: %v", id, err)
	}
}
