package aggregatestore

import (
	"context"
	"encoding/json"
	"errors"
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
	WriteSnapshot(ctx context.Context, aggregateID typeid.AnyID, aggregateVersion int64, entityData []byte) error
}

type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.AnyID, aggregateVersion int64, timestamp time.Time) bool
}

type EntitySnapshotSerde[E estoria.Entity] interface {
	MarshalEntitySnapshot(entity E) ([]byte, error)
	UnmarshalEntitySnapshot(data []byte, dest *E) error
}

type SnapshottingAggregateStore[E estoria.Entity] struct {
	inner  AggregateStore[E]
	reader SnapshotReader
	writer SnapshotWriter
	policy SnapshotPolicy
	serde  EntitySnapshotSerde[E]
}

var _ AggregateStore[estoria.Entity] = (*SnapshottingAggregateStore[estoria.Entity])(nil)

func NewSnapshottingAggregateStore[E estoria.Entity](
	inner AggregateStore[E],
	reader SnapshotReader,
	writer SnapshotWriter,
	policy SnapshotPolicy,
	opts ...SnapshottingAggregateStoreOption[E],
) *SnapshottingAggregateStore[E] {
	store := &SnapshottingAggregateStore[E]{
		inner:  inner,
		reader: reader,
		writer: writer,
		policy: policy,
		serde:  JSONEntitySnapshotSerde[E]{},
	}

	for _, opt := range opts {
		opt(store)
	}

	return store
}

// Allow allows an event type to be used with the aggregate store.
func (s *SnapshottingAggregateStore[E]) Allow(prototypes ...estoria.EventData) {
	s.inner.Allow(prototypes...)
}

// NewAggregate creates a new aggregate.
func (s *SnapshottingAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
	return s.inner.NewAggregate()
}

// Load loads an aggregate by its ID.
func (s *SnapshottingAggregateStore[E]) Load(ctx context.Context, aggregateID typeid.AnyID) (*estoria.Aggregate[E], error) {
	snapshot, err := s.reader.ReadSnapshot(ctx, aggregateID)
	if err != nil {
		slog.Warn("failed to read snapshot", "error", err)
		return s.inner.Load(ctx, aggregateID)
	} else if snapshot == nil {
		slog.Debug("no snapshot found")
		return s.inner.Load(ctx, aggregateID)
	}

	aggregate, err := s.NewAggregate()
	if err != nil {
		slog.Warn("failed to create new aggregate", "error", err)
		return s.inner.Load(ctx, aggregateID)
	}

	aggregate.SetID(aggregateID)

	entity := aggregate.Entity()
	if err := s.serde.UnmarshalEntitySnapshot(snapshot.EntityData(), &entity); err != nil {
		slog.Warn("failed to unmarshal snapshot", "error", err)
		return s.inner.Load(ctx, aggregateID)
	}

	slog.Debug("loaded snapshot",
		"aggregate_id", aggregate.ID(),
		"version", snapshot.AggregateVersion(),
		"entity", fmt.Sprintf("%+v", entity),
	)

	aggregate.SetEntity(entity)
	aggregate.SetVersion(snapshot.AggregateVersion())

	if err := s.inner.Hydrate(ctx, aggregate); err != nil {
		slog.Warn("failed to hydrate aggregate", "error", err)
		return s.inner.Load(ctx, aggregateID)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *SnapshottingAggregateStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E]) error {
	return s.inner.Hydrate(ctx, aggregate)
}

// Save saves an aggregate.
func (s *SnapshottingAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.SaveAggregateOptions) error {

	// defer applying events so a snapshot can be taken at an exact version if needed
	opts.SkipApply = true

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return fmt.Errorf("saving aggregate: %w", err)
	}

	slog.Debug("saved aggregate", "aggregate_id", aggregate.ID(), "version", aggregate.Version())

	now := time.Now()
	for {
		err := aggregate.ApplyNext(ctx)
		if errors.Is(err, estoria.ErrNoUnappliedEvents) {
			break
		} else if err != nil {
			return fmt.Errorf("applying next event: %w", err)
		}

		if s.policy.ShouldSnapshot(aggregate.ID(), aggregate.Version(), now) {
			data, err := s.serde.MarshalEntitySnapshot(aggregate.Entity())
			if err != nil {
				slog.Error("failed to marshal snapshot", "error", err)
				continue
			}

			if err := s.writer.WriteSnapshot(ctx, aggregate.ID(), aggregate.Version(), data); err != nil {
				slog.Error("failed to write snapshot", "error", err)
				continue
			}
		}

		slog.Debug("applied event", "aggregate_id", aggregate.ID(), "version", aggregate.Version())
	}

	return nil
}

type SnapshottingAggregateStoreOption[E estoria.Entity] func(*SnapshottingAggregateStore[E]) error

func WithSnapshotSerde[E estoria.Entity](serde EntitySnapshotSerde[E]) SnapshottingAggregateStoreOption[E] {
	return func(s *SnapshottingAggregateStore[E]) error {
		s.serde = serde
		return nil
	}
}

type JSONEntitySnapshotSerde[E estoria.Entity] struct{}

func (JSONEntitySnapshotSerde[E]) MarshalEntitySnapshot(entity E) ([]byte, error) {
	return json.Marshal(entity)
}

func (JSONEntitySnapshotSerde[E]) UnmarshalEntitySnapshot(data []byte, dest *E) error {
	return json.Unmarshal(data, dest)
}
