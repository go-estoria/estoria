package aggregatestore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
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

type PreloadHook func(ctx context.Context, id typeid.TypeID) error

type Hook[E estoria.Entity] func(ctx context.Context, aggregate *estoria.Aggregate[E]) error

type HookableAggregateStore[E estoria.Entity] struct {
	store          estoria.AggregateStore[E]
	precreateHooks []PrecreateHook
	preloadHooks   []PreloadHook
	hooks          map[HookStage][]Hook[E]
	log            *slog.Logger
}

var _ estoria.AggregateStore[estoria.Entity] = (*HookableAggregateStore[estoria.Entity])(nil)

func NewHookableAggregateStore[E estoria.Entity](
	inner estoria.AggregateStore[E],
) *HookableAggregateStore[E] {
	return &HookableAggregateStore[E]{
		store:          inner,
		precreateHooks: make([]PrecreateHook, 0),
		preloadHooks:   make([]PreloadHook, 0),
		hooks:          make(map[HookStage][]Hook[E]),
		log:            slog.Default().WithGroup("hookableaggregatestore"),
	}
}

func (s *HookableAggregateStore[E]) AddPrecreateHook(hook PrecreateHook) {
	s.precreateHooks = append(s.precreateHooks, hook)
}

func (s *HookableAggregateStore[E]) AddPreloadHook(hook PreloadHook) {
	s.preloadHooks = append(s.preloadHooks, hook)
}

func (s *HookableAggregateStore[E]) AddHook(stage HookStage, hook Hook[E]) {
	s.hooks[stage] = append(s.hooks[stage], hook)
}

// Allow allows an event type to be used with the aggregate store.
func (s *HookableAggregateStore[E]) AllowEvents(prototypes ...estoria.EntityEventData) {
	s.store.AllowEvents(prototypes...)
}

// NewAggregate creates a new aggregate.
func (s *HookableAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
	s.log.Debug("creating new aggregate")
	for _, hook := range s.precreateHooks {
		if err := hook(); err != nil {
			return nil, fmt.Errorf("precreate hook failed: %w", err)
		}
	}

	aggregate, err := s.store.NewAggregate()
	if err != nil {
		return nil, err
	}

	for _, hook := range s.hooks[AfterCreate] {
		if err := hook(context.Background(), aggregate); err != nil {
			return nil, fmt.Errorf("post-create hook failed: %w", err)
		}
	}

	return aggregate, nil
}

func (s *HookableAggregateStore[E]) Load(ctx context.Context, id typeid.TypeID, opts estoria.LoadAggregateOptions) (*estoria.Aggregate[E], error) {
	s.log.Debug("loading aggregate", "aggregate_id", id)
	for _, hook := range s.preloadHooks {
		if err := hook(ctx, id); err != nil {
			return nil, fmt.Errorf("preload hook failed: %w", err)
		}
	}

	aggregate, err := s.store.Load(ctx, id, opts)
	if err != nil {
		return nil, err
	}

	for _, hook := range s.hooks[AfterLoad] {
		if err := hook(ctx, aggregate); err != nil {
			return nil, fmt.Errorf("post-load hook failed: %w", err)
		}
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *HookableAggregateStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.HydrateAggregateOptions) error {
	s.log.Debug("hydrating aggregate", "aggregate_id", aggregate.ID(), "from_version", aggregate.Version(), "to_version", opts.ToVersion)
	for _, hook := range s.hooks[BeforeHydrate] {
		if err := hook(ctx, aggregate); err != nil {
			return fmt.Errorf("pre-hydrate hook failed: %w", err)
		}
	}

	err := s.store.Hydrate(ctx, aggregate, opts)
	if err != nil {
		return err
	}

	for _, hook := range s.hooks[AfterHydrate] {
		if err := hook(ctx, aggregate); err != nil {
			return fmt.Errorf("post-hydrate hook failed: %w", err)
		}
	}

	return nil
}

// Save saves an aggregate.
func (s *HookableAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.SaveAggregateOptions) error {
	s.log.Debug("saving aggregate", "aggregate_id", aggregate.ID())
	for _, hook := range s.hooks[BeforeSave] {
		if err := hook(ctx, aggregate); err != nil {
			return fmt.Errorf("pre-save hook failed: %w", err)
		}
	}

	if err := s.store.Save(ctx, aggregate, opts); err != nil {
		return err
	}

	for _, hook := range s.hooks[AfterSave] {
		if err := hook(ctx, aggregate); err != nil {
			return fmt.Errorf("post-save hook failed: %w", err)
		}
	}

	return nil
}
