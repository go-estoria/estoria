package memory

import (
	"context"
	"sync"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type Snapshotter[E estoria.Entity] struct {
	Snapshots map[string][]estoria.Snapshot[E]

	mu sync.RWMutex
}

func NewSnapshotter[E estoria.Entity]() *Snapshotter[E] {
	return &Snapshotter[E]{}
}

func (s *Snapshotter[E]) LoadSnapshot(ctx context.Context, aggregateID typeid.AnyID) (estoria.Snapshot[E], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.Snapshots == nil {
		return nil, estoria.ErrNoSnapshotFound
	}

	snapshots, ok := s.Snapshots[aggregateID.String()]
	if !ok || len(snapshots) == 0 {
		return nil, estoria.ErrNoSnapshotFound
	}

	// for now, just return the latest snapshot
	return snapshots[len(snapshots)-1], nil
}

func (s *Snapshotter[E]) SaveSnapshot(ctx context.Context, snapshot estoria.Snapshot[E]) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Snapshots == nil {
		s.Snapshots = map[string][]estoria.Snapshot[E]{}
	}

	aggregateID := snapshot.AggregateID().String()
	s.Snapshots[aggregateID] = append(s.Snapshots[aggregateID], snapshot)
	return nil
}
