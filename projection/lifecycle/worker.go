package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"
	"time"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
)

// defaultWorkerPollInterval is the delay between tail reads once the worker
// is caught up, absent WithPollInterval.
const defaultWorkerPollInterval = time.Second

// A Worker converges cutover setters onto the recorded routing truth. It is
// a stateless fold of the lifecycle streams' cutover history: on every start
// it folds the entire history from the beginning — validating revision and
// lineage continuity per projection, applying nothing — captures the global
// position of that completed read as its high-water mark, applies each
// projection's final cutover through every registered setter in ascending
// name order, signals readiness, and then tails the sequence strictly after
// the mark, folding and delivering each newly recorded cutover. Flips
// superseded before the worker started are never delivered: the worker is a
// convergence mechanism, not a per-event feed.
//
// The worker keeps no durable progress — no checkpoint, no cursor, no state
// shared with any other worker — so any number of workers may run
// concurrently over the same store, and every delivery may repeat: setters
// absorb both through the apply-if-newer contract. Full lifecycle history is
// the flip side of that statelessness: the fold is correct only over the
// complete record, so lifecycle streams must never be truncated.
//
// Any failure stops the worker: a decode or continuity failure means the
// record cannot be trusted, and a failed delivery means a setter has not
// converged — skipping either is how routing diverges from the recorded
// truth. Supervise Run and restart on error by creating a new Worker, which
// refolds from zero.
//
// The worker assumes the lifecycle store's default JSON domain event codec
// and reads the same global sequence the lifecycle streams are appended to,
// relying on the reader's stable-prefix contract: once a completed read has
// observed a position, no later commit introduces an unseen event at or
// below it.
type Worker struct {
	events  eventstore.GlobalReader
	setters []CutoverSetter
	poll    time.Duration

	started atomic.Bool
	ready   chan struct{}
}

// NewWorker creates a Worker that folds cutover events from the store
// holding the lifecycle streams. At least one setter must be registered via
// WithCutoverSetter.
func NewWorker(events eventstore.GlobalReader, opts ...WorkerOption) (*Worker, error) {
	if events == nil {
		return nil, errors.New("global event reader is required")
	}

	config := workerConfig{poll: defaultWorkerPollInterval}

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

	return &Worker{
		events:  events,
		setters: config.setters,
		poll:    config.poll,
		ready:   make(chan struct{}),
	}, nil
}

// Run blocks, converging setters: it folds the recorded cutover history,
// applies each projection's final cutover, signals readiness, and tails the
// global sequence. It returns the context's error on cancellation — honored
// before each read, after each read, before each setter application, and
// before readiness, even when the reader or setters do not observe it; a
// canceled worker issues no further reads, applies no further setters, and
// never signals readiness it has not earned. It returns a non-nil error on
// any read, validation, or delivery failure. Run may be called at most once
// and consumes the worker's single start even when it returns immediately;
// a stopped worker is restarted by creating a new Worker, which refolds
// from zero.
func (w *Worker) Run(ctx context.Context) error {
	if !w.started.CompareAndSwap(false, true) {
		return errors.New("the worker has already run: create a new worker to refold from zero")
	}

	// The initial fold applies nothing: intermediate flips are already
	// superseded, and validation must clear the whole prefix before any
	// setter acts on state derived from it.
	live := map[string]cutoverFold{}

	mark, err := w.drain(ctx, live, 0, nil)
	if err != nil {
		return err
	}

	for _, name := range slices.Sorted(maps.Keys(live)) {
		if err := w.deliver(ctx, live[name].current); err != nil {
			return err
		}
	}

	// A canceled worker must not report itself initialized.
	if err := ctx.Err(); err != nil {
		return err
	}

	close(w.ready)

	for {
		if mark, err = w.drain(ctx, live, mark, w.deliver); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.poll):
		}
	}
}

// Ready returns a channel that is closed once the worker is initialized:
// the stable prefix folded and every setter holding each projection's final
// cutover. It is the worker's readiness signal — health beyond it is
// supervision of Run — and it never closes if initialization fails.
func (w *Worker) Ready() <-chan struct{} { return w.ready }

