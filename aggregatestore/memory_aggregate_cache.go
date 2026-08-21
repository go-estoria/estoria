package aggregatestore

import (
	"context"
	"sync"

	"github.com/go-estoria/estoria/typeid"
)

// MemoryAggregateCache is an in-memory AggregateCache. It enforces the
// interface's ordering contract: a put below the newest stored version or
// the fence is dropped, a fence evicts the entry it outranks, and each
// comparison is atomic with its effect. The cache is unbounded — entries and
// fences live until the process exits.
type MemoryAggregateCache[S any] struct {
	mu      sync.Mutex
	entries map[string]CachedAggregate[S]
	fences  map[string]int64
}

// NewMemoryAggregateCache creates a new MemoryAggregateCache.
func NewMemoryAggregateCache[S any]() *MemoryAggregateCache[S] {
	return &MemoryAggregateCache[S]{
		entries: map[string]CachedAggregate[S]{},
		fences:  map[string]int64{},
	}
}

var _ AggregateCache[struct{}] = (*MemoryAggregateCache[struct{}])(nil)

// GetAggregate returns the cached entry for the aggregate, or nil if there is none.
func (c *MemoryAggregateCache[S]) GetAggregate(_ context.Context, aggregateID typeid.ID) (*CachedAggregate[S], error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[aggregateID.String()]
	if !ok {
		return nil, nil //nolint:nilnil // a nil entry with a nil error is the cache-miss contract
	}

	return &entry, nil
}

// PutAggregate stores the entry unless a newer entry or fence outranks it.
func (c *MemoryAggregateCache[S]) PutAggregate(_ context.Context, aggregateID typeid.ID, entry CachedAggregate[S]) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := aggregateID.String()
	if entry.Version < c.floorLocked(key) {
		return nil
	}

	c.entries[key] = entry

	return nil
}

// FenceAggregate raises the aggregate's fence to the given version and
// evicts a stored entry below it. Fences only rise.
func (c *MemoryAggregateCache[S]) FenceAggregate(_ context.Context, aggregateID typeid.ID, version int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := aggregateID.String()
	if version > c.fences[key] {
		c.fences[key] = version
	}

	if entry, ok := c.entries[key]; ok && entry.Version < version {
		delete(c.entries, key)
	}

	return nil
}

// floorLocked is the lowest version a put may carry: the newer of the stored
// entry and the fence. The caller must hold c.mu.
func (c *MemoryAggregateCache[S]) floorLocked(key string) int64 {
	floor := c.fences[key]
	if entry, ok := c.entries[key]; ok && entry.Version > floor {
		floor = entry.Version
	}

	return floor
}
