package snapshotstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

type EventStreamStore struct {
	eventReader eventstore.StreamReader
	eventWriter eventstore.StreamWriter
	marshaler   estoria.Marshaler[AggregateSnapshot, *AggregateSnapshot]
}

func NewEventStreamStore(eventStore eventstore.Store) *EventStreamStore {
	return &EventStreamStore{
		eventReader: eventStore,
		eventWriter: eventStore,
		marshaler:   estoria.JSONMarshaler[AggregateSnapshot]{},
	}
}

func (s *EventStreamStore) ReadSnapshot(ctx context.Context, aggregateID typeid.UUID, _ ReadSnapshotOptions) (*AggregateSnapshot, error) {
	estoria.GetLogger().Debug("finding snapshot", "aggregate_id", aggregateID)

	snapshotStreamID := typeid.FromUUID(aggregateID.TypeName()+"snapshot", aggregateID.UUID())

	stream, err := s.eventReader.ReadStream(ctx, snapshotStreamID, eventstore.ReadStreamOptions{
		Offset:    0,
		Count:     1,
		Direction: eventstore.Reverse,
	})
	if err != nil {
		return nil, fmt.Errorf("finding snapshot stream: %w", err)
	}

	event, err := stream.Next(ctx)
	switch {
	case errors.Is(err, eventstore.ErrEndOfEventStream):
		return nil, ErrSnapshotNotFound
	case err != nil:
		return nil, fmt.Errorf("reading snapshot event: %w", err)
	case event == nil:
		return nil, errors.New("snapshot event not found")
	}

	estoria.GetLogger().Debug("snapshot event found",
		"stream_id", snapshotStreamID,
		"stream_version", event.StreamVersion)

	var snapshot AggregateSnapshot
	if err := s.marshaler.Unmarshal(event.Data, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshaling snapshot: %w", err)
	}

	return &snapshot, nil
}

func (s *EventStreamStore) WriteSnapshot(ctx context.Context, snap *AggregateSnapshot) error {
	estoria.GetLogger().Debug("writing snapshot",
		"aggregate_id", snap.AggregateID,
		"aggregate_version",
		snap.AggregateVersion,
		"data_length", len(snap.Data))

	snapshotStreamPrefix := snap.AggregateID.TypeName() + "snapshot"

	snapshotStreamID := typeid.FromUUID(snapshotStreamPrefix, snap.AggregateID.UUID())

	eventID, err := typeid.NewUUID(snapshotStreamPrefix)
	if err != nil {
		return fmt.Errorf("generating snapshot event ID: %w", err)
	}

	// event data includes the aggregate ID, aggregate version, and snapshot data
	eventData, err := s.marshaler.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshaling snapshot data for stream event: %w", err)
	}

	if err := s.eventWriter.AppendStream(ctx, snapshotStreamID, []*eventstore.WritableEvent{
		{
			ID:   eventID,
			Data: eventData,
		},
	}, eventstore.AppendStreamOptions{}); err != nil {
		return fmt.Errorf("appending snapshot stream: %w", err)
	}

	estoria.GetLogger().Debug("wrote snapshot", "aggregate_id", snap.AggregateID, "snapshot_event_id", eventID)

	return nil
}
