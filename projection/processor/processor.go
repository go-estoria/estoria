// Package processor provides the continuous projection runtime: replay from
// the checkpoint, drain to the head of the store's global event sequence, then
// tail it by polling.
//
// Delivery is at least once: the handler is invoked before the checkpoint is
// saved, so a crash between the two redelivers the event on restart. Handlers
// must be idempotent. Handlers should not perform external side effects (use
// an outbox): under versioned rebuilds every handler replays history at some
// point, so "am I replaying?" is not a meaningful question.
package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
)

// DefaultPollInterval is the delay between drain cycles once the processor is
// at the head of the event sequence, absent WithPollInterval.
const DefaultPollInterval = time.Second

// iteratorCloseTimeout bounds iterator cleanup: it must survive the caller's
// cancellation without inheriting an unbounded wait from a Close that blocks.
const iteratorCloseTimeout = 5 * time.Second

// A Processor drives one projection version against the global event
// sequence. It resumes from the projection's checkpoint, applies each event
// to the handler, and checkpoints its progress; once it reaches the head it
// polls for new events at a fixed interval, re-saving the checkpoint each
// idle cycle so checkpoint recency doubles as a liveness signal.
type Processor struct {
	events      eventstore.GlobalReader
	checkpoints checkpointstore.Store
	id          projection.ID
	handler     projection.EventHandler

	pollInterval           time.Duration
	batchSize              int64
	checkpointEvery        int
	continueOnHandlerError bool
	log                    estoria.Logger

	position         atomic.Int64
	caughtUpPosition atomic.Int64
	caughtUp         chan struct{}
	caughtUpOnce     sync.Once
	running          atomic.Bool

	// sinceCheckpoint counts handled events since the last checkpoint save.
	// It lives on the Processor rather than in drain so the cadence carries
	// across batch-limited reads; only Run's goroutine touches it.
	sinceCheckpoint int
}

// New creates a new Processor for the given projection ID, which must be
// valid per projection.ID.Validate.
func New(
	events eventstore.GlobalReader,
	checkpoints checkpointstore.Store,
	id projection.ID,
	handler projection.EventHandler,
	opts ...Option,
) (*Processor, error) {
	switch {
	case events == nil:
		return nil, errors.New("global event reader is required")
	case checkpoints == nil:
		return nil, errors.New("checkpoint store is required")
	case handler == nil:
		return nil, errors.New("event handler is required")
	}

	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("invalid projection ID: %w", err)
	}

	processor := &Processor{
		events:          events,
		checkpoints:     checkpoints,
		id:              id,
		handler:         handler,
		pollInterval:    DefaultPollInterval,
		checkpointEvery: 1,
		caughtUp:        make(chan struct{}),
		log:             estoria.GetLogger().WithGroup("processor"),
	}

	for _, opt := range opts {
		opt(processor)
	}

	switch {
	case processor.pollInterval <= 0:
		return nil, errors.New("poll interval must be positive")
	case processor.batchSize < 0:
		return nil, errors.New("batch size must not be negative")
	case processor.checkpointEvery < 1:
		return nil, errors.New("checkpoint interval must be positive")
	case processor.log == nil:
		return nil, errors.New("logger must not be nil")
	}

	return processor, nil
}

