package projection_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/typeid"
)

func TestNewStreamProjection(t *testing.T) {
	t.Parallel()

	t.Run("rejects a nil iterator", func(t *testing.T) {
		t.Parallel()

		if _, err := projection.NewStreamProjection(nil); err == nil {
			t.Error("want an error for a nil iterator, got nil")
		}
	})

	t.Run("accepts an iterator", func(t *testing.T) {
		t.Parallel()

		p, err := projection.NewStreamProjection(newIterator(1))
		if err != nil {
			t.Fatalf("creating projection: %v", err)
		}

		if p == nil {
			t.Error("want a projection, got nil")
		}
	})
}

func TestStreamProjection_Project(t *testing.T) {
	t.Parallel()

	t.Run("rejects a nil event handler", func(t *testing.T) {
		t.Parallel()

		p := newProjection(t, newIterator(1))

		if _, err := p.Project(t.Context(), nil); err == nil {
			t.Error("want an error for a nil handler, got nil")
		}
	})

	t.Run("projects every event in the stream", func(t *testing.T) {
		t.Parallel()

		var handled int

		p := newProjection(t, newIterator(5))

		result, err := p.Project(t.Context(), projection.EventHandlerFunc(
			func(context.Context, *eventstore.Event) error {
				handled++
				return nil
			}))
		if err != nil {
			t.Fatalf("projecting: %v", err)
		}

		if handled != 5 {
			t.Errorf("want the handler called 5 times, got %d", handled)
		}

		if result.NumProjectedEvents != 5 {
			t.Errorf("want 5 projected events, got %d", result.NumProjectedEvents)
		}

		if result.NumFailedEvents != 0 {
			t.Errorf("want 0 failed events, got %d", result.NumFailedEvents)
		}
	})

	t.Run("reports zero projected events for an empty stream", func(t *testing.T) {
		t.Parallel()

		result, err := newProjection(t, newIterator(0)).Project(t.Context(), failingHandler(t))
		if err != nil {
			t.Fatalf("projecting: %v", err)
		}

		if result.NumProjectedEvents != 0 {
			t.Errorf("want 0 projected events, got %d", result.NumProjectedEvents)
		}
	})

	t.Run("stops at the first handler error by default", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("handler failed")

		var handled int

		result, err := newProjection(t, newIterator(5)).Project(t.Context(), projection.EventHandlerFunc(
			func(_ context.Context, event *eventstore.Event) error {
				handled++
				if event.StreamVersion == 3 {
					return wantErr
				}

				return nil
			}))
		if !errors.Is(err, wantErr) {
			t.Fatalf("want the handler error wrapped, got %v", err)
		}

		// Stopping means stopping: the handler must not see event 4.
		if handled != 3 {
			t.Errorf("want the handler called 3 times before stopping, got %d", handled)
		}

		// The partial result tells a caller how far the projection got.
		if result.NumProjectedEvents != 2 {
			t.Errorf("want 2 projected events before the failure, got %d", result.NumProjectedEvents)
		}

		if result.NumFailedEvents != 1 {
			t.Errorf("want 1 failed event, got %d", result.NumFailedEvents)
		}
	})

	t.Run("continues past handler errors when configured to", func(t *testing.T) {
		t.Parallel()

		p, err := projection.NewStreamProjection(newIterator(5),
			projection.WithContinueOnHandlerError(true),
			projection.WithLogger(discardLogger{}),
		)
		if err != nil {
			t.Fatalf("creating projection: %v", err)
		}

		result, err := p.Project(t.Context(), projection.EventHandlerFunc(
			func(_ context.Context, event *eventstore.Event) error {
				if event.StreamVersion%2 == 1 {
					return errors.New("handler failed")
				}

				return nil
			}))
		if err != nil {
			t.Fatalf("want the projection to complete, got %v", err)
		}

		if result.NumProjectedEvents != 2 {
			t.Errorf("want 2 projected events, got %d", result.NumProjectedEvents)
		}

		if result.NumFailedEvents != 3 {
			t.Errorf("want 3 failed events, got %d", result.NumFailedEvents)
		}
	})

	t.Run("reports a read failure rather than treating it as the end of the stream", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("backend went away")

		iter := newIterator(2)
		iter.failWith = wantErr

		result, err := newProjection(t, iter).Project(t.Context(), projection.EventHandlerFunc(
			func(context.Context, *eventstore.Event) error { return nil }))
		if !errors.Is(err, wantErr) {
			t.Fatalf("want the read error wrapped, got %v", err)
		}

		if result.NumProjectedEvents != 2 {
			t.Errorf("want the 2 events projected before the failure, got %d", result.NumProjectedEvents)
		}
	})
}

// TestEventHandlerFunc_Handle covers the adapter that lets a bare function satisfy
// EventHandler.
func TestEventHandlerFunc_Handle(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("handler failed")
	wantEvent := &eventstore.Event{StreamID: typeid.NewV4("stream"), StreamVersion: 1}

	var gotEvent *eventstore.Event

	handler := projection.EventHandlerFunc(func(_ context.Context, event *eventstore.Event) error {
		gotEvent = event
		return wantErr
	})

	if err := handler.Handle(t.Context(), wantEvent); !errors.Is(err, wantErr) {
		t.Errorf("want the function's error returned unchanged, got %v", err)
	}

	if gotEvent != wantEvent {
		t.Error("want the event passed through to the function")
	}
}

func newProjection(t *testing.T, iter eventstore.StreamIterator) *projection.StreamProjection {
	t.Helper()

	p, err := projection.NewStreamProjection(iter, projection.WithLogger(discardLogger{}))
	if err != nil {
		t.Fatalf("creating projection: %v", err)
	}

	return p
}

// failingHandler fails the test if it is ever called.
func failingHandler(t *testing.T) projection.EventHandler {
	t.Helper()

	return projection.EventHandlerFunc(func(context.Context, *eventstore.Event) error {
		t.Error("want the handler not to be called")
		return nil
	})
}

// iterator yields total events numbered from 1, then either ends the stream or fails.
type iterator struct {
	total    int
	produced int
	failWith error
}

func newIterator(total int) *iterator {
	return &iterator{total: total}
}

func (i *iterator) Next(context.Context) (*eventstore.Event, error) {
	if i.produced == i.total {
		if i.failWith != nil {
			return nil, i.failWith
		}

		return nil, eventstore.ErrEndOfEventStream
	}

	i.produced++

	return &eventstore.Event{StreamVersion: int64(i.produced)}, nil
}

func (i *iterator) Close(context.Context) error {
	return nil
}

// discardLogger keeps the error path in "continue on handler error" from writing to the
// test's output, where it reads as a real failure.
type discardLogger struct{}

var _ estoria.Logger = discardLogger{}

func (discardLogger) Debug(string, ...any)              {}
func (discardLogger) Info(string, ...any)               {}
func (discardLogger) Warn(string, ...any)               {}
func (discardLogger) Error(string, ...any)              {}
func (l discardLogger) With(...any) estoria.Logger      { return l }
func (l discardLogger) WithGroup(string) estoria.Logger { return l }
