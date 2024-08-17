package memory

import (
	"context"
	"io"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

type StreamIterator struct {
	streamID  typeid.UUID
	events    []*eventStoreDocument
	cursor    int64
	direction eventstore.ReadStreamDirection
	limit     int64
	retrieved int64
	marshaler estoria.Marshaler[eventstore.Event, *eventstore.Event]
}

func (i *StreamIterator) Next(ctx context.Context) (*eventstore.Event, error) {
	if i.direction == eventstore.Forward && i.cursor >= int64(len(i.events)) {
		return nil, io.EOF
	} else if i.direction == eventstore.Reverse && i.cursor < 0 {
		return nil, io.EOF
	} else if i.limit > 0 && i.retrieved >= i.limit {
		return nil, io.EOF
	}

	doc := i.events[i.cursor]
	if i.direction == eventstore.Reverse {
		i.cursor--
	} else {
		i.cursor++
	}

	i.retrieved++

	event := &eventstore.Event{}
	if err := i.marshaler.Unmarshal(doc.Data, event); err != nil {
		return nil, err
	}

	return event, nil
}
