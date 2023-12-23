package continuum

type AggregateStore interface {
	Load(id string) (Aggregate, error)
	Save(a Aggregate) error
}
