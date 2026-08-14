package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/gofrs/uuid/v5"
)

// DefaultReconcileInterval is how often a running rebuild rehydrates its
// lifecycle aggregate to observe transitions recorded elsewhere, absent
// WithReconcileInterval.
const DefaultReconcileInterval = 10 * time.Second

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

	// Projections is the aggregate store holding projection lifecycle
	// aggregates. NewStore wires one; back it with the domain event store or
	// with separate storage.
	Projections aggregatestore.Store[State]
}

// An Orchestrator is a process manager for projection rebuilds: it loads a
// projection's lifecycle aggregate, decides the next transition, records it,
// and acts on it. The lifecycle stream is the arbitration domain — every
// competing decision about one projection, from admitting a rebuild to
// promoting, rolling back, or retiring, is arbitrated by optimistic
// concurrency on that one stream. A conflicting decision surfaces as a
// version-mismatch error from the underlying save, and the loser reloads to
// observe the transition that won.
type Orchestrator struct {
	config            Config
	autoPromote       bool
	reconcileInterval time.Duration
	processorOptions  []processor.Option
	log               estoria.Logger

	// optionErr collects invalid options for NewOrchestrator to report, so a
	// nil logger or option fails construction instead of panicking at use.
	optionErr error
}

// NewOrchestrator creates a new Orchestrator.
func NewOrchestrator(config Config, opts ...OrchestratorOption) (*Orchestrator, error) {
	switch {
	case config.Events == nil:
		return nil, errors.New("global event reader is required")
	case config.Checkpoints == nil:
		return nil, errors.New("checkpoint store is required")
	case config.Handler == nil:
		return nil, errors.New("handler factory is required")
	case config.Projections == nil:
		return nil, errors.New("projection aggregate store is required")
	}

	orchestrator := &Orchestrator{
		config:            config,
		reconcileInterval: DefaultReconcileInterval,
		log:               estoria.GetLogger().WithGroup("lifecycle"),
	}

	for _, opt := range opts {
		opt(orchestrator)
	}

	if orchestrator.optionErr != nil {
		return nil, orchestrator.optionErr
	}

	return orchestrator, nil
}

// Begin admits a rebuild of the named projection, allocating the next
// version number: one past the highest ever allocated, or version 1 for a
// projection never rebuilt. Admission and allocation are one append to the
// projection's lifecycle stream, and Begin is non-destructive by
// construction — it consults no cache and cleans nothing up. It refuses a
// projection that already has a rebuild in flight; two concurrent Begins
// conflict on the stream, and the loser's save reports a version mismatch.
//
// An error carrying aggregatestore.ErrEventsAppended means the admission was
// durably recorded even though the save could not observe it; Resume the
// projection by name to obtain a usable handle.
func (o *Orchestrator) Begin(ctx context.Context, name, reason string) (*Rebuild, error) {
	aggregate, err := o.config.Projections.Load(ctx, StreamUUID(name), nil)
	if errors.Is(err, aggregatestore.ErrAggregateNotFound) {
		aggregate = o.config.Projections.New(StreamUUID(name))
	} else if err != nil {
		return nil, fmt.Errorf("loading projection lifecycle: %w", err)
	}

	if err := o.checkAggregate(aggregate, name); err != nil {
		return nil, err
	}

	state := aggregate.State()
	if state.Attempt.Phase != PhaseNone {
		return nil, fmt.Errorf("projection %q already has a rebuild in flight: attempt %s is %s, targeting %s",
			name, state.Attempt.ID, state.Attempt.Phase, state.Attempt.Target)
	}

	target := projection.ID{Name: name, Version: state.Allocated + 1}
	if err := target.Validate(); err != nil {
		return nil, fmt.Errorf("invalid projection ID: %w", err)
	}

	aggregate.Append(RebuildInitiated{
		Attempt:  uuid.Must(uuid.NewV4()),
		Target:   target,
		Previous: state.Live,
		Reason:   reason,
		At:       time.Now(),
	})

	if err := o.config.Projections.Save(ctx, aggregate, nil); err != nil {
		// Discard the failed admission so it cannot ride along with a later
		// save. When the error carries ErrEventsAppended the admission is
		// durable regardless, and resuming by name observes it.
		aggregate.DiscardUnsavedEvents()

		return nil, fmt.Errorf("recording %s: %w", RebuildInitiated{}.EventType(), err)
	}

	return &Rebuild{orchestrator: o, aggregate: aggregate}, nil
}

