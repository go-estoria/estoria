package snapshot

import (
	"context"
	"encoding/json"
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
}

func NewEventStreamReader(eventReader estoria.EventStreamReader) *EventStreamReader {
	return &EventStreamReader{
		eventReader: eventReader,
	}
}

func (s *EventStreamReader) ReadSnapshot(ctx context.Context, aggregateID typeid.TypeID, opts ReadOptions) (estoria.Snapshot, error) {
	slog.Debug("finding snapshot", "aggregate_id", aggregateID)

	snapshotStreamID, err := typeid.From(aggregateID.TypeName()+"snapshot", aggregateID.Value())
	if err != nil {
		return nil, fmt.Errorf("deriving snapshot ID: %w", err)
	}

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
		"stream_version", event.StreamVersion())

	var snapshot snapshot
	if err := json.Unmarshal(event.Data(), &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshalling snapshot: %w", err)
	}

	return &snapshot, nil
}

type ReadOptions struct {
	MaxVersion int64
}

type EventStreamWriter struct {
	eventWriter estoria.EventStreamWriter
}

func NewEventStreamWriter(eventWriter estoria.EventStreamWriter) *EventStreamWriter {
	return &EventStreamWriter{
		eventWriter: eventWriter,
	}
}

func (s *EventStreamWriter) WriteSnapshot(ctx context.Context, aggregateID typeid.TypeID, aggregateVersion int64, data []byte) error {
	slog.Debug("writing snapshot", "aggregate_id", aggregateID, "aggregate_version", aggregateVersion, "data_length", len(data))

	snapshotStreamPrefix := aggregateID.TypeName() + "snapshot"

	snapshotStreamID, err := typeid.From(snapshotStreamPrefix, aggregateID.Value())
	if err != nil {
		return fmt.Errorf("deriving snapshot ID: %w", err)
	}

	eventID, err := typeid.New(snapshotStreamPrefix)
	if err != nil {
		return fmt.Errorf("generating snapshot event ID: %w", err)
	}

	// this data wraps the snapshot data with the aggregate ID and version
	eventData, err := json.Marshal(snapshot{
		SnapshotAggregateID:      aggregateID,
		SnapshotAggregateVersion: aggregateVersion,
		SnapshotData:             data,
	})
	if err != nil {
		return fmt.Errorf("marshalling snapshot data for stream event: %w", err)
	}

	event := &snapshotEvent{
		EventID:        eventID,
		EventStreamID:  snapshotStreamID,
		EventTimestamp: time.Now(),
		EventData:      eventData,
	}

	if err := s.eventWriter.AppendStream(ctx, snapshotStreamID, estoria.AppendStreamOptions{}, []estoria.EventStoreEvent{event}); err != nil {
		return fmt.Errorf("appending snapshot stream: %w", err)
	}

	slog.Debug("wrote snapshot", "aggregate_id", aggregateID, "snapshot_event_id", event.ID())

	return nil
}

type snapshot struct {
	SnapshotAggregateID      typeid.TypeID `json:"aggregate_id"`
	SnapshotAggregateVersion int64         `json:"aggregate_version"`
	SnapshotData             []byte        `json:"data"`
}

func (s *snapshot) AggregateID() typeid.TypeID {
	return s.SnapshotAggregateID
}

func (s *snapshot) AggregateVersion() int64 {
	return s.SnapshotAggregateVersion
}

func (s *snapshot) Data() []byte {
	return s.SnapshotData
}

type snapshotEvent struct {
	EventID            typeid.TypeID
	EventStreamID      typeid.TypeID
	EventStreamVersion int64
	EventTimestamp     time.Time
	EventData          json.RawMessage
}

func (e *snapshotEvent) ID() typeid.TypeID {
	return e.EventID
}

func (e *snapshotEvent) StreamID() typeid.TypeID {
	return e.EventStreamID
}

func (e *snapshotEvent) StreamVersion() int64 {
	return e.EventStreamVersion
}

func (e *snapshotEvent) Timestamp() time.Time {
	return e.EventTimestamp
}

func (e *snapshotEvent) Data() []byte {
	return e.EventData
}
