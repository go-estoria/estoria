package estoria

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"go.jetpack.io/typeid"
)

type EventStreamIterator interface {
	Next(ctx context.Context) (Event, error)
}

type EventStreamReader interface {
	ReadStream(ctx context.Context, id typeid.AnyID, opts ReadStreamOptions) (EventStreamIterator, error)
}

type EventStreamWriter interface {
	AppendStream(ctx context.Context, id typeid.AnyID, opts AppendStreamOptions, events ...Event) error
}

type AppendStreamOptions struct{}

type ReadStreamOptions struct {
	FromVersion int64
	ToVersion   int64
}

// An AggregateStore loads and saves aggregates using an EventStore.
type AggregateStore[E Entity] struct {
	EventReader EventStreamReader
	EventWriter EventStreamWriter

	SnapshotReader EventStreamReader
	SnapshotWriter EventStreamWriter

	newEntity          EntityFactory[E]
	eventDataFactories map[string]func() EventData

	deserializeEventData func([]byte, any) error
	serializeEventData   func(any) ([]byte, error)

	deserializeSnapshotData func([]byte, any) error
	serializeSnapshotData   func(any) ([]byte, error)

	log *slog.Logger
}

func NewAggregateStore[E Entity](
	eventReader EventStreamReader,
	eventWriter EventStreamWriter,
	entityFactory EntityFactory[E],
) *AggregateStore[E] {
	return &AggregateStore[E]{
		EventReader:             eventReader,
		EventWriter:             eventWriter,
		newEntity:               entityFactory,
		eventDataFactories:      make(map[string]func() EventData),
		deserializeEventData:    json.Unmarshal,
		serializeEventData:      json.Marshal,
		deserializeSnapshotData: json.Unmarshal,
		serializeSnapshotData:   json.Marshal,
		log:                     slog.Default().WithGroup("aggregatestore"),
	}
}

// Allow allows an event type to be used with the aggregate store.
func (c *AggregateStore[E]) Allow(eventDataFactory func() EventData) {
	data := eventDataFactory()
	c.eventDataFactories[data.EventType()] = eventDataFactory
}

func (c *AggregateStore[E]) Create() *Aggregate[E] {
	data := c.newEntity()
	return &Aggregate[E]{
		id:   data.EntityID(),
		data: data,
	}
}

