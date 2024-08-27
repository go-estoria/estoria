package aggregatecache

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/typeid"
)

type CacheEvictionPolicy struct {
	// EvictionInterval is the interval at which the cache is checked for items to evict.
	// The default is 0, which means no periodic evictions will occur.
	EvictionInterval time.Duration

	// MaxAge is the maximum age of an item in the cache before it is evicted.
	// A non-zero EvictionInterval is required for this to take effect.
	// The default is 0, which means no periodic evictions based on age will occur.
	MaxAge time.Duration

	// MaxIdle is the maximum time an item can be idle in the cache before it is evicted.
	// A non-zero EvictionInterval is required for this to take effect.
	// The default is 0, which means no periodic evictions based on idle time will occur.
	MaxIdle time.Duration

	// MaxSize is the maximum number of items in the cache before it starts evicting.
	// The default is 0, which means the cache can grow unbounded.
	MaxSize int
}

// InMemoryCache is an aggregate cache that stores aggregates in memory.
type InMemoryCache[E estoria.Entity] struct {
	cancel         context.CancelFunc
	entries        map[typeid.UUID]*cacheEntry[E]
	evictionPolicy CacheEvictionPolicy
	mu             sync.RWMutex
}

var _ aggregatestore.AggregateCache[estoria.Entity] = &InMemoryCache[estoria.Entity]{}

type cacheEntry[E estoria.Entity] struct {
	aggregate *aggregatestore.Aggregate[E]
	added     time.Time
	lastUsed  time.Time
}

// NewInMemoryCache creates a new in-memory cache.
// By default, the cache will grow unbounded and no evictions will occur.
// Use the WithEvictionPolicy option to configure an eviction policy.
func NewInMemoryCache[E estoria.Entity](opts ...InMemoryCacheOption[E]) *InMemoryCache[E] {
	cache := &InMemoryCache[E]{
		evictionPolicy: CacheEvictionPolicy{},
	}

	for _, opt := range opts {
		opt(cache)
	}

	return cache
}

// Start starts the cache's eviction polling loop, which will periodically check for and
// evict items based on the configured eviction policy. If no eviction interval is set, periodic
// evictions will be disabled, and calling Start() will result in a no-op.
func (c *InMemoryCache[E]) Start(ctx context.Context) error {
	if c.evictionPolicy.EvictionInterval == 0 {
		slog.Warn("no cache eviction interval set, periodic evictions disabled")
		return nil
	}

	ctx, c.cancel = context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(c.evictionPolicy.EvictionInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				c.clear()
				return
			case <-ticker.C:
				c.evictTTL()
			}
		}
	}()

	return nil
}

// Stop stops the cache's eviction polling loop.
// If the eviction loop is not running, calling Stop() will result in a no-op.
func (c *InMemoryCache[E]) Stop() error {
	if c.cancel != nil {
		c.cancel()
	}

	return nil
}

// GetAggregate retrieves an aggregate from the cache by its ID.
func (c *InMemoryCache[E]) GetAggregate(_ context.Context, id typeid.UUID) (*aggregatestore.Aggregate[E], error) {
	entry := c.get(id)
	if entry == nil {
		return nil, nil
	}

	entry.lastUsed = time.Now()

	return entry.aggregate, nil
}

// PutAggregate puts an aggregate in the cache by its ID.
func (c *InMemoryCache[E]) PutAggregate(_ context.Context, aggregate *aggregatestore.Aggregate[E]) error {
	now := time.Now()
	c.put(aggregate.ID(), &cacheEntry[E]{
		aggregate: aggregate,
		added:     now,
		lastUsed:  now,
	})

	return nil
}

func (c *InMemoryCache[E]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
}

func (c *InMemoryCache[E]) get(id typeid.UUID) *cacheEntry[E] {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.entries == nil {
		return nil
	}

	entry, ok := c.entries[id]
	if !ok {
		return nil
	}

	return entry
}

func (c *InMemoryCache[E]) put(id typeid.UUID, entry *cacheEntry[E]) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[typeid.UUID]*cacheEntry[E])
	}

	if c.evictionPolicy.MaxSize > 0 && len(c.entries) >= c.evictionPolicy.MaxSize {
		c.evictLRU()
	}

	c.entries[id] = entry
}

func (c *InMemoryCache[E]) evictLRU() {
	var (
		lruID    typeid.UUID
		lruEntry *cacheEntry[E]
	)

	for id, entry := range c.entries {
		if lruEntry == nil || entry.lastUsed.Before(lruEntry.lastUsed) {
			lruID = id
			lruEntry = entry
		}
	}

	delete(c.entries, lruID)
}

func (c *InMemoryCache[E]) evictTTL() {
	for aggregateID, entry := range c.entries {
		if c.evictionPolicy.MaxAge > 0 && time.Since(entry.added) > c.evictionPolicy.MaxAge {
			delete(c.entries, aggregateID)
			continue
		}

		if c.evictionPolicy.MaxIdle > 0 && time.Since(entry.lastUsed) > c.evictionPolicy.MaxIdle {
			delete(c.entries, aggregateID)
			continue
		}
	}
}

type InMemoryCacheOption[E estoria.Entity] func(*InMemoryCache[E])

func WithEvictionPolicy[E estoria.Entity](policy CacheEvictionPolicy) InMemoryCacheOption[E] {
	return func(c *InMemoryCache[E]) {
		c.evictionPolicy = policy
	}
}
