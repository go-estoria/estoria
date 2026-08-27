package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/typeid"
)

// untouchableReader counts reads and must not receive any.
type untouchableReader struct{ calls int }

func (r *untouchableReader) ReadAll(context.Context, eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	r.calls++
	return nil, errors.New("the reader must not be touched")
}

type nopSetter struct{}

func (nopSetter) ApplyCutover(context.Context, Cutover) error { return nil }

func (nopSetter) AppliedCutover(context.Context, string) (Cutover, error) {
	return Cutover{}, ErrNoLiveVersion
}

// untouchableSetter records whether it was ever applied.
type untouchableSetter struct{ touched bool }

func (s *untouchableSetter) ApplyCutover(context.Context, Cutover) error {
	s.touched = true
	return nil
}

func (s *untouchableSetter) AppliedCutover(context.Context, string) (Cutover, error) {
	return Cutover{}, ErrNoLiveVersion
}

// stubReader hands out one fixed iterator.
type stubReader struct{ iter eventstore.StreamIterator }

func (r stubReader) ReadAll(context.Context, eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	return r.iter, nil
}

// inHandIterator yields one event, firing a trigger — a cancellation, or a
// wait that outlives a deadline — from inside the yielding Next call.
type inHandIterator struct {
	trigger func()
	event   *eventstore.Event
	served  bool
}

func (i *inHandIterator) Next(context.Context) (*eventstore.Event, error) {
	if i.served {
		return nil, eventstore.ErrEndOfEventStream
	}

	i.served = true
	i.trigger()

	return i.event, nil
}

func (i *inHandIterator) Close(context.Context) error { return nil }

// deadlineTrippedCtx deterministically transitions to DeadlineExceeded when
// tripped, avoiding wall-clock deadlines entirely: the deadline "expires"
// exactly when the fixture says so. Nothing here runs concurrently — the
// drain, the iterator, and the trip all share the test goroutine.
type deadlineTrippedCtx struct {
	context.Context //nolint:containedctx // The type IS a context: embedding is how it implements the interface.
	tripped         bool
}

func (c *deadlineTrippedCtx) Err() error {
	if c.tripped {
		return context.DeadlineExceeded
	}

	return c.Context.Err()
}

func (c *deadlineTrippedCtx) trip() { c.tripped = true }

// promotedEvent builds a well-formed cutover event at the given global
// position.
func promotedEvent(t *testing.T, position int64) *eventstore.Event {
	t.Helper()

	data, err := json.Marshal(Promoted{Next: projection.ID{Name: "orders", Version: 1}, Revision: 1})
	if err != nil {
		t.Fatalf("marshaling promoted event: %v", err)
	}

	return &eventstore.Event{
		ID:             typeid.NewV4(Promoted{}.EventType()),
		StreamID:       typeid.ID{Type: StreamType, UUID: StreamUUID("orders")},
		Data:           data,
		GlobalPosition: &position,
	}
}

