package aggregatestore_test

import (
	"context"
	"fmt"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// Mocks in this file are shared by the tests for multiple aggregate store implementations.

type mockAggregateStore[E estoria.Entity] struct {
	NewFn     func(uuid.UUID) *aggregatestore.Aggregate[E]
	LoadFn    func(context.Context, uuid.UUID, *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[E], error)
	HydrateFn func(context.Context, *aggregatestore.Aggregate[E], *aggregatestore.HydrateOptions) error
	SaveFn    func(context.Context, *aggregatestore.Aggregate[E], *aggregatestore.SaveOptions) error
}

func (s *mockAggregateStore[E]) New(id uuid.UUID) *aggregatestore.Aggregate[E] {
	if s.NewFn != nil {
		return s.NewFn(id)
	}

	return nil
}

func (s *mockAggregateStore[E]) Load(ctx context.Context, aggregateID uuid.UUID, opts *aggregatestore.LoadOptions) (*aggregatestore.Aggregate[E], error) {
	if s.LoadFn != nil {
		return s.LoadFn(ctx, aggregateID, opts)
	}

	return nil, fmt.Errorf("unexpected call: Load(aggregateID=%s, opts=%v)", aggregateID, opts)
}

func (s *mockAggregateStore[E]) Hydrate(ctx context.Context, aggregate *aggregatestore.Aggregate[E], opts *aggregatestore.HydrateOptions) error {
	if s.HydrateFn != nil {
		return s.HydrateFn(ctx, aggregate, opts)
	}

	return fmt.Errorf("unexpected call: Hydrate(aggregate=%v, opts=%v)", aggregate, opts)
}

func (s *mockAggregateStore[E]) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[E], opts *aggregatestore.SaveOptions) error {
	if s.SaveFn != nil {
		return s.SaveFn(ctx, aggregate, opts)
	}

	return fmt.Errorf("unexpected call: Save(aggregate=%v, opts=%v)", aggregate, opts)
}

type mockEntity struct {
	ID               typeid.UUID
	numAppliedEvents int64
}

var _ estoria.Entity = mockEntity{}

func newMockEntity(id uuid.UUID) mockEntity {
	return mockEntity{
		ID: typeid.FromUUID("mockentity", id),
	}
}

func (e mockEntity) EntityID() typeid.UUID {
	return e.ID
}
