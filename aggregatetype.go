package continuum

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// An AggregateIDFactory returns a new aggregate ID.
type AggregateIDFactory func() Identifier

// An AggregateDataFactory returns a new aggregate data instance.
type AggregateDataFactory func() AggregateData

type AggregateType struct {
	name string

	// newID returns a new aggregate ID.
	newID AggregateIDFactory

	// newData returns a new aggregate data instance.
	newData AggregateDataFactory
}

func NewAggregateType(name string, dataFactory AggregateDataFactory, opts ...AggregateTypeOption) (*AggregateType, error) {
	slog.Debug("creating aggregate type", "type", name)
	if name == "" {
		return nil, fmt.Errorf("aggregate type name is required")
	}

	if dataFactory == nil {
		return nil, fmt.Errorf("aggregate type %s requires a data factory", name)
	}

	aggregateType := &AggregateType{
		name:    name,
		newID:   DefaultAggregateIDFactory(),
		newData: dataFactory,
	}

	for i, opt := range opts {
		slog.Debug(fmt.Sprintf("applying aggregate type option (%d of %d)", i+1, len(opts)))
		opt(aggregateType)
	}

	if aggregateType.newID == nil {
		return nil, fmt.Errorf("aggregate type %s requires an ID factory", name)
	}

	return aggregateType, nil
}

// NewAggregate returns a new aggregate instance.
func (t AggregateType) NewAggregate(id Identifier) *Aggregate {
	isNew := id == nil
	if isNew {
		if t.newID == nil {
			slog.Warn("aggregate type has no ID factory, using default", "aggregate_type", t.name)
			t.newID = DefaultAggregateIDFactory()
		}

		id = t.newID()
	}

	if t.newData == nil {
		panic("aggregate type has no data factory")
	}

	aggregate := &Aggregate{
		ID: AggregateID{
			Type: t,
			ID:   id,
		},
		Data: t.newData(),
	}

	slog.Debug("instantiating aggregate", "new", isNew, "type", t.name, "id", aggregate.ID)

	return aggregate
}

// DefaultAggregateIDFactory returns a new UUID.
func DefaultAggregateIDFactory() AggregateIDFactory {
	return func() Identifier {
		return UUID(uuid.New())
	}
}

// An AggregateTypeOption is an option for configuring an AggregateType.
type AggregateTypeOption func(*AggregateType)

// WithAggregateIDFactory is an AggregateTypeOption that sets the aggregate ID factory.
func WithAggregateIDFactory(factory AggregateIDFactory) AggregateTypeOption {
	return func(t *AggregateType) {
		t.newID = factory
	}
}
