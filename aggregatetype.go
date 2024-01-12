package continuum

import (
	"log/slog"

	"github.com/google/uuid"
)

// An AggregateIDFactory returns a new aggregate ID.
type AggregateIDFactory func() Identifier

// An AggregateDataFactory returns a new aggregate data instance.
type AggregateDataFactory[D AggregateData] func() D

type AggregateType[D AggregateData] struct {
	Name string

	// newID returns a new aggregate ID.
	newID AggregateIDFactory

	// newData returns a new aggregate data instance.
	newData AggregateDataFactory[D]
}

func NewAggregateType[D AggregateData](name string, dataFactory AggregateDataFactory[D], opts ...AggregateTypeOption[D]) AggregateType[D] {
	if dataFactory == nil {
		panic("aggregate type has no data factory")
	}

	aggregateType := AggregateType[D]{
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
func (t AggregateType[D]) NewAggregate(id Identifier) *Aggregate[D] {
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

	aggregate := &Aggregate[D]{
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

type AggregateTypeOption[D AggregateData] func(*AggregateType[D])

// WithAggregateIDFactory is an AggregateTypeOption that sets the aggregate ID factory.
func WithAggregateIDFactory[D AggregateData](factory AggregateIDFactory) AggregateTypeOption[D] {
	return func(t *AggregateType[D]) {
		t.newID = factory
	}
}
