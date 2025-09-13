package memory

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
)

// A RetentionPolicy determines which snapshots the store should retain.
type RetentionPolicy interface {
	// ShouldRetain returns true if the snapshot should be retained.
	ShouldRetain(snap *snapshotstore.AggregateSnapshot, snapshotIndex, totalSnapshots int64) bool
}

type SnapshotMarshaler interface {
	MarshalSnapshot(snap *snapshotstore.AggregateSnapshot) ([]byte, error)
	UnmarshalSnapshot(data []byte, dest *snapshotstore.AggregateSnapshot) error
}

type SnapshotStore struct {
	snapshots map[typeid.ID][]*snapshotstore.AggregateSnapshot
	marshaler SnapshotMarshaler
	retention RetentionPolicy
}

func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{
		snapshots: map[typeid.ID][]*snapshotstore.AggregateSnapshot{},
		marshaler: snapshotstore.JSONSnapshotMarshaler{},
		retention: snapshotstore.MaxSnapshotsRetentionPolicy{N: 1},
	}
}

func (s *SnapshotStore) ReadSnapshot(_ context.Context, aggregateID typeid.ID, opts snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
	estoria.GetLogger().Debug("finding snapshot", "aggregate_id", aggregateID)

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

	snapshots, ok := s.snapshots[snap.AggregateID]
	if !ok {
		s.snapshots[snap.AggregateID] = []*snapshotstore.AggregateSnapshot{}
		snapshots = s.snapshots[snap.AggregateID]
	}

	if len(snapshots) > 0 {
		if snap.AggregateVersion <= snapshots[len(snapshots)-1].AggregateVersion {
			return errors.New("aggregate version is older than the most recent snapshot version")
		}
	}

	s.snapshots[snap.AggregateID] = append(s.snapshots[snap.AggregateID], snap)

	retained := []*snapshotstore.AggregateSnapshot{}
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

type JSONSnapshotMarshaler struct{}

func (m JSONSnapshotMarshaler) MarshalSnapshot(snap *snapshotstore.AggregateSnapshot) ([]byte, error) {
	return json.Marshal(snap)
}

func (m JSONSnapshotMarshaler) UnmarshalSnapshot(data []byte, dest *snapshotstore.AggregateSnapshot) error {
	return json.Unmarshal(data, dest)
}
