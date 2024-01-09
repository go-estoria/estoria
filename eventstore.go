package continuum

import "context"

type EventReader interface {
	ReadEvents(ctx context.Context, aggregateID AggregateID) ([]Event, error)
}

type EventWriter interface {
	WriteEvent(ctx context.Context, event Event) error
}

type EventStore struct {
	Reader EventReader
	Writer EventWriter
}

func (s EventStore) LoadEvents(ctx context.Context, aggregateID AggregateID) ([]Event, error) {
	return s.Reader.ReadEvents(ctx, aggregateID)
}

func (s EventStore) SaveEvent(ctx context.Context, event Event) error {
	return s.Writer.WriteEvent(ctx, event)
}
