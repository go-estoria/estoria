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
	eventReader eventstore.StreamReader
	eventWriter eventstore.StreamWriter
}

// New creates a new event-stream-backed snapshot store on top of the given event store.
func New(eventStore eventstore.Store) *Store {
	return &Store{
		eventReader: eventStore,
		eventWriter: eventStore,
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
	// re-encodes bytes it was handed.
	metadata := map[string]string{
		SnapshotVersionMetadataKey: strconv.FormatInt(snap.AggregateVersion, 10),
	}
	if !snap.Timestamp.IsZero() {
		metadata[SnapshotTimestampMetadataKey] = snap.Timestamp.Format(time.RFC3339Nano)
	}

	if err := s.eventWriter.AppendStream(ctx, snapshotStreamID, []*eventstore.WritableEvent{
		{
			Type:     snapshotStreamPrefix,
			Data:     snap.Data,
			Metadata: metadata,
		},
	}, eventstore.AppendStreamOptions{}); err != nil {
		return fmt.Errorf("appending snapshot stream: %w", err)
	}

	estoria.GetLogger().Debug("wrote snapshot", "aggregate_id", snap.AggregateID, "prefix", snapshotStreamPrefix)

	return nil
}
