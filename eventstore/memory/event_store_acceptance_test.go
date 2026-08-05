package memory_test

import (
	"testing"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/eventstore/storetest"
)

// The in-memory store is the reference implementation: it defines what the acceptance
// suite's clauses mean. A clause it cannot satisfy is a wrong clause, not a wrong store.
func TestEventStore_AcceptanceTest(t *testing.T) {
	t.Parallel()

	store, err := memory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	storetest.RunEventStoreSuite(t, func(*testing.T) eventstore.Store {
		return store
	})
}
