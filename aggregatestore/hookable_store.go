package aggregatestore

import (
	"context"
	"errors"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// HookStage represents a stage in the aggregate store lifecycle where hooks can be executed.
type HookStage int

const (
	AfterLoad HookStage = iota
	BeforeHydrate
	AfterHydrate
	BeforeSave
	AfterSave
)

// A PreloadHook is a hook that runs before an aggregate is loaded.
type PreloadHook func(ctx context.Context, id uuid.UUID) error

// A Hook is a hook that runs at a specific stage in the aggregate store lifecycle.
type Hook[S any] func(ctx context.Context, aggregate *Aggregate[S]) error

// A HookableStore wraps an aggregate store and provides lifecycle hooks for aggregate store operations.
type HookableStore[S any] struct {
	inner        Store[S]
	preloadHooks []PreloadHook
	hooks        map[HookStage][]Hook[S]
	log          estoria.Logger
}

var _ Store[struct{}] = (*HookableStore[struct{}])(nil)

// NewHookableStore creates a new HookableStore.
func NewHookableStore[S any](inner Store[S]) (*HookableStore[S], error) {
	if inner == nil {
		return nil, errors.New("inner store is required")
	}

	return &HookableStore[S]{
		inner: inner,
		hooks: make(map[HookStage][]Hook[S]),
		log:   estoria.GetLogger().WithGroup("hookablestore"),
	}, nil
}

// BeforeLoad adds a hook that runs before an aggregate is loaded.
func (s *HookableStore[S]) BeforeLoad(hooks ...PreloadHook) {
	s.preloadHooks = append(s.preloadHooks, hooks...)
}

// AfterLoad adds a hook that runs after an aggregate is loaded.
func (s *HookableStore[S]) AfterLoad(hooks ...Hook[S]) {
	s.hooks[AfterLoad] = append(s.hooks[AfterLoad], hooks...)
}

// BeforeHydrate adds a hook that runs before an aggregate is hydrated.
func (s *HookableStore[S]) BeforeHydrate(hooks ...Hook[S]) {
	s.hooks[BeforeHydrate] = append(s.hooks[BeforeHydrate], hooks...)
}

// AfterHydrate adds a hook that runs after an aggregate is hydrated.
func (s *HookableStore[S]) AfterHydrate(hooks ...Hook[S]) {
	s.hooks[AfterHydrate] = append(s.hooks[AfterHydrate], hooks...)
}

// BeforeSave adds a hook that runs before an aggregate is saved.
func (s *HookableStore[S]) BeforeSave(hooks ...Hook[S]) {
	s.hooks[BeforeSave] = append(s.hooks[BeforeSave], hooks...)
}

// AfterSave adds a hook that runs after an aggregate is saved.
func (s *HookableStore[S]) AfterSave(hooks ...Hook[S]) {
	s.hooks[AfterSave] = append(s.hooks[AfterSave], hooks...)
}

// AggregateType returns the aggregate type name of the inner store.
func (s *HookableStore[S]) AggregateType() string {
	return s.inner.AggregateType()
}

// New creates a new aggregate with the given ID.
func (s *HookableStore[S]) New(id uuid.UUID) *Aggregate[S] {
	return s.inner.New(id)
}

// Load loads an aggregate by ID, executing any pre- and post-load hooks.
func (s *HookableStore[S]) Load(ctx context.Context, id uuid.UUID, opts *LoadOptions) (*Aggregate[S], error) {
	aggregateID := typeid.New(s.inner.AggregateType(), id)

	s.log.Debug("loading aggregate", "aggregate_id", aggregateID)
	for _, hook := range s.preloadHooks {
		if err := hook(ctx, id); err != nil {
			return nil, LoadError{AggregateID: aggregateID, Operation: "pre-load hook", Err: err}
		}
	}

	aggregate, err := s.inner.Load(ctx, id, opts)
	if err != nil {
		return nil, LoadError{AggregateID: aggregateID, Operation: "loading aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterLoad] {
		if err := hook(ctx, aggregate); err != nil {
			return nil, LoadError{AggregateID: aggregateID, Operation: "post-load hook", Err: err}
		}
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate, executing any pre- and post-hydrate hooks.
func (s *HookableStore[S]) Hydrate(ctx context.Context, aggregate *Aggregate[S], opts *HydrateOptions) error {
	if aggregate == nil {
		return HydrateError{Err: ErrNilAggregate}
	}

	s.log.Debug("hydrating aggregate", "aggregate_id", aggregate.ID())
	for _, hook := range s.hooks[BeforeHydrate] {
		if err := hook(ctx, aggregate); err != nil {
			return HydrateError{AggregateID: aggregate.ID(), Operation: "pre-hydrate hook", Err: err}
		}
	}

	if err := s.inner.Hydrate(ctx, aggregate, opts); err != nil {
		return HydrateError{AggregateID: aggregate.ID(), Operation: "hydrating aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterHydrate] {
		if err := hook(ctx, aggregate); err != nil {
			return HydrateError{AggregateID: aggregate.ID(), Operation: "post-hydrate hook", Err: err}
		}
	}

	return nil
}

// Save saves an aggregate, executing any pre- and post-save hooks. Its
// errors carry the save-outcome markers the Store contract requires: a
// pre-save hook error refuses the save with nothing appended
// (ErrNoEventsAppended), an inner save error passes through with whatever
// markers the inner store attached, and a post-save hook error follows a
// save that succeeded, so it carries ErrEventsAppended — the events are
// facts regardless of what the hook failed to do with them.
func (s *HookableStore[S]) Save(ctx context.Context, aggregate *Aggregate[S], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: withSaveOutcome(ErrNoEventsAppended, ErrNilAggregate)}
	}

	s.log.Debug("saving aggregate", "aggregate_id", aggregate.ID())
	for _, hook := range s.hooks[BeforeSave] {
		if err := hook(ctx, aggregate); err != nil {
			return SaveError{
				AggregateID: aggregate.ID(), Operation: "pre-save hook",
				Err: withSaveOutcome(ErrNoEventsAppended, err),
			}
		}
	}

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterSave] {
		if err := hook(ctx, aggregate); err != nil {
			return SaveError{
				AggregateID: aggregate.ID(), Operation: "post-save hook",
				Err: withSaveOutcome(ErrEventsAppended, err),
			}
		}
	}

	return nil
}
