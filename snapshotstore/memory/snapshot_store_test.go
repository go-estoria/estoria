package memory_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/snapshotstore/memory"
	"github.com/go-estoria/estoria/typeid"
)

var _ snapshotstore.SnapshotStore = (*memory.SnapshotStore)(nil)

// TestSnapshotStore_ConcurrentReadWrite guards against the store's map being accessed
// without synchronization. The sibling eventstore/memory has always held a mutex; this one
// did not, so any concurrent reader and writer raced. Meaningful only under -race, which CI
// runs.
func TestSnapshotStore_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()

	store := memory.NewSnapshotStore()
	aggregateID := typeid.NewV4("mockentity")

	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 1; i <= iterations; i++ {
			_ = store.WriteSnapshot(t.Context(), &snapshotstore.AggregateSnapshot{
				AggregateID:      aggregateID,
				AggregateVersion: int64(i),
				Data:             []byte(`{}`),
			})
		}
	}()

	go func() {
		defer wg.Done()
		for range iterations {
			_, _ = store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{})
		}
	}()

	wg.Wait()
}

func TestSnapshotStore_ReadSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("reports not found for an aggregate with no snapshots", func(t *testing.T) {
		t.Parallel()

		store := memory.NewSnapshotStore()

		_, err := store.ReadSnapshot(t.Context(), typeid.NewV4("mockentity"), snapshotstore.ReadSnapshotOptions{})
		if !errors.Is(err, snapshotstore.ErrSnapshotNotFound) {
			t.Errorf("want ErrSnapshotNotFound, got %v", err)
		}
	})

	t.Run("returns the latest snapshot by default", func(t *testing.T) {
		t.Parallel()

		store := memory.NewSnapshotStore()
		aggregateID := typeid.NewV4("mockentity")
		writeVersions(t, store, aggregateID, 1, 2, 3)

		snap, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{})
		if err != nil {
			t.Fatalf("reading snapshot: %v", err)
		}

		if want := int64(3); snap.AggregateVersion != want {
			t.Errorf("want version %d, got %d", want, snap.AggregateVersion)
		}
	})

	t.Run("rejects a snapshot older than the most recent", func(t *testing.T) {
		t.Parallel()

		store := memory.NewSnapshotStore()
		aggregateID := typeid.NewV4("mockentity")
		writeVersions(t, store, aggregateID, 5)

		err := store.WriteSnapshot(t.Context(), &snapshotstore.AggregateSnapshot{
			AggregateID:      aggregateID,
			AggregateVersion: 4,
			Data:             []byte(`{}`),
		})
		if err == nil {
			t.Error("want an error writing a snapshot older than the most recent, got nil")
		}
	})

	// The default retention policy keeps only the most recent snapshot, so a MaxVersion
	// below it has nothing to return. This pins the retention loop's behavior.
	t.Run("reports not found when MaxVersion predates every retained snapshot", func(t *testing.T) {
		t.Parallel()

		store := memory.NewSnapshotStore()
		aggregateID := typeid.NewV4("mockentity")
		writeVersions(t, store, aggregateID, 1, 2, 3)

		_, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{MaxVersion: 2})
		if !errors.Is(err, snapshotstore.ErrSnapshotNotFound) {
			t.Errorf("want ErrSnapshotNotFound, got %v", err)
		}
	})

	t.Run("honors a MaxVersion at or above the retained snapshot", func(t *testing.T) {
		t.Parallel()

		store := memory.NewSnapshotStore()
		aggregateID := typeid.NewV4("mockentity")
		writeVersions(t, store, aggregateID, 1, 2, 3)

		snap, err := store.ReadSnapshot(t.Context(), aggregateID, snapshotstore.ReadSnapshotOptions{MaxVersion: 3})
		if err != nil {
			t.Fatalf("reading snapshot: %v", err)
		}

		if want := int64(3); snap.AggregateVersion != want {
			t.Errorf("want version %d, got %d", want, snap.AggregateVersion)
		}
	})
}

func writeVersions(t *testing.T, store *memory.SnapshotStore, aggregateID typeid.ID, versions ...int64) {
	t.Helper()

	for _, version := range versions {
		if err := store.WriteSnapshot(context.Background(), &snapshotstore.AggregateSnapshot{
			AggregateID:      aggregateID,
			AggregateVersion: version,
			Data:             []byte(`{}`),
		}); err != nil {
			t.Fatalf("writing snapshot at version %d: %v", version, err)
		}
	}
}
