// Package storetest provides an acceptance suite that every eventstore.Store
// implementation is expected to pass.
//
// The suite is the executable form of the contract documented on the eventstore
// interfaces. It lives beside those interfaces so that core's own reference
// implementation and every third-party backend are held to one definition of correct
// behavior, and so that adding a clause here surfaces every implementation that violates
// it.
//
// A backend wires it up with a single test:
//
//	func TestEventStore_AcceptanceTest(t *testing.T) {
//		storetest.RunEventStoreSuite(t, func(t *testing.T) eventstore.Store {
//			return newStoreForTest(t)
//		})
//	}
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

// NewStoreFunc returns an event store for one clause of the acceptance suite to use.
//
// Implementations may return the same store on every call. Every clause writes to a
// freshly generated stream ID and asserts only on that stream, so sharing one store across
// clauses is safe and avoids standing up a backend per clause.
type NewStoreFunc func(t *testing.T) eventstore.Store

// RunEventStoreSuite runs the event store acceptance suite against stores returned by
// newStore, reporting each clause as its own named subtest so a failing backend learns
// which part of the contract it violates.
//
// The suite does not call t.Parallel: callers typically parallelize at the outer level, and
// parallel clauses sharing one store would race. It does not skip on -short either; whether
// a backend is reachable is the caller's concern.
func RunEventStoreSuite(t *testing.T, newStore NewStoreFunc) {
	t.Helper()

	for _, clause := range []struct {
		name string
		run  func(t *testing.T, store eventstore.Store)
	}{
		{"assigns an ID, stream ID, stream version, and timestamp to each appended event", clauseAssignsEventFields},
		{"round-trips event data", clauseRoundTripsData},
		{"round-trips event metadata", clauseRoundTripsMetadata},
		{"does not modify the events it is given", clauseDoesNotMutateEvents},
		{"reports ErrStreamNotFound for a stream that was never written", clauseUnwrittenStreamNotFound},
		{"yields an empty iterator when reading past the stream tip", clauseReadPastTip},
		{"reads a stream in reverse", clauseReadsReverse},
		{"limits a read to Count events", clauseHonorsCount},
		{"appends when ExpectVersion matches the current version", clauseExpectVersionMatch},
		{"rejects an append when ExpectVersion does not match", clauseExpectVersionMismatch},
		{"appends to a new stream when StreamMustNotExist is set", clauseStreamMustNotExistNew},
		{"rejects an append to an existing stream when StreamMustNotExist is set", clauseStreamMustNotExistExisting},
		{"rejects an append setting both ExpectVersion and StreamMustNotExist", clauseMutuallyExclusiveOptions},
	} {
		t.Run(clause.name, func(t *testing.T) {
			clause.run(t, newStore(t))
		})
	}
}

// clauseAssignsEventFields pins the fields an event store is responsible for populating.
// A caller supplies only Type, Data, and Metadata; everything else on a read event is the
// store's to assign, and callers rely on all four.
func clauseAssignsEventFields(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	appendEvents(t, store, streamID, 3, eventstore.AppendStreamOptions{})

	events := readStream(t, store, streamID, eventstore.ReadStreamOptions{})
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}

	for i, event := range events {
		if event.ID.UUID.IsNil() {
			t.Errorf("event %d: want a non-nil assigned event ID, got the zero UUID", i)
		}

		if event.StreamID != streamID {
			t.Errorf("event %d: want stream ID %s, got %s", i, streamID, event.StreamID)
		}

		// Stream versions are 1-based and gapless: the first event in a stream is version 1.
		if want := int64(i + 1); event.StreamVersion != want {
			t.Errorf("event %d: want stream version %d, got %d", i, want, event.StreamVersion)
		}

		if event.Timestamp.IsZero() {
			t.Errorf("event %d: want an assigned timestamp, got the zero time", i)
		}
	}
}

