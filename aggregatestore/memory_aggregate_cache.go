package aggregatestore

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

// MemoryAggregateCache is an in-memory AggregateCache. It enforces the
// interface's ordering contract — a put below the newest stored version or
// the fence is dropped, a fence evicts the entry it outranks, each
// comparison atomic with its effect, and every read observes the mutations
// that completed before it — and its detachment contract: entries are held
// serialized through a state codec, so no cached state shares memory with
// any caller. The codec runs outside the cache's lock and touches only
// clones of the stored bytes, so a codec that returns or installs its input
// cannot alias an entry; decoded state detaches as far as the codec honors
// the estoria.StateCodec contract, concurrency-safety and output ownership
// included. A codec may call back into the cache's fence operations, which
// never invoke a codec; a nested GetAggregate or PutAggregate invokes the
// codec again, so only a codec reentrant in its own right may make one.
// State must round-trip its codec; what the codec drops, the cache forgets.
// The cache is unbounded — entries, fences, and reservations live until the
// process exits — so its ordering guarantee is never retention-scoped.
type MemoryAggregateCache[S any] struct {
	codec        estoria.StateCodec[S]
	mu           sync.Mutex
	entries      map[string]memoryCachedState
	fences       map[string]int64
	reservations map[string]map[FenceToken]int64
	lastToken    uint64
}

// memoryCachedState is a stored entry: the state serialized at put time and
// the version it reflects.
type memoryCachedState struct {
	data    []byte
	version int64
}

// A MemoryAggregateCacheOption configures a MemoryAggregateCache.
type MemoryAggregateCacheOption[S any] func(*MemoryAggregateCache[S])

// WithCacheStateCodec sets the codec entries are serialized through,
// replacing the default JSON codec. A nil codec keeps the default. The
// codec is called outside the cache's lock and must honor the
// estoria.StateCodec contract, concurrent use included; only the fence
// operations are safe for a non-reentrant codec to call back into, because
// a nested GetAggregate or PutAggregate invokes the codec again.
func WithCacheStateCodec[S any](codec estoria.StateCodec[S]) MemoryAggregateCacheOption[S] {
	return func(c *MemoryAggregateCache[S]) {
		if codec != nil {
			c.codec = codec
		}
	}
}

// NewMemoryAggregateCache creates a new MemoryAggregateCache.
func NewMemoryAggregateCache[S any](opts ...MemoryAggregateCacheOption[S]) *MemoryAggregateCache[S] {
	cache := &MemoryAggregateCache[S]{
		codec:        estoria.JSONStateCodec[S]{},
		entries:      map[string]memoryCachedState{},
		fences:       map[string]int64{},
		reservations: map[string]map[FenceToken]int64{},
	}

	for _, opt := range opts {
		opt(cache)
	}

	return cache
}

var _ AggregateCache[struct{}] = (*MemoryAggregateCache[struct{}])(nil)

// GetAggregate returns the cached entry for the aggregate, or nil if there
// is none. The returned state is freshly decoded: the caller owns it alone.
func (c *MemoryAggregateCache[S]) GetAggregate(_ context.Context, aggregateID typeid.ID) (*CachedAggregate[S], error) {
	c.mu.Lock()
	stored, ok := c.entries[aggregateID.String()]
	c.mu.Unlock()

	if !ok {
		return nil, nil //nolint:nilnil // a nil entry with a nil error is the cache-miss contract
	}

	// Stored bytes are immutable once written — a put replaces the whole
	// entry — so decoding outside the lock reads a stable snapshot, and the
	// codec decodes a clone of it, so even a codec that installs its input
	// hands the caller memory the cache never touches again.
	entry := CachedAggregate[S]{Version: stored.version}
	if err := c.codec.UnmarshalState(bytes.Clone(stored.data), &entry.State); err != nil {
		return nil, fmt.Errorf("unmarshaling cached state: %w", err)
	}

	return &entry, nil
}

// PutAggregate stores the entry unless a newer entry or fence outranks it.
// The state is captured serialized, detaching it from the caller.
func (c *MemoryAggregateCache[S]) PutAggregate(_ context.Context, aggregateID typeid.ID, entry CachedAggregate[S]) error {
	key := aggregateID.String()

	// The floor rules before the codec runs: a put that has already lost the
	// race is dropped without error, never marshaled.
	c.mu.Lock()
	floor := c.floorLocked(key)
	c.mu.Unlock()

	if entry.Version < floor {
		return nil
	}

	data, err := c.codec.MarshalState(entry.State)
	if err != nil {
		return fmt.Errorf("marshaling state for cache: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// The codec ran outside the lock — it may even have called back into
	// this cache — so the floor is rechecked atomically with the insert.
	if entry.Version < c.floorLocked(key) {
		return nil
	}

	// The stored clone shares no memory with whatever the codec returned.
	c.entries[key] = memoryCachedState{data: bytes.Clone(data), version: entry.Version}

	return nil
}

// ReserveFence places a provisional fence at the given version: it evicts a
// stored entry below that version and, with the committed fence and every
// other outstanding reservation, forms the floor puts are judged against
// until the reservation is committed or released.
func (c *MemoryAggregateCache[S]) ReserveFence(_ context.Context, aggregateID typeid.ID, version int64) (FenceToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := aggregateID.String()
	c.lastToken++
	token := FenceToken(strconv.FormatUint(c.lastToken, 10))

	if c.reservations[key] == nil {
		c.reservations[key] = map[FenceToken]int64{}
	}
	c.reservations[key][token] = version

	if stored, ok := c.entries[key]; ok && stored.version < version {
		delete(c.entries, key)
	}

	return token, nil
}

// CommitFence makes the identified reservation permanent, raising the
// aggregate's committed fence to the reservation's version and consuming
// the token. Committed fences only rise, and they are never evicted, so a
// committed fence always outlives the entries it outranks.
func (c *MemoryAggregateCache[S]) CommitFence(_ context.Context, aggregateID typeid.ID, token FenceToken) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := aggregateID.String()
	version, err := c.takeReservationLocked(key, token)
	if err != nil {
		return err
	}

	if version > c.fences[key] {
		c.fences[key] = version
	}

	return nil
}

// ReleaseFence withdraws exactly the identified reservation, consuming the
// token: the committed fence and every other outstanding reservation stand,
// so releasing a failed save's reservation cannot lower a floor a
// concurrent save established.
func (c *MemoryAggregateCache[S]) ReleaseFence(_ context.Context, aggregateID typeid.ID, token FenceToken) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.takeReservationLocked(aggregateID.String(), token)

	return err
}

// takeReservationLocked removes and returns the identified reservation. The
// caller must hold c.mu.
func (c *MemoryAggregateCache[S]) takeReservationLocked(key string, token FenceToken) (int64, error) {
	version, ok := c.reservations[key][token]
	if !ok {
		return 0, fmt.Errorf("no outstanding fence reservation %q", token)
	}

	delete(c.reservations[key], token)
	if len(c.reservations[key]) == 0 {
		delete(c.reservations, key)
	}

	return version, nil
}

// floorLocked is the lowest version a put may carry: the newest of the
// stored entry, the committed fence, and every outstanding reservation. The
// caller must hold c.mu.
func (c *MemoryAggregateCache[S]) floorLocked(key string) int64 {
	floor := c.fences[key]
	if stored, ok := c.entries[key]; ok && stored.version > floor {
		floor = stored.version
	}

	for _, version := range c.reservations[key] {
		if version > floor {
			floor = version
		}
	}

	return floor
}
