package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

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
// reach it out of order, and only the backend sees every write. Every write
// operation is therefore conditional on the versions the backend already
// holds, each comparison atomic with its effect, per aggregate — and reads
// carry the same obligation: once a put, reservation, commit, or release
// for an aggregate has completed, no later GetAggregate for that aggregate
// may observe an older view. Reads and writes are linearizable per
// aggregate; a backend that serves reads from replicas must route or gate
// them so this holds.
//
// Fences are two-phase. A reservation is a provisional floor: it blocks
// publications below it while the save that placed it is in flight, and it
// either commits into the aggregate's permanent fence or is released,
// token-checked, without disturbing the committed fence or any concurrent
// reservation. Only committed fences are forever.
//
// A write that returns an error may or may not have taken effect — a
// timeout can outlive its request — and the backend must remain valid
// either way, with every ordering invariant intact. Callers treat an
// errored write as not applied; CachedStore's save protocol relies on the
// reservation failing closed, never open. Because an errored reservation
// may still have taken effect, settlement is idempotent and token-addressed:
// the caller mints the token before reserving and can withdraw by token what
// it cannot confirm was placed. A failed commit or release merely leaves the
// reservation outstanding, which over-blocks — misses, never staleness.
//
// Entries pass by value in both directions: a backend must not retain state
// it was handed, or hand out state it retains, in a way that leaves two
// callers — or a caller and the cache — sharing mutable memory. Serializing
// backends detach as far as their codec honors its output-ownership
// contract (see estoria.StateCodec); in-memory backends must detach
// deliberately.
//
// Eviction is the backend's own policy, under two ordering rules: an
// aggregate's committed fence must outlive the entries it outranks —
// dropping a payload costs a hit, while dropping a fence early re-admits
// the very publication it refused — and an outstanding reservation must not
// be dropped while its save may still commit, or the forgetting re-admits
// publications a pending append is about to outdate. A backend that
// eventually drops everything forgets the aggregate entirely, and the
// ordering guarantee is scoped to that retention.
type AggregateCache[S any] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.ID) (*CachedAggregate[S], error)

	// PutAggregate stores the entry unless the cache holds a newer entry,
	// committed fence, or outstanding reservation for the aggregate: an
	// entry below any of them is dropped without error, having already lost
	// the race at the backend.
	PutAggregate(ctx context.Context, aggregateID typeid.ID, entry CachedAggregate[S]) error

	// ReserveFence places a provisional fence at the given version under the
	// caller-minted token: it evicts a stored entry below that version and
	// blocks every put below it until the reservation is committed or
	// released. Reserving a token already outstanding for the aggregate is
	// idempotent at the same version and an error at a different one — a
	// token names exactly one reservation.
	ReserveFence(ctx context.Context, aggregateID typeid.ID, version int64, token FenceToken) error

	// CommitFence makes the identified reservation permanent, raising the
	// aggregate's committed fence to the reservation's version and consuming
	// the token. Committed fences only rise. Settlement is idempotent: a
	// token with no outstanding reservation is treated as already settled
	// and the call succeeds without effect, so a settlement whose response
	// was lost can be retried.
	CommitFence(ctx context.Context, aggregateID typeid.ID, token FenceToken) error

	// ReleaseFence withdraws exactly the identified reservation, consuming
	// the token: the committed fence and every other outstanding reservation
	// stand, so releasing one save's reservation can never lower a floor a
	// concurrent save established. Idempotent like CommitFence: a token with
	// no outstanding reservation is already settled, and the call succeeds
	// without effect.
	ReleaseFence(ctx context.Context, aggregateID typeid.ID, token FenceToken) error
}

// A FenceToken names one fence reservation at a cache backend, so that
// committing or releasing one reservation cannot touch another. The caller
// mints it — unique per reservation — and the backend treats it as opaque.
// Because the caller knows the token before the reserve call is made, a
// reservation whose reserve call failed ambiguously can still be settled.
type FenceToken string

// fenceSettleTimeout bounds fence settlement — the commit or release that
// resolves a reservation — on a context detached from the caller's.
// Settlement must survive the caller's cancellation, because abandoning it
// leaves the reservation standing to over-block, and the bound keeps a hung
// backend from stranding the completed save instead.
const fenceSettleTimeout = 5 * time.Second

// settleContext returns the context fence settlement runs on: detached from
// the caller's cancellation, bounded by fenceSettleTimeout.
func settleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), fenceSettleTimeout)
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

// commitFence commits the reservation on the settlement context.
func (s *CachedStore[S]) commitFence(ctx context.Context, id typeid.ID, token FenceToken) error {
	sctx, cancel := settleContext(ctx)
	defer cancel()

	return s.cache.CommitFence(sctx, id, token)
}

// releaseFence releases the reservation on the settlement context.
func (s *CachedStore[S]) releaseFence(ctx context.Context, id typeid.ID, token FenceToken) error {
	sctx, cancel := settleContext(ctx)
	defer cancel()

	return s.cache.ReleaseFence(sctx, id, token)
}

