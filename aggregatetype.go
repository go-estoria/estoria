package continuum

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// An AggregateIDFactory returns a new identifier for an aggregate.
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
	if id == nil {
		id = t.newID()
		slog.Debug("creating new aggregate", "type", t.name, "id", id)
	} else {
		slog.Debug("creating aggregate from existing ID", "type", t.name, "id", id)
	}

	aggregate := &Aggregate{
		ID: AggregateID{
			Type: t,
			ID:   id,
		},
		Data: t.newData(),
	}

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
