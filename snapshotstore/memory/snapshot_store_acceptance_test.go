package memory_test

import (
	"testing"

	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/snapshotstore/memory"
	"github.com/go-estoria/estoria/snapshotstore/storetest"
)

// The in-memory store retains only its newest snapshot, which is why the suite's MaxVersion
// clause accepts ErrSnapshotNotFound as an answer.
func TestSnapshotStore_AcceptanceTest(t *testing.T) {
	t.Parallel()

	store := memory.NewSnapshotStore()

	storetest.RunSnapshotStoreSuite(t, func(*testing.T) snapshotstore.SnapshotStore {
		return store
	})
}
