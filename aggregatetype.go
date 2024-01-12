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

// New is a function that returns a new aggregate instance.
func (t AggregateType[D]) New(id Identifier) *Aggregate[D] {
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

func DefaultAggregateIDFactory() AggregateIDFactory {
	return func() Identifier {
		return UUID(uuid.New())
	}
}
