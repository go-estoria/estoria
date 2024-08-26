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
	"github.com/gofrs/uuid/v5"
)

// A SnapshotReader reads snapshots.
type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, aggregateID typeid.UUID, opts snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error)
}

// A SnapshotWriter writes snapshots.
type SnapshotWriter interface {
	WriteSnapshot(ctx context.Context, snap *snapshotstore.AggregateSnapshot) error
}

// A SnapshotStore reads and writes snapshots.
type SnapshotStore interface {
	SnapshotReader
	SnapshotWriter
}

// A SnapshotPolicy determines when to take snapshots.
type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.UUID, aggregateVersion int64, timestamp time.Time) bool
}

// A SnapshottingStore wraps an aggregate store and uses a snapshot store to save snapshots
// and/or hydrate aggregates from snapshots.
type SnapshottingStore[E estoria.Entity] struct {
	inner     Store[E]
	reader    SnapshotReader
	writer    SnapshotWriter
	policy    SnapshotPolicy
	marshaler estoria.Marshaler[E, *E]
	log       *slog.Logger
}

var _ Store[estoria.Entity] = (*SnapshottingStore[estoria.Entity])(nil)

// NewSnapshottingStore creates a new SnapshottingStore.
func NewSnapshottingStore[E estoria.Entity](
	inner Store[E],
	store SnapshotStore,
	policy SnapshotPolicy,
	opts ...SnapshottingStoreOption[E],
) (*SnapshottingStore[E], error) {
	aggregateStore := &SnapshottingStore[E]{
		inner:     inner,
		reader:    store,
		writer:    store,
		policy:    policy,
		marshaler: estoria.JSONMarshaler[E]{},
		log:       slog.Default().WithGroup("snapshottingaggregatestore"),
	}

	for _, opt := range opts {
		if err := opt(aggregateStore); err != nil {
			return nil, fmt.Errorf("applying option: %w", err)
		}
	}

	return aggregateStore, nil
}

// NewAggregate creates a new aggregate.
func (s *SnapshottingStore[E]) New(id uuid.UUID) (*Aggregate[E], error) {
	return s.inner.New(id)
}

// Load loads an aggregate by its ID.
func (s *SnapshottingStore[E]) Load(ctx context.Context, aggregateID typeid.UUID, opts LoadOptions) (*Aggregate[E], error) {
	s.log.Debug("loading aggregate", "aggregate_id", aggregateID)
	aggregate, err := s.New(aggregateID.UUID())
	if err != nil {
		slog.Warn("failed to create new aggregate", "error", err)
		return s.inner.Load(ctx, aggregateID, opts)
	}

	if err := s.Hydrate(ctx, aggregate, HydrateOptions{
		ToVersion: opts.ToVersion,
	}); err != nil {
		slog.Warn("failed to hydrate aggregate", "error", err)
		return s.inner.Load(ctx, aggregateID, opts)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *SnapshottingStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateOptions) error {
	log := s.log.With("aggregate_id", aggregate.ID())
	log.Debug("hydrating aggregate from snapshot", "from_version", aggregate.Version(), "to_version", opts.ToVersion)

	snap, err := s.reader.ReadSnapshot(ctx, aggregate.ID(), snapshotstore.ReadSnapshotOptions{
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
	aggregate.State().SetEntityAtVersion(entity, snap.AggregateVersion)

	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *SnapshottingStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error {
	slog.Debug("saving aggregate", "aggregate_id", aggregate.ID())

	// defer applying events so a snapshot can be taken at an exact version
	opts.SkipApply = true

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return fmt.Errorf("saving aggregate: %w", err)
	}

	now := time.Now()
	for {
		err := aggregate.State().ApplyNext(ctx)
		if errors.Is(err, ErrNoUnappliedEvents) {
			break
		} else if err != nil {
			return fmt.Errorf("applying next event: %w", err)
		}

		if s.policy.ShouldSnapshot(aggregate.ID(), aggregate.Version(), now) {
			slog.Debug("taking snapshot", "aggregate_id", aggregate.ID(), "version", aggregate.Version())
			entity := aggregate.Entity()
			data, err := s.marshaler.Marshal(&entity)
			if err != nil {
				slog.Error("failed to marshal snapshot", "error", err)
				continue
			}

			if err := s.writer.WriteSnapshot(ctx, &snapshotstore.AggregateSnapshot{
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

// A SnapshottingStoreOption configures a SnapshottingStore.
type SnapshottingStoreOption[E estoria.Entity] func(*SnapshottingStore[E]) error

// WithSnapshotMarshaler sets the snapshot marshaler.
func WithSnapshotMarshaler[E estoria.Entity](marshaler estoria.Marshaler[E, *E]) SnapshottingStoreOption[E] {
	return func(s *SnapshottingStore[E]) error {
		s.marshaler = marshaler
		return nil
	}
}

// WithSnapshotReader sets the snapshot reader.
func WithSnapshotReader[E estoria.Entity](reader SnapshotReader) SnapshottingStoreOption[E] {
	return func(s *SnapshottingStore[E]) error {
		s.reader = reader
		return nil
	}
}

// WithSnapshotWriter sets the snapshot writer.
func WithSnapshotWriter[E estoria.Entity](writer SnapshotWriter) SnapshottingStoreOption[E] {
	return func(s *SnapshottingStore[E]) error {
		s.writer = writer
		return nil
	}
}
