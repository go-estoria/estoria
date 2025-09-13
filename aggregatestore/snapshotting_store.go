package aggregatestore

import (
	"context"
	"errors"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// A SnapshotPolicy determines when to take snapshots.
type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.UUID, aggregateVersion int64, timestamp time.Time) bool
}

// A SnapshottingStore wraps an aggregate store and uses a snapshot store to save snapshots
// and/or hydrate aggregates from snapshots.
type SnapshottingStore[E estoria.Entity] struct {
	inner     Store[E]
	reader    snapshotstore.SnapshotReader
	writer    snapshotstore.SnapshotWriter
	policy    SnapshotPolicy
	marshaler estoria.EntityMarshaler[E]
	log       estoria.Logger
}

var _ Store[estoria.Entity] = (*SnapshottingStore[estoria.Entity])(nil)

// NewSnapshottingStore creates a new SnapshottingStore.
func NewSnapshottingStore[E estoria.Entity](
	inner Store[E],
	store snapshotstore.SnapshotStore,
	policy SnapshotPolicy,
	opts ...SnapshottingStoreOption[E],
) (*SnapshottingStore[E], error) {
	switch {
	case inner == nil:
		return nil, InitializeError{Err: errors.New("inner store is required")}
	case store == nil:
		return nil, InitializeError{Err: errors.New("snapshot store is required")}
	case policy == nil:
		return nil, InitializeError{Err: errors.New("snapshot policy is required")}
	}

	aggregateStore := &SnapshottingStore[E]{
		inner:     inner,
		reader:    store,
		writer:    store,
		policy:    policy,
		marshaler: estoria.JSONMarshaler[E]{},
		log:       estoria.GetLogger().WithGroup("snapshottingstore"),
	}

	for _, opt := range opts {
		if err := opt(aggregateStore); err != nil {
			return nil, InitializeError{Operation: "applying option", Err: err}
		}
	}

	return aggregateStore, nil
}

// New creates a new aggregate.
func (s *SnapshottingStore[E]) New(id uuid.UUID) *Aggregate[E] {
	return s.inner.New(id)
}

// Load loads an aggregate by its ID.
func (s *SnapshottingStore[E]) Load(ctx context.Context, id uuid.UUID, opts *LoadOptions) (*Aggregate[E], error) {
	aggregate := s.New(id)
	s.log.Debug("loading aggregate", "aggregate_id", aggregate.ID())

	var hydrateOpts *HydrateOptions
	if opts != nil {
		hydrateOpts = &HydrateOptions{ToVersion: opts.ToVersion}
	}

	if err := s.Hydrate(ctx, aggregate, hydrateOpts); err != nil {
		return nil, LoadError{AggregateID: aggregate.ID(), Operation: "hydrating aggregate", Err: err}
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *SnapshottingStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts *HydrateOptions) error {
	switch {
	case aggregate == nil:
		return HydrateError{Err: ErrNilAggregate}
	case opts.ToVersion < 0:
		return HydrateError{AggregateID: aggregate.ID(), Err: errors.New("invalid target version")}
	case s.reader == nil:
		return HydrateError{AggregateID: aggregate.ID(), Err: errors.New("snapshot store has no snapshot reader")}
	}

	log := s.log.With("aggregate_id", aggregate.ID())

	log.Debug("hydrating aggregate from snapshot",
		"from_version", aggregate.Version(),
		"to_version", opts.ToVersion)

	readSnapshotOpts := snapshotstore.ReadSnapshotOptions{}
	if opts.ToVersion > 0 {
		if v := aggregate.Version(); v == opts.ToVersion {
			log.Debug("aggregate already at target version, nothing to hydrate", "version", opts.ToVersion)
			return s.inner.Hydrate(ctx, aggregate, opts)
		} else if v > opts.ToVersion {
			log.Debug("aggregate version is higher than target version, nothing to hydrate", "version", v, "target_version", opts.ToVersion)
			return s.inner.Hydrate(ctx, aggregate, opts)
		}

		readSnapshotOpts.MaxVersion = opts.ToVersion
	}

	snap, err := s.reader.ReadSnapshot(ctx, aggregate.ID(), readSnapshotOpts)
	if errors.Is(err, snapshotstore.ErrSnapshotNotFound) {
		log.Debug("no snapshot found")
		return s.inner.Hydrate(ctx, aggregate, opts)
	} else if err != nil {
		log.Warn("failed to read snapshot", "error", err)
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	entity := aggregate.Entity()
	if err := s.marshaler.UnmarshalEntity(snap.Data, &entity); err != nil {
		log.Warn("failed to unmarshal snapshot", "error", err)
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	log.Debug("loaded snapshot", "version", snap.AggregateVersion)
	aggregate.state.SetEntityAtVersion(entity, snap.AggregateVersion)

	if opts.ToVersion > 0 && snap.AggregateVersion == opts.ToVersion {
		return nil
	}

	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *SnapshottingStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: ErrNilAggregate}
	}

	log := s.log.With("aggregate_id", aggregate.ID())

	log.Debug("saving aggregate")

	// defer applying events so a snapshot can be taken at an exact version
	opts.SkipApply = true

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving aggregate using inner store", Err: err}
	}

	now := time.Now()

	for {
		err := aggregate.state.ApplyNext(ctx)
		if errors.Is(err, ErrNoUnappliedEvents) {
			break
		} else if err != nil {
			return SaveError{AggregateID: aggregate.ID(), Operation: "applying next aggregate event", Err: err}
		}

		if !s.policy.ShouldSnapshot(aggregate.ID(), aggregate.Version(), now) {
			continue
		}

		log.Debug("taking snapshot", "version", aggregate.Version())

		data, err := s.marshaler.MarshalEntity(aggregate.Entity())
		if err != nil {
			log.Error("failed to marshal snapshot", "error", err)
			continue
		}

		if err := s.writer.WriteSnapshot(ctx, &snapshotstore.AggregateSnapshot{
			AggregateID:      aggregate.ID(),
			AggregateVersion: aggregate.Version(),
			Data:             data,
		}); err != nil {
			log.Error("failed to write snapshot", "error", err)
			continue
		}
	}

	return nil
}

// A SnapshottingStoreOption configures a SnapshottingStore.
type SnapshottingStoreOption[E estoria.Entity] func(*SnapshottingStore[E]) error

// WithSnapshotMarshaler sets the snapshot marshaler.
func WithSnapshotMarshaler[E estoria.Entity](marshaler estoria.EntityMarshaler[E]) SnapshottingStoreOption[E] {
	return func(s *SnapshottingStore[E]) error {
		s.marshaler = marshaler
		return nil
	}
}

// WithSnapshotReader sets the snapshot reader.
func WithSnapshotReader[E estoria.Entity](reader snapshotstore.SnapshotReader) SnapshottingStoreOption[E] {
	return func(s *SnapshottingStore[E]) error {
		s.reader = reader
		return nil
	}
}

// WithSnapshotWriter sets the snapshot writer.
func WithSnapshotWriter[E estoria.Entity](writer snapshotstore.SnapshotWriter) SnapshottingStoreOption[E] {
	return func(s *SnapshottingStore[E]) error {
		s.writer = writer
		return nil
	}
}
