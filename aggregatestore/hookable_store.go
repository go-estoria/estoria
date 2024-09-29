package aggregatestore

import (
	"context"
	"errors"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

type HookStage int

const (
	AfterNew HookStage = iota
	AfterLoad
	BeforeSave
	AfterSave
)

type PrecreateHook func(id uuid.UUID) error

type PreloadHook func(ctx context.Context, id typeid.UUID) error

type Hook[E estoria.Entity] func(ctx context.Context, aggregate *Aggregate[E]) error

// A HookableStore wraps an aggregate store and provides lifecycle hooks for aggregate store operations.
type HookableStore[E estoria.Entity] struct {
	inner          Store[E]
	precreateHooks []PrecreateHook
	preloadHooks   []PreloadHook
	hooks          map[HookStage][]Hook[E]
	log            estoria.Logger
}

var _ Store[estoria.Entity] = (*HookableStore[estoria.Entity])(nil)

// NewHookableStore creates a new HookableStore.
func NewHookableStore[E estoria.Entity](inner Store[E]) (*HookableStore[E], error) {
	if inner == nil {
		return nil, errors.New("inner store is required")
	}

	return &HookableStore[E]{
		inner: inner,
		hooks: make(map[HookStage][]Hook[E]),
		log:   estoria.GetLogger().WithGroup("hookableaggregatestore"),
	}, nil
}

// BeforeNew adds a hook that runs before a new aggregate is created.
func (s *HookableStore[E]) BeforeNew(hooks ...PrecreateHook) {
	s.precreateHooks = append(s.precreateHooks, hooks...)
}

// AfterNew adds a hook that runs after an aggregate is created.
func (s *HookableStore[E]) AfterNew(hooks ...Hook[E]) {
	s.hooks[AfterNew] = append(s.hooks[AfterNew], hooks...)
}

// BeforeLoad adds a hook that runs before an aggregate is loaded.
func (s *HookableStore[E]) BeforeLoad(hooks ...PreloadHook) {
	s.preloadHooks = append(s.preloadHooks, hooks...)
}

// AfterLoad adds a hook that runs after an aggregate is loaded.
func (s *HookableStore[E]) AfterLoad(hooks ...Hook[E]) {
	s.hooks[AfterLoad] = append(s.hooks[AfterLoad], hooks...)
}

// BeforeSave adds a hook that runs before an aggregate is saved.
func (s *HookableStore[E]) BeforeSave(hooks ...Hook[E]) {
	s.hooks[BeforeSave] = append(s.hooks[BeforeSave], hooks...)
}

// AfterSave adds a hook that runs after an aggregate is saved.
func (s *HookableStore[E]) AfterSave(hooks ...Hook[E]) {
	s.hooks[AfterSave] = append(s.hooks[AfterSave], hooks...)
}

// New creates a new aggregate.
func (s *HookableStore[E]) New(id uuid.UUID) (*Aggregate[E], error) {
	for _, hook := range s.precreateHooks {
		if err := hook(id); err != nil {
			return nil, CreateError{Operation: "pre-create hook", Err: err}
		}
	}

	aggregate, err := s.inner.New(id)
	if err != nil {
		return nil, CreateError{Operation: "creating aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterNew] {
		if err := hook(context.Background(), aggregate); err != nil {
			return nil, CreateError{Operation: "post-create hook", Err: err}
		}
	}

	return aggregate, nil
}

// Load loads an aggregate by ID.
func (s *HookableStore[E]) Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (*Aggregate[E], error) {
	s.log.Debug("loading aggregate", "aggregate_id", id)
	for _, hook := range s.preloadHooks {
		if err := hook(ctx, id); err != nil {
			return nil, LoadError{AggregateID: id, Operation: "pre-load hook", Err: err}
		}
	}

	aggregate, err := s.inner.Load(ctx, id, opts)
	if err != nil {
		return nil, LoadError{AggregateID: id, Operation: "loading aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterLoad] {
		if err := hook(ctx, aggregate); err != nil {
			return nil, LoadError{AggregateID: id, Operation: "post-load hook", Err: err}
		}
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *HookableStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateOptions) error {
	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *HookableStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error {
	s.log.Debug("saving aggregate", "aggregate_id", aggregate.ID())
	for _, hook := range s.hooks[BeforeSave] {
		if err := hook(ctx, aggregate); err != nil {
			return SaveError{AggregateID: aggregate.ID(), Operation: "pre-save hook", Err: err}
		}
	}

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving aggregate using inner store", Err: err}
	}

	for _, hook := range s.hooks[AfterSave] {
		if err := hook(ctx, aggregate); err != nil {
			return SaveError{AggregateID: aggregate.ID(), Operation: "post-save hook", Err: err}
		}
	}

	return nil
}
