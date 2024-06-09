package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/serde"
	"github.com/go-estoria/estoria/snapshot"
	"go.jetpack.io/typeid"
)

type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, aggregateID typeid.AnyID, opts snapshot.ReadOptions) (estoria.Snapshot, error)
}

type SnapshotWriter interface {
	WriteSnapshot(ctx context.Context, aggregateID typeid.AnyID, aggregateVersion int64, entityData []byte) error
}

type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.AnyID, aggregateVersion int64, timestamp time.Time) bool
}

type EntitySnapshotSerde[E estoria.Entity] interface {
	Marshal(entity E) ([]byte, error)
	Unmarshal(data []byte, dest *E) error
}

type SnapshottingAggregateStore[E estoria.Entity] struct {
	inner  estoria.AggregateStore[E]
	reader SnapshotReader
	writer SnapshotWriter
	policy SnapshotPolicy
	serde  EntitySnapshotSerde[E]
}

var _ estoria.AggregateStore[estoria.Entity] = (*SnapshottingAggregateStore[estoria.Entity])(nil)

func NewSnapshottingAggregateStore[E estoria.Entity](
	inner estoria.AggregateStore[E],
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
		serde:  serde.JSONEntity[E]{},
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
func (s *SnapshottingAggregateStore[E]) Load(ctx context.Context, aggregateID typeid.AnyID, opts estoria.LoadAggregateOptions) (*estoria.Aggregate[E], error) {
	aggregate, err := s.NewAggregate()
	if err != nil {
		slog.Warn("failed to create new aggregate", "error", err)
		return s.inner.Load(ctx, aggregateID, opts)
	}

	aggregate.SetID(aggregateID)

	if err := s.Hydrate(ctx, aggregate, estoria.HydrateAggregateOptions{
		ToVersion: opts.ToVersion,
	}); err != nil {
		slog.Warn("failed to hydrate aggregate", "error", err)
		return s.inner.Load(ctx, aggregateID, opts)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *SnapshottingAggregateStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.HydrateAggregateOptions) error {
	log := slog.Default().With("aggregate_id", aggregate.ID())
	log.Debug("hydrating aggregate from snapshot", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	snap, err := s.reader.ReadSnapshot(ctx, aggregate.ID(), snapshot.ReadOptions{
		MaxVersion: opts.ToVersion,
	})
	if err != nil {
		slog.Warn("failed to read snapshot", "error", err)
		return s.inner.Hydrate(ctx, aggregate, opts)
	} else if snap == nil {
		slog.Debug("no snapshot found")
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	entity := aggregate.Entity()
	if err := s.serde.Unmarshal(snap.Data(), &entity); err != nil {
		slog.Warn("failed to unmarshal snapshot", "error", err)
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	log.Debug("loaded snapshot", "version", snap.AggregateVersion())

	aggregate.SetEntity(entity)
	aggregate.SetVersion(snap.AggregateVersion())

	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *SnapshottingAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.SaveAggregateOptions) error {
	// defer applying events so a snapshot can be taken at an exact version
	opts.SkipApply = true

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return fmt.Errorf("saving aggregate: %w", err)
	}

	now := time.Now()
	for {
		err := aggregate.ApplyNext(ctx)
		if errors.Is(err, estoria.ErrNoUnappliedEvents) {
			break
		} else if err != nil {
			return fmt.Errorf("applying next event: %w", err)
		}

		if s.policy.ShouldSnapshot(aggregate.ID(), aggregate.Version(), now) {
			slog.Debug("taking snapshot", "aggregate_id", aggregate.ID(), "version", aggregate.Version())
			data, err := s.serde.Marshal(aggregate.Entity())
			if err != nil {
				slog.Error("failed to marshal snapshot", "error", err)
				continue
			}

			if err := s.writer.WriteSnapshot(ctx, aggregate.ID(), aggregate.Version(), data); err != nil {
				slog.Error("failed to write snapshot", "error", err)
				continue
			}
		}
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
