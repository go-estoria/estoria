package memory

import (
	"context"
	"errors"
	"sync"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
)

// A RetentionPolicy determines which snapshots the store should retain.
type RetentionPolicy interface {
	// ShouldRetain returns true if the snapshot should be retained.
	ShouldRetain(snap *snapshotstore.AggregateSnapshot, snapshotIndex, totalSnapshots int64) bool
}

type SnapshotStore struct {
	snapshots map[typeid.ID][]*snapshotstore.AggregateSnapshot
	mu        sync.RWMutex
	retention RetentionPolicy
}

func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{
		snapshots: map[typeid.ID][]*snapshotstore.AggregateSnapshot{},
		retention: snapshotstore.MaxSnapshotsRetentionPolicy{N: 1},
	}
}

func (s *SnapshotStore) ReadSnapshot(_ context.Context, aggregateID typeid.ID, opts snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
	estoria.GetLogger().Debug("finding snapshot", "aggregate_id", aggregateID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshots, ok := s.snapshots[aggregateID]
	if !ok || len(snapshots) == 0 {
		return nil, snapshotstore.ErrSnapshotNotFound
	}

	if opts.MaxVersion > 0 {
		for i := len(snapshots) - 1; i >= 0; i-- {
			if snap := snapshots[i]; snap.AggregateVersion <= opts.MaxVersion {
				estoria.GetLogger().Debug("found snapshot", "aggregate_id", snap.AggregateID, "aggregate_version", snap.AggregateVersion)
				return snapshots[i], nil
			}
		}

		return nil, snapshotstore.ErrSnapshotNotFound
	}

	snap := snapshots[len(snapshots)-1]
	estoria.GetLogger().Debug("found snapshot", "aggregate_id", snap.AggregateID, "aggregate_version", snap.AggregateVersion)
	return snap, nil
}

func (s *SnapshotStore) WriteSnapshot(_ context.Context, snap *snapshotstore.AggregateSnapshot) error {
	estoria.GetLogger().Debug("writing snapshot",
		"aggregate_id", snap.AggregateID,
		"aggregate_version",
		snap.AggregateVersion,
		"data_length", len(snap.Data))

	s.mu.Lock()
	defer s.mu.Unlock()

	snapshots := s.snapshots[snap.AggregateID]

	if len(snapshots) > 0 {
		if snap.AggregateVersion <= snapshots[len(snapshots)-1].AggregateVersion {
			return errors.New("aggregate version is older than the most recent snapshot version")
		}
	}

	snapshots = append(snapshots, snap)

	retained := make([]*snapshotstore.AggregateSnapshot, 0, len(snapshots))
	for i, candidate := range snapshots {
		if !s.retention.ShouldRetain(candidate, int64(i), int64(len(snapshots))) {
			estoria.GetLogger().Debug("deleting snapshot per retention policy",
				"aggregate_id", candidate.AggregateID,
				"aggregate_version", candidate.AggregateVersion)
			continue
		}

		retained = append(retained, candidate)
	}

	s.snapshots[snap.AggregateID] = retained

	estoria.GetLogger().Debug("wrote snapshot", "aggregate_id", snap.AggregateID, "aggregate_version", snap.AggregateVersion)

	return nil
}
