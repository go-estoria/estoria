package aggregatestore

import "github.com/go-estoria/estoria/typeid"

// Test-only exports. These give external tests access to aggregate internals that the
// public API reaches only through full store flows, so that store implementations can
// be unit tested against mock inner stores.

// NewAggregateForTest constructs an aggregate directly, as stores do internally.
func NewAggregateForTest[S any](id typeid.ID, state S, version int64) *Aggregate[S] {
	return newAggregate(id, state, version)
}

// TestOnlyWillApply enqueues an event to be applied, as a store does after persisting it.
func (a *Aggregate[S]) TestOnlyWillApply(event *Event[S]) {
	a.willApply(event)
}

// TestOnlySetStateAtVersion sets the aggregate's state and version, as the snapshotting
// store does after decoding a snapshot.
func (a *Aggregate[S]) TestOnlySetStateAtVersion(state S, version int64) {
	a.setStateAtVersion(state, version)
}

// TestOnlyUnappliedEvents returns the events queued for application, as a save with
// SkipApply leaves them, so tests can observe the identities a save copied onto them.
func (a *Aggregate[S]) TestOnlyUnappliedEvents() []*Event[S] {
	return a.unappliedEvents
}
