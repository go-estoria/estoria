package continuum

import (
	"log/slog"

	"github.com/google/uuid"
)

// An AggregateIDFactory returns a new aggregate ID.
type AggregateIDFactory func() Identifier

// An AggregateDataFactory returns a new aggregate data instance.
type AggregateDataFactory func() AggregateData

type AggregateType struct {
	Name string

	// newID returns a new aggregate ID.
	newID AggregateIDFactory

	// newData returns a new aggregate data instance.
	newData AggregateDataFactory
}

func NewAggregateType(name string, dataFactory AggregateDataFactory, opts ...AggregateTypeOption) AggregateType {
	if dataFactory == nil {
		panic("aggregate type has no data factory")
	}

	aggregateType := AggregateType{
		Name:    name,
		newID:   DefaultAggregateIDFactory(),
		newData: dataFactory,
	}

	for _, opt := range opts {
		opt(&aggregateType)
	}

	return aggregateType
}

// NewAggregate returns a new aggregate instance.
func (t AggregateType) NewAggregate(id Identifier) *Aggregate {
	isNew := id == nil
	if isNew {
		if t.newID == nil {
			slog.Warn("aggregate type has no ID factory, using default", "aggregate_type", t.Name)
			t.newID = DefaultAggregateIDFactory()
		}

		id = t.newID()
	}

	if t.newData == nil {
		panic("aggregate type has no data factory")
	}

	aggregate := &Aggregate{
		Type: t,
		ID:   id,
		Data: t.newData(),
	}

	slog.Info("instantiating aggregate", "new", isNew, "type", t.Name, "id", aggregate.ID)

	return aggregate
}

// DefaultAggregateIDFactory returns a new UUID.
func DefaultAggregateIDFactory() AggregateIDFactory {
	return func() Identifier {
		return UUID(uuid.New())
	}
}

type AggregateTypeOption func(*AggregateType)

// WithAggregateIDFactory is an AggregateTypeOption that sets the aggregate ID factory.
func WithAggregateIDFactory(factory AggregateIDFactory) AggregateTypeOption {
	return func(t *AggregateType) {
		t.newID = factory
	}
}
