package storetest

import (
	"errors"
	"testing"

	"github.com/go-estoria/estoria/eventstore"
)

// A DeleterStore is the capability set the stream deleter suite exercises: it
// appends and reads through the Store half and deletes through the
// StreamDeleter half.
type DeleterStore interface {
	eventstore.Store
	eventstore.StreamDeleter
}

// NewDeleterStoreFunc returns a delete-capable store for one clause of the
// stream deleter suite to use. Implementations may return the same store on
// every call; every clause uses freshly generated stream IDs.
type NewDeleterStoreFunc func(t *testing.T) DeleterStore

// RunStreamDeleterSuite runs the stream deleter acceptance suite against
// stores returned by newStore, reporting each clause as its own named subtest.
func RunStreamDeleterSuite(t *testing.T, newStore NewDeleterStoreFunc) {
	t.Helper()

	for _, clause := range []struct {
		name string
		run  func(t *testing.T, store DeleterStore)
	}{
		{"deletes a stream without touching other streams", clauseDeletesAStream},
		{"reports ErrStreamNotFound deleting a stream that was never written", clauseDeleteNeverWritten},
		{"makes a deleted stream's ID reusable from version 1", clauseDeletedStreamIDReusable},
		{"truncates events at or below ToVersion, retaining later versions", clauseTruncatesToVersion},
		{"preserves the version counter of a stream truncated empty", clauseTruncationPreservesTip},
	} {
		t.Run(clause.name, func(t *testing.T) {
			clause.run(t, newStore(t))
		})
	}
}

// clauseDeletesAStream pins full deletion: the stream reads as never written
// afterward, and deletion is scoped to the one stream it was asked for.
func clauseDeletesAStream(t *testing.T, store DeleterStore) {
	deleted := newStreamID()
	bystander := newStreamID()

	appendEvents(t, store, deleted, 3, eventstore.AppendStreamOptions{})
	appendEvents(t, store, bystander, 2, eventstore.AppendStreamOptions{})

	if err := store.DeleteStream(t.Context(), deleted, eventstore.DeleteStreamOptions{}); err != nil {
		t.Fatalf("deleting stream: %v", err)
	}

	if _, err := store.ReadStream(t.Context(), deleted, eventstore.ReadStreamOptions{}); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Errorf("want ErrStreamNotFound reading a deleted stream, got %v", err)
	}

	if events := readStream(t, store, bystander, eventstore.ReadStreamOptions{}); len(events) != 2 {
		t.Errorf("want the bystander stream's 2 events intact, got %d", len(events))
	}
}

// clauseDeleteNeverWritten pins the absent case: deletion answers about a
// stream that exists, and one that never did is reported, not ignored.
func clauseDeleteNeverWritten(t *testing.T, store DeleterStore) {
	if err := store.DeleteStream(t.Context(), newStreamID(), eventstore.DeleteStreamOptions{}); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Errorf("want ErrStreamNotFound deleting a stream that was never written, got %v", err)
	}
}

// clauseDeletedStreamIDReusable pins that full deletion releases the ID: a
// subsequent append starts a new stream at version 1, as if the ID had never
// been used.
func clauseDeletedStreamIDReusable(t *testing.T, store DeleterStore) {
	streamID := newStreamID()

	appendEvents(t, store, streamID, 3, eventstore.AppendStreamOptions{})

	if err := store.DeleteStream(t.Context(), streamID, eventstore.DeleteStreamOptions{}); err != nil {
		t.Fatalf("deleting stream: %v", err)
	}

	written, err := store.AppendStream(t.Context(), streamID, writableEvents(1), eventstore.AppendStreamOptions{})
	if err != nil {
		t.Fatalf("appending to a deleted stream's ID: %v", err)
	}

	if len(written) != 1 || written[0].StreamVersion != 1 {
		t.Fatalf("want 1 written event at version 1, got %d at version %d", len(written), written[0].StreamVersion)
	}

	events := readStream(t, store, streamID, eventstore.ReadStreamOptions{})
	if len(events) != 1 || events[0].StreamVersion != 1 {
		t.Errorf("want 1 readable event at version 1, got %d events", len(events))
	}
}

// clauseTruncatesToVersion pins truncation: events at or below the bound are
// removed, and the events that remain keep the versions they were written
// with — stream versions are facts, not indexes to renumber.
func clauseTruncatesToVersion(t *testing.T, store DeleterStore) {
	streamID := newStreamID()

	appendEvents(t, store, streamID, 5, eventstore.AppendStreamOptions{})

	if err := store.DeleteStream(t.Context(), streamID, eventstore.DeleteStreamOptions{ToVersion: 2}); err != nil {
		t.Fatalf("truncating stream: %v", err)
	}

	events := readStream(t, store, streamID, eventstore.ReadStreamOptions{})
	if len(events) != 3 {
		t.Fatalf("want 3 events after truncating through version 2, got %d", len(events))
	}

	for i, event := range events {
		if want := int64(i + 3); event.StreamVersion != want {
			t.Errorf("event %d: want retained version %d, got %d", i, want, event.StreamVersion)
		}

		assertJSONEqual(t, i, eventData(i+2), event.Data)
	}

	// A version-bounded read still addresses events by version, not position in
	// what remains — the path a snapshot-hydrating aggregate takes over a
	// truncated stream.
	filtered := readStream(t, store, streamID, eventstore.ReadStreamOptions{AfterVersion: 4})
	if len(filtered) != 1 || filtered[0].StreamVersion != 5 {
		t.Errorf("want a read after version 4 to yield exactly version 5, got %d events", len(filtered))
	}
}

// clauseTruncationPreservesTip pins the difference between truncation and
// deletion: a stream truncated through its tip is empty but still exists — an
// unfiltered read yields an empty iterator, not ErrStreamNotFound — and its
// version counter survives, so the next append continues from the old tip
// rather than restarting at 1.
func clauseTruncationPreservesTip(t *testing.T, store DeleterStore) {
	streamID := newStreamID()

	appendEvents(t, store, streamID, 3, eventstore.AppendStreamOptions{})

	// A bound beyond the tip truncates everything the stream has.
	if err := store.DeleteStream(t.Context(), streamID, eventstore.DeleteStreamOptions{ToVersion: 10}); err != nil {
		t.Fatalf("truncating stream: %v", err)
	}

	iter, err := store.ReadStream(t.Context(), streamID, eventstore.ReadStreamOptions{})
	if err != nil {
		t.Fatalf("want a readable iterator for a stream truncated empty, got error: %v", err)
	}

	defer iter.Close(t.Context())

	if _, err := iter.Next(t.Context()); !errors.Is(err, eventstore.ErrEndOfEventStream) {
		t.Errorf("want ErrEndOfEventStream reading a stream truncated empty, got %v", err)
	}

	written, err := store.AppendStream(t.Context(), streamID, writableEvents(1), eventstore.AppendStreamOptions{})
	if err != nil {
		t.Fatalf("appending to a stream truncated empty: %v", err)
	}

	if len(written) != 1 || written[0].StreamVersion != 4 {
		t.Fatalf("want 1 written event continuing at version 4, got %d at version %d", len(written), written[0].StreamVersion)
	}

	events := readStream(t, store, streamID, eventstore.ReadStreamOptions{})
	if len(events) != 1 || events[0].StreamVersion != 4 {
		t.Errorf("want 1 readable event at version 4, got %d events", len(events))
	}
}