// drain reads the global sequence strictly after the given position,
// folding every cutover event through the per-name continuity folds and
// handing each accepted cutover to deliver when it is non-nil. It returns
// the last observed global position. Validation precedes delivery: a
// cutover that extends no legal history stops the drain undelivered. A
// failed iterator close is a failed drain — the iterator cannot vouch for
// the completeness of what it yielded. Cancellation is checked before the
// read begins and after every event, even when the reader does not observe
// contexts: a canceled drain issues no read, and an event whose read
// completes alongside cancellation is dropped unprocessed.
func (w *Worker) drain(ctx context.Context, live map[string]cutoverFold, after int64,
	deliver func(context.Context, Cutover) error,
) (position int64, err error) {
	if err := ctx.Err(); err != nil {
		return after, err
	}

	iter, err := w.events.ReadAll(ctx, eventstore.ReadAllOptions{AfterPosition: after})
	if err != nil {
		return after, fmt.Errorf("reading events: %w", err)
	}

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iteratorCloseTimeout)
		defer cancel()

		if closeErr := iter.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing event iterator: %w", closeErr))
		}
	}()

	position = after

	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			return position, nil
		} else if err != nil {
			return position, fmt.Errorf("reading event: %w", err)
		}

		if err := ctx.Err(); err != nil {
			return position, err
		}

		if event.GlobalPosition != nil {
			position = *event.GlobalPosition
		}

		raw, ok, err := decodeCutover(event)
		if err != nil {
			return position, err
		} else if !ok {
			continue
		}

		next, err := live[raw.cutover.Live.Name].apply(raw)
		if err != nil {
			return position, err
		}

		live[raw.cutover.Live.Name] = next

		if deliver == nil {
			continue
		}

		if err := deliver(ctx, raw.cutover); err != nil {
			return position, err
		}
	}
}

// deliver applies one cutover through every registered setter, in
// registration order, checking cancellation before each: a setter that
// observes shutdown stops the fan-out even when the rest do not.
func (w *Worker) deliver(ctx context.Context, cutover Cutover) error {
	for _, setter := range w.setters {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := setter.ApplyCutover(ctx, cutover); err != nil {
			return fmt.Errorf("applying cutover for %s: %w", cutover.Live, err)
		}
	}

	return nil
}

// rawCutover is one decoded cutover event: the flip it records and the
// lineage it claims — the version it reports as previously live, and whether
// it is a rollback — for folds that verify the history's continuity.
type rawCutover struct {
	cutover  Cutover
	from     projection.ID
	rollback bool
}

// decodeCutover decodes a Promoted or RolledBack event into the cutover it
// records — the now-live version, its revision, and its claimed lineage —
// reporting ok=false for events that are not cutovers. A cutover must carry
// a valid projection ID and a positive revision, and it must live on the
// stream the projection's name derives — the same address every lifecycle
// command writes through. The claimed lineage is not validated here: it is
// checked against a fold's own state by folds that maintain one.
func decodeCutover(event *eventstore.Event) (rawCutover, bool, error) {
	if event.StreamID.Type != StreamType {
		return rawCutover{}, false, nil
	}

	var raw rawCutover

	switch event.ID.Type {
	case Promoted{}.EventType():
		var promoted Promoted
		if err := json.Unmarshal(event.Data, &promoted); err != nil {
			return rawCutover{}, false, fmt.Errorf("decoding %s event: %w", event.ID.Type, err)
		}

		raw = rawCutover{cutover: Cutover{Live: promoted.Next, Revision: promoted.Revision}, from: promoted.Previous}
	case RolledBack{}.EventType():
		var rolledBack RolledBack
		if err := json.Unmarshal(event.Data, &rolledBack); err != nil {
			return rawCutover{}, false, fmt.Errorf("decoding %s event: %w", event.ID.Type, err)
		}

		raw = rawCutover{cutover: Cutover{Live: rolledBack.RevertedTo, Revision: rolledBack.Revision}, from: rolledBack.From, rollback: true}
	default:
		return rawCutover{}, false, nil
	}

	if err := raw.cutover.Live.Validate(); err != nil {
		return rawCutover{}, false, fmt.Errorf("%s event on stream %s records an invalid live version: %w", event.ID.Type, event.StreamID, err)
	}

	if raw.cutover.Revision < 1 {
		return rawCutover{}, false, fmt.Errorf("%s event on stream %s records an invalid cutover revision %d",
			event.ID.Type, event.StreamID, raw.cutover.Revision)
	}

	if want := StreamUUID(raw.cutover.Live.Name); event.StreamID.UUID != want {
		return rawCutover{}, false, fmt.Errorf("%s event for projection %q on stream %s, want the name-derived stream %s",
			event.ID.Type, raw.cutover.Live.Name, event.StreamID.UUID, want)
	}

	return raw, true, nil
}

// workerConfig collects a Worker's options before construction.
type workerConfig struct {
	setters   []CutoverSetter
	poll      time.Duration
	optionErr error
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

// WithPollInterval sets the delay between tail reads once the worker is
// caught up. The default is one second.
func WithPollInterval(interval time.Duration) WorkerOption {
	return func(c *workerConfig) {
		if interval <= 0 {
			c.optionErr = errors.Join(c.optionErr, errors.New("poll interval must be positive"))
			return
		}

		c.poll = interval
	}
}
