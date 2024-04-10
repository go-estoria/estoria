package aggregatestore

import (
	"context"
	"fmt"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
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

type PreloadHook func(ctx context.Context, id typeid.AnyID) error

type Hook[E estoria.Entity] func(ctx context.Context, aggregate *estoria.Aggregate[E]) error

type HookableAggregateStore[E estoria.Entity] struct {
	store          AggregateStore[E]
	precreateHooks []PrecreateHook
	preloadHooks   []PreloadHook
	hooks          map[HookStage][]Hook[E]
}

func NewHookableAggregateStore[E estoria.Entity](
	inner AggregateStore[E],
) *HookableAggregateStore[E] {
	return &HookableAggregateStore[E]{
		store:          inner,
		precreateHooks: make([]PrecreateHook, 0),
		preloadHooks:   make([]PreloadHook, 0),
		hooks:          make(map[HookStage][]Hook[E]),
	}
}

func (s *HookableAggregateStore[E]) AddPrecreateHook(stage HookStage, hook PrecreateHook) {
	s.precreateHooks = append(s.precreateHooks, hook)
}

func (s *HookableAggregateStore[E]) AddPreloadHook(stage HookStage, hook PreloadHook) {
	s.preloadHooks = append(s.preloadHooks, hook)
}

func (s *HookableAggregateStore[E]) AddHook(stage HookStage, hook Hook[E]) {
	s.hooks[stage] = append(s.hooks[stage], hook)
}

// Allow allows an event type to be used with the aggregate store.
func (s *HookableAggregateStore[E]) Allow(prototypes ...estoria.EventData) {
	s.store.Allow(prototypes...)
}

// NewAggregate creates a new aggregate.
func (s *HookableAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
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

func (s *HookableAggregateStore[E]) Load(ctx context.Context, id typeid.AnyID, opts estoria.LoadAggregateOptions) (*estoria.Aggregate[E], error) {
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
