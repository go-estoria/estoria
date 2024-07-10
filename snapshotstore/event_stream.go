package snapshotstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

type EventStreamReader struct {
	eventReader estoria.EventStreamReader
	marshaler   estoria.Marshaler[estoria.AggregateSnapshot, *estoria.AggregateSnapshot]
}

func NewEventStreamReader(eventReader estoria.EventStreamReader) *EventStreamReader {
	return &EventStreamReader{
		eventReader: eventReader,
		marshaler:   estoria.JSONMarshaler[estoria.AggregateSnapshot]{},
	}
}

func (s *EventStreamReader) ReadSnapshot(ctx context.Context, aggregateID typeid.UUID, opts ReadOptions) (*estoria.AggregateSnapshot, error) {
	slog.Debug("finding snapshot", "aggregate_id", aggregateID)

	snapshotStreamID := typeid.FromUUID(aggregateID.TypeName()+"snapshot", aggregateID.UUID())

	stream, err := s.eventReader.ReadStream(ctx, snapshotStreamID, estoria.ReadStreamOptions{
		Offset:    0,
		Count:     1,
		Direction: estoria.Reverse,
	})
	if err != nil {
		return nil, fmt.Errorf("finding snapshot stream: %w", err)
	}

	event, err := stream.Next(ctx)
	if err == io.EOF {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("reading snapshot event: %w", err)
	} else if event == nil {
		return nil, errors.New("snapshot event not found")
	}

	slog.Debug("snapshot event found",
		"stream_id", snapshotStreamID,
		"stream_version", event.StreamVersion)

	var snapshot estoria.AggregateSnapshot
	if err := s.marshaler.Unmarshal(event.Data, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshaling snapshot: %w", err)
	}

	return &snapshot, nil
}

type ReadOptions struct {
	MaxVersion int64
}

type EventStreamWriter struct {
	eventWriter estoria.EventStreamWriter
	marshaler   estoria.Marshaler[estoria.AggregateSnapshot, *estoria.AggregateSnapshot]
}

func NewEventStreamWriter(eventWriter estoria.EventStreamWriter) *EventStreamWriter {
	return &EventStreamWriter{
		eventWriter: eventWriter,
		marshaler:   estoria.JSONMarshaler[estoria.AggregateSnapshot]{},
	}
}

func (s *EventStreamWriter) WriteSnapshot(ctx context.Context, snap *estoria.AggregateSnapshot) error {
	slog.Debug("writing snapshot",
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

	if err := s.eventWriter.AppendStream(ctx, snapshotStreamID, estoria.AppendStreamOptions{}, []*estoria.EventStoreEvent{
		{
			ID:        eventID,
			StreamID:  snapshotStreamID,
			Timestamp: time.Now(),
			Data:      eventData,
		},
	}); err != nil {
		return fmt.Errorf("appending snapshot stream: %w", err)
	}

	slog.Debug("wrote snapshot", "aggregate_id", snap.AggregateID, "snapshot_event_id", eventID)

	return nil
}
