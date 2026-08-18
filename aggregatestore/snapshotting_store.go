package aggregatestore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// A SnapshotPolicy determines when to take snapshots.
type SnapshotPolicy interface {
	ShouldSnapshot(aggregateID typeid.ID, aggregateVersion int64, timestamp time.Time) bool
}

// A SnapshotStateValidator is implemented by state types that can vouch for
// what their snapshots legitimately claim. SnapshottingStore consults it
// after decoding a snapshot and before installing the state on the
// aggregate — on the decoded state itself; on its address when only the
// pointer receiver implements it; or, when the state type is an interface,
// on an addressable copy of the dynamic value, which replaces the decoded
// state on acceptance: a rejected payload is skipped in favor of full
// hydration, exactly like an undecodable one, so tampered or truncated
// snapshot storage degrades to replaying the events rather than seeding
// the fold with fabricated state.
type SnapshotStateValidator interface {
	ValidateSnapshotState() error
}

// A SnapshottingStore wraps an aggregate store and uses a snapshot store to save snapshots
// and/or hydrate aggregates from snapshots.
type SnapshottingStore[S any] struct {
	inner      Store[S]
	reader     snapshotstore.SnapshotReader
	writer     snapshotstore.SnapshotWriter
	policy     SnapshotPolicy
	stateCodec estoria.StateCodec[S]
	log        estoria.Logger
}

var _ Store[struct{}] = (*SnapshottingStore[struct{}])(nil)

// NewSnapshottingStore creates a new SnapshottingStore.
func NewSnapshottingStore[S any](
	inner Store[S],
	store snapshotstore.SnapshotStore,
	policy SnapshotPolicy,
	opts ...SnapshottingStoreOption[S],
) (*SnapshottingStore[S], error) {
	switch {
	case inner == nil:
		return nil, InitializeError{Err: errors.New("inner store is required")}
	case store == nil:
		return nil, InitializeError{Err: errors.New("snapshot store is required")}
	case policy == nil:
		return nil, InitializeError{Err: errors.New("snapshot policy is required")}
	}

	aggregateStore := &SnapshottingStore[S]{
		inner:      inner,
		reader:     store,
		writer:     store,
		policy:     policy,
		stateCodec: estoria.JSONStateCodec[S]{},
		log:        estoria.GetLogger().WithGroup("snapshottingstore"),
	}

	for _, opt := range opts {
		if err := opt(aggregateStore); err != nil {
			return nil, InitializeError{Operation: "applying option", Err: err}
		}
	}

	return aggregateStore, nil
}

// AggregateType returns the aggregate type name of the inner store.
func (s *SnapshottingStore[S]) AggregateType() string {
	return s.inner.AggregateType()
}

// New creates a new aggregate.
func (s *SnapshottingStore[S]) New(id uuid.UUID) *Aggregate[S] {
	return s.inner.New(id)
}

