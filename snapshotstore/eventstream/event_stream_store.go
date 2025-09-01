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

func (s *EventStreamStore) ReadSnapshot(ctx context.Context, aggregateID typeid.UUID, _ snapshotstore.ReadSnapshotOptions) (*snapshotstore.AggregateSnapshot, error) {
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
		return nil, snapshotstore.ErrSnapshotNotFound
	case err != nil:
		return nil, fmt.Errorf("reading snapshot event: %w", err)
	case event == nil:
		return nil, errors.New("snapshot event not found")
	}

	estoria.GetLogger().Debug("snapshot event found",
		"stream_id", snapshotStreamID,
		"stream_version", event.StreamVersion)

	var snapshot snapshotstore.AggregateSnapshot
	if err := s.marshaler.UnmarshalSnapshot(event.Data, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshaling snapshot: %w", err)
	}

	return &snapshot, nil
}

func (s *EventStreamStore) WriteSnapshot(ctx context.Context, snap *snapshotstore.AggregateSnapshot) error {
	estoria.GetLogger().Debug("writing snapshot",
		"aggregate_id", snap.AggregateID,
		"aggregate_version",
		snap.AggregateVersion,
		"data_length", len(snap.Data))

	snapshotStreamPrefix := snap.AggregateID.TypeName() + "snapshot"

	snapshotStreamID := typeid.FromUUID(snapshotStreamPrefix, snap.AggregateID.UUID())

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
