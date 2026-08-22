package aggregatestore

import (
	"testing"

	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// TestMemoryAggregateCache_FenceSupersedesRecordsAtOrBelow pins the
// retention contract from the inside: reservation records live only above
// the committed fence. A commit sweeps every record the risen fence reaches
// — the pending reservation an unknown-outcome save left standing, a
// withdrawal's tombstone, and the committed reservation itself — so the
// map's growth is bounded by durable progress, and the happy path retains
// nothing at all.
func TestMemoryAggregateCache_FenceSupersedesRecordsAtOrBelow(t *testing.T) {
	t.Parallel()

	cache := NewMemoryAggregateCache[struct{}]()
	id := typeid.New("thing", uuid.Must(uuid.NewV4()))
	key := id.String()

	// An unknown-outcome save's reservation, left standing at version 2.
	if err := cache.ReserveFence(t.Context(), id, 2, "pending-2"); err != nil {
		t.Fatalf("reserving version 2: %v", err)
	}

	// A withdrawal that outran its reserve, tombstoned at version 3.
	if err := cache.ReleaseFence(t.Context(), id, 3, "settled-3"); err != nil {
		t.Fatalf("releasing version 3: %v", err)
	}

	// A later save durably commits version 3.
	if err := cache.ReserveFence(t.Context(), id, 3, "committed-3"); err != nil {
		t.Fatalf("reserving version 3: %v", err)
	}
	if err := cache.CommitFence(t.Context(), id, 3, "committed-3"); err != nil {
		t.Fatalf("committing version 3: %v", err)
	}

	cache.mu.Lock()
	remaining := len(cache.reservations[key])
	cache.mu.Unlock()

	if remaining != 0 {
		t.Errorf("want every record at or below the committed fence superseded, got %d retained", remaining)
	}

	// A reservation above the fence survives the sweep.
	if err := cache.ReserveFence(t.Context(), id, 9, "pending-9"); err != nil {
		t.Fatalf("reserving version 9: %v", err)
	}

	if err := cache.ReserveFence(t.Context(), id, 5, "committed-5"); err != nil {
		t.Fatalf("reserving version 5: %v", err)
	}
	if err := cache.CommitFence(t.Context(), id, 5, "committed-5"); err != nil {
		t.Fatalf("committing version 5: %v", err)
	}

	cache.mu.Lock()
	record, ok := cache.reservations[key]["pending-9"]
	remaining = len(cache.reservations[key])
	cache.mu.Unlock()

	if !ok || record.settled || record.version != 9 || remaining != 1 {
		t.Errorf("want only the pending version-9 reservation retained, got %d records (pending-9: %+v, present %v)",
			remaining, record, ok)
	}
}
