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

func (s *EventStreamSnapshotReader) ReadSnapshot(ctx context.Context, aggregateID typeid.AnyID) (estoria.Snapshot, error) {
	slog.Debug("reading snapshot", "aggregate_id", aggregateID)

	snapshotStreamID, err := typeid.From(aggregateID.Prefix()+"snapshot", aggregateID.Suffix())
	if err != nil {
		return nil, fmt.Errorf("deriving snapshot ID: %w", err)
	}

	stream, err := s.eventReader.ReadStream(ctx, snapshotStreamID, estoria.ReadStreamOptions{
		FromVersion: 0,
		Count:       1,
		Direction:   -1,
	})
	if err != nil {
		return nil, fmt.Errorf("finding snapshot stream: %w", err)
	}

	event, err := stream.Next(ctx)
	if err == io.EOF {
		return nil, errors.New("snapshot not found")
	} else if err != nil {
		return nil, fmt.Errorf("reading snapshot event: %w", err)
	} else if event == nil {
		return nil, errors.New("snapshot not found")
	}

	slog.Debug("snapshot found", "aggregate_id", aggregateID, "snapshot_event_id", event.ID())

	var snapshot snapshot
	if err := json.Unmarshal(event.Data(), &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshalling snapshot: %w", err)
	}

	return &snapshot, nil
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

	snapshotStreamID, err := typeid.From(aggregateID.Prefix()+"snapshot", aggregateID.Suffix())
	if err != nil {
		return fmt.Errorf("deriving snapshot ID: %w", err)
	}

	eventID, err := typeid.WithPrefix(aggregateID.Prefix())
	if err != nil {
		return fmt.Errorf("generating snapshot event ID: %w", err)
	}

	snap := snapshot{
		aggregateID:      aggregateID,
		aggregateVersion: aggregateVersion,
		data:             data,
	}

	snapshotData, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshalling snapshot: %w", err)
	}

	event := &snapshotEvent{
		EventID:        eventID,
		EventStreamID:  snapshotStreamID,
		EventTimestamp: time.Now(),
		EventData:      snapshotData,
	}

	if err := s.eventWriter.AppendStream(ctx, snapshotStreamID, estoria.AppendStreamOptions{}, event); err != nil {
		return fmt.Errorf("appending snapshot stream: %w", err)
	}

	return nil
}

type snapshot struct {
	aggregateID      typeid.AnyID
	aggregateVersion int64
	data             []byte
}

func (s *snapshot) AggregateID() typeid.AnyID {
	return s.aggregateID
}

func (s *snapshot) AggregateVersion() int64 {
	return s.aggregateVersion
}

func (s *snapshot) Data() []byte {
	return s.data
}

type snapshotEvent struct {
	EventID        typeid.AnyID
	EventStreamID  typeid.AnyID
	EventTimestamp time.Time
	EventData      []byte
}

func (e *snapshotEvent) ID() typeid.AnyID {
	return e.EventID
}

func (e *snapshotEvent) StreamID() typeid.AnyID {
	return e.EventStreamID
}

func (e *snapshotEvent) Timestamp() time.Time {
	return e.EventTimestamp
}

func (e *snapshotEvent) Data() []byte {
	return e.EventData
}
