package aggregatestore_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

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

// rawCodec is a valid zero-copy StateCodec: it returns and installs the
// very slices it is handed.
type rawCodec struct{}

func (rawCodec) MarshalState(state []byte) ([]byte, error) { return state, nil }

func (rawCodec) UnmarshalState(data []byte, dest *[]byte) error {
	*dest = data
	return nil
}

func (rawCodec) ContentType() string { return "application/octet-stream" }

// TestMemoryAggregateCache_ZeroCopyCodecCannotAlias pins the byte-level half
// of the detachment contract: the cache clones what a marshal returns and
// what an unmarshal consumes, so even a codec that retains or installs its
// slices leaves callers and cache sharing nothing.
func TestMemoryAggregateCache_ZeroCopyCodecCannotAlias(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[[]byte](aggregatestore.WithCacheStateCodec[[]byte](rawCodec{}))
	id := typeid.New("blob", uuid.Must(uuid.NewV4()))

	state := []byte("aaaa")
	if err := cache.PutAggregate(t.Context(), id, aggregatestore.CachedAggregate[[]byte]{State: state, Version: 1}); err != nil {
		t.Fatalf("putting: %v", err)
	}

	// The putter mutates its slice after the put: the entry must not see it.
	state[0] = 'z'

	first, err := cache.GetAggregate(t.Context(), id)
	if err != nil || first == nil {
		t.Fatalf("first get: %+v, %v", first, err)
	}

	if string(first.State) != "aaaa" {
		t.Fatalf("want the entry detached from the putter (%q), got %q", "aaaa", first.State)
	}

	// One getter mutates its slice: the next get must not see it.
	first.State[0] = 'q'

	second, err := cache.GetAggregate(t.Context(), id)
	if err != nil || second == nil {
		t.Fatalf("second get: %+v, %v", second, err)
	}

	if string(second.State) != "aaaa" {
		t.Errorf("want each get detached from every other (%q), got %q", "aaaa", second.State)
	}
}

// fencingCodec fences its own cache from inside a codec call, exercising a
// codec that calls back into the cache mid-encode or mid-decode.
type fencingCodec struct {
	cache         *aggregatestore.MemoryAggregateCache[account]
	id            typeid.ID
	fenceOnEncode int64
	fenceOnDecode int64
}

func (c *fencingCodec) MarshalState(state account) ([]byte, error) {
	if version := c.fenceOnEncode; version > 0 {
		c.fenceOnEncode = 0
		if err := c.cache.FenceAggregate(context.Background(), c.id, version); err != nil {
			return nil, err
		}
	}

	return json.Marshal(state)
}

func (c *fencingCodec) UnmarshalState(data []byte, dest *account) error {
	if version := c.fenceOnDecode; version > 0 {
		c.fenceOnDecode = 0
		if err := c.cache.FenceAggregate(context.Background(), c.id, version); err != nil {
			return err
		}
	}

	return json.Unmarshal(data, dest)
}

func (c *fencingCodec) ContentType() string { return estoria.ContentTypeJSON }

// newFencingFixture builds a cache whose codec fences that cache reentrantly.
func newFencingFixture(t *testing.T) (*aggregatestore.MemoryAggregateCache[account], *fencingCodec, typeid.ID) {
	t.Helper()

	id := typeid.New("account", uuid.Must(uuid.NewV4()))
	codec := &fencingCodec{id: id}
	cache := aggregatestore.NewMemoryAggregateCache[account](
		aggregatestore.WithCacheStateCodec[account](estoria.StateCodec[account](codec)))
	codec.cache = cache

	return cache, codec, id
}

// mustFinish fails the test if the operation does not return promptly — the
// deadlock a codec calling back into a lock-holding cache would produce.
func mustFinish(t *testing.T, name string, op func() error) {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- op() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s deadlocked on the reentrant codec", name)
	}
}

// TestMemoryAggregateCache_ReentrantCodecCannotDeadlock pins that the codec
// runs outside the cache's lock: a codec that calls back into the cache
// completes, a reentrant fence the put outranks changes nothing, and a
// reentrant fence that outranks the put is honored by the atomic recheck
// before insertion.
func TestMemoryAggregateCache_ReentrantCodecCannotDeadlock(t *testing.T) {
	t.Parallel()

	t.Run("an outranked fence during encoding changes nothing", func(t *testing.T) {
		t.Parallel()

		cache, codec, id := newFencingFixture(t)
		codec.fenceOnEncode = 3

		mustFinish(t, "put", func() error {
			return cache.PutAggregate(context.Background(), id, cacheEntry(5))
		})

		if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry == nil || entry.Version != 5 {
			t.Errorf("want the put stored over the outranked fence, got %+v, %v", entry, err)
		}
	})

	t.Run("an outranking fence during encoding drops the put", func(t *testing.T) {
		t.Parallel()

		cache, codec, id := newFencingFixture(t)
		codec.fenceOnEncode = 9

		mustFinish(t, "put", func() error {
			return cache.PutAggregate(context.Background(), id, cacheEntry(5))
		})

		if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
			t.Errorf("want the put dropped by the atomic recheck, got %+v, %v", entry, err)
		}
	})

	t.Run("a fence during decoding completes", func(t *testing.T) {
		t.Parallel()

		cache, codec, id := newFencingFixture(t)

		mustFinish(t, "put", func() error {
			return cache.PutAggregate(context.Background(), id, cacheEntry(5))
		})

		codec.fenceOnDecode = 2

		var entry *aggregatestore.CachedAggregate[account]
		mustFinish(t, "get", func() error {
			var err error
			entry, err = cache.GetAggregate(context.Background(), id)
			return err
		})

		if entry == nil || entry.Version != 5 {
			t.Errorf("want the entry the decode-time fence does not outrank still served (version 5), got %+v", entry)
		}
	})
}

// TestMemoryAggregateCache_StalePutIsDroppedWithoutEncoding pins put's
// ordering: the floor is checked before the codec runs, so a put that has
// already lost the race is dropped without error — the failing codec here
// proves it is never invoked.
func TestMemoryAggregateCache_StalePutIsDroppedWithoutEncoding(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[*ptrCounter](
		aggregatestore.WithCacheStateCodec[*ptrCounter](brokenCodec{failMarshal: true}))
	id := typeid.New("ptrcounter", uuid.Must(uuid.NewV4()))

	if err := cache.FenceAggregate(t.Context(), id, 2); err != nil {
		t.Fatalf("fencing at 2: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), id, aggregatestore.CachedAggregate[*ptrCounter]{State: &ptrCounter{Balance: 10}, Version: 1}); err != nil {
		t.Errorf("want the below-fence put dropped without error, got %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want nothing cached, got %+v, %v", entry, err)
	}
}

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
