package estoria_test

import (
	"context"
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// mockEntity is value-typed, the shape every entity double in this repo used before the
// snapshot-corruption fix.
type mockEntity struct {
	ID      uuid.UUID `json:"id"`
	Owner   string    `json:"owner"`
	Balance int64     `json:"balance"`
}

func (e mockEntity) EntityID() typeid.ID {
	return typeid.New("mockentity", e.ID)
}

// mockPointerEntity is pointer-typed. Marshaling writes through it in place, which is the
// property that made a failed snapshot decode corrupt live state.
type mockPointerEntity struct {
	ID      uuid.UUID `json:"id"`
	Owner   string    `json:"owner"`
	Balance int64     `json:"balance"`
}

func (e *mockPointerEntity) EntityID() typeid.ID {
	return typeid.New("mockpointerentity", e.ID)
}

func TestJSONMarshaler_ValueEntity(t *testing.T) {
	t.Parallel()

	marshaler := estoria.JSONMarshaler[mockEntity]{}
	want := mockEntity{ID: uuid.Must(uuid.NewV4()), Owner: "alice", Balance: 42}

	data, err := marshaler.MarshalEntity(want)
	if err != nil {
		t.Fatalf("marshaling entity: %v", err)
	}

	var got mockEntity
	if err := marshaler.UnmarshalEntity(data, &got); err != nil {
		t.Fatalf("unmarshaling entity: %v", err)
	}

	if got != want {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

func TestJSONMarshaler_PointerEntity(t *testing.T) {
	t.Parallel()

	marshaler := estoria.JSONMarshaler[*mockPointerEntity]{}
	want := &mockPointerEntity{ID: uuid.Must(uuid.NewV4()), Owner: "alice", Balance: 42}

	data, err := marshaler.MarshalEntity(want)
	if err != nil {
		t.Fatalf("marshaling entity: %v", err)
	}

	got := &mockPointerEntity{}
	if err := marshaler.UnmarshalEntity(data, &got); err != nil {
		t.Fatalf("unmarshaling entity: %v", err)
	}

	if *got != *want {
		t.Errorf("want %+v, got %+v", *want, *got)
	}
}

// TestJSONMarshaler_UnmarshalEntity_Invalid pins that a decode failure is reported rather
// than swallowed. SnapshottingStore relies on the error to decide whether to fall back to
// full hydration instead of trusting a half-decoded entity.
func TestJSONMarshaler_UnmarshalEntity_Invalid(t *testing.T) {
	t.Parallel()

	var dest mockEntity
	if err := (estoria.JSONMarshaler[mockEntity]{}).UnmarshalEntity([]byte(`{"balance":"not a number"}`), &dest); err == nil {
		t.Error("want an error for a type-mismatched field, got nil")
	}
}

// mockEntityEvent is a minimal EntityEvent used to exercise the event marshaler.
type mockEntityEvent struct {
	Amount int64 `json:"amount"`
}

func (e *mockEntityEvent) EventType() string { return "mockentityevent" }

func (e *mockEntityEvent) New() estoria.EntityEvent[mockEntity] {
	return &mockEntityEvent{}
}

func (e *mockEntityEvent) ApplyTo(_ context.Context, entity mockEntity) (mockEntity, error) {
	entity.Balance += e.Amount
	return entity, nil
}

func TestJSONEntityEventMarshaler(t *testing.T) {
	t.Parallel()

	marshaler := estoria.JSONEntityEventMarshaler[mockEntity]{}

	data, err := marshaler.MarshalEntityEvent(&mockEntityEvent{Amount: 100})
	if err != nil {
		t.Fatalf("marshaling entity event: %v", err)
	}

	got := &mockEntityEvent{}
	if err := marshaler.UnmarshalEntityEvent(data, got); err != nil {
		t.Fatalf("unmarshaling entity event: %v", err)
	}

	if got.Amount != 100 {
		t.Errorf("want amount 100, got %d", got.Amount)
	}
}

// TestJSONEntityEventMarshaler_UnmarshalEntityEvent_Invalid pins that a malformed payload is
// reported. EventSourcedStore surfaces this as a hydration failure rather than applying an
// event it could not read.
func TestJSONEntityEventMarshaler_UnmarshalEntityEvent_Invalid(t *testing.T) {
	t.Parallel()

	dest := &mockEntityEvent{}
	if err := (estoria.JSONEntityEventMarshaler[mockEntity]{}).UnmarshalEntityEvent([]byte(`{`), dest); err == nil {
		t.Error("want an error for malformed JSON, got nil")
	}
}

// TestEntityEvent_ApplyTo covers the double satisfying the EntityEvent contract, so the
// interface's shape is exercised rather than only its marshaling.
func TestEntityEvent_ApplyTo(t *testing.T) {
	t.Parallel()

	var event estoria.EntityEvent[mockEntity] = &mockEntityEvent{Amount: 25}

	if got := event.EventType(); got != "mockentityevent" {
		t.Errorf("want event type %q, got %q", "mockentityevent", got)
	}

	if got := event.New(); got == nil {
		t.Error("want a new instance, got nil")
	}

	got, err := event.ApplyTo(t.Context(), mockEntity{Balance: 100})
	if err != nil {
		t.Fatalf("applying event: %v", err)
	}

	if got.Balance != 125 {
		t.Errorf("want balance 125, got %d", got.Balance)
	}
}

// TestEntityFactory covers the factory signature aggregate stores are constructed with.
func TestEntityFactory(t *testing.T) {
	t.Parallel()

	id := uuid.Must(uuid.NewV4())

	var factory estoria.EntityFactory[mockEntity] = func(id uuid.UUID) mockEntity {
		return mockEntity{ID: id}
	}

	if got := factory(id); got.EntityID() != typeid.New("mockentity", id) {
		t.Errorf("want the factory to build an entity carrying the given ID, got %s", got.EntityID())
	}
}
