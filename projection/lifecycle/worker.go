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

// An Effect applies one physical consequence of a cutover — repointing a
// view, swapping an alias, updating a pointer cache — for the now-live
// version. Effects must be idempotent: the worker delivers at least once.
type Effect func(ctx context.Context, live projection.ID) error

// A Worker applies cutover effects. It is a checkpointed processor folding
// the lifecycle streams' Promoted and RolledBack events in global order,
// invoking every registered effect with the now-live version — ordered,
// durable, and retried, where an inline hook would be none of the three. A
// failed effect stops the worker with the failed cutover still ahead of its
// checkpoint, so a restart redelivers it: a persistently failing effect
// blocks later cutovers rather than being skipped, because silently skipping
// a flip is how physical storage diverges from the recorded truth.
//
// Run at most one worker per checkpoint identity at a time: two workers
// sharing an identity can interleave competing cutovers out of order and
// rewind each other's progress, leaving physical state divergent from the
// recorded truth.
//
// The worker assumes the lifecycle store's default JSON domain event codec.
// It reads the same global sequence the lifecycle streams are appended to.
type Worker struct {
	processor *processor.Processor
}

// NewWorker creates a Worker that folds cutover events from the store
// holding the lifecycle streams, checkpointing its progress in checkpoints.
// At least one effect must be registered via WithEffect or WithLiveSetter.
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
	case len(config.effects) == 0:
		return nil, errors.New("at least one effect is required")
	}

	// Stop-on-error is pinned after the forwarded options: a processor that
	// logged and advanced past a failed effect would checkpoint beyond a
	// cutover that was never applied, permanently skipping the flip.
	config.processorOptions = append(config.processorOptions, processor.WithContinueOnHandlerError(false))

	proc, err := processor.New(events, checkpoints, config.checkpoint,
		effectHandler(config.effects), config.processorOptions...)
	if err != nil {
		return nil, fmt.Errorf("creating processor: %w", err)
	}

	return &Worker{processor: proc}, nil
}

// Run blocks, applying cutover effects: it replays cutover history from the
// worker's checkpoint, then tails the global event sequence. It returns the
// context's error on cancellation and a non-nil error on any failure to
// read, apply, or checkpoint. Run may be called at most once; a stopped
// worker is resumed by creating a new Worker, which picks up from the
// checkpoint.
func (w *Worker) Run(ctx context.Context) error {
	return w.processor.Run(ctx)
}

// effectHandler returns the event handler driving the worker's effects: it
// decodes each cutover event from the lifecycle streams, validates it, and
// applies every effect, in registration order, with the now-live version. A
// cutover that decodes but is not a valid projection ID on its own name's
// stream is an error, not a skip: the reserved namespace is a guardrail, and
// effects must not act on infrastructure state that fails its own scheme.
func effectHandler(effects []Effect) projection.EventHandlerFunc {
	return func(ctx context.Context, event *eventstore.Event) error {
		live, ok, err := decodeCutover(event)
		if err != nil || !ok {
			return err
		}

		for _, effect := range effects {
			if err := effect(ctx, live); err != nil {
				return fmt.Errorf("applying cutover effect for %s: %w", live, err)
			}
		}

		return nil
	}
}

// decodeCutover decodes a Promoted or RolledBack event into the now-live
// version it records, reporting ok=false for events that are not cutovers. A
// cutover must carry a valid projection ID, and it must live on the stream
// the projection's name derives — the same address every lifecycle command
// writes through.
func decodeCutover(event *eventstore.Event) (projection.ID, bool, error) {
	if event.StreamID.Type != StreamType {
		return projection.ID{}, false, nil
	}

	var live projection.ID

	switch event.ID.Type {
	case Promoted{}.EventType():
		var promoted Promoted
		if err := json.Unmarshal(event.Data, &promoted); err != nil {
			return projection.ID{}, false, fmt.Errorf("decoding %s event: %w", event.ID.Type, err)
		}

		live = promoted.Next
	case RolledBack{}.EventType():
		var rolledBack RolledBack
		if err := json.Unmarshal(event.Data, &rolledBack); err != nil {
			return projection.ID{}, false, fmt.Errorf("decoding %s event: %w", event.ID.Type, err)
		}

		live = rolledBack.RevertedTo
	default:
		return projection.ID{}, false, nil
	}

	if err := live.Validate(); err != nil {
		return projection.ID{}, false, fmt.Errorf("%s event on stream %s records an invalid live version: %w", event.ID.Type, event.StreamID, err)
	}

	if want := StreamUUID(live.Name); event.StreamID.UUID != want {
		return projection.ID{}, false, fmt.Errorf("%s event for projection %q on stream %s, want the name-derived stream %s",
			event.ID.Type, live.Name, event.StreamID.UUID, want)
	}

	return live, true, nil
}

// workerConfig collects a Worker's options before construction.
type workerConfig struct {
	effects          []Effect
	checkpoint       projection.ID
	processorOptions []processor.Option
	optionErr        error
}

// A WorkerOption configures a Worker.
type WorkerOption func(*workerConfig)

// WithEffect registers an effect to apply for every promotion or rollback.
// Repeatable; effects run in registration order.
func WithEffect(effect Effect) WorkerOption {
	return func(c *workerConfig) {
		if effect == nil {
			c.optionErr = errors.Join(c.optionErr, errors.New("effect must not be nil"))
			return
		}

		c.effects = append(c.effects, effect)
	}
}

// WithLiveSetter registers a pointer cache to update for every promotion or
// rollback. Repeatable. Equivalent to WithEffect(setter.SetLive).
func WithLiveSetter(setter LiveSetter) WorkerOption {
	return func(c *workerConfig) {
		if setter == nil {
			c.optionErr = errors.Join(c.optionErr, errors.New("live setter must not be nil"))
			return
		}

		c.effects = append(c.effects, setter.SetLive)
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
