package aggregatestore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

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

// TestSaveOutcome pins outcome resolution: one answer per error, outermost
// marker first, contradictions to unknown.
func TestSaveOutcome(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		err  error
		want aggregatestore.AppendOutcome
	}{
		{name: "nil vouches for nothing", err: nil, want: aggregatestore.AppendOutcomeUnknown},
		{name: "an unmarked error vouches for nothing", err: errors.New("connection reset"), want: aggregatestore.AppendOutcomeUnknown},
		{name: "the bare appended sentinel", err: aggregatestore.ErrEventsAppended, want: aggregatestore.AppendOutcomeAppended},
		{name: "the bare nothing-appended sentinel", err: aggregatestore.ErrNoEventsAppended, want: aggregatestore.AppendOutcomeNothingAppended},
		{
			name: "a wrapped marker resolves through any depth",
			err:  fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", aggregatestore.ErrEventsAppended)),
			want: aggregatestore.AppendOutcomeAppended,
		},
		{
			name: "agreeing branches resolve to their shared outcome",
			err:  errors.Join(fmt.Errorf("a: %w", aggregatestore.ErrNoEventsAppended), errors.New("b")),
			want: aggregatestore.AppendOutcomeNothingAppended,
		},
		{
			name: "contradicting branches at the same depth resolve to unknown",
			err:  errors.Join(fmt.Errorf("a: %w", aggregatestore.ErrEventsAppended), fmt.Errorf("b: %w", aggregatestore.ErrNoEventsAppended)),
			want: aggregatestore.AppendOutcomeUnknown,
		},
		{
			name: "the shallower marker wins across branches",
			err: errors.Join(
				aggregatestore.ErrNoEventsAppended,
				fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", aggregatestore.ErrEventsAppended)),
			),
			want: aggregatestore.AppendOutcomeNothingAppended,
		},
		{
			name: "a contradiction at the marker depth poisons an agreeing sibling branch",
			err: errors.Join(
				errors.Join(aggregatestore.ErrEventsAppended, aggregatestore.ErrNoEventsAppended),
				errors.Join(aggregatestore.ErrNoEventsAppended),
			),
			want: aggregatestore.AppendOutcomeUnknown,
		},
		{
			name: "a shallower marker shadows a deeper contradiction",
			err: errors.Join(
				aggregatestore.ErrNoEventsAppended,
				fmt.Errorf("outer: %w", errors.Join(aggregatestore.ErrEventsAppended, aggregatestore.ErrNoEventsAppended)),
			),
			want: aggregatestore.AppendOutcomeNothingAppended,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := aggregatestore.SaveOutcome(tt.err); got != tt.want {
				t.Errorf("want %v, got %v", tt.want, got)
			}
		})
	}

	t.Run("a store's own marker shadows a marker inside the cause", func(t *testing.T) {
		t.Parallel()

		store, err := aggregatestore.NewHookableStore[mockEntity](&mockAggregateStore[mockEntity]{})
		if err != nil {
			t.Fatalf("creating hookable store: %v", err)
		}

		store.BeforeSave(func(context.Context, *aggregatestore.Aggregate[mockEntity]) error {
			return fmt.Errorf("propagating a foreign failure: %w", aggregatestore.ErrEventsAppended)
		})

		saveErr := store.Save(t.Context(), newMockAggregate(uuid.Must(uuid.NewV4()), 1), nil)
		if !errors.Is(saveErr, aggregatestore.ErrEventsAppended) {
			t.Fatalf("want the buried foreign marker still visible to errors.Is, got %v", saveErr)
		}
		if got := aggregatestore.SaveOutcome(saveErr); got != aggregatestore.AppendOutcomeNothingAppended {
			t.Errorf("want the refusal's own marker to win, got %v", got)
		}
	})
}
