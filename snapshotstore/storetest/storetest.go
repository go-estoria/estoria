// Package storetest provides an acceptance suite that every snapshotstore.SnapshotStore
// implementation is expected to pass.
//
// A backend wires it up with a single test:
//
//	func TestSnapshotStore_AcceptanceTest(t *testing.T) {
//		storetest.RunSnapshotStoreSuite(t, func(t *testing.T) snapshotstore.SnapshotStore {
//			return newStoreForTest(t)
//		})
//	}
package storetest

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
)

// NewStoreFunc returns a snapshot store for one clause of the acceptance suite to use.
//
// Implementations may return the same store on every call. Every clause uses a freshly
// generated aggregate ID and asserts only on that aggregate.
type NewStoreFunc func(t *testing.T) snapshotstore.SnapshotStore

// RunSnapshotStoreSuite runs the snapshot store acceptance suite against stores returned by
// newStore, reporting each clause as its own named subtest.
//
// The suite deliberately says nothing about retention. How many snapshots a store keeps is
// a store's own policy, so the clauses below are written to hold for a store that retains
// only the newest snapshot and for one that retains every snapshot ever written.
//
// The suite does not call t.Parallel; parallelism is the caller's to choose.
func RunSnapshotStoreSuite(t *testing.T, newStore NewStoreFunc) {
	t.Helper()

	for _, clause := range []struct {
		name string
		run  func(t *testing.T, store snapshotstore.SnapshotStore)
	}{
		{"reports ErrSnapshotNotFound for an aggregate that was never snapshotted", clauseNeverSnapshotted},
		{"round-trips a snapshot", clauseRoundTripsSnapshot},
		{"does not modify the snapshot it is given", clauseDoesNotMutateSnapshot},
		{"returns the most recently written snapshot by default", clauseReturnsMostRecent},
		{"never returns a snapshot newer than MaxVersion", clauseHonorsMaxVersion},
		{"keeps snapshots for different aggregates separate", clauseSeparatesAggregates},
	} {
		t.Run(clause.name, func(t *testing.T) {
			clause.run(t, newStore(t))
		})
	}
}

// clauseNeverSnapshotted pins the sentinel that SnapshottingStore checks before falling
// back to full hydration. A store reporting anything else for a first-ever load turns an
// ordinary cold start into a load failure.
func clauseNeverSnapshotted(t *testing.T, store snapshotstore.SnapshotStore) {
	_, err := store.ReadSnapshot(t.Context(), newAggregateID(), snapshotstore.ReadSnapshotOptions{})
	if !errors.Is(err, snapshotstore.ErrSnapshotNotFound) {
		t.Errorf("want ErrSnapshotNotFound for an aggregate with no snapshots, got %v", err)
	}
}

// clauseRoundTripsSnapshot pins every field a store is responsible for preserving. The
// payload matters most: it is an opaque entity encoding, and a store that mangles it
// produces an aggregate hydrated to a corrupt state rather than an outright error.
func clauseRoundTripsSnapshot(t *testing.T, store snapshotstore.SnapshotStore) {
	aggregateID := newAggregateID()

	const wantVersion = int64(7)

	const wantContentType = "application/x-storetest"

	wantTimestamp := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	wantData := []byte(`{"owner":"alice","balance":42}`)

	// Hand the store its own copy and assert against the locals above, never against the
	// struct the store was given. A store that writes through the caller's snapshot would
	// otherwise mutate the expectation into agreeing with whatever it stored.
	if err := store.WriteSnapshot(t.Context(), &snapshotstore.AggregateSnapshot{
		AggregateID:      aggregateID,
		AggregateVersion: wantVersion,
		Timestamp:        wantTimestamp,
		Data:             slices.Clone(wantData),
		DataContentType:  wantContentType,
	}); err != nil {
		t.Fatalf("writing snapshot: %v", err)
	}

	got, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{})
	if err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}

	if got.AggregateID != aggregateID {
		t.Errorf("want aggregate ID %s, got %s", aggregateID, got.AggregateID)
	}

	if got.AggregateVersion != wantVersion {
		t.Errorf("want aggregate version %d, got %d", wantVersion, got.AggregateVersion)
	}

	if !got.Timestamp.Equal(wantTimestamp) {
		t.Errorf("want timestamp %s, got %s", wantTimestamp, got.Timestamp)
	}

	if !reflect.DeepEqual(got.Data, wantData) {
		t.Errorf("want data %s, got %s", wantData, got.Data)
	}

	// Verbatim, including empty: a store that rewrites the declaration — or fills
	// in a default for the undeclared write below — mislabels bytes it did not
	// produce, and the reader's codec acts on the lie.
	if got.DataContentType != wantContentType {
		t.Errorf("want data content type %q, got %q", wantContentType, got.DataContentType)
	}

	undeclared := newAggregateID()
	if err := store.WriteSnapshot(t.Context(), &snapshotstore.AggregateSnapshot{
		AggregateID:      undeclared,
		AggregateVersion: wantVersion,
		Data:             slices.Clone(wantData),
	}); err != nil {
		t.Fatalf("writing undeclared snapshot: %v", err)
	}

	got, err = store.ReadSnapshot(t.Context(), undeclared, snapshotstore.ReadSnapshotOptions{})
	if err != nil {
		t.Fatalf("reading undeclared snapshot: %v", err)
	}

	if got.DataContentType != "" {
		t.Errorf("want an empty data content type for an undeclared payload, got %q", got.DataContentType)
	}
}

