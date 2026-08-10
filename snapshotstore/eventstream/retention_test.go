package eventstream_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/snapshotstore/eventstream"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

func TestWithMaxSnapshots_PrunesOldSnapshots(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := eventstream.New(eventStore, eventstream.WithMaxSnapshots(2))
	if err != nil {
		t.Fatalf("creating snapshot store: %v", err)
	}

	aggregateID := typeid.New("mockentity", uuid.Must(uuid.NewV4()))
	writeSnapshots(t, store, aggregateID, 1, 2, 3, 4, 5)

	// The snapshot stream itself holds only the newest two events.
	iter, err := eventStore.ReadStream(t.Context(), typeid.New("mockentitysnapshot", aggregateID.UUID), eventstore.ReadStreamOptions{})
	if err != nil {
		t.Fatalf("reading snapshot stream: %v", err)
	}

	t.Cleanup(func() { _ = iter.Close(t.Context()) })

	events, err := eventstore.Collect(t.Context(), iter)
	if err != nil {
		t.Fatalf("collecting snapshot events: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("want 2 retained snapshot events, got %d", len(events))
	}

	// The newest snapshot is untouched by pruning.
	snap, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{})
	if err != nil {
		t.Fatalf("reading newest snapshot: %v", err)
	}

	if snap.AggregateVersion != 5 {
		t.Errorf("want the newest snapshot at aggregate version 5, got %d", snap.AggregateVersion)
	}

	// A read bounded below the retained window finds nothing, so hydration
	// falls back to full replay rather than trusting a pruned snapshot.
	if _, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{MaxVersion: 2}); !errors.Is(err, snapshotstore.ErrSnapshotNotFound) {
		t.Errorf("want ErrSnapshotNotFound below the retained window, got %v", err)
	}
}

// storeWithoutDeletion narrows a store to the plain Store interface, so its
// method set does not include DeleteStream regardless of the wrapped value.
type storeWithoutDeletion struct {
	eventstore.Store
}

func TestWithMaxSnapshots_RequiresStreamDeleter(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	if _, err := eventstream.New(storeWithoutDeletion{Store: eventStore}, eventstream.WithMaxSnapshots(2)); err == nil {
		t.Error("want an error for an event store without stream deletion, got nil")
	}
}

func TestWithMaxSnapshots_RejectsNonPositiveCount(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	if _, err := eventstream.New(eventStore, eventstream.WithMaxSnapshots(0)); err == nil {
		t.Error("want an error for a non-positive snapshot count, got nil")
	}
}

// failingDeleterStore is a delete-capable store whose deletions always fail.
type failingDeleterStore struct {
	*memory.EventStore
}

func (s failingDeleterStore) DeleteStream(_ context.Context, _ typeid.ID, _ eventstore.DeleteStreamOptions) error {
	return errors.New("deletion failed")
}

// TestWithMaxSnapshots_PruningFailureDoesNotFailTheWrite pins pruning as
// best-effort housekeeping: the snapshot was durably written, so a failed
// prune must not turn a successful write into an error.
func TestWithMaxSnapshots_PruningFailureDoesNotFailTheWrite(t *testing.T) {
	t.Parallel()

	eventStore, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := eventstream.New(failingDeleterStore{EventStore: eventStore}, eventstream.WithMaxSnapshots(1))
	if err != nil {
		t.Fatalf("creating snapshot store: %v", err)
	}

	aggregateID := typeid.New("mockentity", uuid.Must(uuid.NewV4()))
	writeSnapshots(t, store, aggregateID, 1, 2, 3)

	snap, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{})
	if err != nil {
		t.Fatalf("reading newest snapshot: %v", err)
	}

	if snap.AggregateVersion != 3 {
		t.Errorf("want the newest snapshot at aggregate version 3, got %d", snap.AggregateVersion)
	}
}
