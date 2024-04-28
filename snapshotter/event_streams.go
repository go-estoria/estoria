package snapshotter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type EventStreamSnapshotReader struct {
	eventReader estoria.EventStreamReader
}

func NewEventStreamSnapshotReader(eventReader estoria.EventStreamReader) *EventStreamSnapshotReader {
	return &EventStreamSnapshotReader{
		eventReader: eventReader,
	}
}

func (s *EventStreamSnapshotReader) ReadSnapshot(ctx context.Context, aggregateID typeid.AnyID, opts ReadSnapshotOptions) (estoria.Snapshot, error) {
	slog.Debug("finding snapshot", "aggregate_id", aggregateID)

	snapshotStreamID, err := typeid.From(aggregateID.Prefix()+"snapshot", aggregateID.Suffix())
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

type ReadSnapshotOptions struct {
	MaxVersion int64
}

type EventStreamSnapshotWriter struct {
	eventWriter estoria.EventStreamWriter
}

func NewEventStreamSnapshotWriter(eventWriter estoria.EventStreamWriter) *EventStreamSnapshotWriter {
	return &EventStreamSnapshotWriter{
		eventWriter: eventWriter,
	}
}

func (s *EventStreamSnapshotWriter) WriteSnapshot(ctx context.Context, aggregateID typeid.AnyID, aggregateVersion int64, data []byte) error {
	slog.Debug("writing snapshot", "aggregate_id", aggregateID, "aggregate_version", aggregateVersion, "data_length", len(data))

	snapshotStreamPrefix := aggregateID.Prefix() + "snapshot"

	snapshotStreamID, err := typeid.From(snapshotStreamPrefix, aggregateID.Suffix())
	if err != nil {
		return fmt.Errorf("deriving snapshot ID: %w", err)
	}

	eventID, err := typeid.WithPrefix(snapshotStreamPrefix)
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

	if err := s.eventWriter.AppendStream(ctx, snapshotStreamID, estoria.AppendStreamOptions{}, event); err != nil {
		return fmt.Errorf("appending snapshot stream: %w", err)
	}

	slog.Debug("wrote snapshot", "aggregate_id", aggregateID, "snapshot_event_id", event.ID())

	return nil
}

type snapshot struct {
	SnapshotAggregateID      typeid.AnyID `json:"aggregate_id"`
	SnapshotAggregateVersion int64        `json:"aggregate_version"`
	SnapshotData             []byte       `json:"data"`
}

func (s *snapshot) AggregateID() typeid.AnyID {
	return s.SnapshotAggregateID
}

func (s *snapshot) AggregateVersion() int64 {
	return s.SnapshotAggregateVersion
}

func (s *snapshot) Data() []byte {
	return s.SnapshotData
}

type snapshotEvent struct {
	EventID            typeid.AnyID
	EventStreamID      typeid.AnyID
	EventStreamVersion int64
	EventTimestamp     time.Time
	EventData          json.RawMessage
}

func (e *snapshotEvent) ID() typeid.AnyID {
	return e.EventID
}

func (e *snapshotEvent) StreamID() typeid.AnyID {
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
