package projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

type StreamProjection struct {
	events   eventstore.StreamReader
	streamID typeid.UUID
	readOps  eventstore.ReadStreamOptions

	log estoria.Logger
}

func NewStreamProjection(events eventstore.StreamReader, streamID typeid.UUID, opts ...StreamProjectionOption) (*StreamProjection, error) {
	switch {
	case events == nil:
		return nil, errors.New("event stream reader is required")
	case streamID.IsEmpty():
		return nil, errors.New("stream ID is required")
	}

	projection := &StreamProjection{
		events:   events,
		streamID: streamID,
		readOps:  eventstore.ReadStreamOptions{},
		log:      estoria.GetLogger().WithGroup("projection"),
	}

	for _, opt := range opts {
		opt(projection)
	}

	return projection, nil
}

type EventProjectionFunc func(event *eventstore.Event) error

type Result struct {
	NumProjectedEvents int64
}

func (p *StreamProjection) Project(ctx context.Context, applyEvent EventProjectionFunc) (*Result, error) {
	iter, err := p.events.ReadStream(ctx, p.streamID, p.readOps)
	if err != nil {
		return nil, fmt.Errorf("obtaining stream iterator: %w", err)
	}

	defer iter.Close(ctx)

	result := &Result{}

	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			break
		} else if err != nil {
			return result, fmt.Errorf("reading event: %w", err)
		}

		p.log.Debug("projecting event", "stream_id", p.streamID, "event_id", event.ID, "stream_version", event.StreamVersion)

		if err := applyEvent(event); err != nil {
			return result, fmt.Errorf("processing event: %w", err)
		}

		result.NumProjectedEvents++
	}

	p.log.Debug("projected events", "stream_id", p.streamID, "count", result.NumProjectedEvents)

	return result, nil
}

type StreamProjectionOption func(*StreamProjection)

func WithReadStreamOptions(opts eventstore.ReadStreamOptions) StreamProjectionOption {
	return func(p *StreamProjection) {
		p.readOps = opts
	}
}

func WithLogger(log estoria.Logger) StreamProjectionOption {
	return func(p *StreamProjection) {
		p.log = log
	}
}
