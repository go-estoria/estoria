package aggregatestore_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/go-estoria/estoria"
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

// TestMemoryAggregateCache_DetachesStateAtBothBoundaries pins the detachment
// contract directly: state handed to a put stays the caller's to mutate, and
// each get returns state nothing else holds.
func TestMemoryAggregateCache_DetachesStateAtBothBoundaries(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[*ptrCounter]()
	id := typeid.New("ptrcounter", uuid.Must(uuid.NewV4()))

	original := &ptrCounter{Balance: 10}
	if err := cache.PutAggregate(t.Context(), id, aggregatestore.CachedAggregate[*ptrCounter]{State: original, Version: 1}); err != nil {
		t.Fatalf("putting: %v", err)
	}

	// The caller mutates its state after the put: the entry must not see it.
	original.Balance = 999

	first, err := cache.GetAggregate(t.Context(), id)
	if err != nil || first == nil {
		t.Fatalf("first get: %+v, %v", first, err)
	}

	if first.State.Balance != 10 {
		t.Fatalf("want the entry detached from the putter (balance 10), got %d", first.State.Balance)
	}

	// One getter mutates its state: the next get must not see it.
	first.State.Balance = 555

	second, err := cache.GetAggregate(t.Context(), id)
	if err != nil || second == nil {
		t.Fatalf("second get: %+v, %v", second, err)
	}

	if second.State.Balance != 10 {
		t.Errorf("want each get detached from every other (balance 10), got %d", second.State.Balance)
	}
}

// brokenCodec fails marshaling or unmarshaling on command.
type brokenCodec struct {
	failMarshal   bool
	failUnmarshal bool
}

func (c brokenCodec) MarshalState(state *ptrCounter) ([]byte, error) {
	if c.failMarshal {
		return nil, errors.New("marshal refused")
	}

	return json.Marshal(state)
}

func (c brokenCodec) UnmarshalState(data []byte, dest **ptrCounter) error {
	if c.failUnmarshal {
		return errors.New("unmarshal refused")
	}

	return json.Unmarshal(data, dest)
}

func (c brokenCodec) ContentType() string { return estoria.ContentTypeJSON }

// TestMemoryAggregateCache_CodecFailuresSurface pins that serialization
// failures are reported, not swallowed: a put that cannot capture state
// errors instead of caching something else, and a get that cannot decode
// errors instead of serving it.
func TestMemoryAggregateCache_CodecFailuresSurface(t *testing.T) {
	t.Parallel()

	id := typeid.New("ptrcounter", uuid.Must(uuid.NewV4()))

	t.Run("marshal failure fails the put", func(t *testing.T) {
		t.Parallel()

		cache := aggregatestore.NewMemoryAggregateCache[*ptrCounter](
			aggregatestore.WithCacheStateCodec[*ptrCounter](brokenCodec{failMarshal: true}))

		err := cache.PutAggregate(t.Context(), id, aggregatestore.CachedAggregate[*ptrCounter]{State: &ptrCounter{Balance: 10}, Version: 1})
		if err == nil {
			t.Fatal("want the put refused when state cannot be marshaled, got nil")
		}

		if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
			t.Errorf("want nothing cached after the failed put, got %+v, %v", entry, err)
		}
	})

	t.Run("unmarshal failure fails the get", func(t *testing.T) {
		t.Parallel()

		cache := aggregatestore.NewMemoryAggregateCache[*ptrCounter](
			aggregatestore.WithCacheStateCodec[*ptrCounter](brokenCodec{failUnmarshal: true}))

		if err := cache.PutAggregate(t.Context(), id, aggregatestore.CachedAggregate[*ptrCounter]{State: &ptrCounter{Balance: 10}, Version: 1}); err != nil {
			t.Fatalf("putting: %v", err)
		}

		if _, err := cache.GetAggregate(t.Context(), id); err == nil {
			t.Fatal("want the get failing when the entry cannot be decoded, got nil")
		}
	})
}
