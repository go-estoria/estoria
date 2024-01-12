package continuum

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

type AggregateReader[D AggregateData] interface {
	ReadAggregate(ctx context.Context, id AggregateID) (*Aggregate[D], error)
}

type AggregateWriter[D AggregateData] interface {
	WriteAggregate(ctx context.Context, aggregate *Aggregate[D]) error
}

// An AggregateCollection is a collection of a specific type of aggregate.
type AggregateCollection[D AggregateData] struct {
	AggregateType AggregateType[D]
	Reader        AggregateReader[D]
	Writer        AggregateWriter[D]
}

func NewAggregateCollection[D AggregateData](aggregateType AggregateType[D], reader AggregateReader[D], writer AggregateWriter[D]) (*AggregateCollection[D], error) {
	if aggregateType.newID == nil {
		slog.Warn("aggregate type defaults to UUID ID factory", "type", aggregateType.Name)
		aggregateType.newID = func() Identifier {
			return UUID(uuid.New())
		}
	}

	if aggregateType.newData == nil {
		return nil, fmt.Errorf("aggregate type %s is missing a data factory", aggregateType.Name)
	}

	return &AggregateCollection[D]{
		Reader:        reader,
		Writer:        writer,
		AggregateType: aggregateType,
	}, nil
}

func (c *AggregateCollection[D]) Create(id Identifier) *Aggregate[D] {
	return c.AggregateType.NewAggregate(id)
}

func (c *AggregateCollection[D]) Load(ctx context.Context, id Identifier) (*Aggregate[D], error) {
	return c.Reader.ReadAggregate(ctx, AggregateID{
		ID:   id,
		Type: c.AggregateType.Name,
	})
}

func (c *AggregateCollection[D]) Save(ctx context.Context, aggregate *Aggregate[D]) error {
	return c.Writer.WriteAggregate(ctx, aggregate)
}
