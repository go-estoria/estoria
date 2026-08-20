package aggregatestore

import (
	"context"
	"errors"
	"sync"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// A CachedAggregate is what a cache holds for an aggregate: its state and the version
// that state reflects. Identity is not part of the entry — entries are keyed by typed
// ID, and CachedStore composes the aggregate from the key and the entry on a hit.
type CachedAggregate[S any] struct {
	State   S
	Version int64
}

// An AggregateCache is a cache for aggregates.
//
// Entries are keyed by their typed ID rather than a bare UUID so that a cache backend
// shared across aggregate types produces distinct, self-describing keys. A get that
// finds nothing returns a nil entry and a nil error.
type AggregateCache[S any] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.ID) (*CachedAggregate[S], error)
	PutAggregate(ctx context.Context, aggregateID typeid.ID, entry CachedAggregate[S]) error
}

// CachedStore wraps an aggreate store with an AggregateCache to cache aggregates.
type CachedStore[S any] struct {
	inner Store[S]
	cache AggregateCache[S]
	log   estoria.Logger

	// freshness records the newest version published per aggregate.
	// Publications race — two loads or saves can reach the cache backend out
	// of order — and the backend itself offers no ordering, so this store
	// tracks the high-water mark itself: publish skips writes below it, and a
	// hit below it is served as a miss instead. The mutex guards only the
	// map; no backend call runs under it.
	mu        sync.Mutex
	freshness map[string]int64
}

// NewCachedStore creates a new CachedStore.
func NewCachedStore[S any](
	inner Store[S],
	cacher AggregateCache[S],
) (*CachedStore[S], error) {
	switch {
	case inner == nil:
		return nil, errors.New("inner store is required")
	case cacher == nil:
		return nil, errors.New("aggregate cache is required")
	}

	return &CachedStore[S]{
		inner:     inner,
		cache:     cacher,
		log:       estoria.GetLogger().WithGroup("cachedstore"),
		freshness: map[string]int64{},
	}, nil
}

var _ Store[struct{}] = (*CachedStore[struct{}])(nil)

// AggregateType returns the aggregate type name of the inner store.
func (s *CachedStore[S]) AggregateType() string {
	return s.inner.AggregateType()
}

// New creates a new aggregate with the given ID.
func (s *CachedStore[S]) New(id uuid.UUID) *Aggregate[S] {
	return s.inner.New(id)
}

// Load loads an aggregate, first checking the cache before deferring to the inner store.
// If the aggregate is loaded from the inner store, it is added to the cache.
// A load for a specific version bypasses the cache entirely, in both directions. The cache
// holds whatever version an aggregate was last saved or loaded at, which is not necessarily
// the requested one, and writing a deliberately-truncated aggregate back would then serve
// that stale version to subsequent full loads.
func (s *CachedStore[S]) Load(ctx context.Context, id uuid.UUID, opts *LoadOptions) (*Aggregate[S], error) {
	if opts != nil && opts.ToVersion > 0 {
		s.log.Debug("bypassing cache for versioned load", "aggregate_id", id, "to_version", opts.ToVersion)

		aggregate, err := s.inner.Load(ctx, id, opts)
		if err != nil {
			return nil, LoadError{Operation: "loading from inner aggregate store", Err: err}
		}

		return aggregate, nil
	}

	aggregateID := typeid.New(s.inner.AggregateType(), id)

	entry, err := s.cache.GetAggregate(ctx, aggregateID)
	switch {
	case err == nil && entry != nil && s.isFresh(aggregateID, entry.Version):
		return newAggregate(aggregateID, entry.State, entry.Version), nil
	case err == nil && entry != nil:
		// An out-of-order publication landed after a newer one: serving the
		// entry would regress reads, so load from the inner store instead —
		// and let the publication below heal the backend entry.
		s.log.Debug("cache entry predates a newer publication", "aggregate_id", aggregateID)
	case err != nil:
		s.log.Warn("failed to read cache", "aggregate_id", aggregateID, "error", err)
	default:
		s.log.Debug("aggregate not in cache", "aggregate_id", aggregateID)
	}

	aggregate, err := s.inner.Load(ctx, id, opts)
	if err != nil {
		return nil, LoadError{Operation: "loading from inner aggregate store", Err: err}
	}

	if err := s.publish(ctx, aggregate.ID(), CachedAggregate[S]{State: aggregate.State(), Version: aggregate.Version()}); err != nil {
		s.log.Warn("failed to write cache", "aggregate_id", aggregateID, "error", err)
	}

	return aggregate, nil
}

// publish writes the entry to the cache unless a newer version has already
// been published, advancing the aggregate's freshness high-water mark. The
// skip alone cannot prevent every regression — a write that passed the check
// can still land at the backend after a newer one — so serving reads applies
// the same mark to hits; between them, an out-of-order publication is never
// served. Re-publishing the version already at the mark is allowed: that is
// how a hit refused as stale heals the backend entry.
func (s *CachedStore[S]) publish(ctx context.Context, aggregateID typeid.ID, entry CachedAggregate[S]) error {
	s.mu.Lock()
	if entry.Version < s.freshness[aggregateID.String()] {
		s.mu.Unlock()
		return nil
	}

	s.freshness[aggregateID.String()] = entry.Version
	s.mu.Unlock()

	return s.cache.PutAggregate(ctx, aggregateID, entry)
}

// isFresh reports whether a cached version is at or above the aggregate's
// published high-water mark and may be served.
func (s *CachedStore[S]) isFresh(aggregateID typeid.ID, version int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return version >= s.freshness[aggregateID.String()]
}

// Hydrate hydrates an aggregate by deferring to the inner store.
func (s *CachedStore[S]) Hydrate(ctx context.Context, aggregate *Aggregate[S], opts *HydrateOptions) error {
	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate using the inner store, then updates the cache.
func (s *CachedStore[S]) Save(ctx context.Context, aggregate *Aggregate[S], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: ErrNilAggregate}
	}

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving to inner aggregate store", Err: err}
	}

	// An aggregate saved with SkipApply still has events queued, so its state and version
	// trail what was just persisted. Caching it would serve that trailing state to later
	// loads, so leave the previous entry alone and let the next load repopulate it.
	if len(aggregate.unappliedEvents) > 0 {
		s.log.Debug("skipping cache write for aggregate with unapplied events",
			"aggregate_id", aggregate.ID(),
			"unapplied_events", len(aggregate.unappliedEvents))
		return nil
	}

	if err := s.publish(ctx, aggregate.ID(), CachedAggregate[S]{State: aggregate.State(), Version: aggregate.Version()}); err != nil {
		s.log.Warn("failed to write cache", "aggregate_id", aggregate.ID(), "error", err)
	}

	return nil
}
