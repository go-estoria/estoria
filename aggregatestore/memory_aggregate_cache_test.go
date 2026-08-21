package aggregatestore_test

import (
	"sync"
	"testing"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

func cacheEntry(version int64) aggregatestore.CachedAggregate[account] {
	return aggregatestore.CachedAggregate[account]{
		State:   account{Balance: int(version)},
		Version: version,
	}
}

func TestMemoryAggregateCache_PutRefusesRegression(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(2)); err != nil {
		t.Fatalf("putting version 2: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(1)); err != nil {
		t.Fatalf("putting version 1: %v", err)
	}

	entry, err := cache.GetAggregate(t.Context(), id)
	if err != nil || entry == nil {
		t.Fatalf("reading entry: %+v, %v", entry, err)
	}

	if entry.Version != 2 {
		t.Errorf("want the older put dropped (version 2 kept), got %d", entry.Version)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(3)); err != nil {
		t.Fatalf("putting version 3: %v", err)
	}

	if entry, _ = cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 3 {
		t.Errorf("want the newer put stored, got %+v", entry)
	}
}

func TestMemoryAggregateCache_PutAtTheFloorReplaces(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(3)); err != nil {
		t.Fatalf("putting version 3: %v", err)
	}

	replacement := aggregatestore.CachedAggregate[account]{State: account{Balance: 33}, Version: 3}
	if err := cache.PutAggregate(t.Context(), id, replacement); err != nil {
		t.Fatalf("re-putting version 3: %v", err)
	}

	entry, err := cache.GetAggregate(t.Context(), id)
	if err != nil || entry == nil {
		t.Fatalf("reading entry: %+v, %v", entry, err)
	}

	if entry.State.Balance != 33 {
		t.Errorf("want the same-version re-put stored, got balance %d", entry.State.Balance)
	}
}

func TestMemoryAggregateCache_FenceEvictsAndBlocksBelow(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(1)); err != nil {
		t.Fatalf("putting version 1: %v", err)
	}

	if err := cache.FenceAggregate(t.Context(), id, 3); err != nil {
		t.Fatalf("fencing at 3: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the fenced entry evicted, got %+v, %v", entry, err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(2)); err != nil {
		t.Fatalf("putting version 2: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the below-fence put dropped, got %+v, %v", entry, err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(3)); err != nil {
		t.Fatalf("putting version 3: %v", err)
	}

	if entry, _ := cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 3 {
		t.Errorf("want the at-fence put stored, got %+v", entry)
	}
}

func TestMemoryAggregateCache_FenceKeepsTheEntryItDoesNotOutrank(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(3)); err != nil {
		t.Fatalf("putting version 3: %v", err)
	}

	if err := cache.FenceAggregate(t.Context(), id, 3); err != nil {
		t.Fatalf("fencing at 3: %v", err)
	}

	if entry, _ := cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 3 {
		t.Errorf("want the at-fence entry kept, got %+v", entry)
	}
}

func TestMemoryAggregateCache_FenceWithoutEntryStillBlocks(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	if err := cache.FenceAggregate(t.Context(), id, 3); err != nil {
		t.Fatalf("fencing at 3: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(2)); err != nil {
		t.Fatalf("putting version 2: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the below-fence put dropped with no entry stored, got %+v, %v", entry, err)
	}
}

func TestMemoryAggregateCache_FencesOnlyRise(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	if err := cache.FenceAggregate(t.Context(), id, 5); err != nil {
		t.Fatalf("fencing at 5: %v", err)
	}

	if err := cache.FenceAggregate(t.Context(), id, 2); err != nil {
		t.Fatalf("fencing at 2: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(3)); err != nil {
		t.Fatalf("putting version 3: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the lower fence ignored and the put dropped, got %+v, %v", entry, err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(5)); err != nil {
		t.Fatalf("putting version 5: %v", err)
	}

	if entry, _ := cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 5 {
		t.Errorf("want the at-fence put stored, got %+v", entry)
	}
}

func TestMemoryAggregateCache_AggregatesAreIndependent(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	fenced := typeid.New("account", uuid.Must(uuid.NewV4()))
	other := typeid.New("account", uuid.Must(uuid.NewV4()))

	if err := cache.FenceAggregate(t.Context(), fenced, 5); err != nil {
		t.Fatalf("fencing: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), other, cacheEntry(1)); err != nil {
		t.Fatalf("putting: %v", err)
	}

	if entry, _ := cache.GetAggregate(t.Context(), other); entry == nil || entry.Version != 1 {
		t.Errorf("want the other aggregate unaffected by the fence, got %+v", entry)
	}
}

// TestMemoryAggregateCache_ConcurrentPublicationsConvergeToNewest races puts
// and a fence: whatever order they land in, the newest version wins, because
// each comparison is atomic with its effect.
func TestMemoryAggregateCache_ConcurrentPublicationsConvergeToNewest(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	var wg sync.WaitGroup
	for version := int64(1); version <= 20; version++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := cache.PutAggregate(t.Context(), id, cacheEntry(version)); err != nil {
				t.Errorf("putting version %d: %v", version, err)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := cache.FenceAggregate(t.Context(), id, 10); err != nil {
			t.Errorf("fencing at 10: %v", err)
		}
	}()

	wg.Wait()

	entry, err := cache.GetAggregate(t.Context(), id)
	if err != nil || entry == nil {
		t.Fatalf("reading entry: %+v, %v", entry, err)
	}

	if entry.Version != 20 {
		t.Errorf("want the newest publication stored (version 20), got %d", entry.Version)
	}
}
