package snapshotstore

import (
	"context"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

// A RetentionPolicy determines which snapshots the store should retain.
type RetentionPolicy interface {
	// ShouldRetain returns true if the snapshot should be retained.
	ShouldRetain(snap *AggregateSnapshot, snapshotIndex, totalSnapshots int64) bool
}

type MemoryStore struct {
	snapshots map[typeid.UUID][]*AggregateSnapshot
	marshaler estoria.Marshaler[AggregateSnapshot, *AggregateSnapshot]
	retention RetentionPolicy
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		snapshots: map[typeid.UUID][]*AggregateSnapshot{},
		marshaler: estoria.JSONMarshaler[AggregateSnapshot]{},
		retention: MaxSnapshotsRetentionPolicy{N: 1},
	}
}

func (s *MemoryStore) ReadSnapshot(ctx context.Context, aggregateID typeid.UUID, opts ReadSnapshotOptions) (*AggregateSnapshot, error) {
	estoria.GetLogger().Debug("finding snapshot", "aggregate_id", aggregateID)

	snapshots, ok := s.snapshots[aggregateID]
	if !ok || len(snapshots) == 0 {
		estoria.GetLogger().Debug("no snapshots found", "aggregate_id", aggregateID)
		return nil, nil
	}

	if opts.MaxVersion > 0 {
		for i := len(snapshots) - 1; i >= 0; i-- {
			if snap := snapshots[i]; snap.AggregateVersion <= opts.MaxVersion {
				estoria.GetLogger().Debug("found snapshot", "aggregate_id", snap.AggregateID, "aggregate_version", snap.AggregateVersion)
				return snapshots[i], nil
			}
		}

		estoria.GetLogger().Debug("no snapshots found within version range", "aggregate_id", aggregateID, "max_version", opts.MaxVersion)
		return nil, nil
	}

	snap := snapshots[len(snapshots)-1]
	estoria.GetLogger().Debug("found snapshot", "aggregate_id", snap.AggregateID, "aggregate_version", snap.AggregateVersion)
	return snap, nil
}

func (s *MemoryStore) WriteSnapshot(ctx context.Context, snap *AggregateSnapshot) error {
	estoria.GetLogger().Debug("writing snapshot",
		"aggregate_id", snap.AggregateID,
		"aggregate_version",
		snap.AggregateVersion,
		"data_length", len(snap.Data))

	snapshots, ok := s.snapshots[snap.AggregateID]
	if !ok {
		s.snapshots[snap.AggregateID] = []*AggregateSnapshot{}
		snapshots = s.snapshots[snap.AggregateID]
	}

	if len(snapshots) > 0 {
		if snap.AggregateVersion <= snapshots[len(snapshots)-1].AggregateVersion {
			return fmt.Errorf("aggregate version is older than the most recent snapshot version")
		}
	}

	s.snapshots[snap.AggregateID] = append(s.snapshots[snap.AggregateID], snap)

	retained := []*AggregateSnapshot{}
	for i, snap := range s.snapshots[snap.AggregateID] {
		if !s.retention.ShouldRetain(snap, int64(i), int64(len(s.snapshots[snap.AggregateID]))) {
			estoria.GetLogger().Debug("deleting snapshot per retention policy", "aggregate_id", snap.AggregateID, "aggregate_version", snap.AggregateVersion)
			continue
		}

		retained = append(retained, snap)
	}

	s.snapshots[snap.AggregateID] = retained

	estoria.GetLogger().Debug("wrote snapshot", "aggregate_id", snap.AggregateID, "aggregate_version", snap.AggregateVersion)

	return nil
}