// Load loads an aggregate by its ID.
func (s *SnapshottingStore[S]) Load(ctx context.Context, id uuid.UUID, opts *LoadOptions) (*Aggregate[S], error) {
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

// Hydrate hydrates an aggregate, first attempting to load from a snapshot.
func (s *SnapshottingStore[S]) Hydrate(ctx context.Context, aggregate *Aggregate[S], opts *HydrateOptions) error {
	if opts == nil {
		opts = &HydrateOptions{}
	}

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

	// A snapshot declaring a content type the codec does not read is skipped
	// before any decode is attempted: a payload in the wrong encoding can decode
	// into state "successfully" with nothing matched, which is silent corruption,
	// not an error. Full hydration is always correct, and the next snapshot write
	// self-heals in the codec's own encoding. A snapshot declaring nothing
	// predates content-type declarations and is decoded as before.
	if snap.DataContentType != "" && snap.DataContentType != s.stateCodec.ContentType() {
		log.Warn("snapshot content type does not match the state codec, falling back to full hydration",
			"snapshot_content_type", snap.DataContentType,
			"codec_content_type", s.stateCodec.ContentType())
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	// Decode into fresh state rather than the aggregate's live state. When S is a
	// pointer type the codec writes through to the state in place, so a snapshot
	// that is valid JSON but disagrees on a field's type — ordinary schema drift —
	// applies the fields it can before failing. Decoding into the live state would
	// leave that partial state behind and then replay events on top of it, silently
	// returning a corrupt aggregate with a nil error.
	state := s.inner.New(aggregate.ID().UUID).State()
	if err := s.stateCodec.UnmarshalState(snap.Data, &state); err != nil {
		log.Warn("failed to unmarshal snapshot, falling back to full hydration", "error", err)
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	// An encoded null decodes into a nil pointer, map, or slice without
	// error — any nilable kind, under a pluggable codec. Nil state can be
	// neither validated nor installed: a validator method would dereference
	// its nil receiver, and a nil map or slice would stand in for folded
	// history the events never produced, while full replay reproduces any
	// legitimately nil state on its own. The payload is treated exactly
	// like an undecodable one.
	if v := reflect.ValueOf(state); !v.IsValid() || (nilableKind(v.Kind()) && v.IsNil()) {
		log.Warn("snapshot decoded to nil state, falling back to full hydration")
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	// A state type that implements SnapshotStateValidator vouches for what
	// snapshots of it can legitimately claim. A rejected payload is treated
	// exactly like an undecodable one, because installing it would seed the
	// tail's fold with fabricated state — the one thing full event replay
	// can never produce on its own.
	if err := validateSnapshotState(&state); err != nil {
		log.Warn("snapshot state failed validation, falling back to full hydration", "error", err)
		return s.inner.Hydrate(ctx, aggregate, opts)
	}

	log.Debug("loaded snapshot", "version", snap.AggregateVersion)
	aggregate.setStateAtVersion(state, snap.AggregateVersion)

	if opts.ToVersion > 0 && snap.AggregateVersion == opts.ToVersion {
		return nil
	}

	return s.inner.Hydrate(ctx, aggregate, opts)
}

// nilableKind reports whether a value of kind k can hold nil — the shapes a
// codec can produce from an encoded null.
func nilableKind(k reflect.Kind) bool {
	return k == reflect.Chan || k == reflect.Func || k == reflect.Interface ||
		k == reflect.Map || k == reflect.Pointer || k == reflect.Slice ||
		k == reflect.UnsafePointer
}

// validateSnapshotState consults the state's SnapshotStateValidator, if it
// declares one, and reports whether the decoded payload may be installed.
// The state value is consulted first, then its address, then — when the
// state type is an interface, which hides its dynamic value's
// pointer-receiver validator from both — the address of a copy of the
// dynamic value; an accepted copy replaces the state, so the value the
// validator vouched for is the value installed. At most one validator is
// invoked; without one, every decoded payload is accepted.
func validateSnapshotState[S any](state *S) error {
	if validator, ok := any(*state).(SnapshotStateValidator); ok {
		return validator.ValidateSnapshotState()
	}

	if validator, ok := any(state).(SnapshotStateValidator); ok {
		return validator.ValidateSnapshotState()
	}

	dynamic := reflect.ValueOf(*state)
	if !dynamic.IsValid() || dynamic.Type() == reflect.TypeFor[S]() {
		return nil
	}

	addressable := reflect.New(dynamic.Type())
	addressable.Elem().Set(dynamic)

	validator, ok := addressable.Interface().(SnapshotStateValidator)
	if !ok {
		return nil
	}

	if err := validator.ValidateSnapshotState(); err != nil {
		return err
	}

	if accepted, ok := addressable.Elem().Interface().(S); ok {
		*state = accepted
	}

	return nil
}

// Save saves an aggregate, taking snapshots as needed.
// An error that carries ErrEventsAppended means the events were appended but
// not applied to the in-memory aggregate.
func (s *SnapshottingStore[S]) Save(ctx context.Context, aggregate *Aggregate[S], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: ErrNilAggregate}
	}

	log := s.log.With("aggregate_id", aggregate.ID())

	log.Debug("saving aggregate")

	// Defer applying events so a snapshot can be taken at an exact version.
	// This is set on a copy: opts belongs to the caller, and mutating it would
	// leak SkipApply into every later save that reuses the same struct.
	innerOpts := SaveOptions{}
	if opts != nil {
		innerOpts = *opts
	}

	innerOpts.SkipApply = true

	if err := s.inner.Save(ctx, aggregate, &innerOpts); err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving aggregate using inner store", Err: err}
	}

	now := time.Now()

	for {
		err := aggregate.applyNext()
		if errors.Is(err, ErrNoUnappliedEvents) {
			break
		} else if err != nil {
			// The inner save succeeded, so the events are already facts in the store.
			return SaveError{
				AggregateID: aggregate.ID(),
				Operation:   "applying next aggregate event",
				Err:         fmt.Errorf("%w: %w", ErrEventsAppended, err),
			}
		}

		if !s.policy.ShouldSnapshot(aggregate.ID(), aggregate.Version(), now) {
			continue
		}

		log.Debug("taking snapshot", "version", aggregate.Version())

		data, err := s.stateCodec.MarshalState(aggregate.State())
		if err != nil {
			log.Error("failed to marshal snapshot", "error", err)
			continue
		}

		if err := s.writer.WriteSnapshot(ctx, &snapshotstore.AggregateSnapshot{
			AggregateID:      aggregate.ID(),
			AggregateVersion: aggregate.Version(),
			Data:             data,
			DataContentType:  s.stateCodec.ContentType(),
		}); err != nil {
			log.Error("failed to write snapshot", "error", err)
			continue
		}
	}

	return nil
}

// A SnapshottingStoreOption is a functional option for configuring a SnapshottingStore.
type SnapshottingStoreOption[S any] func(*SnapshottingStore[S]) error

// WithStateCodec sets the codec used to marshal state into snapshot payloads.
func WithStateCodec[S any](codec estoria.StateCodec[S]) SnapshottingStoreOption[S] {
	return func(s *SnapshottingStore[S]) error {
		s.stateCodec = codec
		return nil
	}
}

// WithSnapshotReader sets the snapshot reader.
func WithSnapshotReader[S any](reader snapshotstore.SnapshotReader) SnapshottingStoreOption[S] {
	return func(s *SnapshottingStore[S]) error {
		s.reader = reader
		return nil
	}
}

// WithSnapshotWriter sets the snapshot writer.
func WithSnapshotWriter[S any](writer snapshotstore.SnapshotWriter) SnapshottingStoreOption[S] {
	return func(s *SnapshottingStore[S]) error {
		s.writer = writer
		return nil
	}
}
