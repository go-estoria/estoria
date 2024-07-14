package memory

import (
	"context"
	"io"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

type StreamIterator struct {
	streamID  typeid.UUID
	events    []*eventstore.EventStoreEvent
	cursor    int64
	direction eventstore.ReadStreamDirection
	limit     int64
	retrieved int64
}

func (i *StreamIterator) Next(ctx context.Context) (*eventstore.EventStoreEvent, error) {
	if i.direction == eventstore.Forward && i.cursor >= int64(len(i.events)) {
		return nil, io.EOF
	} else if i.direction == eventstore.Reverse && i.cursor < 0 {
		return nil, io.EOF
	} else if i.limit > 0 && i.retrieved >= i.limit {
		return nil, io.EOF
	}

	event := i.events[i.cursor]
	if i.direction == eventstore.Reverse {
		i.cursor--
	} else {
		i.cursor++
	}

	i.retrieved++

	return event, nil
}