// Load loads an aggregate by its ID.
func (c *AggregateStore[E]) Load(ctx context.Context, id typeid.AnyID) (*Aggregate[E], error) {
	c.log.Debug("loading aggregate", "aggregate_id", id)

	aggregate := &Aggregate[E]{
		id:   id,
		data: c.newEntity(),
	}

	// load the entire event stream for the aggregate unless a snapshot is found
	var loadFromVersion int64

	if snapshot, err := c.loadSnapshot(ctx, id); err != nil {
		if errors.Is(err, ErrSnapshotNotFound) {
			c.log.Debug("no snapshot found", "aggregate_id", id)
		} else {
			c.log.Warn("error loading snapshot", "aggregate_id", id, "error", err)
		}
	} else {
		if err := c.deserializeSnapshotData(snapshot.Data(), &aggregate.data); err != nil {
			c.log.Warn("error deserializing snapshot data", "aggregate_id", id, "error", err)
		} else {
			loadFromVersion = snapshot.AggregateVersion
			c.log.Debug("loaded snapshot", "aggregate_id", id, "version", snapshot.AggregateVersion)
		}
	}

	stream, err := c.EventReader.ReadStream(ctx, id, ReadStreamOptions{
		FromVersion: loadFromVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("finding event stream: %w", err)
	}

	for {
		evt, err := stream.Next(ctx)
		if err != nil {
			if err == io.EOF {
				break
			}

			return nil, fmt.Errorf("reading event: %w", err)
		}

		rawEventData := evt.Data()
		if len(rawEventData) == 0 {
			c.log.Warn("event has no data", "event_id", evt.ID())
			continue
		}

		newEventData, ok := c.eventDataFactories[evt.ID().Prefix()]
		if !ok {
			return nil, fmt.Errorf("no event data factory for event type %s", evt.ID().Prefix())
		}

		eventData := newEventData()
		if err := c.deserializeEventData(rawEventData, &eventData); err != nil {
			return nil, fmt.Errorf("deserializing event data: %w", err)
		}

		c.log.Debug("event data", "event_id", evt.ID(), "data", eventData)

		if err := aggregate.apply(ctx, &event{
			id:        evt.ID(),
			streamID:  evt.StreamID(),
			timestamp: evt.Timestamp(),
			data:      eventData,
			raw:       rawEventData,
		}); err != nil {
			return nil, fmt.Errorf("applying event: %w", err)
		}
	}

	return aggregate, nil
}

func (c *AggregateStore[E]) loadSnapshot(ctx context.Context, aggregateID typeid.AnyID) (*snapshot, error) {
	if c.SnapshotReader == nil {
		return nil, nil
	}

	snapshotStream, err := c.SnapshotReader.ReadStream(ctx, aggregateID, ReadStreamOptions{
		FromVersion: -1,
		ToVersion:   -1,
	})
	if err != nil {
		return nil, fmt.Errorf("finding snapshot stream: %w", err)
	}

	snapshotEvent, err := snapshotStream.Next(ctx)
	if err == io.EOF {
		return nil, ErrSnapshotNotFound
	} else if err != nil {
		return nil, fmt.Errorf("reading snapshot event: %w", err)
	}

	c.log.Debug("snapshot found", "aggregate_id", aggregateID, "snapshot_id", snapshotEvent.ID())

	var snapshot snapshot
	if err := c.deserializeEventData(snapshotEvent.Data(), &snapshot); err != nil {
		return nil, fmt.Errorf("deserializing snapshot event data: %w", err)
	}

	c.log.Debug("snapshot data", "aggregate_id", aggregateID, "version", snapshot.AggregateVersion)

	return &snapshot, nil
}

// Save saves an aggregate.
func (c *AggregateStore[E]) Save(ctx context.Context, aggregate *Aggregate[E]) error {
	c.log.Debug("saving aggregate", "aggregate_id", aggregate.ID(), "events", len(aggregate.unsavedEvents))

	if len(aggregate.unsavedEvents) == 0 {
		c.log.Debug("no events to save")
		return nil
	}

	toSave := make([]Event, len(aggregate.unsavedEvents))
	for i := range toSave {
		data, err := c.serializeEventData(aggregate.unsavedEvents[i].data)
		if err != nil {
			return fmt.Errorf("serializing event data: %w", err)
		}

		aggregate.unsavedEvents[i].raw = data
		toSave[i] = aggregate.unsavedEvents[i]
	}

	// assume to be atomic, for now (it's not)
	if err := c.EventWriter.AppendStream(ctx, aggregate.ID(), AppendStreamOptions{}, toSave...); err != nil {
		return fmt.Errorf("saving events: %w", err)
	}

	aggregate.unsavedEvents = []*event{}

	return nil
}

// SaveSnapshot saves a snapshot of an aggregate.
func (c *AggregateStore[E]) saveSnapshot(ctx context.Context, aggregate *Aggregate[E]) error {
	if c.SnapshotWriter == nil {
		return ErrSnapshotsNotSupported
	}

	data, err := c.serializeSnapshotData(aggregate.Entity())
	if err != nil {
		return fmt.Errorf("serializing snapshot data: %w", err)
	}

	eventID, err := typeid.From(aggregate.ID().Prefix(), "")
	if err != nil {
		return fmt.Errorf("generating event ID: %w", err)
	}

	snapshot := &snapshot{
		AggregateID:      aggregate.ID(),
		AggregateVersion: aggregate.Version(),
		event: event{
			id:        eventID,
			streamID:  aggregate.ID(),
			timestamp: time.Now(),
			raw:       data,
		},
	}

	if err := c.SnapshotWriter.AppendStream(ctx, aggregate.ID(), AppendStreamOptions{}, snapshot); err != nil {
		return fmt.Errorf("saving snapshot: %w", err)
	}

	return nil
}

var ErrSnapshotsNotSupported = errors.New("snapshots not supported")

var ErrSnapshotNotFound = errors.New("snapshot not found")

var ErrStreamNotFound = errors.New("stream not found")

var ErrAggregateNotFound = errors.New("aggregate not found")
