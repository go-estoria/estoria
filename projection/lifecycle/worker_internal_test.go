package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
			// The trigger outlives the deadline instead of canceling, so
			// the read completes with the deadline already exceeded.
			name: "deadline exceeded",
			ctx: func(t *testing.T) (context.Context, func()) {
				t.Helper()

				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
				t.Cleanup(cancel)

				return ctx, func() { time.Sleep(100 * time.Millisecond) }
			},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, trigger := tt.ctx(t)

			worker, err := NewWorker(stubReader{iter: &inHandIterator{trigger: trigger, event: promotedEvent(t, 9)}},
				WithCutoverSetter(nopSetter{}))
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			live := map[string]cutoverFold{}

			position, err := worker.drain(ctx, live, 3, nil)
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
