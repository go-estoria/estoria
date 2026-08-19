package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
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

// inHandIterator yields one event, canceling a context from inside the
// yielding Next call.
type inHandIterator struct {
	cancel context.CancelFunc
	event  *eventstore.Event
	served bool
}

func (i *inHandIterator) Next(context.Context) (*eventstore.Event, error) {
	if i.served {
		return nil, eventstore.ErrEndOfEventStream
	}

	i.served = true
	i.cancel()

	return i.event, nil
}

func (i *inHandIterator) Close(context.Context) error { return nil }

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

// TestDrainDropsTheEventInHand pins that a cutover read alongside
// cancellation is wholly unprocessed: the returned position is unmoved and
// the fold is untouched.
func TestDrainDropsTheEventInHand(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(Promoted{Next: projection.ID{Name: "orders", Version: 1}, Revision: 1})
	if err != nil {
		t.Fatalf("marshaling promoted event: %v", err)
	}

	yielded := int64(9)
	event := &eventstore.Event{
		ID:             typeid.NewV4(Promoted{}.EventType()),
		StreamID:       typeid.ID{Type: StreamType, UUID: StreamUUID("orders")},
		Data:           data,
		GlobalPosition: &yielded,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	worker, err := NewWorker(stubReader{iter: &inHandIterator{cancel: cancel, event: event}},
		WithCutoverSetter(nopSetter{}))
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	live := map[string]cutoverFold{}

	position, err := worker.drain(ctx, live, 3, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	// The read succeeded; only the cancellation is reported.
	if strings.Contains(err.Error(), "reading event") {
		t.Errorf("want the bare cancellation for the successful read, got %v", err)
	}

	if position != 3 {
		t.Errorf("want the position unmoved at 3, got %d", position)
	}

	if len(live) != 0 {
		t.Errorf("want the fold untouched, got %d entries", len(live))
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
