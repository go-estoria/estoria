package rebuild

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/gofrs/uuid/v5"
)

// Config carries the collaborators an Orchestrator drives rebuilds with.
type Config struct {
	// Events is the domain event store the projection consumes, via global
	// reads.
	Events eventstore.GlobalReader

	// Checkpoints persists the progress of the versions being built.
	Checkpoints checkpointstore.Store

	// Handler returns the event handler for a projection version. The ID
	// flows through so the handler targets versioned storage; a handler that
	// also implements projection.Teardowner lets the orchestrator remove that
	// storage when a version is retired or an abandoned build is cleaned up.
	Handler func(id projection.ID) (projection.EventHandler, error)

	// Rebuilds is the aggregate store holding rebuild aggregates. NewStore
	// wires one; back it with the domain event store or with separate
	// storage.
	Rebuilds aggregatestore.Store[State]

	// Router answers which version of a projection is live. Begin derives
	// the next version number and the rollback target from it.
	Router Router
}

// An Orchestrator is a process manager for projection rebuilds: it loads a
// rebuild aggregate, decides the next transition, records it, and acts on it.
// Optimistic concurrency on the aggregate stream arbitrates competing
// orchestrators — a conflicting decision surfaces as a version-mismatch error
// from the underlying save, and the loser reloads to observe the transition
// that won.
type Orchestrator struct {
	config           Config
	autoPromote      bool
	hooks            []CutoverHook
	processorOptions []processor.Option
}

// A CutoverHook runs after a promotion or rollback is recorded, receiving the
// now-live version. Physical-cutover deployments repoint a view or swap an
// alias here; pointer caches are registered via WithLiveSetter. The recorded
// event is authoritative — a hook error does not undo the cutover, it reports
// a cache or storage object that still needs the flip applied.
type CutoverHook func(ctx context.Context, live projection.ID) error

// NewOrchestrator creates a new Orchestrator.
func NewOrchestrator(config Config, opts ...OrchestratorOption) (*Orchestrator, error) {
	switch {
	case config.Events == nil:
		return nil, errors.New("global event reader is required")
	case config.Checkpoints == nil:
		return nil, errors.New("checkpoint store is required")
	case config.Handler == nil:
		return nil, errors.New("handler factory is required")
	case config.Rebuilds == nil:
		return nil, errors.New("rebuild aggregate store is required")
	case config.Router == nil:
		return nil, errors.New("router is required")
	}

	orchestrator := &Orchestrator{config: config}

	for _, opt := range opts {
		opt(orchestrator)
	}

	return orchestrator, nil
}

// Begin creates a rebuild for the next version of the named projection: the
// version after the currently live one, or version 1 for a projection that
// has never been live. The rollback target is the live version at creation.
func (o *Orchestrator) Begin(ctx context.Context, name, reason string) (*Rebuild, error) {
	previous, err := o.config.Router.Live(ctx, name)
	if err != nil && !errors.Is(err, ErrNoLiveVersion) {
		return nil, fmt.Errorf("determining live version: %w", err)
	}

	next := projection.ID{Name: name, Version: previous.Version + 1}
	if err := next.Validate(); err != nil {
		return nil, fmt.Errorf("invalid projection ID: %w", err)
	}

	aggregate := o.config.Rebuilds.New(uuid.Must(uuid.NewV4()))
	aggregate.Append(Created{
		Name:     name,
		Next:     next,
		Previous: previous,
		Reason:   reason,
		At:       time.Now(),
	})

	if err := o.config.Rebuilds.Save(ctx, aggregate, nil); err != nil {
		return nil, fmt.Errorf("recording %s: %w", Created{}.EventType(), err)
	}

	return &Rebuild{orchestrator: o, aggregate: aggregate}, nil
}

// Resume loads an existing rebuild and returns a handle to it. Inspect its
// State to decide what to do next: Run continues a created or in-flight
// build; a terminal rebuild can only be read.
func (o *Orchestrator) Resume(ctx context.Context, id uuid.UUID) (*Rebuild, error) {
	aggregate, err := o.config.Rebuilds.Load(ctx, id, nil)
	if err != nil {
		return nil, fmt.Errorf("loading rebuild: %w", err)
	}

	return &Rebuild{orchestrator: o, aggregate: aggregate}, nil
}

// cutover applies the now-live version to every registered hook, joining
// errors: the cutover is already recorded, so each failure identifies a cache
// or storage object that still needs the flip applied.
func (o *Orchestrator) cutover(ctx context.Context, live projection.ID) error {
	var errs []error

	for _, hook := range o.hooks {
		if err := hook(ctx, live); err != nil {
			errs = append(errs, fmt.Errorf("cutover hook: %w", err))
		}
	}

	return errors.Join(errs...)
}

// cleanup best-effort removes a version's storage (when the handler
// implements projection.Teardowner) and its checkpoint.
func (o *Orchestrator) cleanup(ctx context.Context, id projection.ID) error {
	var errs []error

	handler, err := o.config.Handler(id)
	if err != nil {
		errs = append(errs, fmt.Errorf("creating handler for %s: %w", id, err))
	} else if teardowner, ok := handler.(projection.Teardowner); ok {
		if err := teardowner.Teardown(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("tearing down %s: %w", id, err))
		}
	}

	if err := o.config.Checkpoints.Delete(ctx, id); err != nil && !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		errs = append(errs, fmt.Errorf("deleting checkpoint for %s: %w", id, err))
	}

	return errors.Join(errs...)
}

// An OrchestratorOption configures an Orchestrator.
type OrchestratorOption func(*Orchestrator)

// WithAutoPromote sets whether a rebuild promotes automatically when the
// build first catches up to the head of the event sequence.
//
// The default is manual promotion via Promote.
func WithAutoPromote(auto bool) OrchestratorOption {
	return func(o *Orchestrator) {
		o.autoPromote = auto
	}
}

// WithCutoverHook registers a hook to run after every promotion or rollback.
// Repeatable; hooks run in registration order.
func WithCutoverHook(hook CutoverHook) OrchestratorOption {
	return func(o *Orchestrator) {
		o.hooks = append(o.hooks, hook)
	}
}

// WithLiveSetter registers a pointer cache to update after every promotion or
// rollback. Repeatable. Equivalent to WithCutoverHook(setter.SetLive).
func WithLiveSetter(setter LiveSetter) OrchestratorOption {
	return func(o *Orchestrator) {
		o.hooks = append(o.hooks, setter.SetLive)
	}
}

// WithProcessorOptions passes options through to the processors the
// orchestrator runs for versions being built.
func WithProcessorOptions(opts ...processor.Option) OrchestratorOption {
	return func(o *Orchestrator) {
		o.processorOptions = append(o.processorOptions, opts...)
	}
}
