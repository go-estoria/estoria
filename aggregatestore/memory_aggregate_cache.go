package aggregatestore

import (
	"context"
	"fmt"
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
// any caller. State must round-trip its codec; what the codec drops, the
// cache forgets. The cache is unbounded — entries and fences live until the
// process exits — so its ordering guarantee is never retention-scoped.
type MemoryAggregateCache[S any] struct {
	codec   estoria.StateCodec[S]
	mu      sync.Mutex
	entries map[string]memoryCachedState
	fences  map[string]int64
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
// replacing the default JSON codec. A nil codec keeps the default.
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
		codec:   estoria.JSONStateCodec[S]{},
		entries: map[string]memoryCachedState{},
		fences:  map[string]int64{},
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

	// The stored bytes are immutable once written — a put replaces the whole
	// entry — so decoding outside the lock reads a stable snapshot.
	entry := CachedAggregate[S]{Version: stored.version}
	if err := c.codec.UnmarshalState(stored.data, &entry.State); err != nil {
		return nil, fmt.Errorf("unmarshaling cached state: %w", err)
	}

	return &entry, nil
}

// PutAggregate stores the entry unless a newer entry or fence outranks it.
// The state is captured serialized, detaching it from the caller.
func (c *MemoryAggregateCache[S]) PutAggregate(_ context.Context, aggregateID typeid.ID, entry CachedAggregate[S]) error {
	data, err := c.codec.MarshalState(entry.State)
	if err != nil {
		return fmt.Errorf("marshaling state for cache: %w", err)
	}

	key := aggregateID.String()

	c.mu.Lock()
	defer c.mu.Unlock()

	if entry.Version < c.floorLocked(key) {
		return nil
	}

	c.entries[key] = memoryCachedState{data: data, version: entry.Version}

	return nil
}

// FenceAggregate raises the aggregate's fence to the given version and
// evicts a stored entry below it. Fences only rise, and they are never
// evicted, so the fence always outlives the entries it outranks.
func (c *MemoryAggregateCache[S]) FenceAggregate(_ context.Context, aggregateID typeid.ID, version int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := aggregateID.String()
	if version > c.fences[key] {
		c.fences[key] = version
	}

	if stored, ok := c.entries[key]; ok && stored.version < version {
		delete(c.entries, key)
	}

	return nil
}

// floorLocked is the lowest version a put may carry: the newer of the stored
// entry and the fence. The caller must hold c.mu.
func (c *MemoryAggregateCache[S]) floorLocked(key string) int64 {
	floor := c.fences[key]
	if stored, ok := c.entries[key]; ok && stored.version > floor {
		floor = stored.version
	}

	return floor
}
