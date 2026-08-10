package eventstream

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
)

// SnapshotVersionMetadataKey is the event metadata key under which the store records
// the aggregate version a snapshot captures, as a decimal integer.
//
// Metadata keys prefixed "estoria." are reserved for estoria itself; backends and
// callers must not write them.
const SnapshotVersionMetadataKey = "estoria.snapshot_version"

// SnapshotTimestampMetadataKey is the event metadata key under which the store
// records a writer-supplied snapshot timestamp, in RFC 3339 format with
// nanoseconds. It is written only when the writer supplied a non-zero timestamp;
// otherwise a read snapshot carries the snapshot event's own timestamp.
const SnapshotTimestampMetadataKey = "estoria.snapshot_timestamp"

// A Store is a snapshot store that persists snapshots as events in a parallel
// stream of the event store it wraps, one stream per aggregate.
type Store struct {
	eventReader   eventstore.StreamReader
	eventWriter   eventstore.StreamWriter
	streamDeleter eventstore.StreamDeleter
	maxSnapshots  int64
}

// New creates a new event-stream-backed snapshot store on top of the given event store.
func New(eventStore eventstore.Store, opts ...StoreOption) (*Store, error) {
	store := &Store{
		eventReader: eventStore,
		eventWriter: eventStore,
	}

	for _, opt := range opts {
		if err := opt(store); err != nil {
			return nil, fmt.Errorf("applying option: %w", err)
		}
	}

	return store, nil
}

// A StoreOption configures a Store.
type StoreOption func(*Store) error

// WithMaxSnapshots retains only the newest n snapshots per aggregate: each
// write prunes older snapshot events from the aggregate's snapshot stream.
// It requires an event store that implements eventstore.StreamDeleter.
// Pruning is best-effort housekeeping — a pruning failure is logged, never
// surfaced as a write failure.
func WithMaxSnapshots(n int64) StoreOption {
	return func(s *Store) error {
		if n < 1 {
			return errors.New("max snapshots must be at least 1")
		}

		deleter, ok := s.eventWriter.(eventstore.StreamDeleter)
		if !ok {
			return errors.New("the event store does not support stream deletion")
		}

		s.streamDeleter = deleter
		s.maxSnapshots = n

		return nil
	}
}

