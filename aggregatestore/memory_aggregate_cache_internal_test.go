package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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

	cache.lock <- struct{}{}
	remaining := len(cache.reservations[key])
	<-cache.lock

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

	cache.lock <- struct{}{}
	record, ok := cache.reservations[key]["pending-9"]
	remaining = len(cache.reservations[key])
	<-cache.lock

	if !ok || record.settled || record.version != 9 || remaining != 1 {
		t.Errorf("want only the pending version-9 reservation retained, got %d records (pending-9: %+v, present %v)",
			remaining, record, ok)
	}
}

// TestMemoryAggregateCache_ObservedSettlementRetainsNoRecord pins the other
// growth bound: settling a reservation the cache observes removes its record
// outright — the record is the delivery, so nothing needs a tombstone — and
// repeated failed save attempts above the committed fence retain nothing.
func TestMemoryAggregateCache_ObservedSettlementRetainsNoRecord(t *testing.T) {
	t.Parallel()

	cache := NewMemoryAggregateCache[struct{}]()
	id := typeid.New("thing", uuid.Must(uuid.NewV4()))
	key := id.String()

	for i := range 100 {
		token := FenceToken(fmt.Sprintf("cycle-%d", i))
		if err := cache.ReserveFence(t.Context(), id, 5, token); err != nil {
			t.Fatalf("reserving cycle %d: %v", i, err)
		}

		if err := cache.ReleaseFence(t.Context(), id, 5, token); err != nil {
			t.Fatalf("releasing cycle %d: %v", i, err)
		}
	}

	cache.lock <- struct{}{}
	records, present := cache.reservations[key]
	<-cache.lock

	if present {
		t.Errorf("want acknowledged reserve/release cycles to retain no records, got %d", len(records))
	}
}

// TestMemoryAggregateCache_LockWaitHonorsContext pins the locking boundary
// half of the context contract: an operation whose context ends while the
// lock is contended returns the context's error instead of waiting it out.
func TestMemoryAggregateCache_LockWaitHonorsContext(t *testing.T) {
	t.Parallel()

	cache := NewMemoryAggregateCache[struct{}]()
	id := typeid.New("thing", uuid.Must(uuid.NewV4()))

	// Hold the cache's lock so the reserve must wait on it.
	cache.lock <- struct{}{}

	bounded, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	result := make(chan error, 1)

	go func() { result <- cache.ReserveFence(bounded, id, 5, "tok") }()

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("want the contended reserve refused with the context's error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the contended reserve outlived its context")
	}

	<-cache.lock

	// With the lock free again, the same reserve proceeds.
	if err := cache.ReserveFence(t.Context(), id, 5, "tok"); err != nil {
		t.Errorf("want the reserve to proceed once the lock frees, got %v", err)
	}
}
