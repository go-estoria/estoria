package rebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
)

// ErrNoLiveVersion indicates that no version of the named projection has ever
// been promoted.
var ErrNoLiveVersion = errors.New("no live version")

// A Router answers which version of a named projection serves reads. The
// orchestrator consults it to derive next-version numbers and rollback
// targets; a read-model repository consults it only in logical-cutover
// deployments, where it composes physical storage names per query.
// Physical-cutover deployments — a view repoint or alias swap in a cutover
// hook — never read it.
type Router interface {
	Live(ctx context.Context, name string) (projection.ID, error)
}

// A LiveSetter is the write side of the cutover. Pointer caches (a postgres
// row, a redis key) implement it, and the orchestrator invokes it after a
// promotion or rollback is recorded. The recorded event is authoritative;
// setters are caches of it.
type LiveSetter interface {
	SetLive(ctx context.Context, id projection.ID) error
}

// A MemoryRouter is an in-memory live-version pointer, for tests and for
// single-process deployments where the orchestrator and the query side share
// memory.
type MemoryRouter struct {
	mu   sync.RWMutex
	live map[string]projection.ID
}

// NewMemoryRouter creates a new in-memory router.
func NewMemoryRouter() *MemoryRouter {
	return &MemoryRouter{live: map[string]projection.ID{}}
}

// Live returns the live version of the named projection, or ErrNoLiveVersion.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (r *MemoryRouter) Live(_ context.Context, name string) (projection.ID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id, ok := r.live[name]
	if !ok {
		return projection.ID{}, fmt.Errorf("%q: %w", name, ErrNoLiveVersion)
	}

	return id, nil
}

// SetLive records id as the live version of its projection.
// ctx is accepted for interface compatibility but is not used by this implementation.
func (r *MemoryRouter) SetLive(_ context.Context, id projection.ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.live[id.Name] = id

	return nil
}

var (
	_ Router     = (*MemoryRouter)(nil)
	_ LiveSetter = (*MemoryRouter)(nil)
)

// A StreamRouter derives the live version of every projection from the
// rebuild aggregates' cutover history: Promoted and RolledBack events are the
// authoritative record, and this router is a fold of them. It assumes the
// rebuild store's default JSON domain event codec.
//
// The fold is computed lazily on first use, cached, and advanced
// incrementally from the last folded global position. Refresh advances it on
// demand — in process, wire the orchestrator's cutover hook to Refresh so
// promotions are visible immediately — and WithRefreshInterval advances it
// automatically when the cache is older than the interval.
type StreamRouter struct {
	events          eventstore.GlobalReader
	refreshInterval time.Duration

	mu          sync.Mutex
	live        map[string]projection.ID
	position    int64
	refreshedAt time.Time
}

// NewStreamRouter creates a router that folds the cutover history from the
// store holding the rebuild aggregate streams.
func NewStreamRouter(events eventstore.GlobalReader, opts ...StreamRouterOption) (*StreamRouter, error) {
	if events == nil {
		return nil, errors.New("global event reader is required")
	}

	router := &StreamRouter{events: events}

	for _, opt := range opts {
		opt(router)
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
// the cached live map. The caller must hold r.mu.
func (r *StreamRouter) advance(ctx context.Context) error {
	iter, err := r.events.ReadAll(ctx, eventstore.ReadAllOptions{AfterPosition: r.position})
	if err != nil {
		return fmt.Errorf("reading events: %w", err)
	}

	defer func() {
		_ = iter.Close(context.WithoutCancel(ctx))
	}()

	if r.live == nil {
		r.live = map[string]projection.ID{}
	}

	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			break
		} else if err != nil {
			return fmt.Errorf("reading event: %w", err)
		}

		if event.GlobalPosition != nil {
			r.position = *event.GlobalPosition
		}

		if event.StreamID.Type != StreamType {
			continue
		}

		switch event.ID.Type {
		case Promoted{}.EventType():
			var promoted Promoted
			if err := json.Unmarshal(event.Data, &promoted); err != nil {
				return fmt.Errorf("decoding %s event: %w", event.ID.Type, err)
			}

			r.live[promoted.Next.Name] = promoted.Next
		case RolledBack{}.EventType():
			var rolledBack RolledBack
			if err := json.Unmarshal(event.Data, &rolledBack); err != nil {
				return fmt.Errorf("decoding %s event: %w", event.ID.Type, err)
			}

			r.live[rolledBack.RevertedTo.Name] = rolledBack.RevertedTo
		}
	}

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
