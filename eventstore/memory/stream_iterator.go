package memory

import (
	"context"
	"io"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type StreamIterator struct {
	streamID typeid.AnyID
	events   []estoria.Event
	cursor   int
}

func (i *StreamIterator) Next(ctx context.Context) (estoria.Event, error) {
	if i.cursor < len(i.events) {
		event := i.events[i.cursor]
		i.cursor++
		return event, nil
	}

	return nil, io.EOF
}
