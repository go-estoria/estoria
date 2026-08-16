package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

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
// report, resumption from an exclusive position, a frontier settled by the
// time ReadAll returns (commits racing the drain wait for the next read —
// empty, caught-up, and Count-limited reads included), and empty reads as
// empty iterators rather than errors.
//
// One clause of the contract is deliberately absent: stable-prefix commit
// ordering — no commit may introduce an unseen event at or below a yielded
// position — cannot be forced deterministically through this interface, and a
// sleep-based race would prove nothing. Each backend must carry its own
// ordering regression at the layer where its commit timing is controllable.
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
		{"fixes its frontier by the time ReadAll returns", clauseGlobalFrontierFreshRead},
		{"fixes a resumed read's frontier by the time ReadAll returns", clauseGlobalFrontierResumedRead},
		{"keeps empty and caught-up reads empty while appends race them", clauseGlobalFrontierEmptyAndCaughtUp},
		{"truncates a Count-limited read at its frontier rather than topping it up", clauseGlobalFrontierUnderCount},
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

// A trackedIterator pairs an open iterator with a failure-path safety net.
// Clauses close iterators as soon as they finish reading — an iterator held
// open can pin a transaction or pooled connection on some backends — so the
// registered cleanup acts only when a failure exits the clause early, using a
// bounded, non-canceled context because t.Context is already canceled by the
// time cleanups run.
type trackedIterator struct {
	iter   eventstore.StreamIterator
	closed bool
}

func trackIterator(t *testing.T, iter eventstore.StreamIterator) *trackedIterator {
	t.Helper()

	tracked := &trackedIterator{iter: iter}

	t.Cleanup(func() {
		if tracked.closed {
			return
		}

		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()

		_ = tracked.iter.Close(ctx)
	})

	return tracked
}

// close closes the iterator at its point of last use, asserting the backend
// releases it cleanly, and disarms the failure-path net.
func (i *trackedIterator) close(t *testing.T) {
	t.Helper()

	if err := i.iter.Close(t.Context()); err != nil {
		t.Errorf("closing iterator: %v", err)
	}

	i.closed = true
}

// openGlobalRead starts a global read and tracks the iterator; the caller
// closes it as soon as it finishes reading.
func openGlobalRead(t *testing.T, store GlobalStore, opts eventstore.ReadAllOptions) *trackedIterator {
	t.Helper()

	iter, err := store.ReadAll(t.Context(), opts)
	if err != nil {
		t.Fatalf("reading all streams: %v", err)
	}

	return trackIterator(t, iter)
}

// appendTo appends count events to the stream, failing the clause on error.
func appendTo(t *testing.T, store GlobalStore, streamID typeid.ID, count int) []*eventstore.Event {
	t.Helper()

	written, err := store.AppendStream(t.Context(), streamID, writableEvents(count), eventstore.AppendStreamOptions{})
	if err != nil {
		t.Fatalf("appending %d events to stream %s: %v", count, streamID, err)
	}

	return written
}

func readGlobal(t *testing.T, store GlobalStore, opts eventstore.ReadAllOptions) []*eventstore.Event {
	t.Helper()

	tracked := openGlobalRead(t, store, opts)

	events, err := eventstore.Collect(t.Context(), tracked.iter)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	tracked.close(t)

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

// clauseGlobalFrontierFreshRead pins the finite-read half of the stable-prefix
// contract: a read's frontier is settled by the time ReadAll returns, so an
// event committed while the iterator drains — before the first Next or midway
// through — waits for the next read, and exhaustion is terminal even as later
// commits land. Without a settled frontier, draining to ErrEndOfEventStream is
// not an observation a consumer can act on.
func clauseGlobalFrontierFreshRead(t *testing.T, store GlobalStore) {
	appendInterleavedStreams(t, store)

	baseline := readGlobal(t, store, eventstore.ReadAllOptions{})

	tracked := openGlobalRead(t, store, eventstore.ReadAllOptions{})

	late := newStreamID()

	// One commit before anything is consumed, another midway through the read.
	appendTo(t, store, late, 1)

	yielded := make([]*eventstore.Event, 0, len(baseline))

	for range 2 {
		event, err := tracked.iter.Next(t.Context())
		if err != nil {
			t.Fatalf("reading event: %v", err)
		}

		yielded = append(yielded, event)
	}

	appendTo(t, store, late, 2)

	rest, err := eventstore.Collect(t.Context(), tracked.iter)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	yielded = append(yielded, rest...)

	if len(yielded) != len(baseline) {
		t.Fatalf("want exactly the %d events from before the read, got %d", len(baseline), len(yielded))
	}

	for i, event := range yielded {
		if event.ID != baseline[i].ID {
			t.Errorf("event %d: want the pre-read event %s, got %s", i, baseline[i].ID, event.ID)
		}
	}

	// Exhaustion is terminal: the late commits exist by now, and a drained
	// iterator still must not yield them.
	if _, err := tracked.iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
		t.Errorf("want an exhausted iterator to keep reporting ErrEndOfEventStream, got %v", err)
	}

	tracked.close(t)

	// The late commits are not lost: the next read's frontier includes them.
	full := readGlobal(t, store, eventstore.ReadAllOptions{})
	if want := len(baseline) + 3; len(full) != want {
		t.Fatalf("want a fresh read to see all %d events, got %d", want, len(full))
	}
}

