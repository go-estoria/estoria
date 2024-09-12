package aggregatestore

import (
	"context"
	"errors"
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
	log       estoria.Logger
}

var _ Store[estoria.Entity] = (*SnapshottingStore[estoria.Entity])(nil)

// NewSnapshottingStore creates a new SnapshottingStore.
func NewSnapshottingStore[E estoria.Entity](
	inner Store[E],
	store SnapshotStore,
	policy SnapshotPolicy,
	opts ...SnapshottingStoreOption[E],
) (*SnapshottingStore[E], error) {
	switch {
	case inner == nil:
		return nil, InitializeAggregateStoreError{Err: errors.New("inner store is required")}
	case store == nil:
		return nil, InitializeAggregateStoreError{Err: errors.New("snapshot store is required")}
	case policy == nil:
		return nil, InitializeAggregateStoreError{Err: errors.New("snapshot policy is required")}
	}

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
			return nil, InitializeAggregateStoreError{Operation: "applying option", Err: err}
		}
	}

	return aggregateStore, nil
}

// New creates a new aggregate.
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
	switch {
	case aggregate == nil:
		return HydrateAggregateError{Err: ErrNilAggregate}
	case opts.ToVersion < 0:
		return HydrateAggregateError{AggregateID: aggregate.ID(), Err: errors.New("invalid target version")}
	case s.reader == nil:
		return HydrateAggregateError{AggregateID: aggregate.ID(), Err: errors.New("snapshot store has no snapshot reader")}
	}

	s.log.Debug("hydrating aggregate from snapshot",
		"aggregate_id", aggregate.ID(),
		"from_version", aggregate.Version(),
		"to_version", opts.ToVersion)

	readSnapshotOpts := snapshotstore.ReadSnapshotOptions{}
	if opts.ToVersion > 0 {
		if v := aggregate.Version(); v == opts.ToVersion {
			s.log.Debug("aggregate already at target version, nothing to hydrate",
				"aggregate_id", aggregate.ID(),
				"version", opts.ToVersion)
			return s.inner.Hydrate(ctx, aggregate, opts)
		} else if v > opts.ToVersion {
			s.log.Debug("aggregate version is higher than target version, nothing to hydrate",
				"aggregate_id", aggregate.ID(),
				"version", v,
				"target_version", opts.ToVersion)
			return s.inner.Hydrate(ctx, aggregate, opts)
		}

		readSnapshotOpts.MaxVersion = opts.ToVersion
	}

	snap, err := s.reader.ReadSnapshot(ctx, aggregate.ID(), readSnapshotOpts)
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

	s.log.Debug("loaded snapshot", "aggregate_id", aggregate.ID(), "version", snap.AggregateVersion)
	aggregate.State().SetEntityAtVersion(entity, snap.AggregateVersion)

	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *SnapshottingStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error {
	slog.Debug("saving aggregate", "aggregate_id", aggregate.ID())

	// defer applying events so a snapshot can be taken at an exact version
	opts.SkipApply = true

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "saving aggregate using inner store", Err: err}
	}

	now := time.Now()

	for {
		err := aggregate.State().ApplyNext(ctx)
		if errors.Is(err, ErrNoUnappliedEvents) {
			break
		} else if err != nil {
			return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "applying next aggregate event", Err: err}
		}

		if !s.policy.ShouldSnapshot(aggregate.ID(), aggregate.Version(), now) {
			continue
		}

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
