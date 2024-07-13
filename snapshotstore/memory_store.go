package snapshotstore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

type MemorySnapshotStore struct {
	snapshots map[typeid.UUID][]*AggregateSnapshot
	marshaler estoria.Marshaler[AggregateSnapshot, *AggregateSnapshot]
	retention RetentionPolicy
}

func NewMemorySnapshotStore() *MemorySnapshotStore {
	return &MemorySnapshotStore{
		snapshots: map[typeid.UUID][]*AggregateSnapshot{},
		marshaler: estoria.JSONMarshaler[AggregateSnapshot]{},
		retention: MaxSnapshotsRetentionPolicy{N: 0},
	}
}

func (s *MemorySnapshotStore) ReadSnapshot(ctx context.Context, aggregateID typeid.UUID, opts ReadSnapshotOptions) (*AggregateSnapshot, error) {
	slog.Debug("finding snapshot", "aggregate_id", aggregateID)

	snapshots, ok := s.snapshots[aggregateID]
	if !ok || len(snapshots) == 0 {
		return nil, nil
	}

	if opts.MaxVersion > 0 {
		for i := len(snapshots) - 1; i >= 0; i-- {
			if snapshots[i].AggregateVersion <= opts.MaxVersion {
				return snapshots[i], nil
			}
		}

		return nil, nil
	}

	return snapshots[len(snapshots)-1], nil
}

func (s *MemorySnapshotStore) WriteSnapshot(ctx context.Context, snap *AggregateSnapshot) error {
	slog.Debug("writing snapshot",
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

	slog.Debug("wrote snapshot", "aggregate_id", snap.AggregateID, "aggregate_version", snap.AggregateVersion)

	return nil
}
