package projection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

type StreamProjection struct {
	events   eventstore.StreamReader
	streamID typeid.UUID
	readOps  eventstore.ReadStreamOptions
}

func NewStreamProjection(events eventstore.StreamReader, streamID typeid.UUID, readOpts *eventstore.ReadStreamOptions) (*StreamProjection, error) {
	switch {
	case events == nil:
		return nil, errors.New("event stream reader is required")
	case streamID.IsEmpty():
		return nil, errors.New("stream ID is required")
	}

	if readOpts == nil {
		readOpts = &eventstore.ReadStreamOptions{}
	}

	slog.Info("created stream projection")

	return &StreamProjection{
		events:   events,
		streamID: streamID,
		readOps:  *readOpts,
	}, nil
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

		slog.Info("projecting event")

		if err := applyEvent(event); err != nil {
			return result, fmt.Errorf("processing event: %w", err)
		}

		result.NumProjectedEvents++
	}

	return result, nil
}
