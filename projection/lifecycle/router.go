package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
)

// ErrNoLiveVersion indicates that no version of the named projection has ever
// been promoted.
var ErrNoLiveVersion = errors.New("no live version")

// iteratorCloseTimeout bounds iterator cleanup: it must survive the caller's
// cancellation without inheriting an unbounded wait from a Close that blocks.
const iteratorCloseTimeout = 5 * time.Second

// A Router answers which version of a named projection serves reads. A
// read-model repository consults it in logical-cutover deployments, where it
// composes physical storage names per query. Physical-cutover deployments —
// a view repoint or alias swap applied by the cutover worker — never read it.
type Router interface {
	Live(ctx context.Context, name string) (projection.ID, error)
}

// A MemoryRouter is an in-memory managed cutover setter and router, for
// tests and for single-process deployments where the cutover worker and the
// query side share memory. Its single mutex is the atomicity the setter
// contract requires: the served route and the stored revision change
// together or not at all.
type MemoryRouter struct {
	mu      sync.RWMutex
	applied map[string]Cutover
}

// NewMemoryRouter creates a new in-memory router.
func NewMemoryRouter() *MemoryRouter {
	return &MemoryRouter{applied: map[string]Cutover{}}
}

// Live returns the live version of the named projection, or ErrNoLiveVersion.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (r *MemoryRouter) Live(_ context.Context, name string) (projection.ID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cutover, ok := r.applied[name]
	if !ok {
		return projection.ID{}, fmt.Errorf("%q: %w", name, ErrNoLiveVersion)
	}

	return cutover.Live, nil
}

// ApplyCutover applies the cutover if it is newer than the one currently
// served, per the CutoverSetter contract: higher revisions apply, the same
// revision must carry the same live version, and lower revisions are stale
// no-ops. The cutover's live version must be valid per projection.ID.Validate
// and its revision positive.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (r *MemoryRouter) ApplyCutover(_ context.Context, cutover Cutover) error {
	if err := cutover.Live.Validate(); err != nil {
		return fmt.Errorf("invalid live version: %w", err)
	}

	if cutover.Revision < 1 {
		return fmt.Errorf("invalid cutover revision %d for %s", cutover.Revision, cutover.Live)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.applied[cutover.Live.Name]

	switch {
	case !ok || cutover.Revision > current.Revision:
		r.applied[cutover.Live.Name] = cutover
	case cutover.Revision == current.Revision && cutover.Live != current.Live:
		return fmt.Errorf("conflicting cutovers claim revision %d of projection %q: serving %s, delivered %s",
			cutover.Revision, cutover.Live.Name, current.Live, cutover.Live)
	default:
		// The same cutover redelivered, or an older one: already converged.
	}

	return nil
}

// AppliedCutover reports the cutover currently serving the named projection's
// reads, or ErrNoLiveVersion if none was ever applied.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (r *MemoryRouter) AppliedCutover(_ context.Context, name string) (Cutover, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cutover, ok := r.applied[name]
	if !ok {
		return Cutover{}, fmt.Errorf("%q: %w", name, ErrNoLiveVersion)
	}

	return cutover, nil
}

var (
	_ Router        = (*MemoryRouter)(nil)
	_ CutoverSetter = (*MemoryRouter)(nil)
)

// A StreamRouter derives the live version of every projection from the
// lifecycle streams' cutover history: Promoted and RolledBack events are the
// authoritative record, and this router is a fold of them. It assumes the
// lifecycle store's default JSON domain event codec.
//
// The fold validates the history it consumes: per name, revisions must
// advance by exactly one from 1, each event's claimed lineage — the version
// it records as previously live — must match the fold, promoted versions
// must exceed every version promoted before (numbers are never reused), and
// a rollback is legal only directly after a promotion that recorded a
// non-zero previous, reverting to exactly that version. The authoritative
// record is one stream per name under optimistic concurrency, so a history
// that skips, repeats, regresses, misreports lineage, or rolls back to a
// version the promotion did not retain is tampered or foreign, and the fold
// fails closed rather than serving last-write-wins.
//
// The fold is computed lazily on first use, cached, and advanced
// incrementally from the last folded global position. Refresh advances it on
// demand, and WithRefreshInterval advances it automatically when the cache is
// older than the interval.
//
// A StreamRouter is deliberately not a RetirementWitness: it re-derives
// routes from the record, so it can only attest what the record says —
// never that the routes actually serving reads have converged on it. With
// the default zero refresh interval its cache may serve a stale route
// indefinitely, so no finite retirement delay over it is safe; retirement
// gating requires witnesses that serve routes, which is what CutoverSetter
// implementations are.
type StreamRouter struct {
	events          eventstore.GlobalReader
	refreshInterval time.Duration

	mu          sync.Mutex
	live        map[string]cutoverFold
	position    int64
	refreshedAt time.Time
}

// cutoverFold is the per-name state the validating fold maintains: the
// cutover serving reads, the sole legal rollback target — the previously
// live version the latest promotion retained, zero when none, which also
// gates whether a rollback may follow at all — and the high-water of
// promoted versions.
type cutoverFold struct {
	current  Cutover
	rollback projection.ID
	promoted int
}

