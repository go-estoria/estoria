package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	"github.com/go-estoria/estoria/projection/processor"
)

// DefaultWorkerCheckpointID is the checkpoint identity a Worker uses absent
// WithCheckpointIdentity. It is an ordinary projection checkpoint key: do not
// run a projection under the same name, or the two would share progress.
//
//nolint:gochecknoglobals // A fixed default identity; Go cannot declare struct constants.
var DefaultWorkerCheckpointID = projection.ID{Name: "estoria_cutover_effects", Version: 1}

// A Worker converges cutover setters onto the recorded routing truth. It is
// a checkpointed processor folding the lifecycle streams' Promoted and
// RolledBack events in global order, delivering each recorded cutover — the
// live version and its revision — to every registered setter through the
// apply-if-newer contract: ordered, durable, and retried, where an inline
// hook would be none of the three. A failed delivery stops the worker with
// the failed cutover still ahead of its checkpoint, so a restart redelivers
// it: a persistently failing setter blocks later cutovers rather than being
// skipped, because silently skipping a flip is how routing diverges from the
// recorded truth. Redelivery is safe by contract — a setter already at or
// past the delivered revision treats it as a stale no-op.
//
// Run at most one worker per checkpoint identity at a time: workers sharing
// an identity rewind each other's progress, so deliveries repeat
// arbitrarily. The setter contract absorbs the repetition — revisions make
// every replayed delivery a no-op — but shared progress is meaningless as a
// convergence signal.
//
// The worker assumes the lifecycle store's default JSON domain event codec.
// It reads the same global sequence the lifecycle streams are appended to.
type Worker struct {
	processor *processor.Processor
}

// NewWorker creates a Worker that folds cutover events from the store
// holding the lifecycle streams, checkpointing its progress in checkpoints.
// At least one setter must be registered via WithCutoverSetter.
func NewWorker(events eventstore.GlobalReader, checkpoints checkpointstore.Store, opts ...WorkerOption) (*Worker, error) {
	config := workerConfig{checkpoint: DefaultWorkerCheckpointID}

	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("worker option must not be nil")
		}

		opt(&config)
	}

	switch {
	case config.optionErr != nil:
		return nil, config.optionErr
	case len(config.setters) == 0:
		return nil, errors.New("at least one cutover setter is required")
	}

	// Stop-on-error is pinned after the forwarded options: a processor that
	// logged and advanced past a failed delivery would checkpoint beyond a
	// cutover that was never applied, permanently skipping the flip.
	config.processorOptions = append(config.processorOptions, processor.WithContinueOnHandlerError(false))

	proc, err := processor.New(events, checkpoints, config.checkpoint,
		cutoverHandler(config.setters), config.processorOptions...)
	if err != nil {
		return nil, fmt.Errorf("creating processor: %w", err)
	}

	return &Worker{processor: proc}, nil
}

// Run blocks, delivering cutovers: it replays cutover history from the
// worker's checkpoint, then tails the global event sequence. It returns the
// context's error on cancellation and a non-nil error on any failure to
// read, apply, or checkpoint. Run may be called at most once; a stopped
// worker is resumed by creating a new Worker, which picks up from the
// checkpoint.
func (w *Worker) Run(ctx context.Context) error {
	return w.processor.Run(ctx)
}

// cutoverHandler returns the event handler driving the worker's deliveries:
// it decodes each cutover event from the lifecycle streams, validates it,
// and applies it through every registered setter, in registration order. A
// cutover that decodes but fails its own scheme — an invalid live version,
// a non-positive revision, a stream its projection's name does not derive —
// is an error, not a skip: the reserved namespace is a guardrail, and
// setters must not act on infrastructure state that fails it.
func cutoverHandler(setters []CutoverSetter) projection.EventHandlerFunc {
	return func(ctx context.Context, event *eventstore.Event) error {
		cutover, ok, err := decodeCutover(event)
		if err != nil || !ok {
			return err
		}

		for _, setter := range setters {
			if err := setter.ApplyCutover(ctx, cutover); err != nil {
				return fmt.Errorf("applying cutover for %s: %w", cutover.Live, err)
			}
		}

		return nil
	}
}

