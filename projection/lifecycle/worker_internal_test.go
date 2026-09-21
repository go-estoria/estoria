package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// scriptedIterator yields its events, then one terminal result, and records
// whether Close has completed so setters can assert the iterator lifecycle.
type scriptedIterator struct {
	events      []*eventstore.Event
	terminalErr error
	closeErr    error
	onClose     func()

	next int
	open bool
}

func newScriptedIterator(events ...*eventstore.Event) *scriptedIterator {
	return &scriptedIterator{events: events, open: true}
}

func (i *scriptedIterator) Next(context.Context) (*eventstore.Event, error) {
	if i.next < len(i.events) {
		event := i.events[i.next]
		i.next++

		return event, nil
	}

	if i.terminalErr != nil {
		return nil, i.terminalErr
	}

	return nil, eventstore.ErrEndOfEventStream
}

func (i *scriptedIterator) Close(context.Context) error {
	i.open = false

	if i.onClose != nil {
		i.onClose()
	}

	return i.closeErr
}

type callbackSetter struct {
	apply func(context.Context, Cutover) error
}

func (s callbackSetter) ApplyCutover(ctx context.Context, cutover Cutover) error {
	return s.apply(ctx, cutover)
}

func (callbackSetter) AppliedCutover(context.Context, string) (Cutover, error) {
	return Cutover{}, ErrNoLiveVersion
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

	return promotedEventWith(t, position, Promoted{Next: projection.ID{Name: "orders", Version: 1}, Revision: 1})
}

func promotedEventWith(t *testing.T, position int64, promoted Promoted) *eventstore.Event {
	t.Helper()

	data, err := json.Marshal(promoted)
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

func TestDrainTailDeliveryWaitsForIteratorClose(t *testing.T) {
	t.Parallel()

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}
	iter := newScriptedIterator(
		promotedEvent(t, 9),
		promotedEventWith(t, 10, Promoted{Previous: ordersV1, Next: ordersV2, Revision: 2}),
	)
	var applied []Cutover

	setter := callbackSetter{apply: func(_ context.Context, cutover Cutover) error {
		if iter.open {
			t.Error("setter ran while the iterator was open")
		}

		applied = append(applied, cutover)

		return nil
	}}

	worker, err := NewWorker(stubReader{iter: iter}, WithCutoverSetter(setter))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	live := map[string]cutoverFold{}
	position, err := worker.drain(t.Context(), live, 3, worker.deliver)
	if err != nil {
		t.Fatalf("draining tail: %v", err)
	}

	if iter.open {
		t.Error("want the iterator closed before drain returned")
	}

	if position != 10 {
		t.Errorf("want position 10, got %d", position)
	}

	want := []Cutover{
		{Live: ordersV1, Revision: 1},
		{Live: ordersV2, Revision: 2},
	}
	if len(applied) != len(want) || applied[0] != want[0] || applied[1] != want[1] {
		t.Errorf("want deliveries %v in order after close, got %v", want, applied)
	}

	if got := live["orders"].current; got != want[1] {
		t.Errorf("want the final accepted cutover folded as %+v, got %+v", want[1], got)
	}
}

func TestDrainTailCloseFailurePreventsBufferedDelivery(t *testing.T) {
	t.Parallel()

	errClose := errors.New("close failed")
	iter := newScriptedIterator(promotedEvent(t, 9))
	iter.closeErr = errClose

	setter := &untouchableSetter{}
	worker, err := NewWorker(stubReader{iter: iter}, WithCutoverSetter(setter))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	live := map[string]cutoverFold{}
	position, err := worker.drain(t.Context(), live, 3, worker.deliver)
	if !errors.Is(err, errClose) {
		t.Fatalf("want the close failure, got %v", err)
	}

	if setter.touched {
		t.Error("want no delivery from an iterator that failed to close")
	}

	if position != 9 {
		t.Errorf("want position 9, got %d", position)
	}

	if got := live["orders"].current.Revision; got != 1 {
		t.Errorf("want the accepted cutover retained in fold state, got revision %d", got)
	}
}

func TestDrainTailReadAndCloseFailuresPreventBufferedDelivery(t *testing.T) {
	t.Parallel()

	errRead := errors.New("read failed")
	errClose := errors.New("close failed")
	iter := newScriptedIterator(promotedEvent(t, 9))
	iter.terminalErr = errRead
	iter.closeErr = errClose

	setter := &untouchableSetter{}
	worker, err := NewWorker(stubReader{iter: iter}, WithCutoverSetter(setter))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	_, err = worker.drain(t.Context(), map[string]cutoverFold{}, 3, worker.deliver)
	if !errors.Is(err, errRead) || !errors.Is(err, errClose) {
		t.Fatalf("want the read and close failures joined, got %v", err)
	}

	if setter.touched {
		t.Error("want no buffered delivery after the close failure")
	}
}

func TestDrainTailValidationAndCloseFailuresPreventBufferedDelivery(t *testing.T) {
	t.Parallel()

	errClose := errors.New("close failed")
	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}
	iter := newScriptedIterator(
		promotedEvent(t, 9),
		promotedEventWith(t, 10, Promoted{Previous: ordersV1, Next: ordersV2, Revision: 5}),
	)
	iter.closeErr = errClose

	setter := &untouchableSetter{}
	worker, err := NewWorker(stubReader{iter: iter}, WithCutoverSetter(setter))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	_, err = worker.drain(t.Context(), map[string]cutoverFold{}, 3, worker.deliver)
	if !errors.Is(err, errClose) || !strings.Contains(err.Error(), "records revision 5 after revision 1") {
		t.Fatalf("want the validation and close failures joined, got %v", err)
	}

	if setter.touched {
		t.Error("want no buffered delivery after the close failure")
	}
}