// clauseDoesNotMutateSnapshot pins that WriteSnapshot treats its argument as read-only.
// Callers reuse and inspect the struct after writing, and a store that scribbles on it
// corrupts state the caller still owns — the failure mode is silent, because the caller's
// own later reads agree with the damage.
func clauseDoesNotMutateSnapshot(t *testing.T, store snapshotstore.SnapshotStore) {
	aggregateID := newAggregateID()

	const wantVersion = int64(4)

	const wantContentType = "application/x-storetest"

	wantTimestamp := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	wantData := []byte(`{"owner":"bob","balance":7}`)

	snap := &snapshotstore.AggregateSnapshot{
		AggregateID:      aggregateID,
		AggregateVersion: wantVersion,
		Timestamp:        wantTimestamp,
		Data:             slices.Clone(wantData),
		DataContentType:  wantContentType,
	}

	if err := store.WriteSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("writing snapshot: %v", err)
	}

	if snap.AggregateID != aggregateID {
		t.Errorf("store modified AggregateID: want %s, got %s", aggregateID, snap.AggregateID)
	}

	if snap.AggregateVersion != wantVersion {
		t.Errorf("store modified AggregateVersion: want %d, got %d", wantVersion, snap.AggregateVersion)
	}

	if !snap.Timestamp.Equal(wantTimestamp) {
		t.Errorf("store modified Timestamp: want %s, got %s", wantTimestamp, snap.Timestamp)
	}

	if !reflect.DeepEqual(snap.Data, wantData) {
		t.Errorf("store modified Data: want %s, got %s", wantData, snap.Data)
	}

	if snap.DataContentType != wantContentType {
		t.Errorf("store modified DataContentType: want %q, got %q", wantContentType, snap.DataContentType)
	}
}

// clauseReturnsMostRecent pins that an unbounded read finds the newest snapshot. Returning
// an older one is not a correctness failure on its own — hydration replays the remaining
// events either way — but it silently forfeits the point of snapshotting.
func clauseReturnsMostRecent(t *testing.T, store snapshotstore.SnapshotStore) {
	aggregateID := newAggregateID()
	writeVersions(t, store, aggregateID, 1, 2, 3)

	snap, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{})
	if err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}

	if want := int64(3); snap.AggregateVersion != want {
		t.Errorf("want aggregate version %d, got %d", want, snap.AggregateVersion)
	}
}

// clauseHonorsMaxVersion pins the bound that makes time-travel loads correct: hydrating an
// aggregate to version N must never start from a snapshot taken after N, or the replay
// applies events already baked into the snapshot.
//
// Reporting ErrSnapshotNotFound is a valid answer here. A store that retains only its
// newest snapshot has nothing at or below the bound to return, and the caller falls back to
// full hydration. What the store may not do is answer with a snapshot past the bound.
func clauseHonorsMaxVersion(t *testing.T, store snapshotstore.SnapshotStore) {
	aggregateID := newAggregateID()
	writeVersions(t, store, aggregateID, 1, 2, 3)

	const maxVersion = 2

	snap, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{
		MaxVersion: maxVersion,
	})
	if errors.Is(err, snapshotstore.ErrSnapshotNotFound) {
		return
	} else if err != nil {
		t.Fatalf("reading snapshot with MaxVersion %d: %v", maxVersion, err)
	}

	if snap.AggregateVersion > maxVersion {
		t.Errorf("want a snapshot at or below version %d, got version %d", maxVersion, snap.AggregateVersion)
	}
}

// clauseSeparatesAggregates pins that snapshots are keyed by the whole aggregate ID. Two
// aggregates of different types can share a UUID, so a store keying on the UUID alone
// hands one aggregate the other's state.
func clauseSeparatesAggregates(t *testing.T, store snapshotstore.SnapshotStore) {
	snapshotted := newAggregateID()
	writeVersions(t, store, snapshotted, 1)

	other := typeid.New(snapshotted.Type+"other", snapshotted.UUID)

	if _, err := store.ReadSnapshot(t.Context(), other, snapshotstore.ReadSnapshotOptions{}); !errors.Is(err, snapshotstore.ErrSnapshotNotFound) {
		t.Errorf("want ErrSnapshotNotFound for an aggregate sharing another's UUID, got %v", err)
	}
}

// newAggregateID returns an aggregate ID unique to one clause, so clauses sharing a store
// cannot observe each other's writes.
func newAggregateID() typeid.ID {
	return typeid.NewV4("storetestentity")
}

func writeVersions(t *testing.T, store snapshotstore.SnapshotStore, aggregateID typeid.ID, versions ...int64) {
	t.Helper()

	for _, version := range versions {
		err := store.WriteSnapshot(t.Context(), &snapshotstore.AggregateSnapshot{
			AggregateID:      aggregateID,
			AggregateVersion: version,
			Data:             []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("writing snapshot at version %d: %v", version, err)
		}
	}
}
