package eventstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

func TestVersionPtr(t *testing.T) {
	t.Parallel()

	p := eventstore.VersionPtr(7)
	if p == nil {
		t.Fatal("want a non-nil pointer, got nil")
	}

	if *p != 7 {
		t.Errorf("want 7, got %d", *p)
	}

	// Each call must yield its own pointer, or two AppendStreamOptions built from this
	// helper would alias one version and a write to either would move both.
	if other := eventstore.VersionPtr(9); other == p {
		t.Error("want distinct pointers from successive calls, got the same address")
	}
}

func TestCollect(t *testing.T) {
	t.Parallel()

	t.Run("reads every event up to the end of the stream", func(t *testing.T) {
		t.Parallel()

		events, err := eventstore.Collect(t.Context(), newIterator(3, nil))
		if err != nil {
			t.Fatalf("reading events: %v", err)
		}

		if len(events) != 3 {
			t.Fatalf("want 3 events, got %d", len(events))
		}

		for i, event := range events {
			if want := int64(i + 1); event.StreamVersion != want {
				t.Errorf("event %d: want stream version %d, got %d", i, want, event.StreamVersion)
			}
		}
	})

	t.Run("returns an empty slice for a stream with no events", func(t *testing.T) {
		t.Parallel()

		events, err := eventstore.Collect(t.Context(), newIterator(0, nil))
		if err != nil {
			t.Fatalf("reading events: %v", err)
		}

		// Empty rather than nil: callers range over the result and check len, and a nil
		// return would be indistinguishable from a failure that dropped its events.
		if events == nil {
			t.Error("want an empty slice, got nil")
		}

		if len(events) != 0 {
			t.Errorf("want 0 events, got %d", len(events))
		}
	})

	t.Run("returns the events read so far alongside a read failure", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("backend went away")

		events, err := eventstore.Collect(t.Context(), newIterator(2, wantErr))
		if !errors.Is(err, wantErr) {
			t.Fatalf("want the underlying error to be wrapped, got %v", err)
		}

		// The partial result is the point: a caller that hits a mid-stream failure can see
		// how far it got rather than being told only that something broke.
		if len(events) != 2 {
			t.Errorf("want the 2 events read before the failure, got %d", len(events))
		}
	})
}

func TestErrorTypes(t *testing.T) {
	t.Parallel()

	underlying := errors.New("underlying")
	streamID := typeid.NewV4("stream")

	for _, tt := range []struct {
		name     string
		err      error
		wantMsg  string
		other    error
		unwraps  bool
		matchAll error
	}{
		{
			name:     "EventMarshalingError",
			err:      eventstore.EventMarshalingError{StreamID: streamID, Err: underlying},
			wantMsg:  "marshaling event: underlying",
			other:    eventstore.EventUnmarshalingError{},
			unwraps:  true,
			matchAll: eventstore.EventMarshalingError{},
		},
		{
			name:     "EventUnmarshalingError",
			err:      eventstore.EventUnmarshalingError{StreamID: streamID, Err: underlying},
			wantMsg:  "unmarshaling event: underlying",
			other:    eventstore.EventMarshalingError{},
			unwraps:  true,
			matchAll: eventstore.EventUnmarshalingError{},
		},
		{
			name:     "InitializationError",
			err:      eventstore.InitializationError{Err: underlying},
			wantMsg:  "initializing event store: underlying",
			other:    eventstore.EventMarshalingError{},
			unwraps:  true,
			matchAll: eventstore.InitializationError{},
		},
		{
			name:     "StreamVersionMismatchError",
			err:      eventstore.StreamVersionMismatchError{StreamID: streamID, ExpectedVersion: 2, ActualVersion: 5},
			wantMsg:  "stream version mismatch: expected version 2, got version 5",
			other:    eventstore.InitializationError{},
			unwraps:  false,
			matchAll: eventstore.StreamVersionMismatchError{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.err.Error(); got != tt.wantMsg {
				t.Errorf("want message %q, got %q", tt.wantMsg, got)
			}

			// Is matches on type alone, so a zero value of the same type matches a populated
			// one. That is what lets a caller write errors.Is(err, StreamVersionMismatchError{}).
			if !errors.Is(tt.err, tt.matchAll) {
				t.Error("want the error to match a zero value of its own type")
			}

			if errors.Is(tt.err, tt.other) {
				t.Errorf("want no match against %T", tt.other)
			}

			if tt.unwraps && !errors.Is(tt.err, underlying) {
				t.Error("want the underlying error to be reachable through Unwrap")
			}
		})
	}
}

// TestStreamVersionMismatchError_As pins that the versions survive errors.As, which is how a
// caller decides whether to retry and from what version.
func TestStreamVersionMismatchError_As(t *testing.T) {
	t.Parallel()

	err := error(eventstore.StreamVersionMismatchError{ExpectedVersion: 2, ActualVersion: 5})

	var mismatch eventstore.StreamVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatal("want errors.As to match")
	}

	if mismatch.ExpectedVersion != 2 || mismatch.ActualVersion != 5 {
		t.Errorf("want expected 2 and actual 5, got %d and %d", mismatch.ExpectedVersion, mismatch.ActualVersion)
	}
}

// iterator yields total events numbered from 1, then either ends the stream or fails.
type iterator struct {
	total    int
	produced int
	failWith error
}

func newIterator(total int, failWith error) *iterator {
	return &iterator{total: total, failWith: failWith}
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
