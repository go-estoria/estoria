package snapshotstore_test

import (
	"testing"

	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/snapshotstore"
	eventstreamstore "github.com/go-estoria/estoria/snapshotstore/eventstream"
	"github.com/go-estoria/estoria/snapshotstore/storetest"
)

// Unlike the in-memory snapshot store, this one retains every snapshot ever written, so it
// exercises the MaxVersion clause's non-trivial branch: it must return the newest snapshot
// at or below the bound rather than reporting one absent.
func TestEventStreamStore_AcceptanceTest(t *testing.T) {
	t.Parallel()

	storetest.RunSnapshotStoreSuite(t, func(t *testing.T) snapshotstore.SnapshotStore {
		t.Helper()

		// A fresh backing event store per clause: snapshot streams are derived from the
		// aggregate ID, so clauses cannot collide, but a shared store would also share the
		// global position counter for no benefit.
		eventStore, err := memory.NewEventStore()
		if err != nil {
			t.Fatalf("creating backing event store: %v", err)
		}

		return eventstreamstore.NewEventStreamStore(eventStore)
	})
}
