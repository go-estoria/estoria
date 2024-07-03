package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
)

type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, aggregateID typeid.TypeID, opts snapshotstore.ReadOptions) (*estoria.AggregateSnapshot, error)
}

type SnapshotWriter interface {
	WriteSnapshot(ctx context.Context, snap *estoria.AggregateSnapshot) error
}

type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.TypeID, aggregateVersion int64, timestamp time.Time) bool
}

type SnapshottingAggregateStore[E estoria.Entity] struct {
	inner     estoria.AggregateStore[E]
	reader    SnapshotReader
	writer    SnapshotWriter
	policy    SnapshotPolicy
	marshaler estoria.EntityMarshaler[E]
	log       *slog.Logger
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
		inner:     inner,
		reader:    reader,
		writer:    writer,
		policy:    policy,
		marshaler: estoria.JSONEntityMarshaler[E]{},
		log:       slog.Default().WithGroup("snapshottingaggregatestore"),
	}

	for _, opt := range opts {
		opt(store)
	}

	return store
}

// NewAggregate creates a new aggregate.
func (s *SnapshottingAggregateStore[E]) NewAggregate(id typeid.TypeID) (*estoria.Aggregate[E], error) {
	return s.inner.NewAggregate(id)
}

// Load loads an aggregate by its ID.
func (s *SnapshottingAggregateStore[E]) Load(ctx context.Context, aggregateID typeid.TypeID, opts estoria.LoadAggregateOptions) (*estoria.Aggregate[E], error) {
	s.log.Debug("loading aggregate", "aggregate_id", aggregateID)
	aggregate, err := s.NewAggregate(aggregateID)
	if err != nil {
		slog.Warn("failed to create new aggregate", "error", err)
		return s.inner.Load(ctx, aggregateID, opts)
	}

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
	log := s.log.With("aggregate_id", aggregate.ID())
	log.Debug("hydrating aggregate from snapshot", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	snap, err := s.reader.ReadSnapshot(ctx, aggregate.ID(), snapshotstore.ReadOptions{
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
	if err := s.marshaler.Unmarshal(snap.Data, &entity); err != nil {
		slog.Warn("failed to unmarshal snapshot", "error", err)
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	if entity.EntityID() != aggregate.ID() {
		slog.Warn("snapshot entity ID does not match aggregate ID", "snapshot_entity_id", entity.EntityID(), "aggregate_id", aggregate.ID())
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	log.Debug("loaded snapshot", "version", snap.AggregateVersion)

	aggregate.SetEntity(entity)
	aggregate.SetVersion(snap.AggregateVersion)

	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *SnapshottingAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.SaveAggregateOptions) error {
	slog.Debug("saving aggregate", "aggregate_id", aggregate.ID())

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
			data, err := s.marshaler.Marshal(aggregate.Entity())
			if err != nil {
				slog.Error("failed to marshal snapshot", "error", err)
				continue
			}

			if err := s.writer.WriteSnapshot(ctx, &estoria.AggregateSnapshot{
				AggregateID:      aggregate.ID(),
				AggregateVersion: aggregate.Version(),
				Data:             data,
			}); err != nil {
				slog.Error("failed to write snapshot", "error", err)
				continue
			}
		}
	}

	return nil
}

type SnapshottingAggregateStoreOption[E estoria.Entity] func(*SnapshottingAggregateStore[E]) error

func WithSnapshotMarshaler[E estoria.Entity](marshaler estoria.EntityMarshaler[E]) SnapshottingAggregateStoreOption[E] {
	return func(s *SnapshottingAggregateStore[E]) error {
		s.marshaler = marshaler
		return nil
	}
}
