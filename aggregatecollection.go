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

// An AggregateCollection is a collection of a specific type of aggregate.
type AggregateCollection struct {
	AggregateFactory AggregateCreator
	Reader           AggregateReader
	Writer           AggregateWriter
}

func NewAggregateCollection(aggregateType *AggregateType, reader AggregateReader, writer AggregateWriter) (*AggregateCollection, error) {
	return &AggregateCollection{
		Reader:           reader,
		Writer:           writer,
		AggregateFactory: aggregateType,
	}, nil
}

func (c *AggregateCollection) Create(id Identifier) *Aggregate {
	return c.AggregateFactory.NewAggregate(id)
}

func (c *AggregateCollection) Load(ctx context.Context, id AggregateID) (*Aggregate, error) {
	return c.Reader.ReadAggregate(ctx, id)
}

func (c *AggregateCollection) Save(ctx context.Context, aggregate *Aggregate) error {
	return c.Writer.WriteAggregate(ctx, aggregate)
}
