package continuum

import "fmt"

type AggregateData interface {
	AggregateTypeName() string
	ApplyEvent(event EventData) error
}

type Aggregate[D AggregateData] struct {
	ID            string
	Version       int64
	Events        []*Event
	UnsavedEvents []*Event
	Data          D
}

func (a *Aggregate[D]) Apply(event EventData) error {
	if err := a.Data.ApplyEvent(event); err != nil {
		return fmt.Errorf("applying event: %w", err)
	}

	a.UnsavedEvents = append(a.Events, &Event{
		AggregateID:   a.ID,
		AggregateType: a.TypeName(),
		Data:          event,
		Version:       a.Version + 1,
	})

	return nil
}

func (a *Aggregate[D]) TypeName() string {
	return a.Data.AggregateTypeName()
}

type AggregatesByID[D AggregateData] map[string]*Aggregate[D]