// decodeCutover decodes a Promoted or RolledBack event into the cutover it
// records — the now-live version and its revision — reporting ok=false for
// events that are not cutovers. A cutover must carry a valid projection ID
// and a positive revision, and it must live on the stream the projection's
// name derives — the same address every lifecycle command writes through.
func decodeCutover(event *eventstore.Event) (Cutover, bool, error) {
	if event.StreamID.Type != StreamType {
		return Cutover{}, false, nil
	}

	var cutover Cutover

	switch event.ID.Type {
	case Promoted{}.EventType():
		var promoted Promoted
		if err := json.Unmarshal(event.Data, &promoted); err != nil {
			return Cutover{}, false, fmt.Errorf("decoding %s event: %w", event.ID.Type, err)
		}

		cutover = Cutover{Live: promoted.Next, Revision: promoted.Revision}
	case RolledBack{}.EventType():
		var rolledBack RolledBack
		if err := json.Unmarshal(event.Data, &rolledBack); err != nil {
			return Cutover{}, false, fmt.Errorf("decoding %s event: %w", event.ID.Type, err)
		}

		cutover = Cutover{Live: rolledBack.RevertedTo, Revision: rolledBack.Revision}
	default:
		return Cutover{}, false, nil
	}

	if err := cutover.Live.Validate(); err != nil {
		return Cutover{}, false, fmt.Errorf("%s event on stream %s records an invalid live version: %w", event.ID.Type, event.StreamID, err)
	}

	if cutover.Revision < 1 {
		return Cutover{}, false, fmt.Errorf("%s event on stream %s records an invalid cutover revision %d",
			event.ID.Type, event.StreamID, cutover.Revision)
	}

	if want := StreamUUID(cutover.Live.Name); event.StreamID.UUID != want {
		return Cutover{}, false, fmt.Errorf("%s event for projection %q on stream %s, want the name-derived stream %s",
			event.ID.Type, cutover.Live.Name, event.StreamID.UUID, want)
	}

	return cutover, true, nil
}

// workerConfig collects a Worker's options before construction.
type workerConfig struct {
	setters          []CutoverSetter
	checkpoint       projection.ID
	processorOptions []processor.Option
	optionErr        error
}

// A WorkerOption configures a Worker.
type WorkerOption func(*workerConfig)

// WithCutoverSetter registers a managed setter to converge on every recorded
// cutover. Repeatable; setters are applied in registration order.
func WithCutoverSetter(setter CutoverSetter) WorkerOption {
	return func(c *workerConfig) {
		if setter == nil {
			c.optionErr = errors.Join(c.optionErr, errors.New("cutover setter must not be nil"))
			return
		}

		c.setters = append(c.setters, setter)
	}
}

// WithCheckpointIdentity sets the projection ID under which the worker
// checkpoints its progress, instead of DefaultWorkerCheckpointID. Give
// workers with different effect sets different identities, and never run two
// workers under one identity at a time.
func WithCheckpointIdentity(id projection.ID) WorkerOption {
	return func(c *workerConfig) {
		c.checkpoint = id
	}
}

// WithWorkerProcessorOptions passes options through to the processor that
// drives the worker. The worker's stop-on-error delivery cannot be disabled:
// WithContinueOnHandlerError is forced off after the forwarded options.
func WithWorkerProcessorOptions(opts ...processor.Option) WorkerOption {
	return func(c *workerConfig) {
		for _, opt := range opts {
			if opt == nil {
				c.optionErr = errors.Join(c.optionErr, errors.New("processor option must not be nil"))
				return
			}
		}

		c.processorOptions = append(c.processorOptions, opts...)
	}
}
