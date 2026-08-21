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
// holds, each comparison atomic with its effect, per aggregate — and reads
// carry the same obligation: once a put or fence for an aggregate has
// completed, no later GetAggregate for that aggregate may observe an older
// view. Reads and writes are linearizable per aggregate; a backend that
// serves reads from replicas must route or gate them so this holds.
//
// A write that returns an error may or may not have taken effect — a
// timeout can outlive its request — and the backend must remain valid
// either way, with every ordering invariant intact. Callers treat an
// errored write as not applied; CachedStore's save protocol relies on the
// fence failing closed, never open.
//
// Entries pass by value in both directions: a backend must not retain state
// it was handed, or hand out state it retains, in a way that leaves two
// callers — or a caller and the cache — sharing mutable memory. Serializing
// backends detach for free; in-memory backends must detach deliberately.
//
// Eviction is the backend's own policy, under one ordering rule: an
// aggregate's fence must outlive the entries it outranks — dropping a
// payload costs a hit, while dropping a fence early re-admits the very
// publication it refused. A backend that eventually drops both forgets the
// aggregate entirely, and the ordering guarantee is scoped to that
// retention.
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
// out of order, so any number of stores can share one backend without
// serving a regressed entry, for as long as the backend retains its
// ordering fences. Saves are fail-closed against the cache — see Save.
//
// The save protocol reads the aggregate's queued events as exactly what the
// inner store will append. A decorator that introduces events during the
// save — a before-save hook that appends, for example — must wrap
// CachedStore rather than sit inside it: between CachedStore and the event
// store, its events are invisible to the fence decision.
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

// Save persists the aggregate through the inner store behind a fail-closed
// cache protocol. Before anything is appended, the cache is fenced one past
// the aggregate's version: the entry the append is about to outdate stops
// being served before the events can become facts, so no cache failure
// after the append can leave it authoritative — every later cache write is
// an optimization whose loss costs a miss, never a stale hit. A fence that
// cannot be raised refuses the save with nothing appended; the caller's
// events remain queued for retry. Cache work after the append runs on the
// caller's context: a publication lost to cancellation costs only a miss,
// and a cache that honors the context cannot block the completed save.
//
// A save with no queued events appends nothing and validates nothing, so it
// is delegated with the cache untouched: without an append there is no
// durable fact to publish and no version to fence ahead of.
//
// A save that leaves the aggregate with queued events (SkipApply) publishes
// no entry — the state trails what was persisted — and instead raises the
// fence to the newest appended version, the exact durable tip. A save that
// fails after its events were appended (ErrEventsAppended) needs nothing
// further: the pre-append fence already outranks every entry the append
// outdated, and the documented discard-and-reload recovery republishes at
// or above it.
func (s *CachedStore[S]) Save(ctx context.Context, aggregate *Aggregate[S], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: ErrNilAggregate}
	}

	if len(aggregate.unsavedEvents) == 0 {
		if err := s.inner.Save(ctx, aggregate, opts); err != nil {
			return SaveError{AggregateID: aggregate.ID(), Operation: "saving to inner aggregate store", Err: err}
		}

		return nil
	}

	if err := s.cache.FenceAggregate(ctx, aggregate.ID(), aggregate.Version()+1); err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "fencing cache before save", Err: err}
	}

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
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
