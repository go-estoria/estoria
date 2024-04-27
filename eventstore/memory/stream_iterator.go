package memory

import (
	"context"
	"io"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type StreamIterator struct {
	streamID  typeid.AnyID
	events    []estoria.EventStoreEvent
	cursor    int64
	direction estoria.ReadStreamDirection
	limit     int64
	retrieved int64
}

func (i *StreamIterator) Next(ctx context.Context) (estoria.EventStoreEvent, error) {
	if i.direction == estoria.Forward && i.cursor >= int64(len(i.events)) {
		return nil, io.EOF
	} else if i.direction == estoria.Reverse && i.cursor < 0 {
		return nil, io.EOF
	} else if i.limit > 0 && i.retrieved >= i.limit {
		return nil, io.EOF
	}

	event := i.events[i.cursor]
	if i.direction == estoria.Reverse {
		i.cursor--
	} else {
		i.cursor++
	}

	i.retrieved++

	return event, nil
}
