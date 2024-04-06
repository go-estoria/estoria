package aggregatestore

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type InMemoryCacher[E estoria.Entity] struct {
	cache          map[typeid.AnyID]*cacheEntry[E]
	evictionPolicy CacheEvictionPolicy
	mu             sync.RWMutex
}

var _ Cacher[estoria.Entity] = &InMemoryCacher[estoria.Entity]{}

type cacheEntry[E estoria.Entity] struct {
	aggregate *estoria.Aggregate[E]
	added     time.Time
	lastUsed  time.Time
}

func NewInMemoryCacher[E estoria.Entity](opts ...InMemoryCacheOption) *InMemoryCacher[E] {
	return &InMemoryCacher[E]{
		evictionPolicy: CacheEvictionPolicy{},
	}
}

func (c *InMemoryCacher[E]) Start(ctx context.Context) error {
	if c.evictionPolicy.EvictionInterval == 0 {
		slog.Warn("no cache eviction interval set, periodic evictions disabled")
		return nil
	}

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

func (c *InMemoryCacher[E]) GetAggregate(_ context.Context, id typeid.AnyID) (*estoria.Aggregate[E], error) {
	entry := c.get(id)
	if entry == nil {
		return nil, nil
	}

	entry.lastUsed = time.Now()

	return entry.aggregate, nil
}

func (c *InMemoryCacher[E]) PutAggregate(_ context.Context, aggregate *estoria.Aggregate[E]) error {
	now := time.Now()
	c.put(aggregate.ID(), &cacheEntry[E]{
		aggregate: aggregate,
		added:     now,
		lastUsed:  now,
	})

	return nil
}

func (c *InMemoryCacher[E]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = nil
}

func (c *InMemoryCacher[E]) get(id typeid.AnyID) *cacheEntry[E] {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.cache == nil {
		return nil
	}

	entry, ok := c.cache[id]
	if !ok {
		return nil
	}

	return entry
}

func (c *InMemoryCacher[E]) put(id typeid.AnyID, entry *cacheEntry[E]) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cache == nil {
		c.cache = make(map[typeid.AnyID]*cacheEntry[E])
	}

	if c.evictionPolicy.MaxSize > 0 && len(c.cache) >= c.evictionPolicy.MaxSize {
		c.evictLRU()
	}

	c.cache[id] = entry
}

func (c *InMemoryCacher[E]) evictLRU() {
	var (
		lruID    typeid.AnyID
		lruEntry *cacheEntry[E]
	)

	for id, entry := range c.cache {
		if lruEntry == nil || entry.lastUsed.Before(lruEntry.lastUsed) {
			lruID = id
			lruEntry = entry
		}
	}

	delete(c.cache, lruID)
}

func (c *InMemoryCacher[E]) evictTTL() {
	for id, entry := range c.cache {
		if c.evictionPolicy.MaxAge > 0 && time.Since(entry.added) > c.evictionPolicy.MaxAge {
			delete(c.cache, id)
			continue
		}

		if c.evictionPolicy.MaxIdle > 0 && time.Since(entry.lastUsed) > c.evictionPolicy.MaxIdle {
			delete(c.cache, id)
			continue
		}
	}
}

type InMemoryCacheOption func(*InMemoryCacher[estoria.Entity])

func WithEvictionPolicy(policy CacheEvictionPolicy) InMemoryCacheOption {
	return func(c *InMemoryCacher[estoria.Entity]) {
		c.evictionPolicy = policy
	}
}
