package estoria

import (
	"context"
	"io"
)

type EventStream interface {
	io.Closer
	ID() Identifier
	Next(ctx context.Context) (Event, error)
	Append(ctx context.Context, events ...Event) error
}
