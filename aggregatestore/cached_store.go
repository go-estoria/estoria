package aggregatestore

import (
	"context"
	"errors"

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
//
// The backend is the ordering authority for publications. Writes race: one
// store's concurrent commands, or several stores sharing one backend, can
// reach it out of order, and only the backend sees every write. Both write
// operations are therefore conditional on the versions the backend already
// holds, and each comparison must be atomic with its effect, per aggregate.
// Eviction remains the backend's own policy: dropping an entry or a fence
// forgets history rather than regressing it, re-admitting an out-of-order
// publication only if it arrives after the backend's retention horizon.
type AggregateCache[S any] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.ID) (*CachedAggregate[S], error)

	// PutAggregate stores the entry unless the cache holds a newer entry or
	// fence for the aggregate: an entry below either is dropped without
	// error, having already lost the race at the backend.
	PutAggregate(ctx context.Context, aggregateID typeid.ID, entry CachedAggregate[S]) error

	// FenceAggregate records that versions below the given one are stale
	// even when no newer entry exists to displace them: it evicts any entry
	// below the fence and makes future puts below it no-ops. Fences only
	// rise; fencing below an existing fence or entry changes nothing.
	FenceAggregate(ctx context.Context, aggregateID typeid.ID, version int64) error
}

// CachedStore wraps an aggregate store with an AggregateCache to cache aggregates.
// Publication ordering lives in the cache backend (see AggregateCache): the
// store publishes unconditionally and lets the backend drop whatever arrives
// out of order, so any number of stores can share one backend without any of
// them serving a regressed entry.
type CachedStore[S any] struct {
	inner Store[S]
	cache AggregateCache[S]
	log   estoria.Logger
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
		inner: inner,
		cache: cacher,
		log:   estoria.GetLogger().WithGroup("cachedstore"),
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
	case err == nil && entry != nil:
		return newAggregate(aggregateID, entry.State, entry.Version), nil
	case err != nil:
		s.log.Warn("failed to read cache", "aggregate_id", aggregateID, "error", err)
	default:
		s.log.Debug("aggregate not in cache", "aggregate_id", aggregateID)
	}

	aggregate, err := s.inner.Load(ctx, id, opts)
	if err != nil {
		return nil, LoadError{Operation: "loading from inner aggregate store", Err: err}
	}

	if err := s.cache.PutAggregate(ctx, aggregate.ID(), CachedAggregate[S]{State: aggregate.State(), Version: aggregate.Version()}); err != nil {
		s.log.Warn("failed to write cache", "aggregate_id", aggregateID, "error", err)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate by deferring to the inner store.
func (s *CachedStore[S]) Hydrate(ctx context.Context, aggregate *Aggregate[S], opts *HydrateOptions) error {
	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate using the inner store, then updates the cache. The
// cache write is conditional at the backend, so a publication that loses a
// race cannot regress what a concurrent save published (see AggregateCache).
// A save that advanced the stream without leaving a publishable state — a
// SkipApply save, or a failure after its events were appended — fences the
// cache instead, so the entry it outdated cannot be served while the next
// load repopulates from the inner store.
func (s *CachedStore[S]) Save(ctx context.Context, aggregate *Aggregate[S], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: ErrNilAggregate}
	}

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		// Events carried by ErrEventsAppended are durable facts the cached
		// entry no longer reflects. The exact durable tip is unknowable here —
		// the inner store may be any composition — but it is at least one past
		// the version this aggregate applied, and any entry this save could be
		// racing is at or below that version, so the minimal fence evicts
		// every entry the append outdated without ever refusing an honest
		// republication from the recovery reload.
		if errors.Is(err, ErrEventsAppended) {
			if fenceErr := s.cache.FenceAggregate(ctx, aggregate.ID(), aggregate.Version()+1); fenceErr != nil {
				s.log.Warn("failed to fence cache after a post-append save failure",
					"aggregate_id", aggregate.ID(), "error", fenceErr)
			}
		}

		return SaveError{AggregateID: aggregate.ID(), Operation: "saving to inner aggregate store", Err: err}
	}

	// An aggregate saved with SkipApply still has events queued, so its state
	// and version trail what was just persisted, and there is nothing to
	// publish. The queued events carry their store-assigned versions, so the
	// newest one is the exact durable tip to fence at.
	if n := len(aggregate.unappliedEvents); n > 0 {
		if err := s.cache.FenceAggregate(ctx, aggregate.ID(), aggregate.unappliedEvents[n-1].Version); err != nil {
			s.log.Warn("failed to fence cache after a skip-apply save",
				"aggregate_id", aggregate.ID(), "error", err)
		}

		return nil
	}

	if err := s.cache.PutAggregate(ctx, aggregate.ID(), CachedAggregate[S]{State: aggregate.State(), Version: aggregate.Version()}); err != nil {
		s.log.Warn("failed to write cache", "aggregate_id", aggregate.ID(), "error", err)
	}

	return nil
}
