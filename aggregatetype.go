package continuum

import (
	"log/slog"

	"github.com/google/uuid"
)

// An AggregateIDFactory is a function that returns a new aggregate ID.
type AggregateIDFactory func() Identifier

// An AggregateDataFactory is a function that returns a new aggregate data instance.
type AggregateDataFactory[D AggregateData] func() D

type AggregateType[D AggregateData] struct {
	Name string

	// IDFactory is a function that returns a new aggregate ID.
	IDFactory AggregateIDFactory

	// DataFactory is a function that returns a new aggregate data instance.
	DataFactory AggregateDataFactory[D]
}

func NewAggregateType[D AggregateData](name string, dataFactory AggregateDataFactory[D], opts ...AggregateTypeOption[D]) AggregateType[D] {
	if dataFactory == nil {
		panic("aggregate type has no data factory")
	}

	aggregateType := AggregateType[D]{
		Name:        name,
		IDFactory:   DefaultAggregateIDFactory(),
		DataFactory: dataFactory,
	}

	for _, opt := range opts {
		opt(&aggregateType)
	}

	return aggregateType
}

// NewAggregate is a function that returns a new aggregate instance.
func (t AggregateType[D]) NewAggregate(id Identifier) *Aggregate[D] {
	isNew := id == nil
	if isNew {
		if t.IDFactory == nil {
			slog.Warn("aggregate type has no ID factory, using default", "aggregate_type", t.Name)
			t.IDFactory = DefaultAggregateIDFactory()
		}

		id = t.IDFactory()
	}

	if t.DataFactory == nil {
		panic("aggregate type has no data factory")
	}

	aggregate := &Aggregate[D]{
		Type: t,
		ID:   id,
		Data: t.DataFactory(),
	}

	slog.Info("instantiating aggregate", "new", isNew, "type", t.Name, "id", aggregate.ID)

	return aggregate
}

// DefaultAggregateIDFactory is a function that returns a new UUID.
func DefaultAggregateIDFactory() AggregateIDFactory {
	return func() Identifier {
		return UUID(uuid.New())
	}
}

type AggregateTypeOption[D AggregateData] func(*AggregateType[D])

// WithAggregateIDFactory is an AggregateTypeOption that sets the aggregate ID factory.
func WithAggregateIDFactory[D AggregateData](factory AggregateIDFactory) AggregateTypeOption[D] {
	return func(t *AggregateType[D]) {
		t.IDFactory = factory
	}
}
