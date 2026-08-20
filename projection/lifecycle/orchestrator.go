package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
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
	// flows through so the handler targets versioned storage. Retiring a
	// version's predecessor requires the predecessor's handler to implement
	// projection.Teardowner, which performs the storage removal. The factory
	// must succeed for versions whose storage is already absent: a retirement
	// interrupted after its teardown re-resolves the handler on repair, so a
	// factory that validates or prepares against the removed storage would
	// wedge the lifecycle in the retiring phase forever. Independent repairs
	// can resolve the same version concurrently — nothing serializes retries
	// across processes — so the factory must tolerate concurrent invocation.
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
	witnesses         map[string]RetirementWitness
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
		witnesses:         map[string]RetirementWitness{},
		log:               estoria.GetLogger().WithGroup("lifecycle"),
	}

	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("orchestrator option must not be nil")
		}

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

	if err := checkLifecycleAggregate(aggregate, name); err != nil {
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

	return &Rebuild{orchestrator: o, name: name, aggregate: aggregate}, nil
}

// Resume loads the named projection's lifecycle and returns a handle to it.
// Inspect its State to decide what to do next: Run continues an in-flight
// build; a projection with no rebuild in flight can only be read. A resumed
// handle cannot promote until it has Run — promotion requires the catch-up
// certification only a running drain establishes — while Rollback, Abandon,
// and Retire are deliberately not runner-scoped.
func (o *Orchestrator) Resume(ctx context.Context, name string) (*Rebuild, error) {
	aggregate, err := o.loadAggregate(ctx, name)
	if err != nil {
		return nil, err
	}

	return &Rebuild{orchestrator: o, name: name, aggregate: aggregate}, nil
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

	return aggregate.State().clone(), nil
}

// SetRetirementPolicy records an audited transition of the named
// projection's retirement policy: the witness IDs that must vouch for the
// live cutover before a retirement destroys storage, or the explicit choice
// to retire unwitnessed. History defines the required policy — a restarted
// process configured with fewer witnesses cannot weaken the gate — and
// every transition carries the actor and reason authorizing it. The witness
// set is canonicalized (sorted, unique) before recording. The projection
// must have a recorded lifecycle; concurrent transitions are arbitrated by
// the lifecycle stream, and the loser's save reports a version mismatch.
func (o *Orchestrator) SetRetirementPolicy(ctx context.Context, name string, change RetirementPolicyChange) error {
	switch {
	case change.Actor == "" || change.Reason == "":
		return errors.New("a retirement policy transition requires an actor and a reason")
	case change.Unwitnessed && len(change.Witnesses) != 0:
		return errors.New("a retirement policy names witnesses or is unwitnessed, not both")
	case !change.Unwitnessed && len(change.Witnesses) == 0:
		return errors.New("a retirement policy names at least one witness or is explicitly unwitnessed")
	}

	witnesses := slices.Clone(change.Witnesses)
	slices.Sort(witnesses)

	if err := invalidWitnessSet(witnesses); err != nil {
		return fmt.Errorf("retirement policy witnesses: %w", err)
	}

	aggregate, err := o.loadAggregate(ctx, name)
	if err != nil {
		return err
	}

	current := aggregate.State().RetirementPolicy.Generation
	if current >= math.MaxInt64-1 {
		return fmt.Errorf("cannot record a retirement policy for projection %q: the policy generation is exhausted", name)
	}

	aggregate.Append(RetirementPolicySet{
		Generation:  current + 1,
		Witnesses:   witnesses,
		Unwitnessed: change.Unwitnessed,
		Reason:      change.Reason,
		Actor:       change.Actor,
		At:          time.Now(),
	})

	if err := o.config.Projections.Save(ctx, aggregate, nil); err != nil {
		if errors.Is(err, aggregatestore.ErrEventsAppended) {
			return fmt.Errorf("retirement policy recorded, but this view is stale; reload before issuing further commands: %w", err)
		}

		return fmt.Errorf("recording retirement policy: %w", err)
	}

	return nil
}

