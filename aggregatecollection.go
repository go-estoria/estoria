package continuum

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

type AggregateReader interface {
	ReadAggregate(ctx context.Context, id AggregateID) (*Aggregate, error)
}

type AggregateWriter interface {
	WriteAggregate(ctx context.Context, aggregate *Aggregate) error
}

// An AggregateCollection is a collection of a specific type of aggregate.
type AggregateCollection struct {
	AggregateType AggregateType
	Reader        AggregateReader
	Writer        AggregateWriter
}

func NewAggregateCollection(aggregateType AggregateType, reader AggregateReader, writer AggregateWriter) (*AggregateCollection, error) {
	if aggregateType.newID == nil {
		slog.Warn("aggregate type defaults to UUID ID factory", "type", aggregateType.Name)
		aggregateType.newID = func() Identifier {
			return UUID(uuid.New())
		}
	}

	if aggregateType.newData == nil {
		return nil, fmt.Errorf("aggregate type %s is missing a data factory", aggregateType.Name)
	}

	return &AggregateCollection{
		Reader:        reader,
		Writer:        writer,
		AggregateType: aggregateType,
	}, nil
}

func (c *AggregateCollection) Create(id Identifier) *Aggregate {
	return c.AggregateType.NewAggregate(id)
}

func (c *AggregateCollection) Load(ctx context.Context, id Identifier) (*Aggregate, error) {
	return c.Reader.ReadAggregate(ctx, AggregateID{
		ID:   id,
		Type: c.AggregateType.Name,
	})
}

func (c *AggregateCollection) Save(ctx context.Context, aggregate *Aggregate) error {
	return c.Writer.WriteAggregate(ctx, aggregate)
}