// Resume loads the named projection's lifecycle and returns a handle to it.
// Inspect its State to decide what to do next: Run continues an in-flight
// build; a projection with no rebuild in flight can only be read.
func (o *Orchestrator) Resume(ctx context.Context, name string) (*Rebuild, error) {
	aggregate, err := o.loadAggregate(ctx, name)
	if err != nil {
		return nil, err
	}

	return &Rebuild{orchestrator: o, aggregate: aggregate}, nil
}

// Get returns the named projection's lifecycle state: the live version, the
// allocation high-water mark, and the rebuild attempt in flight, if any. The
// error carries aggregatestore.ErrAggregateNotFound for a projection that
// has never had a rebuild.
func (o *Orchestrator) Get(ctx context.Context, name string) (State, error) {
	aggregate, err := o.loadAggregate(ctx, name)
	if err != nil {
		return State{}, err
	}

	return aggregate.State(), nil
}

// loadAggregate loads the named projection's lifecycle aggregate and
// sanity-checks it against the name that addressed it.
func (o *Orchestrator) loadAggregate(ctx context.Context, name string) (*aggregatestore.Aggregate[State], error) {
	aggregate, err := o.config.Projections.Load(ctx, StreamUUID(name), nil)
	if err != nil {
		return nil, fmt.Errorf("loading projection lifecycle: %w", err)
	}

	if err := o.checkAggregate(aggregate, name); err != nil {
		return nil, err
	}

	return aggregate, nil
}

// checkAggregate verifies that the aggregate is a lifecycle aggregate and
// that its recorded name matches the name that addressed it. A mismatch
// means the store is wired with the wrong stream type, or the stream holds
// tampered data — either way, no command should act on it.
func (o *Orchestrator) checkAggregate(aggregate *aggregatestore.Aggregate[State], name string) error {
	if streamType := aggregate.ID().Type; streamType != StreamType {
		return fmt.Errorf("projection store manages %q streams, want %q; wire it with lifecycle.NewStore", streamType, StreamType)
	}

	if got := aggregate.State().Name; got != "" && got != name {
		return fmt.Errorf("lifecycle stream addressed by projection %q holds state for %q", name, got)
	}

	return nil
}

// cleanup removes a version's storage (when the handler implements
// projection.Teardowner) and then its checkpoint. The checkpoint goes last,
// and only after the storage cleanup succeeded: it is the durable marker
// that a build of this identity existed, so it must outlive any failure to
// remove the storage it marks — residue stays enumerable rather than
// becoming invisible while still present.
func (o *Orchestrator) cleanup(ctx context.Context, id projection.ID) error {
	handler, err := o.config.Handler(id)
	if err != nil {
		return fmt.Errorf("creating handler for %s: %w", id, err)
	}

	if teardowner, ok := handler.(projection.Teardowner); ok {
		if err := teardowner.Teardown(ctx, id); err != nil {
			return fmt.Errorf("tearing down %s: %w", id, err)
		}
	}

	if err := o.config.Checkpoints.Delete(ctx, id); err != nil && !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		return fmt.Errorf("deleting checkpoint for %s: %w", id, err)
	}

	return nil
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

// WithProcessorOptions passes options through to the processors the
// orchestrator runs for versions being built.
func WithProcessorOptions(opts ...processor.Option) OrchestratorOption {
	return func(o *Orchestrator) {
		for _, opt := range opts {
			if opt == nil {
				o.optionErr = errors.Join(o.optionErr, errors.New("processor option must not be nil"))
				return
			}
		}

		o.processorOptions = append(o.processorOptions, opts...)
	}
}

// WithReconcileInterval sets how often a running rebuild rehydrates its
// lifecycle aggregate to observe transitions recorded elsewhere, stopping
// its processor once the attempt it is building is no longer in flight.
//
// The default is DefaultReconcileInterval.
func WithReconcileInterval(interval time.Duration) OrchestratorOption {
	return func(o *Orchestrator) {
		if interval <= 0 {
			o.optionErr = errors.Join(o.optionErr, errors.New("reconcile interval must be positive"))
			return
		}

		o.reconcileInterval = interval
	}
}

// WithLogger sets the logger for the Orchestrator.
func WithLogger(log estoria.Logger) OrchestratorOption {
	return func(o *Orchestrator) {
		if log == nil {
			o.optionErr = errors.Join(o.optionErr, errors.New("logger must not be nil"))
			return
		}

		o.log = log
	}
}
