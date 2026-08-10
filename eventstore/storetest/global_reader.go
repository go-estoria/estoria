package storetest

import (
	"errors"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

// A GlobalStore is the capability set the global reader suite exercises: it
// appends through the Store half and observes the results through the
// GlobalReader half.
type GlobalStore interface {
	eventstore.Store
	eventstore.GlobalReader
}

// NewGlobalStoreFunc returns a global-capable store for one clause of the
// global reader suite to use.
//
// Unlike the event store suite, every call MUST return a store whose event
// history the suite exclusively owns — a fresh store, database, or namespace —
// because a global read observes every stream in the store, so writes shared
// with anything else make its results unassertable.
type NewGlobalStoreFunc func(t *testing.T) GlobalStore

// RunGlobalReaderSuite runs the global reader acceptance suite against stores
// returned by newStore, reporting each clause as its own named subtest.
//
// The suite is the executable form of the GlobalReader contract: events from
// all streams in ascending global order, a non-nil strictly-increasing
// GlobalPosition on every yielded event agreeing with what per-stream reads
// report, resumption from an exclusive position, and empty reads as empty
// iterators rather than errors.
func RunGlobalReaderSuite(t *testing.T, newStore NewGlobalStoreFunc) {
	t.Helper()

	for _, clause := range []struct {
		name string
		run  func(t *testing.T, store GlobalStore)
	}{
		{"reads events from all streams in ascending global order", clauseGlobalOrderAcrossStreams},
		{"yields the same global positions per-stream reads report", clauseGlobalPositionsMatchStreamReads},
		{"resumes reading after an exclusive position", clauseGlobalResumesAfterPosition},
		{"limits a global read to Count events", clauseGlobalHonorsCount},
		{"yields an empty iterator rather than an error when there is nothing to read", clauseGlobalEmptyReads},
	} {
		t.Run(clause.name, func(t *testing.T) {
			clause.run(t, newStore(t))
		})
	}
}

// appendInterleavedStreams writes a fixture of seven events interleaved across
// three streams, so global-order assertions exercise interleaving rather than
// one stream's internal order. It returns the stream IDs with their appended
// event counts.
func appendInterleavedStreams(t *testing.T, store GlobalStore) map[typeid.ID]int {
	t.Helper()

	streamA := newStreamID()
	streamB := newStreamID()
	streamC := newStreamID()

	for _, batch := range []struct {
		streamID typeid.ID
		count    int
	}{
		{streamA, 2},
		{streamB, 1},
		{streamA, 1},
		{streamC, 2},
		{streamB, 1},
	} {
		if _, err := store.AppendStream(t.Context(), batch.streamID, writableEvents(batch.count), eventstore.AppendStreamOptions{}); err != nil {
			t.Fatalf("appending %d events to stream %s: %v", batch.count, batch.streamID, err)
		}
	}

	return map[typeid.ID]int{streamA: 3, streamB: 2, streamC: 2}
}

func readGlobal(t *testing.T, store GlobalStore, opts eventstore.ReadAllOptions) []*eventstore.Event {
	t.Helper()

	iter, err := store.ReadAll(t.Context(), opts)
	if err != nil {
		t.Fatalf("reading all streams: %v", err)
	}

	t.Cleanup(func() { _ = iter.Close(t.Context()) })

	events, err := eventstore.Collect(t.Context(), iter)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	return events
}

// clauseGlobalOrderAcrossStreams pins the core promise: one pass over the store
// yields every stream's events, ascending by global position, with each
// stream's own version order preserved within the interleaving.
func clauseGlobalOrderAcrossStreams(t *testing.T, store GlobalStore) {
	streams := appendInterleavedStreams(t, store)

	total := 0
	for _, count := range streams {
		total += count
	}

	events := readGlobal(t, store, eventstore.ReadAllOptions{})
	if len(events) != total {
		t.Fatalf("want %d events across all streams, got %d", total, len(events))
	}

	var lastPosition int64
	lastVersions := map[typeid.ID]int64{}
	counts := map[typeid.ID]int{}

	for i, event := range events {
		if event.GlobalPosition == nil {
			t.Fatalf("event %d: want a non-nil global position, got nil", i)
		}

		if *event.GlobalPosition <= lastPosition {
			t.Errorf("event %d: want a global position above %d, got %d", i, lastPosition, *event.GlobalPosition)
		}
		lastPosition = *event.GlobalPosition

		if want := lastVersions[event.StreamID] + 1; event.StreamVersion != want {
			t.Errorf("event %d: want stream %s at version %d, got %d", i, event.StreamID, want, event.StreamVersion)
		}
		lastVersions[event.StreamID] = event.StreamVersion

		counts[event.StreamID]++
	}

	for streamID, want := range streams {
		if counts[streamID] != want {
			t.Errorf("want %d events for stream %s, got %d", want, streamID, counts[streamID])
		}
	}
}

// clauseGlobalPositionsMatchStreamReads pins that the global read and the
// per-stream read agree on an event's position: a consumer that checkpoints
// positions from one path must be able to reason about the other.
func clauseGlobalPositionsMatchStreamReads(t *testing.T, store GlobalStore) {
	streams := appendInterleavedStreams(t, store)

	byID := map[typeid.ID]*eventstore.Event{}
	for _, event := range readGlobal(t, store, eventstore.ReadAllOptions{}) {
		byID[event.ID] = event
	}

	for streamID := range streams {
		for i, event := range readStream(t, store, streamID, eventstore.ReadStreamOptions{}) {
			global, ok := byID[event.ID]
			if !ok {
				t.Errorf("stream %s event %d: not yielded by the global read", streamID, i)
				continue
			}

			switch {
			case event.GlobalPosition == nil:
				t.Errorf("stream %s event %d: want a non-nil global position from the stream read, got nil", streamID, i)
			case *global.GlobalPosition != *event.GlobalPosition:
				t.Errorf("stream %s event %d: want the stream read's global position %d, got %d",
					streamID, i, *event.GlobalPosition, *global.GlobalPosition)
			}
		}
	}
}

// clauseGlobalResumesAfterPosition pins the checkpoint mechanic: AfterPosition
// is exclusive, and a read from event N's position yields exactly the events
// after N.
func clauseGlobalResumesAfterPosition(t *testing.T, store GlobalStore) {
	appendInterleavedStreams(t, store)

	full := readGlobal(t, store, eventstore.ReadAllOptions{})
	if len(full) < 4 {
		t.Fatalf("want at least 4 events in the fixture, got %d", len(full))
	}

	const resumeAfter = 2

	position := *full[resumeAfter].GlobalPosition

	resumed := readGlobal(t, store, eventstore.ReadAllOptions{AfterPosition: position})
	if want := len(full) - resumeAfter - 1; len(resumed) != want {
		t.Fatalf("want %d events after position %d, got %d", want, position, len(resumed))
	}

	for i, event := range resumed {
		if want := full[resumeAfter+1+i]; event.ID != want.ID {
			t.Errorf("event %d: want the full read's event %s, got %s", i, want.ID, event.ID)
		}

		if *event.GlobalPosition <= position {
			t.Errorf("event %d: want a global position above %d, got %d", i, position, *event.GlobalPosition)
		}
	}
}

// clauseGlobalHonorsCount pins that Count bounds the read from the front: the
// first Count events of the unbounded read, and no more.
func clauseGlobalHonorsCount(t *testing.T, store GlobalStore) {
	appendInterleavedStreams(t, store)

	full := readGlobal(t, store, eventstore.ReadAllOptions{})

	const count = 3

	limited := readGlobal(t, store, eventstore.ReadAllOptions{Count: count})
	if len(limited) != count {
		t.Fatalf("want %d events, got %d", count, len(limited))
	}

	for i, event := range limited {
		if event.ID != full[i].ID {
			t.Errorf("event %d: want the full read's event %s, got %s", i, full[i].ID, event.ID)
		}
	}
}

// clauseGlobalEmptyReads pins that having nothing to yield is an ordinary
// result, not a failure: a brand-new store, and a reader caught up to the tip,
// both get a valid iterator that immediately reports ErrEndOfEventStream. A
// store reporting ErrStreamNotFound here turns every cold start and every
// caught-up poll into an error path.
func clauseGlobalEmptyReads(t *testing.T, store GlobalStore) {
	iter, err := store.ReadAll(t.Context(), eventstore.ReadAllOptions{})
	if err != nil {
		t.Fatalf("want a valid iterator from an empty store, got error: %v", err)
	}

	defer iter.Close(t.Context())

	if _, err := iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
		t.Errorf("want ErrEndOfEventStream from an empty store, got %v", err)
	}

	appendInterleavedStreams(t, store)

	full := readGlobal(t, store, eventstore.ReadAllOptions{})
	tip := *full[len(full)-1].GlobalPosition

	for name, after := range map[string]int64{"at the tip": tip, "past the tip": tip + 100} {
		caughtUp, err := store.ReadAll(t.Context(), eventstore.ReadAllOptions{AfterPosition: after})
		if err != nil {
			t.Fatalf("want a valid iterator %s, got error: %v", name, err)
		}

		defer caughtUp.Close(t.Context())

		if _, err := caughtUp.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
			t.Errorf("want ErrEndOfEventStream %s, got %v", name, err)
		}
	}
}