// ReadSnapshot returns the most recent snapshot for an aggregate, or the most recent one at
// or below opts.MaxVersion when that is set.
//
// Snapshots live as events in a parallel stream, carrying the state payload as the event
// body and the aggregate version in metadata. A snapshot event's stream version is
// unrelated to the aggregate version it captures, so a bounded read walks the stream
// backwards until it finds a snapshot within the bound. An unbounded read only ever needs
// the newest event.
//
// An event without the version metadata key is not a snapshot this store can read —
// notably, events written by versions of this store that marshaled the whole snapshot
// envelope into the body. Such events are skipped, never decoded as state: an old envelope
// body would typically decode into a state type "successfully" with nothing matched,
// which is silent corruption, not an error. Skipping means pre-existing snapshots are
// invisible after an upgrade; the first load replays the full stream and the next
// snapshot write self-heals.
func (s *Store) ReadSnapshot(ctx context.Context, aggregateID typeid.ID, opts snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
	estoria.GetLogger().Debug("finding snapshot", "aggregate_id", aggregateID, "max_version", opts.MaxVersion)

	snapshotStreamID := typeid.New(aggregateID.Type+"snapshot", aggregateID.UUID)

	readOpts := eventstore.ReadStreamOptions{
		AfterVersion: 0,
		Direction:    eventstore.Reverse,
	}

	if opts.MaxVersion <= 0 {
		readOpts.Count = 1
	}

	stream, err := s.eventReader.ReadStream(ctx, snapshotStreamID, readOpts)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		// An aggregate that has never been snapshotted has no snapshot stream. That is the
		// ordinary case on a first load, not a failure, so it maps to ErrSnapshotNotFound —
		// which is what SnapshottingStore checks before falling back to full hydration.
		return nil, snapshotstore.ErrSnapshotNotFound
	} else if err != nil {
		return nil, fmt.Errorf("finding snapshot stream: %w", err)
	}

	defer stream.Close(ctx)

	for {
		event, err := stream.Next(ctx)
		switch {
		case errors.Is(err, eventstore.ErrEndOfEventStream):
			return nil, snapshotstore.ErrSnapshotNotFound
		case err != nil:
			return nil, fmt.Errorf("reading snapshot event: %w", err)
		case event == nil:
			return nil, errors.New("snapshot event not found")
		}

		versionStr, ok := event.Metadata[SnapshotVersionMetadataKey]
		if !ok {
			estoria.GetLogger().Debug("skipping event without a snapshot version; not a readable snapshot",
				"stream_id", snapshotStreamID,
				"stream_version", event.StreamVersion)
			continue
		}

		version, err := strconv.ParseInt(versionStr, 10, 64)
		if err != nil {
			estoria.GetLogger().Warn("skipping snapshot event with an unparseable version",
				"stream_id", snapshotStreamID,
				"stream_version", event.StreamVersion,
				"error", err)
			continue
		}

		if opts.MaxVersion > 0 && version > opts.MaxVersion {
			continue
		}

		timestamp := event.Timestamp
		if timestampStr, ok := event.Metadata[SnapshotTimestampMetadataKey]; ok {
			parsed, err := time.Parse(time.RFC3339Nano, timestampStr)
			if err != nil {
				estoria.GetLogger().Warn("snapshot event has an unparseable timestamp; using the event's own",
					"stream_id", snapshotStreamID,
					"stream_version", event.StreamVersion,
					"error", err)
			} else {
				timestamp = parsed
			}
		}

		estoria.GetLogger().Debug("snapshot event found",
			"stream_id", snapshotStreamID,
			"stream_version", event.StreamVersion,
			"aggregate_version", version)

		return &snapshotstore.AggregateSnapshot{
			AggregateID:      aggregateID,
			AggregateVersion: version,
			Timestamp:        timestamp,
			Data:             event.Data,
			DataContentType:  event.DataContentType,
		}, nil
	}
}

func (s *Store) WriteSnapshot(ctx context.Context, snap *snapshotstore.AggregateSnapshot) error {
	estoria.GetLogger().Debug("writing snapshot",
		"aggregate_id", snap.AggregateID,
		"aggregate_version",
		snap.AggregateVersion,
		"data_length", len(snap.Data))

	snapshotStreamPrefix := snap.AggregateID.Type + "snapshot"

	snapshotStreamID := typeid.New(snapshotStreamPrefix, snap.AggregateID.UUID)

	// The state payload is the event body as-is; the aggregate version — and the
	// writer's timestamp, when one was supplied — ride in metadata. Nothing
	// re-encodes bytes it was handed, so the writer's content-type declaration
	// passes through untouched as well.
	metadata := map[string]string{
		SnapshotVersionMetadataKey: strconv.FormatInt(snap.AggregateVersion, 10),
	}
	if !snap.Timestamp.IsZero() {
		metadata[SnapshotTimestampMetadataKey] = snap.Timestamp.Format(time.RFC3339Nano)
	}

	written, err := s.eventWriter.AppendStream(ctx, snapshotStreamID, []*eventstore.WritableEvent{
		{
			Type:            snapshotStreamPrefix,
			Data:            snap.Data,
			DataContentType: snap.DataContentType,
			Metadata:        metadata,
		},
	}, eventstore.AppendStreamOptions{})
	if err != nil {
		return fmt.Errorf("appending snapshot stream: %w", err)
	}

	if s.streamDeleter != nil && len(written) == 1 {
		if toVersion := written[0].StreamVersion - s.maxSnapshots; toVersion > 0 {
			if err := s.streamDeleter.DeleteStream(ctx, snapshotStreamID, eventstore.DeleteStreamOptions{
				ToVersion: toVersion,
			}); err != nil {
				estoria.GetLogger().Warn("failed to prune old snapshots",
					"stream_id", snapshotStreamID,
					"to_version", toVersion,
					"error", err)
			}
		}
	}

	estoria.GetLogger().Debug("wrote snapshot", "aggregate_id", snap.AggregateID, "prefix", snapshotStreamPrefix)

	return nil
}
