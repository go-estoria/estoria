// Package snapshotstore_test exercises the event-stream-backed snapshot store.
//
// Note the package name does not match its directory (`eventstream`), so importers must
// alias it. Renaming is deferred to the Phase 3 distillation pass.
package snapshotstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/snapshotstore"
	eventstream "github.com/go-estoria/estoria/snapshotstore/eventstream"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

var _ snapshotstore.SnapshotStore = (*eventstream.EventStreamStore)(nil)

// closeCountingStore counts the iterators handed out and the ones closed, so a test can
// assert the snapshot store does not leak cursors. Against a real backend a leaked iterator
// is an open rows handle or server-side cursor.
type closeCountingStore struct {
	eventstore.Store
	opened int
	closed int
}

func (s *closeCountingStore) ReadStream(ctx context.Context, id typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	iter, err := s.Store.ReadStream(ctx, id, opts)
	if err != nil {
		return nil, err //nolint:wrapcheck // pass the sentinel through unchanged
	}

	s.opened++
	return &closeCountingIterator{StreamIterator: iter, store: s}, nil
}

type closeCountingIterator struct {
	eventstore.StreamIterator
	store *closeCountingStore
}

func (i *closeCountingIterator) Close(ctx context.Context) error {
	i.store.closed++
	return i.StreamIterator.Close(ctx) //nolint:wrapcheck // pass through unchanged
}

func newStore(t *testing.T) (*eventstream.EventStreamStore, *closeCountingStore) {
	t.Helper()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	counting := &closeCountingStore{Store: eventStore}
	return eventstream.NewEventStreamStore(counting), counting
}

func writeSnapshots(t *testing.T, store *eventstream.EventStreamStore, aggregateID typeid.ID, versions ...int64) {
	t.Helper()

	for _, version := range versions {
		if err := store.WriteSnapshot(t.Context(), &snapshotstore.AggregateSnapshot{
			AggregateID:      aggregateID,
			AggregateVersion: version,
			Data:             []byte(`{"n":1}`),
		}); err != nil {
			t.Fatalf("writing snapshot at version %d: %v", version, err)
		}
	}
}

// TestReadSnapshot_AbsentStreamIsSnapshotNotFound guards against a never-snapshotted
// aggregate surfacing as a wrapped ErrStreamNotFound. SnapshottingStore checks for
// ErrSnapshotNotFound before falling back to full hydration, so anything else took the
// "failed to read snapshot" branch and logged a warning on every first load.
func TestReadSnapshot_AbsentStreamIsSnapshotNotFound(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)

	_, err := store.ReadSnapshot(t.Context(), typeid.NewV4("mockentity"), snapshotstore.ReadSnapshotOptions{})
	if !errors.Is(err, snapshotstore.ErrSnapshotNotFound) {
		t.Errorf("want ErrSnapshotNotFound, got %v", err)
	}
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Error("want the event-store sentinel translated, got a bare ErrStreamNotFound")
	}
}

// TestReadSnapshot_HonorsMaxVersion guards against opts being ignored. Returning the newest
// snapshot regardless of the bound made every ToVersion load fail outright: the aggregate
// was set past the target, and the inner store then rejected it as "more recent than
// requested".
func TestReadSnapshot_HonorsMaxVersion(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name        string
		maxVersion  int64
		wantVersion int64
		wantErr     error
	}{
		{name: "unbounded read returns the newest snapshot", maxVersion: 0, wantVersion: 10},
		{name: "exact match returns that snapshot", maxVersion: 5, wantVersion: 5},
		{name: "no exact match returns the newest below the bound", maxVersion: 7, wantVersion: 6},
		{name: "bound at the newest returns the newest", maxVersion: 10, wantVersion: 10},
		{name: "bound below every snapshot reports not found", maxVersion: 1, wantErr: snapshotstore.ErrSnapshotNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, _ := newStore(t)
			aggregateID := typeid.NewV4("mockentity")
			writeSnapshots(t, store, aggregateID, 2, 4, 5, 6, 10)

			snap, err := store.ReadSnapshot(t.Context(), aggregateID,
				snapshotstore.ReadSnapshotOptions{MaxVersion: tt.maxVersion})

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("want %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("reading snapshot: %v", err)
			}
			if snap.AggregateVersion != tt.wantVersion {
				t.Errorf("want aggregate version %d, got %d", tt.wantVersion, snap.AggregateVersion)
			}
		})
	}
}

