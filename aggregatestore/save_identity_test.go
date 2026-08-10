package aggregatestore_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/gofrs/uuid/v5"
)

// These tests pin what a save does with the events the event store reports back:
// the store-assigned identities become the aggregate's, and a store that fails to
// report what it wrote is surfaced under the post-append recovery contract.

// TestSave_CopiesStoreAssignedIdentityOntoEvents pins that after a save, the
// aggregate's events carry the ID, version, and timestamp the event store
// assigned — the same values a read of the stream reports — rather than values
// invented on the aggregate's side of the write.
func TestSave_CopiesStoreAssignedIdentityOntoEvents(t *testing.T) {
	t.Parallel()

	eventStore, _ := memory.NewEventStore()
	store := newAccountStore(t, eventStore)

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.Append(fundsDeposited{Amount: 10}, fundsDeposited{Amount: 5})

	// SkipApply leaves the saved events in the apply queue, where the test can
	// observe them; applying would drain them.
	if err := store.Save(t.Context(), aggregate, &aggregatestore.SaveOptions{SkipApply: true}); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	unapplied := aggregate.TestOnlyUnappliedEvents()
	read := readStreamEvents(t, eventStore, aggregate.ID())

	if len(unapplied) != 2 || len(read) != 2 {
		t.Fatalf("want 2 events queued and 2 read, got %d and %d", len(unapplied), len(read))
	}

	for i, event := range unapplied {
		if event.ID.UUID.IsNil() {
			t.Errorf("event %d: want a store-assigned ID, got the zero UUID", i)
		}

		if event.ID != read[i].ID {
			t.Errorf("event %d: want the stored event's ID %s, got %s", i, read[i].ID, event.ID)
		}

		if event.Version != read[i].StreamVersion {
			t.Errorf("event %d: want the stored event's version %d, got %d", i, read[i].StreamVersion, event.Version)
		}

		if event.Timestamp.IsZero() {
			t.Errorf("event %d: want a store-assigned timestamp, got the zero time", i)
		}

		if !event.Timestamp.Equal(read[i].Timestamp) {
			t.Errorf("event %d: want the stored event's timestamp %s, got %s", i, read[i].Timestamp, event.Timestamp)
		}
	}
}

// TestSave_MiscountedWrittenEventsCarriesErrEventsAppended pins the defense
// against a store that appends successfully but fails to report what it wrote:
// the events are facts in storage, so the failure carries ErrEventsAppended and
// the caller recovers by discarding and reloading the aggregate.
func TestSave_MiscountedWrittenEventsCarriesErrEventsAppended(t *testing.T) {
	t.Parallel()

	eventStore, _ := memory.NewEventStore()

	// mockStreamWriter reports success while returning no written events.
	store, err := aggregatestore.New(eventStore, "account", newAccount,
		aggregatestore.WithEventTypes[account](fundsDeposited{}),
		aggregatestore.WithEventStreamWriter[account](mockStreamWriter{}))
	if err != nil {
		t.Fatalf("creating aggregate store: %v", err)
	}

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.Append(fundsDeposited{Amount: 10})

	saveErr := store.Save(t.Context(), aggregate, nil)
	if saveErr == nil {
		t.Fatal("want an error when the store reports fewer written events than were appended, got nil")
	}

	if !errors.Is(saveErr, aggregatestore.ErrEventsAppended) {
		t.Errorf("want the error to carry ErrEventsAppended, got %v", saveErr)
	}

	if !strings.Contains(saveErr.Error(), "0 written events for 1 appended") {
		t.Errorf("want the error to report the miscount, got %q", saveErr.Error())
	}
}