// TestDrainCancellationPrecedesTheRead pins the drain's entry check: a drain
// entered with a canceled context issues no read at all.
func TestDrainCancellationPrecedesTheRead(t *testing.T) {
	t.Parallel()

	reader := &untouchableReader{}

	worker, err := NewWorker(reader, WithCutoverSetter(nopSetter{}))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	position, err := worker.drain(ctx, map[string]cutoverFold{}, 7, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	if position != 7 {
		t.Errorf("want the position unmoved at 7, got %d", position)
	}

	if reader.calls != 0 {
		t.Errorf("want no read issued from a canceled drain, got %d", reader.calls)
	}
}

// TestDrainDropsTheEventInHand pins that a cutover read alongside a context
// ending is wholly unprocessed — position unmoved, fold untouched — and
// that the result is exactly the context's own error: not a hard-coded
// cancellation, not the cancellation's cause, not a wrap or join of either.
func TestDrainDropsTheEventInHand(t *testing.T) {
	t.Parallel()

	errCause := errors.New("the root cause")

	for _, tt := range []struct {
		name string
		ctx  func(*testing.T) (context.Context, func())
		want error
	}{
		{
			name: "canceled",
			ctx: func(t *testing.T) (context.Context, func()) {
				t.Helper()

				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)

				return ctx, cancel
			},
			want: context.Canceled,
		},
		{
			name: "canceled with a cause",
			ctx: func(t *testing.T) (context.Context, func()) {
				t.Helper()

				ctx, cancel := context.WithCancelCause(t.Context())
				t.Cleanup(func() { cancel(nil) })

				return ctx, func() { cancel(errCause) }
			},
			want: context.Canceled,
		},
		{
			// The context transitions to DeadlineExceeded from inside the
			// read itself — no wall clock, so the entry check can never
			// preempt the in-hand branch.
			name: "deadline exceeded",
			ctx: func(t *testing.T) (context.Context, func()) {
				t.Helper()

				ctx := &deadlineTrippedCtx{Context: t.Context()}

				return ctx, ctx.trip
			},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, trigger := tt.ctx(t)
			iter := &inHandIterator{trigger: trigger, event: promotedEvent(t, 9)}

			worker, err := NewWorker(stubReader{iter: iter}, WithCutoverSetter(nopSetter{}))
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			live := map[string]cutoverFold{}

			position, err := worker.drain(ctx, live, 3, nil)

			// The row is meaningful only if the read happened: an entry
			// check that preempted the drain would satisfy every assertion
			// below without exercising the in-hand branch.
			if !iter.served {
				t.Fatal("want the read exercised, but the entry check preempted it")
			}

			//nolint:errorlint // Identity is the assertion: a wrapped or joined context error must fail here.
			if err != tt.want {
				t.Fatalf("want exactly the context's error %v, got %v", tt.want, err)
			}

			if position != 3 {
				t.Errorf("want the position unmoved at 3, got %d", position)
			}

			if len(live) != 0 {
				t.Errorf("want the fold untouched, got %d entries", len(live))
			}
		})
	}
}

// TestDeliverCancellationPrecedesEverySetter pins the per-setter check's
// side: a delivery entered with a canceled context applies no setter at
// all, including the first.
func TestDeliverCancellationPrecedesEverySetter(t *testing.T) {
	t.Parallel()

	setter := &untouchableSetter{}

	worker, err := NewWorker(&untouchableReader{}, WithCutoverSetter(setter))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = worker.deliver(ctx, Cutover{Live: projection.ID{Name: "orders", Version: 1}, Revision: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	if setter.touched {
		t.Error("want no setter applied from a canceled delivery")
	}
}

// selfCancelingReader cancels the drain's context from inside ReadAll and
// returns the configured error: the read's result arrives alongside the
// cancellation, exactly the race the whole-error classification governs.
type selfCancelingReader struct {
	cancel func()
	err    error
}

func (r *selfCancelingReader) ReadAll(context.Context, eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	r.cancel()
	return nil, r.err
}

// TestDrainReadAllCancellationProvenance pins the whole-error classification
// at the ReadAll site, mirroring the iterator path: a failure carrying
// nothing but the cancellation folds into exactly the context's own error,
// and an independent read failure racing the cancellation is joined with it
// rather than discarded.
func TestDrainReadAllCancellationProvenance(t *testing.T) {
	t.Parallel()

	t.Run("cancellation-shaped failure is the context's own error", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		reader := &selfCancelingReader{cancel: cancel, err: fmt.Errorf("reader observed: %w", context.Canceled)}

		worker, err := NewWorker(reader, WithCutoverSetter(nopSetter{}))
		if err != nil {
			t.Fatalf("creating worker: %v", err)
		}

		position, err := worker.drain(ctx, map[string]cutoverFold{}, 5, nil)

		//nolint:errorlint // Identity is the assertion: a wrapped or joined context error must fail here.
		if err != context.Canceled {
			t.Fatalf("want exactly the context's error, got %v", err)
		}

		if position != 5 {
			t.Errorf("want the position unmoved at 5, got %d", position)
		}
	})

	t.Run("independent failure racing the cancellation is joined", func(t *testing.T) {
		t.Parallel()

		errRead := errors.New("read refused")

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		reader := &selfCancelingReader{cancel: cancel, err: errRead}

		worker, err := NewWorker(reader, WithCutoverSetter(nopSetter{}))
		if err != nil {
			t.Fatalf("creating worker: %v", err)
		}

		position, err := worker.drain(ctx, map[string]cutoverFold{}, 5, nil)

		if !errors.Is(err, context.Canceled) {
			t.Errorf("want the cancellation kept in the verdict, got %v", err)
		}

		if !errors.Is(err, errRead) {
			t.Errorf("want the independent read failure kept alongside it, got %v", err)
		}

		if position != 5 {
			t.Errorf("want the position unmoved at 5, got %d", position)
		}
	})
}