// Run blocks, driving the projection: it loads the checkpoint (a projection
// that has none starts from the beginning), drains to the head of the event
// sequence, then polls at the configured interval. It returns the context's
// error on cancellation, and a non-nil error on any failure to read, handle
// (absent WithContinueOnHandlerError), or checkpoint an event. Run may be
// called at most once; a crashed or stopped projection is resumed by creating
// a new Processor, which picks up from the checkpoint.
func (p *Processor) Run(ctx context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return errors.New("processor has already been run")
	}

	position, err := p.loadPosition(ctx)
	if err != nil {
		return err
	}

	p.position.Store(position)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		atHead, err := p.drain(ctx)
		if err != nil {
			return err
		}

		// The batch limit ended the cycle, so more events may already be
		// available: read again without signaling or sleeping.
		if !atHead {
			continue
		}

		// The idle touch: re-save an unchanged position so UpdatedAt stays
		// fresh, making checkpoint recency a liveness signal. It precedes the
		// caught-up signal because the signal gates promotion decisions: the
		// head position must be durable before anyone acts on being caught up.
		if err := p.saveCheckpoint(ctx); err != nil {
			return err
		}

		p.caughtUpOnce.Do(func() {
			p.caughtUpPosition.Store(p.position.Load())
			close(p.caughtUp)
		})

		timer := time.NewTimer(p.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// CaughtUp returns a channel that is closed the first time a drain cycle
// reaches the head of the event sequence with the head position durably
// checkpointed.
func (p *Processor) CaughtUp() <-chan struct{} {
	return p.caughtUp
}

// CaughtUpPosition reports the global position at which the processor first
// caught up — the position the CaughtUp closure certified as durably
// checkpointed. It is 0 until CaughtUp is closed and never changes afterward,
// unlike Position, which keeps advancing as the processor tails.
func (p *Processor) CaughtUpPosition() int64 {
	return p.caughtUpPosition.Load()
}

// Position reports the global position of the last event the processor
// handled, or the checkpointed position it resumed from before any. The saved
// checkpoint may trail it between saves when WithCheckpointEvery is above 1.
func (p *Processor) Position() int64 {
	return p.position.Load()
}

// drain reads and handles events from the current position until the iterator
// is exhausted, reporting whether the cycle reached the head of the event
// sequence rather than the batch size limit.
func (p *Processor) drain(ctx context.Context) (bool, error) {
	iter, err := p.events.ReadAll(ctx, eventstore.ReadAllOptions{
		AfterPosition: p.position.Load(),
		Count:         p.batchSize,
	})
	if err != nil {
		return false, fmt.Errorf("reading global event sequence: %w", err)
	}

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), iteratorCloseTimeout)
		defer cancel()

		if err := iter.Close(closeCtx); err != nil {
			p.log.Error("closing event iterator", "projection_id", p.id, "error", err)
		}
	}()

	var yielded int64

	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			break
		} else if err != nil {
			return false, fmt.Errorf("reading event: %w", err)
		}

		if event.GlobalPosition == nil {
			return false, fmt.Errorf("event %s has no global position; the global reader contract requires one", event.ID)
		}

		yielded++

		if err := p.handler.Handle(ctx, event); err != nil {
			if !p.continueOnHandlerError {
				return false, fmt.Errorf("handling event: %w", err)
			}

			// The failed event is advanced past and checkpointed with the
			// rest of the batch: it will not be redelivered on restart.
			p.log.Error("error handling event", "projection_id", p.id, "event_id", event.ID, "global_position", *event.GlobalPosition, "error", err)
		}

		p.position.Store(*event.GlobalPosition)

		p.sinceCheckpoint++
		if p.sinceCheckpoint >= p.checkpointEvery {
			if err := p.saveCheckpoint(ctx); err != nil {
				return false, err
			}
		}
	}

	return p.batchSize == 0 || yielded < p.batchSize, nil
}

// loadPosition returns the checkpointed position to resume after, or 0 for a
// projection that has never checkpointed.
func (p *Processor) loadPosition(ctx context.Context) (int64, error) {
	checkpoint, err := p.checkpoints.Load(ctx, p.id)
	if errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		return 0, nil
	} else if err != nil {
		return 0, fmt.Errorf("loading checkpoint: %w", err)
	}

	return checkpoint.Position, nil
}

// saveCheckpoint saves the current position and, on success, resets the
// cadence counter: any save means zero events are pending checkpointing.
func (p *Processor) saveCheckpoint(ctx context.Context) error {
	if err := p.checkpoints.Save(ctx, p.id, p.position.Load()); err != nil {
		return fmt.Errorf("saving checkpoint: %w", err)
	}

	p.sinceCheckpoint = 0

	return nil
}

// An Option configures a Processor.
type Option func(*Processor)

// WithBatchSize limits how many events each read requests, as
// eventstore.ReadAllOptions.Count. The default of 0 reads without limit, so a
// single drain cycle spans the entire backlog.
func WithBatchSize(size int64) Option {
	return func(p *Processor) {
		p.batchSize = size
	}
}

// WithCheckpointEvery saves the checkpoint every n handled events instead of
// after every event. Raising it trades checkpoint write volume against a wider
// at-least-once redelivery window on crash. The cadence counts across
// batch-limited reads, and reaching the head of the event sequence always
// saves regardless of n.
func WithCheckpointEvery(n int) Option {
	return func(p *Processor) {
		p.checkpointEvery = n
	}
}

// WithContinueOnHandlerError sets whether to continue past events whose
// handling fails, rather than stopping. A failed event is logged, advanced
// past, and checkpointed with the rest of its batch, so it is not redelivered
// on restart.
//
// The default behavior is to stop, leaving the failed event before the
// checkpoint so a restart redelivers it.
func WithContinueOnHandlerError(shouldContinue bool) Option {
	return func(p *Processor) {
		p.continueOnHandlerError = shouldContinue
	}
}

// WithPollInterval sets the fixed delay between drain cycles once the
// processor is at the head of the event sequence.
//
// The default is DefaultPollInterval.
func WithPollInterval(interval time.Duration) Option {
	return func(p *Processor) {
		p.pollInterval = interval
	}
}

// WithLogger sets the logger for the Processor.
func WithLogger(log estoria.Logger) Option {
	return func(p *Processor) {
		p.log = log
	}
}
