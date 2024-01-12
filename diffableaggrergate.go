package continuum

// DiffableAggregateData is an entity that can be diffed against another entity to produce a
// series of events that represent the state changes between the two.
type DiffableAggregateData interface {
	AggregateData
	Diff(newer DiffableAggregateData) ([]EventData, error)
}
