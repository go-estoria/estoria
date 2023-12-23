package continuum

type AggregateData interface {
	AggregateTypeName() string
}

type Aggregate[D AggregateData] struct {
	ID            string
	Version       int64
	Events        []*Event
	UnsavedEvents []*Event
	Data          D
}

func (a *Aggregate[D]) Apply(event EventData) {
	a.UnsavedEvents = append(a.Events, &Event{
		AggregateID:   a.ID,
		AggregateType: a.TypeName(),
		Data:          event,
		Version:       a.Version + 1,
	})
}

func (a *Aggregate[D]) TypeName() string {
	return a.Data.AggregateTypeName()
}

type AggregatesByID[D AggregateData] map[string]*Aggregate[D]
