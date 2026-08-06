package aggregatestore_test

import (
	"context"
	"fmt"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// Mocks in this file are shared by the tests for multiple aggregate store implementations.

type mockAggregateStore[S any] struct {
	AggregateTypeFn func() string
	NewFn           func(uuid.UUID) *aggregatestore.Aggregate[S]
	LoadFn          func(context.Context, uuid.UUID, *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[S], error)
	HydrateFn       func(context.Context, *aggregatestore.Aggregate[S], *aggregatestore.HydrateOptions) error
	SaveFn          func(context.Context, *aggregatestore.Aggregate[S], *aggregatestore.SaveOptions) error
}

func (s *mockAggregateStore[S]) AggregateType() string {
	if s.AggregateTypeFn != nil {
		return s.AggregateTypeFn()
	}

	return "mockentity"
}

func (s *mockAggregateStore[S]) New(id uuid.UUID) *aggregatestore.Aggregate[S] {
	if s.NewFn != nil {
		return s.NewFn(id)
	}

	return nil
}

func (s *mockAggregateStore[S]) Load(ctx context.Context, aggregateID uuid.UUID, opts *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[S], error) {
	if s.LoadFn != nil {
		return s.LoadFn(ctx, aggregateID, opts)
	}

	return nil, fmt.Errorf("unexpected call: Load(aggregateID=%s, opts=%v)", aggregateID, opts)
}

func (s *mockAggregateStore[S]) Hydrate(ctx context.Context, aggregate *aggregatestore.Aggregate[S], opts *aggregatestore.HydrateOptions) error {
	if s.HydrateFn != nil {
		return s.HydrateFn(ctx, aggregate, opts)
	}

	return fmt.Errorf("unexpected call: Hydrate(aggregate=%v, opts=%v)", aggregate, opts)
}

func (s *mockAggregateStore[S]) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[S], opts *aggregatestore.SaveOptions) error {
	if s.SaveFn != nil {
		return s.SaveFn(ctx, aggregate, opts)
	}

	return fmt.Errorf("unexpected call: Save(aggregate=%v, opts=%v)", aggregate, opts)
}

type mockEntity struct {
	ID                  typeid.ID
	numAppliedEvents    int64
	lastValueEventValue string
}

func newMockEntity(id uuid.UUID) mockEntity {
	return mockEntity{
		ID: typeid.New("mockentity", id),
	}
}

func (e mockEntity) EntityID() typeid.ID {
	return e.ID
}

// newMockAggregate builds an aggregate the way a store would: the typed ID composed
// from the aggregate type name and the UUID, the state from the factory, at a version.
func newMockAggregate(id uuid.UUID, version int64) *aggregatestore.Aggregate[mockEntity] {
	return aggregatestore.NewAggregateForTest(typeid.New("mockentity", id), newMockEntity(id), version)
}
