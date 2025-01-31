package memory

import (
	"context"

	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
)

type streamIterator struct {
	streamID  typeid.UUID
	events    []*eventStoreDocument
	cursor    int64
	direction eventstore.ReadStreamDirection
	limit     int64
	retrieved int64
	marshaler EventMarshaler
}

func (i *streamIterator) Next(_ context.Context) (*eventstore.Event, error) {
	switch {
	case i == nil, i.events == nil:
		return nil, eventstore.ErrStreamIteratorClosed
	case i.direction == eventstore.Forward && i.cursor >= int64(len(i.events)):
		return nil, eventstore.ErrEndOfEventStream
	case i.direction == eventstore.Reverse && i.cursor < 0:
		return nil, eventstore.ErrEndOfEventStream
	case i.limit > 0 && i.retrieved >= i.limit:
		return nil, eventstore.ErrEndOfEventStream
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
		return nil, eventstore.EventUnmarshalingError{StreamID: i.streamID, Err: err}
	}

	return event, nil
}

func (i *streamIterator) Close(_ context.Context) error {
	i.events = nil
	return nil
}