// Save persists the aggregate through the inner store behind a fail-closed
// cache protocol. Before anything is appended, a fence is reserved at the
// expected durable tip — the aggregate's version plus its queued events,
// exactly the versions the checked append assigns on success — evicting the
// entry the append is about to outdate and blocking every publication below
// the tip, intermediate versions included, while the append is in flight. A
// reservation that cannot be placed refuses the save with nothing appended;
// the caller's events remain queued for retry. The version arithmetic the
// fence depends on is validated first, before the cache is touched.
//
// The reservation is provisional until the append's outcome is known.
// Success — or a failure carrying ErrEventsAppended, whose events are
// facts — commits it into the cache's permanent floor. A failure carrying
// ErrNoEventsAppended appended nothing, so the reservation is released and
// the floor falls back to committed truth: a failed save cannot leave
// versions the stream never reached permanently outlawed. A failure
// carrying neither marker is an unknown outcome — an append can commit and
// lose its response — and the reservation is left standing: if the events
// are facts the floor is exactly right, and if they are not it over-blocks —
// misses, never staleness — until the stream reaches the reserved tip.
//
// Settlement — the commit or release that resolves a reservation — runs on
// a context detached from the caller's and bounded by fenceSettleTimeout: a
// canceled caller must not abandon a reservation that would then over-block
// indefinitely. The token is minted here, before reserving, so a
// reservation whose reserve call fails ambiguously is withdrawn by its
// token before the save is refused. A settlement that itself fails leaves
// the reservation standing, with the same bounded consequence: misses,
// never staleness, until the stream reaches the reserved version.
//
// Publication after the append runs on the caller's context: a publication
// lost to cancellation costs only a miss, and a cache that honors the
// context cannot block the completed save.
//
// A save with no queued events appends nothing and validates nothing, so it
// is delegated with the cache untouched: without an append there is no
// durable fact to publish and no version to fence ahead of.
//
// A save that leaves the aggregate with queued events (SkipApply) publishes
// no entry — the state trails what was persisted — and needs nothing
// further: the committed fence already stands at the durable tip, and the
// documented discard-and-reload recovery republishes at it.
func (s *CachedStore[S]) Save(ctx context.Context, aggregate *Aggregate[S], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: withSaveOutcome(ErrNoEventsAppended, ErrNilAggregate)}
	}

	if len(aggregate.unsavedEvents) == 0 {
		if err := s.inner.Save(ctx, aggregate, opts); err != nil {
			return SaveError{AggregateID: aggregate.ID(), Operation: "saving to inner aggregate store", Err: err}
		}

		return nil
	}

	// The inner store's version guards, applied before the cache is touched:
	// fence arithmetic over an invalid version would corrupt the ordering the
	// fence exists to protect.
	unsaved := int64(len(aggregate.unsavedEvents))
	if v := aggregate.Version(); v < 0 {
		return SaveError{
			AggregateID: aggregate.ID(),
			Operation:   saveOpValidatingVersion,
			Err:         withSaveOutcome(ErrNoEventsAppended, fmt.Errorf("aggregate version %d is invalid", v)),
		}
	} else if unsaved > math.MaxInt64-v {
		return SaveError{
			AggregateID: aggregate.ID(),
			Operation:   saveOpValidatingVersion,
			Err: withSaveOutcome(ErrNoEventsAppended, fmt.Errorf("cannot append %d events at version %d: aggregate versions end at %d",
				unsaved, v, int64(math.MaxInt64))),
		}
	}

	tokenID, err := uuid.NewV4()
	if err != nil {
		return SaveError{
			AggregateID: aggregate.ID(), Operation: "reserving cache fence before save",
			Err: withSaveOutcome(ErrNoEventsAppended, fmt.Errorf("minting fence token: %w", err)),
		}
	}
	token := FenceToken(tokenID.String())

	if err := s.cache.ReserveFence(ctx, aggregate.ID(), aggregate.Version()+unsaved, token); err != nil {
		// The interface permits an errored reservation to have taken effect;
		// the token was minted here exactly so it can be withdrawn anyway.
		if releaseErr := s.releaseFence(ctx, aggregate.ID(), token); releaseErr != nil {
			s.log.Warn("failed to release cache fence after a refused reservation; if placed, it over-blocks until the stream reaches it",
				"aggregate_id", aggregate.ID(), "error", releaseErr)
		}

		return SaveError{
			AggregateID: aggregate.ID(), Operation: "reserving cache fence before save",
			Err: withSaveOutcome(ErrNoEventsAppended, err),
		}
	}

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		switch {
		case errors.Is(err, ErrEventsAppended):
			// The events are facts at the reserved tip; the fence stands.
			if commitErr := s.commitFence(ctx, aggregate.ID(), token); commitErr != nil {
				s.log.Warn("failed to commit cache fence after an append",
					"aggregate_id", aggregate.ID(), "error", commitErr)
			}
		case errors.Is(err, ErrNoEventsAppended):
			if releaseErr := s.releaseFence(ctx, aggregate.ID(), token); releaseErr != nil {
				s.log.Warn("failed to release cache fence after a failed save",
					"aggregate_id", aggregate.ID(), "error", releaseErr)
			}
		default:
			// Nothing vouches for either outcome: the append may have become
			// durable and lost its response. The reservation stands — misses,
			// never staleness — until the stream reaches the reserved tip.
			s.log.Warn("save failed with an unknown append outcome; cache fence reservation left standing",
				"aggregate_id", aggregate.ID(), "error", err)
		}

		return SaveError{AggregateID: aggregate.ID(), Operation: "saving to inner aggregate store", Err: err}
	}

	if err := s.commitFence(ctx, aggregate.ID(), token); err != nil {
		s.log.Warn("failed to commit cache fence after a save",
			"aggregate_id", aggregate.ID(), "error", err)
	}

	// An aggregate saved with SkipApply still has events queued, so its state
	// and version trail what was just persisted, and there is nothing to
	// publish; the pre-append fence already stands at the durable tip.
	if len(aggregate.unappliedEvents) > 0 {
		return nil
	}

	if err := s.cache.PutAggregate(ctx, aggregate.ID(), CachedAggregate[S]{State: aggregate.State(), Version: aggregate.Version()}); err != nil {
		s.log.Warn("failed to write cache", "aggregate_id", aggregate.ID(), "error", err)
	}

	return nil
}
