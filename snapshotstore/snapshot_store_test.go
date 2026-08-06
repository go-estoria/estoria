package snapshotstore_test

import (
	"testing"
	"time"

	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
)

// TestEventCountSnapshotPolicy covers the policy that decides when a SnapshottingStore takes
// a snapshot. Getting N wrong is expensive in both directions: too eager writes a snapshot
// per save, too lazy replays the whole stream on every cold load.
func TestEventCountSnapshotPolicy(t *testing.T) {
	t.Parallel()

	aggregateID := typeid.NewV4("mockentity")

	t.Run("never snapshots when N is zero", func(t *testing.T) {
		t.Parallel()

		policy := snapshotstore.EventCountSnapshotPolicy{N: 0}
		for version := int64(0); version <= 10; version++ {
			if policy.ShouldSnapshot(aggregateID, version, time.Time{}) {
				t.Errorf("want no snapshot at version %d with N=0", version)
			}
		}
	})

	t.Run("snapshots every Nth version", func(t *testing.T) {
		t.Parallel()

		policy := snapshotstore.EventCountSnapshotPolicy{N: 3}
		for version, want := range map[int64]bool{
			1: false, 2: false, 3: true,
			4: false, 5: false, 6: true,
			7: false, 8: false, 9: true,
		} {
			if got := policy.ShouldSnapshot(aggregateID, version, time.Time{}); got != want {
				t.Errorf("version %d: want %v, got %v", version, want, got)
			}
		}
	})

	t.Run("snapshots at every version when N is one", func(t *testing.T) {
		t.Parallel()

		policy := snapshotstore.EventCountSnapshotPolicy{N: 1}
		for version := int64(1); version <= 5; version++ {
			if !policy.ShouldSnapshot(aggregateID, version, time.Time{}) {
				t.Errorf("want a snapshot at version %d with N=1", version)
			}
		}
	})
}

// TestMaxSnapshotsRetentionPolicy covers the retention policy the in-memory snapshot store
// uses. It is called once per snapshot with that snapshot's index and the total, so the
// decision is positional rather than version-based.
func TestMaxSnapshotsRetentionPolicy(t *testing.T) {
	t.Parallel()

	t.Run("retains everything when N is zero", func(t *testing.T) {
		t.Parallel()

		policy := snapshotstore.MaxSnapshotsRetentionPolicy{N: 0}
		for index := range int64(5) {
			if !policy.ShouldRetain(nil, index, 5) {
				t.Errorf("want index %d retained with N=0", index)
			}
		}
	})

	t.Run("retains only the newest N", func(t *testing.T) {
		t.Parallel()

		policy := snapshotstore.MaxSnapshotsRetentionPolicy{N: 2}
		for index, want := range map[int64]bool{0: false, 1: false, 2: false, 3: true, 4: true} {
			if got := policy.ShouldRetain(nil, index, 5); got != want {
				t.Errorf("index %d of 5: want %v, got %v", index, want, got)
			}
		}
	})

	t.Run("retains everything when N exceeds the count", func(t *testing.T) {
		t.Parallel()

		policy := snapshotstore.MaxSnapshotsRetentionPolicy{N: 10}
		for index := range int64(3) {
			if !policy.ShouldRetain(nil, index, 3) {
				t.Errorf("want index %d retained when N exceeds the total", index)
			}
		}
	})
}

// TestMinAggregateVersionRetentionPolicy covers the version-based alternative, which decides
// per snapshot rather than by position.
func TestMinAggregateVersionRetentionPolicy(t *testing.T) {
	t.Parallel()

	policy := snapshotstore.MinAggregateVersionRetentionPolicy{MinVersion: 5}

	for version, want := range map[int64]bool{3: false, 4: false, 5: true, 6: true} {
		snap := &snapshotstore.AggregateSnapshot{AggregateVersion: version}

		// Index and total are ignored by this policy; passing values that would flip a
		// positional policy keeps that explicit.
		if got := policy.ShouldRetain(snap, 0, 100); got != want {
			t.Errorf("version %d: want %v, got %v", version, want, got)
		}
	}
}