// clauseRoundTripsData is the base guarantee: bytes in, same bytes out, in append order.
func clauseRoundTripsData(t *testing.T, store eventstore.Store) {
	const count = 10

	streamID := newStreamID()
	appendEvents(t, store, streamID, count, eventstore.AppendStreamOptions{})

	read := readStream(t, store, streamID, eventstore.ReadStreamOptions{})
	if len(read) != count {
		t.Fatalf("want %d events, got %d", count, len(read))
	}

	for i := range read {
		// Assert against independently rebuilt payloads rather than the slice handed to
		// AppendStream: a store that writes through the caller's events would otherwise
		// mutate the expectation into agreeing with itself.
		//
		// Compare semantically, since backends that store JSON natively (Postgres jsonb,
		// Mongo BSON) may reorder keys or renormalize whitespace. That is not a break.
		assertJSONEqual(t, i, eventData(i), read[i].Data)
	}
}

// clauseDoesNotMutateEvents pins that AppendStream treats its events as read-only. Callers
// reuse the slice — retrying an append after a version conflict is the obvious case — and a
// store that rewrites entries in place corrupts the retry.
func clauseDoesNotMutateEvents(t *testing.T, store eventstore.Store) {
	const count = 3

	streamID := newStreamID()
	events := appendEvents(t, store, streamID, count, eventstore.AppendStreamOptions{})

	for i, event := range events {
		if event.Type != eventType {
			t.Errorf("event %d: store modified Type: want %q, got %q", i, eventType, event.Type)
		}

		if !reflect.DeepEqual(event.Data, eventData(i)) {
			t.Errorf("event %d: store modified Data: want %s, got %s", i, eventData(i), event.Data)
		}
	}
}

// clauseRoundTripsMetadata covers the field that carries correlation, causation, and tracing
// data. It is separate from the data round-trip because a backend can persist the payload
// faithfully and still drop metadata entirely, which is invisible until something depends
// on it.
func clauseRoundTripsMetadata(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	want := map[string]string{"correlation_id": "abc-123", "actor": "user-7"}

	err := store.AppendStream(t.Context(), streamID, []*eventstore.WritableEvent{{
		Type: eventType,
		Data: eventData(0),
		// Clone, so a store that writes through the caller's map cannot edit the
		// expectation into agreeing with what it stored.
		Metadata: maps.Clone(want),
	}}, eventstore.AppendStreamOptions{})
	if err != nil {
		t.Fatalf("appending event: %v", err)
	}

	events := readStream(t, store, streamID, eventstore.ReadStreamOptions{})
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}

	if got := events[0].Metadata; !reflect.DeepEqual(got, want) {
		t.Errorf("want metadata %v, got %v", want, got)
	}
}

// clauseUnwrittenStreamNotFound pins the "absent" half of the ReadStream contract: a stream
// that was never written reports ErrStreamNotFound.
func clauseUnwrittenStreamNotFound(t *testing.T, store eventstore.Store) {
	iter, err := store.ReadStream(t.Context(), newStreamID(), eventstore.ReadStreamOptions{})
	if err == nil {
		defer iter.Close(t.Context())
	}

	if !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Errorf("want ErrStreamNotFound reading a stream that was never written, got %v", err)
	}
}

// clauseReadPastTip pins the other half: a stream that exists but has nothing matching the
// read options is not the same as an absent stream. Backends that infer absence from an
// empty filtered read make any aggregate snapshotted at its own tip unloadable.
func clauseReadPastTip(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	appendEvents(t, store, streamID, 10, eventstore.AppendStreamOptions{})

	iter, err := store.ReadStream(t.Context(), streamID, eventstore.ReadStreamOptions{AfterVersion: 10})
	if err != nil {
		t.Fatalf("want a readable iterator past the stream tip, got error: %v", err)
	}

	defer iter.Close(t.Context())

	if _, err := iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
		t.Errorf("want ErrEndOfEventStream reading past the stream tip, got %v", err)
	}
}