// clauseGlobalFrontierResumedRead pins the same frontier discipline on the
// checkpoint path — the poll loop every projection processor runs: a read
// resumed from a position yields exactly what was eligible when ReadAll
// returned, with commits racing the drain — before first consumption or
// midway through — deferred to the next poll.
func clauseGlobalFrontierResumedRead(t *testing.T, store GlobalStore) {
	appendInterleavedStreams(t, store)

	baseline := readGlobal(t, store, eventstore.ReadAllOptions{})

	const resumeAfter = 1

	position := *baseline[resumeAfter].GlobalPosition

	tracked := openGlobalRead(t, store, eventstore.ReadAllOptions{AfterPosition: position})

	late := newStreamID()

	appendTo(t, store, late, 1)

	yielded := make([]*eventstore.Event, 0, len(baseline))

	for range 2 {
		event, err := tracked.iter.Next(t.Context())
		if err != nil {
			t.Fatalf("reading event: %v", err)
		}

		yielded = append(yielded, event)
	}

	appendTo(t, store, late, 1)

	rest, err := eventstore.Collect(t.Context(), tracked.iter)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	tracked.close(t)

	yielded = append(yielded, rest...)

	if want := len(baseline) - resumeAfter - 1; len(yielded) != want {
		t.Fatalf("want the %d events above position %d from before the read, got %d", want, position, len(yielded))
	}

	for i, event := range yielded {
		if want := baseline[resumeAfter+1+i]; event.ID != want.ID {
			t.Errorf("event %d: want the full read's event %s, got %s", i, want.ID, event.ID)
		}
	}
}

// clauseGlobalFrontierEmptyAndCaughtUp pins the frontier where it is easiest
// to fake: a read that starts with nothing eligible — an empty store, or a
// resume at the tip — stays empty while appends race it, and the racing event
// lands in the next poll's frontier instead. An implementation that goes live
// exactly when its snapshot is empty passes every nonempty clause and still
// breaks the caught-up poll loop.
func clauseGlobalFrontierEmptyAndCaughtUp(t *testing.T, store GlobalStore) {
	empty := openGlobalRead(t, store, eventstore.ReadAllOptions{})

	appendTo(t, store, newStreamID(), 1)

	if _, err := empty.iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
		t.Errorf("want an empty read to stay empty despite the racing append, got %v", err)
	}

	if _, err := empty.iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
		t.Errorf("want an exhausted empty read to keep reporting ErrEndOfEventStream, got %v", err)
	}

	empty.close(t)

	full := readGlobal(t, store, eventstore.ReadAllOptions{})
	tip := *full[len(full)-1].GlobalPosition

	caughtUp := openGlobalRead(t, store, eventstore.ReadAllOptions{AfterPosition: tip})

	late := appendTo(t, store, newStreamID(), 1)[0]

	if _, err := caughtUp.iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
		t.Errorf("want a read resumed at the tip to stay empty despite the racing append, got %v", err)
	}

	caughtUp.close(t)

	// The racing append is not lost: the next poll from the same position
	// yields exactly it.
	next := readGlobal(t, store, eventstore.ReadAllOptions{AfterPosition: tip})

	switch {
	case len(next) != 1:
		t.Fatalf("want the next poll after position %d to yield exactly one event, got %d", tip, len(next))
	case next[0].ID != late.ID:
		t.Fatalf("want the next poll to yield the racing event %s, got %s", late.ID, next[0].ID)
	}
}

// clauseGlobalFrontierUnderCount pins that Count caps a read without
// extending it: when fewer eligible events exist than Count asks for, the
// read exhausts at the frontier rather than topping up from commits that land
// before or during the drain. Exhaustion below Count is a frontier
// observation; exhaustion at exactly Count certifies nothing.
func clauseGlobalFrontierUnderCount(t *testing.T, store GlobalStore) {
	appendInterleavedStreams(t, store)

	baseline := readGlobal(t, store, eventstore.ReadAllOptions{})

	const resumeAfter = 3

	position := *baseline[resumeAfter].GlobalPosition
	eligible := baseline[resumeAfter+1:]

	// Ask for more than remains eligible, so an implementation that tops up
	// has room to be caught.
	count := int64(len(eligible)) + 2

	tracked := openGlobalRead(t, store, eventstore.ReadAllOptions{AfterPosition: position, Count: count})

	late := newStreamID()

	appendTo(t, store, late, 1)

	first, err := tracked.iter.Next(t.Context())
	if err != nil {
		t.Fatalf("reading event: %v", err)
	}

	appendTo(t, store, late, 2)

	rest, err := eventstore.Collect(t.Context(), tracked.iter)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	tracked.close(t)

	yielded := append([]*eventstore.Event{first}, rest...)

	if len(yielded) != len(eligible) {
		t.Fatalf("want the %d eligible events despite Count %d, got %d", len(eligible), count, len(yielded))
	}

	for i, event := range yielded {
		if event.ID != eligible[i].ID {
			t.Errorf("event %d: want the full read's event %s, got %s", i, eligible[i].ID, event.ID)
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

	empty := trackIterator(t, iter)

	if _, err := empty.iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
		t.Errorf("want ErrEndOfEventStream from an empty store, got %v", err)
	}

	empty.close(t)

	appendInterleavedStreams(t, store)

	full := readGlobal(t, store, eventstore.ReadAllOptions{})
	tip := *full[len(full)-1].GlobalPosition

	for name, after := range map[string]int64{"at the tip": tip, "past the tip": tip + 100} {
		caughtUpIter, err := store.ReadAll(t.Context(), eventstore.ReadAllOptions{AfterPosition: after})
		if err != nil {
			t.Fatalf("want a valid iterator %s, got error: %v", name, err)
		}

		caughtUp := trackIterator(t, caughtUpIter)

		if _, err := caughtUp.iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
			t.Errorf("want ErrEndOfEventStream %s, got %v", name, err)
		}

		caughtUp.close(t)
	}
}