// TestReadSnapshot_ClosesIterator guards against the read leaking a cursor on every call.
func TestReadSnapshot_ClosesIterator(t *testing.T) {
	t.Parallel()

	store, counting := newStore(t)
	aggregateID := typeid.NewV4("mockentity")
	writeSnapshots(t, store, aggregateID, 1, 2, 3)

	if _, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{}); err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}
	if _, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{MaxVersion: 2}); err != nil {
		t.Fatalf("reading bounded snapshot: %v", err)
	}

	if counting.opened == 0 {
		t.Fatal("expected the store to open at least one iterator")
	}
	if counting.closed != counting.opened {
		t.Errorf("leaked iterators: opened %d, closed %d", counting.opened, counting.closed)
	}
}

type mockEntity struct {
	ID    typeid.ID `json:"id"`
	Count int       `json:"count"`
}

func (e *mockEntity) EntityID() typeid.ID { return e.ID }

func newMockEntity(id uuid.UUID) *mockEntity {
	return &mockEntity{ID: typeid.New("mockentity", id)}
}

type mockEntityEvent struct{}

func (e *mockEntityEvent) EventType() string { return "incremented" }

func (e *mockEntityEvent) New() estoria.EntityEvent[*mockEntity] { return &mockEntityEvent{} }

func (e *mockEntityEvent) ApplyTo(_ context.Context, entity *mockEntity) (*mockEntity, error) {
	entity.Count++
	return entity, nil
}

// TestSnapshottingStore_VersionedLoad exercises the symptom that made the ignored MaxVersion
// worth fixing: a versioned load through a SnapshottingStore backed by this store failed
// outright, because the newest snapshot was applied past the requested version and the inner
// store then rejected the aggregate as more recent than requested.
func TestSnapshottingStore_VersionedLoad(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	inner, err := aggregatestore.New[*mockEntity](eventStore, newMockEntity,
		aggregatestore.WithEventTypes[*mockEntity](&mockEntityEvent{}))
	if err != nil {
		t.Fatalf("creating inner store: %v", err)
	}

	// Snapshot on every event, so a snapshot exists at each version including the tip.
	store, err := aggregatestore.NewSnapshottingStore[*mockEntity](
		inner,
		eventstream.NewEventStreamStore(eventStore),
		snapshotstore.EventCountSnapshotPolicy{N: 1},
	)
	if err != nil {
		t.Fatalf("creating snapshotting store: %v", err)
	}

	id := uuid.Must(uuid.NewV4())
	aggregate := store.New(id)
	for range 10 {
		if err := aggregate.Append(&mockEntityEvent{}); err != nil {
			t.Fatalf("appending event: %v", err)
		}
	}
	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	for _, wantVersion := range []int64{1, 3, 7, 10} {
		got, err := store.Load(t.Context(), id, &aggregatestore.LoadOptions{ToVersion: wantVersion})
		if err != nil {
			t.Errorf("loading to version %d: %v", wantVersion, err)
			continue
		}

		if got.Version() != wantVersion {
			t.Errorf("want version %d, got %d", wantVersion, got.Version())
		}
		if got.Entity().Count != int(wantVersion) {
			t.Errorf("want count %d at version %d, got %d", wantVersion, wantVersion, got.Entity().Count)
		}
	}

	// An unbounded load still reaches the tip.
	got, err := store.Load(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("loading latest: %v", err)
	}
	if want := int64(10); got.Version() != want {
		t.Errorf("want version %d for an unbounded load, got %d", want, got.Version())
	}
}