// clauseReadsReverse pins reverse reads, including AfterVersion's inclusive upper-bound
// meaning in that direction, which differs from its exclusive lower-bound meaning going
// forward.
func clauseReadsReverse(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	appendEvents(t, store, streamID, 5, eventstore.AppendStreamOptions{})

	t.Run("from the end of the stream by default", func(t *testing.T) {
		events := readStream(t, store, streamID, eventstore.ReadStreamOptions{
			Direction: eventstore.Reverse,
		})

		assertVersions(t, events, 5, 4, 3, 2, 1)
	})

	t.Run("from an inclusive AfterVersion", func(t *testing.T) {
		events := readStream(t, store, streamID, eventstore.ReadStreamOptions{
			Direction:    eventstore.Reverse,
			AfterVersion: 3,
		})

		assertVersions(t, events, 3, 2, 1)
	})
}

// clauseHonorsCount pins the read limit in both directions. SnapshottingStore reads a
// single event off the tip of a snapshot stream, so a backend that ignores Count reads an
// entire stream to answer that.
func clauseHonorsCount(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	appendEvents(t, store, streamID, 5, eventstore.AppendStreamOptions{})

	t.Run("reading forward", func(t *testing.T) {
		events := readStream(t, store, streamID, eventstore.ReadStreamOptions{Count: 2})
		assertVersions(t, events, 1, 2)
	})

	t.Run("reading in reverse", func(t *testing.T) {
		events := readStream(t, store, streamID, eventstore.ReadStreamOptions{
			Direction: eventstore.Reverse,
			Count:     2,
		})

		assertVersions(t, events, 5, 4)
	})
}

// clauseExpectVersionMatch covers the success half of optimistic concurrency: an append
// whose expectation holds must go through.
func clauseExpectVersionMatch(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	appendEvents(t, store, streamID, 3, eventstore.AppendStreamOptions{})

	appendEvents(t, store, streamID, 1, eventstore.AppendStreamOptions{
		ExpectVersion: eventstore.VersionPtr(3),
	})

	events := readStream(t, store, streamID, eventstore.ReadStreamOptions{})
	if len(events) != 4 {
		t.Fatalf("want 4 events after an append with a matching ExpectVersion, got %d", len(events))
	}
}

// clauseExpectVersionMismatch covers the half that makes optimistic concurrency worth
// anything: a stale expectation must be refused, with a typed error carrying both versions
// so a caller can retry.
func clauseExpectVersionMismatch(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	appendEvents(t, store, streamID, 3, eventstore.AppendStreamOptions{})

	err := store.AppendStream(t.Context(), streamID, writableEvents(1), eventstore.AppendStreamOptions{
		ExpectVersion: eventstore.VersionPtr(2),
	})
	if !errors.Is(err, eventstore.StreamVersionMismatchError{}) {
		t.Fatalf("want StreamVersionMismatchError appending with a stale ExpectVersion, got %v", err)
	}

	// Not folded into the errors.Is above: a backend returning a *pointer* to the error
	// satisfies Is (the method has a value receiver) but not As, and skipping the version
	// assertions on that path would make this clause quietly stop checking anything.
	var mismatch eventstore.StreamVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want an error unwrapping to StreamVersionMismatchError, got %T: %v", err, err)
	}

	if mismatch.ExpectedVersion != 2 {
		t.Errorf("want ExpectedVersion 2, got %d", mismatch.ExpectedVersion)
	}

	if mismatch.ActualVersion != 3 {
		t.Errorf("want ActualVersion 3, got %d", mismatch.ActualVersion)
	}

	// A refused append must not be a partial one.
	if events := readStream(t, store, streamID, eventstore.ReadStreamOptions{}); len(events) != 3 {
		t.Errorf("want the stream unchanged at 3 events after a refused append, got %d", len(events))
	}
}