// loadAggregate loads the named projection's lifecycle aggregate and
// sanity-checks it against the name that addressed it.
func (o *Orchestrator) loadAggregate(ctx context.Context, name string) (*aggregatestore.Aggregate[State], error) {
	aggregate, err := o.config.Projections.Load(ctx, StreamUUID(name), nil)
	if err != nil {
		return nil, fmt.Errorf("loading projection lifecycle: %w", err)
	}

	if err := checkLifecycleAggregate(aggregate, name); err != nil {
		return nil, err
	}

	return aggregate, nil
}

// ErrInvalidState reports a refusal to act on a lifecycle aggregate whose
// address, identity, or folded state failed validation. Every such refusal
// wraps it, so fail-closed handling can be asserted with errors.Is instead
// of by matching message text.
var ErrInvalidState = errors.New("invalid lifecycle state")

// checkLifecycleAggregate verifies that the aggregate is a lifecycle
// aggregate at the stream the given projection name derives, that its
// recorded name matches the name that addressed it, and that its folded
// state is structurally sound. A failure means the store is wired with the
// wrong stream type, or the stream holds tampered data — either way, no
// command should act on it, and every refusal wraps ErrInvalidState.
// Handles re-run this after every hydration against the name they were
// addressed by: the folded name is mutable data, and validating relative to
// it alone would let a malformed but internally consistent history swap the
// projection out from under a retained handle.
func checkLifecycleAggregate(aggregate *aggregatestore.Aggregate[State], name string) error {
	id := aggregate.ID()

	if id.Type != StreamType {
		return fmt.Errorf("%w: projection store manages %q streams, want %q; wire it with lifecycle.NewStore", ErrInvalidState, id.Type, StreamType)
	}

	if want := StreamUUID(name); id.UUID != want {
		return fmt.Errorf("%w: lifecycle aggregate at stream %s does not derive from projection %q (want %s)", ErrInvalidState, id.UUID, name, want)
	}

	state := aggregate.State()
	if state.Name != "" && state.Name != name {
		return fmt.Errorf("%w: lifecycle stream addressed by projection %q holds state for %q", ErrInvalidState, name, state.Name)
	}

	if err := state.validate(); err != nil {
		return fmt.Errorf("%w for projection %q: %w", ErrInvalidState, name, err)
	}

	// A lifecycle stream's first event either records the projection name or
	// poisons the fold, so an aggregate that has applied events yet holds
	// clean nameless state can only mean persistence was reset underneath it
	// — a snapshot erasing the fold — and its allocation history cannot be
	// trusted. Snapshot validation rejects such payloads before they are
	// installed, making this arm unreachable through the core snapshotting
	// store; it stays as defense in depth for state installed by
	// infrastructure that does not consult the validator.
	if aggregate.Version() > 0 && state.Name == "" {
		return fmt.Errorf("%w: lifecycle aggregate at version %d holds uninitialized state", ErrInvalidState, aggregate.Version())
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

// WithRetirementWitness registers the implementation for a witness ID the
// durable retirement policy may name. Configuration resolves
// implementations; lifecycle history decides which IDs must vouch — an
// unregistered required ID refuses the retirement rather than weakening it.
func WithRetirementWitness(id string, witness RetirementWitness) OrchestratorOption {
	return func(o *Orchestrator) {
		switch {
		case id == "":
			o.optionErr = errors.Join(o.optionErr, errors.New("retirement witness ID must not be empty"))
		case witness == nil:
			o.optionErr = errors.Join(o.optionErr, fmt.Errorf("retirement witness %q must not be nil", id))
		default:
			if _, exists := o.witnesses[id]; exists {
				o.optionErr = errors.Join(o.optionErr, fmt.Errorf("retirement witness %q registered twice", id))
				return
			}

			o.witnesses[id] = witness
		}
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