func TestDrainTailDeliversAcceptedPrefixBeforeLaterFailure(t *testing.T) {
	t.Parallel()

	errRead := errors.New("read failed")

	for _, tt := range []struct {
		name         string
		iterator     func(*testing.T) *scriptedIterator
		wantErr      error
		wantContains string
		wantPosition int64
	}{
		{
			name: "read failure",
			iterator: func(t *testing.T) *scriptedIterator {
				t.Helper()

				iter := newScriptedIterator(promotedEvent(t, 9))
				iter.terminalErr = errRead

				return iter
			},
			wantErr:      errRead,
			wantPosition: 9,
		},
		{
			name: "validation failure",
			iterator: func(t *testing.T) *scriptedIterator {
				t.Helper()

				return newScriptedIterator(promotedEvent(t, 9), promotedEvent(t, 10))
			},
			wantContains: "records revision 1 after revision 1",
			wantPosition: 10,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			iter := tt.iterator(t)
			var applied []Cutover

			setter := callbackSetter{apply: func(_ context.Context, cutover Cutover) error {
				if iter.open {
					t.Error("setter ran while the iterator was open")
				}

				applied = append(applied, cutover)

				return nil
			}}

			worker, err := NewWorker(stubReader{iter: iter}, WithCutoverSetter(setter))
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			position, err := worker.drain(t.Context(), map[string]cutoverFold{}, 3, worker.deliver)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("want failure %v after prefix delivery, got %v", tt.wantErr, err)
			}

			if tt.wantContains != "" && (err == nil || !strings.Contains(err.Error(), tt.wantContains)) {
				t.Fatalf("want failure containing %q after prefix delivery, got %v", tt.wantContains, err)
			}

			if position != tt.wantPosition {
				t.Errorf("want position %d, got %d", tt.wantPosition, position)
			}

			want := Cutover{Live: projection.ID{Name: "orders", Version: 1}, Revision: 1}
			if len(applied) != 1 || applied[0] != want {
				t.Errorf("want accepted prefix %v delivered after close, got %v", want, applied)
			}
		})
	}
}

func TestDrainTailDeliveryFailurePrecedesLaterReadFailure(t *testing.T) {
	t.Parallel()

	errRead := errors.New("read failed")
	errDelivery := errors.New("delivery failed")
	iter := newScriptedIterator(promotedEvent(t, 9))
	iter.terminalErr = errRead

	setter := callbackSetter{apply: func(context.Context, Cutover) error {
		if iter.open {
			t.Error("setter ran while the iterator was open")
		}

		return errDelivery
	}}

	worker, err := NewWorker(stubReader{iter: iter}, WithCutoverSetter(setter))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	_, err = worker.drain(t.Context(), map[string]cutoverFold{}, 3, worker.deliver)
	if !errors.Is(err, errDelivery) {
		t.Fatalf("want the delivery failure, got %v", err)
	}

	if errors.Is(err, errRead) {
		t.Fatalf("want the later read failure suppressed by the delivery failure, got %v", err)
	}
}

func TestDrainTailCancellationBeforeDeliveryDropsBufferedCutovers(t *testing.T) {
	t.Parallel()

	errRead := errors.New("later read failed")
	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	for _, tt := range []struct {
		name         string
		iterator     func(*testing.T) *scriptedIterator
		wantErr      error
		wantContains string
		wantPosition int64
		exactContext bool
	}{
		{
			name: "without an independent failure",
			iterator: func(t *testing.T) *scriptedIterator {
				t.Helper()
				return newScriptedIterator(promotedEvent(t, 9))
			},
			wantPosition: 9,
			exactContext: true,
		},
		{
			name: "with an independent read failure",
			iterator: func(t *testing.T) *scriptedIterator {
				t.Helper()
				iter := newScriptedIterator(promotedEvent(t, 9))
				iter.terminalErr = errRead
				return iter
			},
			wantErr:      errRead,
			wantPosition: 9,
		},
		{
			name: "with an independent validation failure",
			iterator: func(t *testing.T) *scriptedIterator {
				t.Helper()
				return newScriptedIterator(
					promotedEvent(t, 9),
					promotedEventWith(t, 10, Promoted{Previous: ordersV1, Next: ordersV2, Revision: 5}),
				)
			},
			wantContains: "records revision 5 after revision 1",
			wantPosition: 10,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)

			iter := tt.iterator(t)
			iter.onClose = cancel
			setter := &untouchableSetter{}
			worker, err := NewWorker(stubReader{iter: iter}, WithCutoverSetter(setter))
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			position, err := worker.drain(ctx, map[string]cutoverFold{}, 3, worker.deliver)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("want context cancellation, got %v", err)
			}
			if tt.exactContext && err != context.Canceled { //nolint:errorlint // Exact identity is the contract without an independent failure.
				t.Fatalf("want exactly the context error, got %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("want independent failure %v joined with cancellation, got %v", tt.wantErr, err)
			}
			if tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
				t.Fatalf("want independent failure containing %q joined with cancellation, got %v", tt.wantContains, err)
			}
			if setter.touched {
				t.Error("want no buffered delivery after cancellation")
			}
			if position != tt.wantPosition {
				t.Errorf("want position %d, got %d", tt.wantPosition, position)
			}
		})
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
