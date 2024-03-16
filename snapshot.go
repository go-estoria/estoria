package estoria

import "go.jetpack.io/typeid"

type snapshot struct {
	AggregateID      typeid.AnyID
	AggregateVersion int64
	event
}
