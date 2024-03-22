package estoria

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.jetpack.io/typeid"
)

type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, aggregateID typeid.AnyID) (Snapshot, error)
}

type SnapshotWriter interface {
	WriteSnapshot(ctx context.Context, aggregateID typeid.AnyID, aggregateVersion int64, data []byte) error
}

type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.AnyID, aggregateVersion int64, timestamp time.Time) bool
}

// An SnapshotStore is used to read and write snapshots of aggregates.
type SnapshotStore[E Entity] struct {
	reader SnapshotReader
	writer SnapshotWriter
	policy SnapshotPolicy
}

func NewSnapshotStore[E Entity](
	reader SnapshotReader,
	writer SnapshotWriter,
	policy SnapshotPolicy,
) *SnapshotStore[E] {

	return &SnapshotStore[E]{
		reader: reader,
		writer: writer,
		policy: policy,
	}
}

func (s *SnapshotStore[E]) AggregateLoadHook() HookFunc[E] {
	return func(ctx context.Context, aggregate *Aggregate[E]) error {
		snapshot, err := s.reader.ReadSnapshot(ctx, aggregate.ID())
		if err != nil {
			slog.Warn("failed to read snapshot", "error", err)
			return nil
		} else if snapshot == nil {
			slog.Debug("no snapshot found")
			return nil
		}

		entity := aggregate.Entity()
		if err := json.Unmarshal(snapshot.Data(), &entity); err != nil {
			return fmt.Errorf("unmarshalling snapshot: %w", err)
		}

		aggregate.SetEntity(entity)
		aggregate.SetVersion(snapshot.AggregateVersion())

		return nil
	}
}

func (s *SnapshotStore[E]) AggregateSaveHook() HookFunc[E] {
	return func(ctx context.Context, aggregate *Aggregate[E]) error {
		if !s.policy.ShouldSnapshot(aggregate.ID(), aggregate.Version(), time.Now()) {
			return nil
		}

		entity := aggregate.Entity()
		data, err := json.Marshal(entity)
		if err != nil {
			return fmt.Errorf("marshalling snapshot: %w", err)
		}

		if err := s.writer.WriteSnapshot(ctx, aggregate.ID(), aggregate.Version(), data); err != nil {
			return fmt.Errorf("writing snapshot: %w", err)
		}

		return nil
	}
}
