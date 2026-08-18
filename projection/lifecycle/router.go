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
// The fold is computed lazily on first use, cached, and advanced
// incrementally from the last folded global position. Refresh advances it on
// demand, and WithRefreshInterval advances it automatically when the cache is
// older than the interval.
type StreamRouter struct {
	events          eventstore.GlobalReader
	refreshInterval time.Duration

	mu          sync.Mutex
	live        map[string]projection.ID
	position    int64
	refreshedAt time.Time
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

	id, ok := r.live[name]
	if !ok {
		return projection.ID{}, fmt.Errorf("%q: %w", name, ErrNoLiveVersion)
	}

	return id, nil
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
// uses. The fold commits only on success: a read, decode, or validation
// failure leaves the cache and cursor untouched, so the next call retries
// from the same position instead of serving a partial fold or silently
// skipping past a malformed event. The caller must hold r.mu.
func (r *StreamRouter) advance(ctx context.Context) error {
	iter, err := r.events.ReadAll(ctx, eventstore.ReadAllOptions{AfterPosition: r.position})
	if err != nil {
		return fmt.Errorf("reading events: %w", err)
	}

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iteratorCloseTimeout)
		defer cancel()

		_ = iter.Close(closeCtx)
	}()

	live := maps.Clone(r.live)
	if live == nil {
		live = map[string]projection.ID{}
	}

	position := r.position

	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			break
		} else if err != nil {
			return fmt.Errorf("reading event: %w", err)
		}

		if cutover, ok, err := decodeCutover(event); err != nil {
			return err
		} else if ok {
			live[cutover.Live.Name] = cutover.Live
		}

		if event.GlobalPosition != nil {
			position = *event.GlobalPosition
		}
	}

	r.live = live
	r.position = position
	r.refreshedAt = time.Now()

	return nil
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
