package continuum

type Aggregate struct {
	ID      string
	Type    string
	Version int64
	Events  []Event
}

func (a *Aggregate) Apply(e Event) {
	a.Events = append(a.Events, e)
}
