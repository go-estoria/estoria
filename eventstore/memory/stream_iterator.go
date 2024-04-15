package memory

import (
	"context"
	"io"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type StreamIterator struct {
	streamID  typeid.AnyID
	events    []estoria.Event
	cursor    int64
	direction estoria.ReadStreamDirection
	limit     int64
}

func (i *StreamIterator) Next(ctx context.Context) (estoria.Event, error) {
	if i.direction == estoria.Forward && i.cursor >= int64(len(i.events)) {
		return nil, io.EOF
	} else if i.direction == estoria.Reverse && i.cursor < 0 {
		return nil, io.EOF
	}

	event := i.events[i.cursor]
	i.cursor++

	return event, nil
}
