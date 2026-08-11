package memory_test

import (
	"testing"

	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/memory"
	"github.com/go-estoria/estoria/projection/storetest"
)

// The in-memory store is the reference implementation: it defines what the acceptance
// suite's clauses mean. A clause it cannot satisfy is a wrong clause, not a wrong store.
func TestCheckpointStore_AcceptanceTest(t *testing.T) {
	t.Parallel()

	store := memory.NewCheckpointStore()

	storetest.RunCheckpointStoreSuite(t, func(*testing.T) projection.CheckpointStore {
		return store
	})
}
