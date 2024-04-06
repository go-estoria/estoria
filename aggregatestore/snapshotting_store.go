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
	store  AggregateStore[E]
	reader SnapshotReader
	writer SnapshotWriter
	policy SnapshotPolicy

	marshalEntitySnapshot   func(entity E) ([]byte, error)
	unmarshalEntitySnapshot func(data []byte, dest E) error
}

var _ AggregateStore[estoria.Entity] = (*SnapshottingAggregateStore[estoria.Entity])(nil)

func NewSnapshottingAggregateStore[E estoria.Entity](
	inner AggregateStore[E],
	reader SnapshotReader,
	writer SnapshotWriter,
	policy SnapshotPolicy,
) *SnapshottingAggregateStore[E] {
	return &SnapshottingAggregateStore[E]{
		store:  inner,
		reader: reader,
		writer: writer,
		policy: policy,

		marshalEntitySnapshot:   func(entity E) ([]byte, error) { return json.Marshal(entity) },
		unmarshalEntitySnapshot: func(data []byte, dest E) error { return json.Unmarshal(data, dest) },
	}
}

// Allow allows an event type to be used with the aggregate store.
func (s *SnapshottingAggregateStore[E]) Allow(prototypes ...estoria.EventData) {
	s.store.Allow(prototypes...)
}

// NewAggregate creates a new aggregate.
func (s *SnapshottingAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
	return s.store.NewAggregate()
}

// Load loads an aggregate by its ID.
func (s *SnapshottingAggregateStore[E]) Load(ctx context.Context, aggregateID typeid.AnyID) (*estoria.Aggregate[E], error) {
	snapshot, err := s.reader.ReadSnapshot(ctx, aggregateID)
	if err != nil {
		slog.Warn("failed to read snapshot", "error", err)
		return s.store.Load(ctx, aggregateID)
	} else if snapshot == nil {
		slog.Debug("no snapshot found")
		return s.store.Load(ctx, aggregateID)
	}

	aggregate, err := s.NewAggregate()
	if err != nil {
		slog.Warn("failed to create new aggregate", "error", err)
		return s.store.Load(ctx, aggregateID)
	}

	entity := aggregate.Entity()
	if err := s.unmarshalEntitySnapshot(snapshot.Data(), entity); err != nil {
		slog.Warn("failed to unmarshal snapshot", "error", err)
		return s.store.Load(ctx, aggregateID)
	}

	slog.Debug("loaded snapshot",
		"aggregate_id", aggregate.ID(),
		"version", snapshot.AggregateVersion(),
		"entity", fmt.Sprintf("%+v", entity),
	)

	aggregate.SetEntity(entity)
	aggregate.SetVersion(snapshot.AggregateVersion())

	if err := s.store.Hydrate(ctx, aggregate); err != nil {
		slog.Warn("failed to hydrate aggregate", "error", err)
		return s.store.Load(ctx, aggregateID)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *SnapshottingAggregateStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E]) error {
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
