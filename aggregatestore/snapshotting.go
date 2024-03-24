package aggregatestore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, aggregateID typeid.AnyID) (estoria.Snapshot, error)
}

type SnapshotWriter interface {
	WriteSnapshot(ctx context.Context, aggregateID typeid.AnyID, aggregateVersion int64, data []byte) error
}

type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.AnyID, aggregateVersion int64, timestamp time.Time) bool
}

type SnapshottingAggregateStore[E estoria.Entity] struct {
	store  *estoria.AggregateStore[E]
	reader SnapshotReader
	writer SnapshotWriter
	policy SnapshotPolicy

	marshalEntitySnapshot   func(entity E) ([]byte, error)
	unmarshalEntitySnapshot func(data []byte, dest E) error
}

func NewSnapshottingAggregateStore[E estoria.Entity](
	store *estoria.AggregateStore[E],
	snapshotReader SnapshotReader,
	snapshotWriter SnapshotWriter,
	snapshotPolicy SnapshotPolicy,
) *SnapshottingAggregateStore[E] {
	return &SnapshottingAggregateStore[E]{
		store:  store,
		reader: snapshotReader,
		writer: snapshotWriter,
		policy: snapshotPolicy,

		marshalEntitySnapshot:   func(entity E) ([]byte, error) { return json.Marshal(entity) },
		unmarshalEntitySnapshot: func(data []byte, dest E) error { return json.Unmarshal(data, dest) },
	}
}

func (s *SnapshottingAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
	return s.store.NewAggregate()
}

// Load loads an aggregate by its ID.
func (s *SnapshottingAggregateStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E]) error {
	snapshot, err := s.reader.ReadSnapshot(ctx, aggregate.ID())
	if err != nil {
		slog.Warn("failed to read snapshot", "error", err)
		return s.store.Hydrate(ctx, aggregate)
	} else if snapshot == nil {
		slog.Debug("no snapshot found")
		return s.store.Hydrate(ctx, aggregate)
	}

	entity := aggregate.Entity()
	if err := s.unmarshalEntitySnapshot(snapshot.Data(), entity); err != nil {
		return fmt.Errorf("unmarshalling snapshot: %w", err)
	}

	slog.Debug("loaded snapshot",
		"aggregate_id", aggregate.ID(),
		"version", snapshot.AggregateVersion(),
		"entity", fmt.Sprintf("%+v", entity),
	)

	aggregate.SetEntity(entity)
	aggregate.SetVersion(snapshot.AggregateVersion())

	return s.store.Hydrate(ctx, aggregate)
}

// Save saves an aggregate.
func (s *SnapshottingAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E]) error {
	if err := s.store.Save(ctx, aggregate); err != nil {
		return fmt.Errorf("saving aggregate: %w", err)
	}

	if !s.policy.ShouldSnapshot(aggregate.ID(), aggregate.Version(), time.Now()) {
		return nil
	}

	data, err := s.marshalEntitySnapshot(aggregate.Entity())
	if err != nil {
		return fmt.Errorf("marshalling snapshot: %w", err)
	}

	if err := s.writer.WriteSnapshot(ctx, aggregate.ID(), aggregate.Version(), data); err != nil {
		return fmt.Errorf("writing snapshot: %w", err)
	}

	return nil
}
