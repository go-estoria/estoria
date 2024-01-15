package continuum

import (
	"context"
)

type AggregateCreator interface {
	NewAggregate(id Identifier) *Aggregate
}

type AggregateReader interface {
	ReadAggregate(ctx context.Context, id AggregateID) (*Aggregate, error)
}

type AggregateWriter interface {
	WriteAggregate(ctx context.Context, aggregate *Aggregate) error
}

// An AggregateStore loads and saves aggregates.
type AggregateStore struct {
	Reader AggregateReader
	Writer AggregateWriter
}

func NewAggregateStore(reader AggregateReader, writer AggregateWriter) (*AggregateStore, error) {
	return &AggregateStore{
		Reader: reader,
		Writer: writer,
	}, nil
}

func (c *AggregateStore) Load(ctx context.Context, id AggregateID) (*Aggregate, error) {
	return c.Reader.ReadAggregate(ctx, id)
}

func (c *AggregateStore) Save(ctx context.Context, aggregate *Aggregate) error {
	return c.Writer.WriteAggregate(ctx, aggregate)
}