// clauseStreamMustNotExistNew covers create-if-absent, the expectation a caller has no
// version number for.
func clauseStreamMustNotExistNew(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	appendEvents(t, store, streamID, 2, eventstore.AppendStreamOptions{StreamMustNotExist: true})

	if events := readStream(t, store, streamID, eventstore.ReadStreamOptions{}); len(events) != 2 {
		t.Errorf("want 2 events, got %d", len(events))
	}
}

func clauseStreamMustNotExistExisting(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()
	appendEvents(t, store, streamID, 2, eventstore.AppendStreamOptions{})

	err := store.AppendStream(t.Context(), streamID, writableEvents(1), eventstore.AppendStreamOptions{
		StreamMustNotExist: true,
	})
	if !errors.Is(err, eventstore.StreamVersionMismatchError{}) {
		t.Fatalf("want StreamVersionMismatchError appending to an existing stream with StreamMustNotExist, got %v", err)
	}

	if events := readStream(t, store, streamID, eventstore.ReadStreamOptions{}); len(events) != 2 {
		t.Errorf("want the stream unchanged at 2 events after a refused append, got %d", len(events))
	}
}

// clauseMutuallyExclusiveOptions pins that the two expectations are refused together rather
// than one silently winning. The contract does not name an error type here, so this asserts
// only that the append fails.
func clauseMutuallyExclusiveOptions(t *testing.T, store eventstore.Store) {
	streamID := newStreamID()

	err := store.AppendStream(t.Context(), streamID, writableEvents(1), eventstore.AppendStreamOptions{
		ExpectVersion:      eventstore.VersionPtr(0),
		StreamMustNotExist: true,
	})
	if err == nil {
		t.Fatal("want an error appending with both ExpectVersion and StreamMustNotExist, got nil")
	}
}

// newStreamID returns a stream ID unique to one clause, so clauses sharing a store cannot
// observe each other's writes.
func newStreamID() typeid.ID {
	return typeid.NewV4("storetest")
}

const eventType = "eventtype"

// eventData is the payload the suite writes at 0-based position i. Clauses rebuild expected
// payloads through this rather than reading them back off the slice they handed the store.
func eventData(i int) []byte {
	return fmt.Appendf(nil, `{"index":%d}`, i+1)
}

// writableEvents builds n events whose data carries a 1-based index, so a read can be
// checked for both content and order.
func writableEvents(n int) []*eventstore.WritableEvent {
	events := make([]*eventstore.WritableEvent, 0, n)
	for i := range n {
		events = append(events, &eventstore.WritableEvent{
			Type: eventType,
			Data: eventData(i),
		})
	}

	return events
}

func appendEvents(t *testing.T, store eventstore.Store, streamID typeid.ID, n int, opts eventstore.AppendStreamOptions) []*eventstore.WritableEvent {
	t.Helper()

	events := writableEvents(n)
	if err := store.AppendStream(t.Context(), streamID, events, opts); err != nil {
		t.Fatalf("appending %d events: %v", n, err)
	}

	return events
}

func readStream(t *testing.T, store eventstore.Store, streamID typeid.ID, opts eventstore.ReadStreamOptions) []*eventstore.Event {
	t.Helper()

	iter, err := store.ReadStream(t.Context(), streamID, opts)
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}

	t.Cleanup(func() { _ = iter.Close(context.WithoutCancel(t.Context())) })

	events, err := eventstore.ReadAll(t.Context(), iter)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	return events
}

func assertVersions(t *testing.T, events []*eventstore.Event, want ...int64) {
	t.Helper()

	got := make([]int64, 0, len(events))
	for _, event := range events {
		got = append(got, event.StreamVersion)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("want stream versions %v, got %v", want, got)
	}
}

func assertJSONEqual(t *testing.T, index int, want, got []byte) {
	t.Helper()

	var wantValue, gotValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("event %d: unmarshaling appended data: %v", index, err)
	}

	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("event %d: unmarshaling read data %q: %v", index, got, err)
	}

	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Errorf("event %d: want data %s, got %s", index, want, got)
	}
}
