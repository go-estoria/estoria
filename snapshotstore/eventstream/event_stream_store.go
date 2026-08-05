package snapshotstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/snapshotstore"
	"github.com/go-estoria/estoria/typeid"
)

type SnapshotMarshaler interface {
	MarshalSnapshot(snap *snapshotstore.AggregateSnapshot) ([]byte, error)
	UnmarshalSnapshot(data []byte, dest *snapshotstore.AggregateSnapshot) error
}

type EventStreamStore struct {
	eventReader eventstore.StreamReader
	eventWriter eventstore.StreamWriter
	marshaler   SnapshotMarshaler
}

func NewEventStreamStore(eventStore eventstore.Store) *EventStreamStore {
	return &EventStreamStore{
		eventReader: eventStore,
		eventWriter: eventStore,
		marshaler:   snapshotstore.JSONSnapshotMarshaler{},
	}
}

// ReadSnapshot returns the most recent snapshot for an aggregate, or the most recent one at
// or below opts.MaxVersion when that is set.
//
// Snapshots live as events in a parallel stream, and a snapshot event's stream version is
// unrelated to the aggregate version it captures, so a bounded read walks the stream
// backwards until it finds a snapshot within the bound. An unbounded read only ever needs
// the newest event.
func (s *EventStreamStore) ReadSnapshot(ctx context.Context, aggregateID typeid.ID, opts snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
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

		var snapshot snapshotstore.AggregateSnapshot
		if err := s.marshaler.UnmarshalSnapshot(event.Data, &snapshot); err != nil {
			return nil, fmt.Errorf("unmarshaling snapshot: %w", err)
		}

		if opts.MaxVersion > 0 && snapshot.AggregateVersion > opts.MaxVersion {
			continue
		}

		estoria.GetLogger().Debug("snapshot event found",
			"stream_id", snapshotStreamID,
			"stream_version", event.StreamVersion,
			"aggregate_version", snapshot.AggregateVersion)

		return &snapshot, nil
	}
}

func (s *EventStreamStore) WriteSnapshot(ctx context.Context, snap *snapshotstore.AggregateSnapshot) error {
	estoria.GetLogger().Debug("writing snapshot",
		"aggregate_id", snap.AggregateID,
		"aggregate_version",
		snap.AggregateVersion,
		"data_length", len(snap.Data))

	snapshotStreamPrefix := snap.AggregateID.Type + "snapshot"

	snapshotStreamID := typeid.New(snapshotStreamPrefix, snap.AggregateID.UUID)

	// event data includes the aggregate ID, aggregate version, and snapshot data
	eventData, err := s.marshaler.MarshalSnapshot(snap)
	if err != nil {
		return fmt.Errorf("marshaling snapshot data for stream event: %w", err)
	}

	if err := s.eventWriter.AppendStream(ctx, snapshotStreamID, []*eventstore.WritableEvent{
		{
			Type: snapshotStreamPrefix,
			Data: eventData,
		},
	}, eventstore.AppendStreamOptions{}); err != nil {
		return fmt.Errorf("appending snapshot stream: %w", err)
	}

	estoria.GetLogger().Debug("wrote snapshot", "aggregate_id", snap.AggregateID, "prefix", snapshotStreamPrefix)

	return nil
}