// apply extends the fold with a decoded cutover, or reports why the history
// is not one the lifecycle could have recorded.
func (f cutoverFold) apply(raw rawCutover) (cutoverFold, error) {
	name := raw.cutover.Live.Name

	switch {
	case raw.rollback && f.current.Revision == 0:
		return f, fmt.Errorf("the cutover history for projection %q opens with a rollback", name)
	case raw.rollback && f.rollback == (projection.ID{}):
		return f, fmt.Errorf("the cutover history for projection %q records a rollback with no promotion to revert", name)
	case raw.cutover.Revision != f.current.Revision+1:
		return f, fmt.Errorf("the cutover history for projection %q records revision %d after revision %d",
			name, raw.cutover.Revision, f.current.Revision)
	case raw.from != f.current.Live:
		return f, fmt.Errorf("the cutover history for projection %q records a flip from %s while %s was live",
			name, raw.from, f.current.Live)
	}

	if raw.rollback {
		if raw.cutover.Live != f.rollback {
			return f, fmt.Errorf("the cutover history for projection %q records a rollback to %s; the promotion retained %s",
				name, raw.cutover.Live, f.rollback)
		}

		// Terminal for the attempt: another rollback needs another promotion.
		return cutoverFold{current: raw.cutover, promoted: f.promoted}, nil
	}

	if raw.cutover.Live.Version <= f.promoted {
		return f, fmt.Errorf("the cutover history for projection %q promotes %s at or below the promoted high-water %d: version numbers are never reused",
			name, raw.cutover.Live, f.promoted)
	}

	return cutoverFold{
		current:  raw.cutover,
		rollback: raw.from,
		promoted: raw.cutover.Live.Version,
	}, nil
}

// NewStreamRouter creates a router that folds the cutover history from the
// store holding the lifecycle streams.
func NewStreamRouter(events eventstore.GlobalReader, opts ...StreamRouterOption) (*StreamRouter, error) {
	if events == nil {
		return nil, errors.New("global event reader is required")
	}

	router := &StreamRouter{events: events}

	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("stream router option must not be nil")
		}

		opt(router)
	}

	if router.refreshInterval < 0 {
		return nil, errors.New("refresh interval must not be negative")
	}

	return router, nil
}

// Live returns the live version of the named projection, or ErrNoLiveVersion.
func (r *StreamRouter) Live(ctx context.Context, name string) (projection.ID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stale := r.refreshInterval > 0 && time.Since(r.refreshedAt) > r.refreshInterval
	if r.live == nil || stale {
		if err := r.advance(ctx); err != nil {
			return projection.ID{}, err
		}
	}

	fold, ok := r.live[name]
	if !ok {
		return projection.ID{}, fmt.Errorf("%q: %w", name, ErrNoLiveVersion)
	}

	return fold.current.Live, nil
}

// Refresh advances the fold past any cutover events recorded since the last
// fold.
func (r *StreamRouter) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.advance(ctx)
}

// advance folds cutover events recorded after the last folded position into
// the cached live map, through the same semantic decoder the cutover worker
// uses. The fold commits only on success: a read, decode, validation, or
// iterator-close failure leaves the cache and cursor untouched, so the next
// call retries from the same position instead of serving a partial fold or
// silently skipping past a malformed event. The caller must hold r.mu.
func (r *StreamRouter) advance(ctx context.Context) error {
	live, position, err := r.fold(ctx)
	if err != nil {
		return err
	}

	r.live = live
	r.position = position
	r.refreshedAt = time.Now()

	return nil
}

// fold reads and validates the cutover events after the router's cursor,
// returning the extended live map and cursor. A failed iterator close is a
// failed fold: the iterator cannot vouch for the completeness of what it
// yielded.
func (r *StreamRouter) fold(ctx context.Context) (live map[string]cutoverFold, position int64, err error) {
	iter, err := r.events.ReadAll(ctx, eventstore.ReadAllOptions{AfterPosition: r.position})
	if err != nil {
		return nil, 0, fmt.Errorf("reading events: %w", err)
	}

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iteratorCloseTimeout)
		defer cancel()

		if closeErr := iter.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing event iterator: %w", closeErr))
		}
	}()

	live = maps.Clone(r.live)
	if live == nil {
		live = map[string]cutoverFold{}
	}

	position = r.position

	for {
		event, err := iter.Next(ctx)

		// The end of the stream is clean only when it is the read's whole
		// story: a failure joined with it is a failed read, not a finished
		// one.
		if leavesMatch(err, eventstore.ErrEndOfEventStream) {
			return live, position, nil
		} else if err != nil {
			return nil, 0, fmt.Errorf("reading event: %w", err)
		}

		if raw, ok, err := decodeCutover(event); err != nil {
			return nil, 0, err
		} else if ok {
			next, err := live[raw.cutover.Live.Name].apply(raw)
			if err != nil {
				return nil, 0, err
			}

			live[raw.cutover.Live.Name] = next
		}

		if event.GlobalPosition != nil {
			position = *event.GlobalPosition
		}
	}
}

var _ Router = (*StreamRouter)(nil)

// A StreamRouterOption configures a StreamRouter.
type StreamRouterOption func(*StreamRouter)

// WithRefreshInterval advances the fold on Live calls when the cache is older
// than the interval, for query-side processes that cannot be notified of
// promotions directly.
//
// The default is no automatic refresh: the fold advances only via Refresh.
func WithRefreshInterval(interval time.Duration) StreamRouterOption {
	return func(r *StreamRouter) {
		r.refreshInterval = interval
	}
}
