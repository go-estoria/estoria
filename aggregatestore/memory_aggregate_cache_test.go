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

// newFenceToken mints a unique caller-side token, as the save protocol does.
func newFenceToken() aggregatestore.FenceToken {
	return aggregatestore.FenceToken(uuid.Must(uuid.NewV4()).String())
}

// commitFence reserves and immediately commits a fence, the shape a
// successful save leaves behind.
func commitFence[S any](t *testing.T, cache *aggregatestore.MemoryAggregateCache[S], id typeid.ID, version int64) {
	t.Helper()

	token := newFenceToken()
	if err := cache.ReserveFence(t.Context(), id, version, token); err != nil {
		t.Fatalf("reserving fence at %d: %v", version, err)
	}

	if err := cache.CommitFence(t.Context(), id, version, token); err != nil {
		t.Fatalf("committing fence at %d: %v", version, err)
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

	commitFence(t, cache, id, 3)

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

	commitFence(t, cache, id, 3)

	if entry, _ := cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 3 {
		t.Errorf("want the at-fence entry kept, got %+v", entry)
	}
}

func TestMemoryAggregateCache_FenceWithoutEntryStillBlocks(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	commitFence(t, cache, id, 3)

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

	commitFence(t, cache, id, 5)

	commitFence(t, cache, id, 2)

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

// TestMemoryAggregateCache_ReservationBlocksAndEvictsUntilReleased pins the
// provisional half of the fence protocol: a reservation evicts and blocks
// below its version like a fence while it stands, and releasing it restores
// the floor the committed state defines — nothing the failed save reserved
// stays outlawed.
func TestMemoryAggregateCache_ReservationBlocksAndEvictsUntilReleased(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(2)); err != nil {
		t.Fatalf("putting version 2: %v", err)
	}

	token := newFenceToken()
	if err := cache.ReserveFence(t.Context(), id, 3, token); err != nil {
		t.Fatalf("reserving fence at 3: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the reserved fence evicting the entry below it, got %+v, %v", entry, err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(2)); err != nil {
		t.Fatalf("putting version 2 under the reservation: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the below-reservation put dropped, got %+v, %v", entry, err)
	}

	if err := cache.ReleaseFence(t.Context(), id, 3, token); err != nil {
		t.Fatalf("releasing: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(2)); err != nil {
		t.Fatalf("putting version 2 after the release: %v", err)
	}

	if entry, _ := cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 2 {
		t.Errorf("want the version-2 put admitted after the release, got %+v", entry)
	}
}

// TestMemoryAggregateCache_ReleaseCannotLowerFloorsOthersHold pins the
// token check: releasing one reservation leaves the committed fence and
// every concurrent reservation standing.
func TestMemoryAggregateCache_ReleaseCannotLowerFloorsOthersHold(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	commitFence(t, cache, id, 2)

	tokenA := newFenceToken()
	if err := cache.ReserveFence(t.Context(), id, 3, tokenA); err != nil {
		t.Fatalf("reserving fence A at 3: %v", err)
	}

	tokenB := newFenceToken()
	if err := cache.ReserveFence(t.Context(), id, 5, tokenB); err != nil {
		t.Fatalf("reserving fence B at 5: %v", err)
	}

	if err := cache.ReleaseFence(t.Context(), id, 3, tokenA); err != nil {
		t.Fatalf("releasing A: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(4)); err != nil {
		t.Fatalf("putting version 4: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the put below B's outstanding reservation dropped, got %+v, %v", entry, err)
	}

	if err := cache.ReleaseFence(t.Context(), id, 5, tokenB); err != nil {
		t.Fatalf("releasing B: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(1)); err != nil {
		t.Fatalf("putting version 1: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the put below the committed fence still dropped, got %+v, %v", entry, err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(4)); err != nil {
		t.Fatalf("putting version 4 after both releases: %v", err)
	}

	if entry, _ := cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 4 {
		t.Errorf("want the version-4 put admitted once only the committed fence stands, got %+v", entry)
	}
}

// TestMemoryAggregateCache_CommitOutlivesItsReservation pins the permanent
// half: a committed reservation joins the fences that only rise, and its
// token is consumed — settlement is idempotent, so a retried commit or a
// late release succeeds without effect, and neither disturbs the committed
// fence.
func TestMemoryAggregateCache_CommitOutlivesItsReservation(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	token := newFenceToken()
	if err := cache.ReserveFence(t.Context(), id, 3, token); err != nil {
		t.Fatalf("reserving fence at 3: %v", err)
	}

	if err := cache.CommitFence(t.Context(), id, 3, token); err != nil {
		t.Fatalf("committing: %v", err)
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(2)); err != nil {
		t.Fatalf("putting version 2: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the below-fence put dropped after the commit, got %+v, %v", entry, err)
	}

	if err := cache.CommitFence(t.Context(), id, 3, token); err != nil {
		t.Errorf("want a retried commit of a consumed token idempotent, got %v", err)
	}

	if err := cache.ReleaseFence(t.Context(), id, 3, token); err != nil {
		t.Errorf("want a late release of a consumed token idempotent, got %v", err)
	}

	// Neither redundant settlement disturbed the committed fence.
	if err := cache.PutAggregate(t.Context(), id, cacheEntry(2)); err != nil {
		t.Fatalf("putting version 2 after the redundant settlements: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the below-fence put still dropped, got %+v, %v", entry, err)
	}
}

// TestMemoryAggregateCache_ReserveRefusesAConflictingToken pins that a token
// names exactly one reservation: re-reserving it at its own version is an
// idempotent no-op, re-reserving it at another version is refused, and the
// original reservation keeps holding its floor either way.
func TestMemoryAggregateCache_ReserveRefusesAConflictingToken(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	token := newFenceToken()
	if err := cache.ReserveFence(t.Context(), id, 10, token); err != nil {
		t.Fatalf("reserving fence at 10: %v", err)
	}

	if err := cache.ReserveFence(t.Context(), id, 10, token); err != nil {
		t.Errorf("want a retried reservation at the same version idempotent, got %v", err)
	}

	if err := cache.ReserveFence(t.Context(), id, 2, token); err == nil {
		t.Error("want a reservation reusing the token at another version refused, got nil")
	}

	if err := cache.PutAggregate(t.Context(), id, cacheEntry(5)); err != nil {
		t.Fatalf("putting version 5: %v", err)
	}

	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the original reservation at 10 still dropping the put, got %+v, %v", entry, err)
	}
}

func TestMemoryAggregateCache_AggregatesAreIndependent(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	fenced := typeid.New("account", uuid.Must(uuid.NewV4()))
	other := typeid.New("account", uuid.Must(uuid.NewV4()))

	commitFence(t, cache, fenced, 5)

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
		token := newFenceToken()
		if err := cache.ReserveFence(t.Context(), id, 10, token); err != nil {
			t.Errorf("reserving fence at 10: %v", err)
			return
		}
		if err := cache.CommitFence(t.Context(), id, 10, token); err != nil {
			t.Errorf("committing fence at 10: %v", err)
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
		if err := c.reentrantFence(version); err != nil {
			return nil, err
		}
	}

	return json.Marshal(state)
}

func (c *fencingCodec) UnmarshalState(data []byte, dest *account) error {
	if version := c.fenceOnDecode; version > 0 {
		c.fenceOnDecode = 0
		if err := c.reentrantFence(version); err != nil {
			return err
		}
	}

	return json.Unmarshal(data, dest)
}

func (c *fencingCodec) reentrantFence(version int64) error {
	token := newFenceToken()
	if err := c.cache.ReserveFence(context.Background(), c.id, version, token); err != nil {
		return err
	}

	return c.cache.CommitFence(context.Background(), c.id, version, token)
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

	commitFence(t, cache, id, 2)

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

// TestMemoryAggregateCache_SettledTokenCannotReturnToPending pins the
// terminal state machine: settlement is forever. A release that found no
// reservation records the terminal state, so the reserve it settled landing
// late is refused; a released reservation refuses re-reservation the same
// way; and neither terminal record contributes to the floor.
func TestMemoryAggregateCache_SettledTokenCannotReturnToPending(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	// A withdrawal outrunning its reserve: release first, reserve after.
	orphan := newFenceToken()
	if err := cache.ReleaseFence(t.Context(), id, 5, orphan); err != nil {
		t.Fatalf("releasing ahead of the delayed reserve: %v", err)
	}

	if err := cache.ReserveFence(t.Context(), id, 5, orphan); !errors.Is(err, aggregatestore.ErrFenceReservationRefused) {
		t.Errorf("want the delayed reserve delivery refused, got %v", err)
	}

	// A placed reservation, released: the token stays settled.
	released := newFenceToken()
	if err := cache.ReserveFence(t.Context(), id, 3, released); err != nil {
		t.Fatalf("reserving at 3: %v", err)
	}
	if err := cache.ReleaseFence(t.Context(), id, 3, released); err != nil {
		t.Fatalf("releasing: %v", err)
	}
	if err := cache.ReserveFence(t.Context(), id, 3, released); !errors.Is(err, aggregatestore.ErrFenceReservationRefused) {
		t.Errorf("want re-reserving a settled token refused, got %v", err)
	}

	// Terminal records hold no floor: a publication below both is admitted.
	if err := cache.PutAggregate(t.Context(), id, cacheEntry(1)); err != nil {
		t.Fatalf("putting version 1: %v", err)
	}

	if entry, _ := cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 1 {
		t.Errorf("want the version-1 put admitted past the settled records, got %+v", entry)
	}
}

// TestMemoryAggregateCache_SettlementRefusesAVersionMismatch pins the
// version half of settlement addressing: a settlement naming a version the
// token does not reserve identifies a different reservation, so the pending
// one stands untouched and the call errors instead of consuming it.
func TestMemoryAggregateCache_SettlementRefusesAVersionMismatch(t *testing.T) {
	t.Parallel()

	cache := aggregatestore.NewMemoryAggregateCache[account]()
	id := typeid.New("account", uuid.Must(uuid.NewV4()))

	token := newFenceToken()
	if err := cache.ReserveFence(t.Context(), id, 10, token); err != nil {
		t.Fatalf("reserving at 10: %v", err)
	}

	if err := cache.ReleaseFence(t.Context(), id, 5, token); err == nil {
		t.Error("want the mismatched release refused, got nil")
	}
	if err := cache.CommitFence(t.Context(), id, 5, token); err == nil {
		t.Error("want the mismatched commit refused, got nil")
	}

	// The reservation held: a publication below it stays refused.
	if err := cache.PutAggregate(t.Context(), id, cacheEntry(5)); err != nil {
		t.Fatalf("putting version 5: %v", err)
	}
	if entry, err := cache.GetAggregate(t.Context(), id); err != nil || entry != nil {
		t.Errorf("want the put below the standing reservation dropped, got %+v, %v", entry, err)
	}

	// And the mismatched commit raised no fence: releasing the real
	// reservation reopens the floor entirely.
	if err := cache.ReleaseFence(t.Context(), id, 10, token); err != nil {
		t.Fatalf("releasing the real reservation: %v", err)
	}
	if err := cache.PutAggregate(t.Context(), id, cacheEntry(5)); err != nil {
		t.Fatalf("putting version 5 after the release: %v", err)
	}
	if entry, _ := cache.GetAggregate(t.Context(), id); entry == nil || entry.Version != 5 {
		t.Errorf("want the version-5 put admitted once the reservation settled, got %+v", entry)
	}
}
