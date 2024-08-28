package aggregatestore

import (
	"context"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

type HookStage int

const (
	AfterCreate HookStage = iota
	AfterLoad
	BeforeHydrate
	AfterHydrate
	BeforeSave
	AfterSave
)

type PrecreateHook func() error

type PreloadHook func(ctx context.Context, id typeid.UUID) error

type Hook[E estoria.Entity] func(ctx context.Context, aggregate *Aggregate[E]) error

// A HookableStore wraps an aggregate store and provides lifecycle hooks for aggregate store operations.
type HookableStore[E estoria.Entity] struct {
	inner          Store[E]
	precreateHooks []PrecreateHook
	preloadHooks   []PreloadHook
	hooks          map[HookStage][]Hook[E]
	log            *slog.Logger
}

var _ Store[estoria.Entity] = (*HookableStore[estoria.Entity])(nil)

// NewHookableStore creates a new HookableStore.
func NewHookableStore[E estoria.Entity](inner Store[E]) *HookableStore[E] {
	return &HookableStore[E]{
		inner:          inner,
		precreateHooks: make([]PrecreateHook, 0),
		preloadHooks:   make([]PreloadHook, 0),
		hooks:          make(map[HookStage][]Hook[E]),
		log:            slog.Default().WithGroup("hookableaggregatestore"),
	}
}

// AddPrecreateHook adds a pre-create hook.
func (s *HookableStore[E]) AddPrecreateHook(hook PrecreateHook) {
	s.precreateHooks = append(s.precreateHooks, hook)
}

// AddPreloadHook adds a preload hook.
func (s *HookableStore[E]) AddPreloadHook(hook PreloadHook) {
	s.preloadHooks = append(s.preloadHooks, hook)
}

// AddHook adds a lifecycle hook.
func (s *HookableStore[E]) AddHook(stage HookStage, hook Hook[E]) {
	s.hooks[stage] = append(s.hooks[stage], hook)
}

// NewAggregate creates a new aggregate.
func (s *HookableStore[E]) New(id uuid.UUID) (*Aggregate[E], error) {
	s.log.Debug("creating new aggregate")
	for _, hook := range s.precreateHooks {
		if err := hook(); err != nil {
			return nil, CreateAggregateError{Operation: "pre-create hook", Err: err}
		}
	}

	aggregate, err := s.inner.New(id)
	if err != nil {
		return nil, CreateAggregateError{Operation: "creating aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterCreate] {
		if err := hook(context.Background(), aggregate); err != nil {
			return nil, CreateAggregateError{Operation: "post-create hook", Err: err}
		}
	}

	return aggregate, nil
}

// Load loads an aggregate by ID.
func (s *HookableStore[E]) Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (*Aggregate[E], error) {
	s.log.Debug("loading aggregate", "aggregate_id", id)
	for _, hook := range s.preloadHooks {
		if err := hook(ctx, id); err != nil {
			return nil, LoadAggregateError{AggregateID: id, Operation: "pre-load hook", Err: err}
		}
	}

	aggregate, err := s.inner.Load(ctx, id, opts)
	if err != nil {
		return nil, LoadAggregateError{AggregateID: id, Operation: "loading aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterLoad] {
		if err := hook(ctx, aggregate); err != nil {
			return nil, LoadAggregateError{AggregateID: id, Operation: "post-load hook", Err: err}
		}
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *HookableStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateOptions) error {
	s.log.Debug("hydrating aggregate", "aggregate_id", aggregate.ID(), "from_version", aggregate.Version(), "to_version", opts.ToVersion)
	for _, hook := range s.hooks[BeforeHydrate] {
		if err := hook(ctx, aggregate); err != nil {
			return HydrateAggregateError{AggregateID: aggregate.ID(), Operation: "pre-hydrate hook", Err: err}
		}
	}

	err := s.inner.Hydrate(ctx, aggregate, opts)
	if err != nil {
		return HydrateAggregateError{AggregateID: aggregate.ID(), Operation: "hydrating aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterHydrate] {
		if err := hook(ctx, aggregate); err != nil {
			return HydrateAggregateError{AggregateID: aggregate.ID(), Operation: "post-hydrate hook", Err: err}
		}
	}

	return nil
}

// Save saves an aggregate.
func (s *HookableStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error {
	s.log.Debug("saving aggregate", "aggregate_id", aggregate.ID())
	for _, hook := range s.hooks[BeforeSave] {
		if err := hook(ctx, aggregate); err != nil {
			return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "pre-save hook", Err: err}
		}
	}

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "saving aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterSave] {
		if err := hook(ctx, aggregate); err != nil {
			return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "post-save hook", Err: err}
		}
	}

	return nil
}
