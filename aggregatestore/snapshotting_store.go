package aggregatestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/snapshot"
	"github.com/go-estoria/estoria/typeid"
)

type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, aggregateID typeid.TypeID, opts snapshot.ReadOptions) (estoria.Snapshot, error)
}

type SnapshotWriter interface {
	WriteSnapshot(ctx context.Context, aggregateID typeid.TypeID, aggregateVersion int64, entityData []byte) error
}

type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.TypeID, aggregateVersion int64, timestamp time.Time) bool
}

type EntitySnapshotSerde[E estoria.Entity] interface {
	Marshal(entity E) ([]byte, error)
	Unmarshal(data []byte, dest *E) error
}

type JSONEntitySnapshotSerde[E estoria.Entity] struct{}

func (JSONEntitySnapshotSerde[E]) Marshal(entity E) ([]byte, error) {
	return json.Marshal(entity)
}

func (JSONEntitySnapshotSerde[E]) Unmarshal(data []byte, dest *E) error {
	return json.Unmarshal(data, dest)
}

type SnapshottingAggregateStore[E estoria.Entity] struct {
	inner  estoria.AggregateStore[E]
	reader SnapshotReader
	writer SnapshotWriter
	policy SnapshotPolicy
	serde  EntitySnapshotSerde[E]
	log    *slog.Logger
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
		serde:  JSONEntitySnapshotSerde[E]{},
		log:    slog.Default().WithGroup("snapshottingaggregatestore"),
	}

	for _, opt := range opts {
		opt(store)
	}

	return store
}

// Allow allows an event type to be used with the aggregate store.
func (s *SnapshottingAggregateStore[E]) AllowEvents(prototypes ...estoria.EntityEventData) {
	s.inner.AllowEvents(prototypes...)
}

// NewAggregate creates a new aggregate.
func (s *SnapshottingAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
	return s.inner.NewAggregate()
}

// Load loads an aggregate by its ID.
func (s *SnapshottingAggregateStore[E]) Load(ctx context.Context, aggregateID typeid.TypeID, opts estoria.LoadAggregateOptions) (*estoria.Aggregate[E], error) {
	s.log.Debug("loading aggregate", "aggregate_id", aggregateID)
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
	log := s.log.With("aggregate_id", aggregate.ID())
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

	if entity.EntityID() != aggregate.ID() {
		slog.Warn("snapshot entity ID does not match aggregate ID", "snapshot_entity_id", entity.EntityID(), "aggregate_id", aggregate.ID())
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	log.Debug("loaded snapshot", "version", snap.AggregateVersion())

	aggregate.SetEntity(entity)
	aggregate.SetVersion(snap.AggregateVersion())

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
