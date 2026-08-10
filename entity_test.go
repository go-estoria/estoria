package estoria_test

import (
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/gofrs/uuid/v5"
)

// mockState is value-typed, the shape every state double in this repo used before the
// snapshot-corruption fix.
type mockState struct {
	ID      uuid.UUID `json:"id"`
	Owner   string    `json:"owner"`
	Balance int64     `json:"balance"`
}

// mockPointerState is pointer-typed. Marshaling writes through it in place, which is the
// property that made a failed snapshot decode corrupt live state.
type mockPointerState struct {
	ID      uuid.UUID `json:"id"`
	Owner   string    `json:"owner"`
	Balance int64     `json:"balance"`
}

func TestJSONStateCodec_ValueState(t *testing.T) {
	t.Parallel()

	codec := estoria.JSONStateCodec[mockState]{}
	want := mockState{ID: uuid.Must(uuid.NewV4()), Owner: "alice", Balance: 42}

	data, err := codec.MarshalState(want)
	if err != nil {
		t.Fatalf("marshaling state: %v", err)
	}

	var got mockState
	if err := codec.UnmarshalState(data, &got); err != nil {
		t.Fatalf("unmarshaling state: %v", err)
	}

	if got != want {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

func TestJSONStateCodec_PointerState(t *testing.T) {
	t.Parallel()

	codec := estoria.JSONStateCodec[*mockPointerState]{}
	want := &mockPointerState{ID: uuid.Must(uuid.NewV4()), Owner: "alice", Balance: 42}

	data, err := codec.MarshalState(want)
	if err != nil {
		t.Fatalf("marshaling state: %v", err)
	}

	got := &mockPointerState{}
	if err := codec.UnmarshalState(data, &got); err != nil {
		t.Fatalf("unmarshaling state: %v", err)
	}

	if *got != *want {
		t.Errorf("want %+v, got %+v", *want, *got)
	}
}

// TestJSONStateCodec_UnmarshalState_Invalid pins that a decode failure is reported rather
// than swallowed. SnapshottingStore relies on the error to decide whether to fall back to
// full hydration instead of trusting half-decoded state.
func TestJSONStateCodec_UnmarshalState_Invalid(t *testing.T) {
	t.Parallel()

	var dest mockState
	if err := (estoria.JSONStateCodec[mockState]{}).UnmarshalState([]byte(`{"balance":"not a number"}`), &dest); err == nil {
		t.Error("want an error for a type-mismatched field, got nil")
	}
}

// mockDomainEvent is a minimal DomainEvent used to exercise the event codec.
type mockDomainEvent struct {
	Amount int64 `json:"amount"`
}

func (e *mockDomainEvent) EventType() string { return "mockdomainevent" }

func (e *mockDomainEvent) New() estoria.DomainEvent[mockState] {
	return &mockDomainEvent{}
}

func (e *mockDomainEvent) ApplyTo(state mockState) mockState {
	state.Balance += e.Amount
	return state
}

func TestJSONDomainEventCodec(t *testing.T) {
	t.Parallel()

	codec := estoria.JSONDomainEventCodec[mockState]{}

	data, err := codec.MarshalDomainEvent(&mockDomainEvent{Amount: 100})
	if err != nil {
		t.Fatalf("marshaling domain event: %v", err)
	}

	got := &mockDomainEvent{}
	if err := codec.UnmarshalDomainEvent(data, got); err != nil {
		t.Fatalf("unmarshaling domain event: %v", err)
	}

	if got.Amount != 100 {
		t.Errorf("want amount 100, got %d", got.Amount)
	}
}

// TestJSONDomainEventCodec_UnmarshalDomainEvent_Invalid pins that a malformed payload is
// reported. EventSourcedStore surfaces this as a hydration failure rather than applying an
// event it could not read.
func TestJSONDomainEventCodec_UnmarshalDomainEvent_Invalid(t *testing.T) {
	t.Parallel()

	dest := &mockDomainEvent{}
	if err := (estoria.JSONDomainEventCodec[mockState]{}).UnmarshalDomainEvent([]byte(`{`), dest); err == nil {
		t.Error("want an error for malformed JSON, got nil")
	}
}

// TestJSONCodecs_ContentType pins the declaration the JSON codecs stamp on every
// payload they produce, against the literal string rather than the constant: the
// value is wire-visible on stored events and snapshots, so changing it is a
// contract break even if the constant and its uses move in lockstep.
func TestJSONCodecs_ContentType(t *testing.T) {
	t.Parallel()

	if got := (estoria.JSONStateCodec[mockState]{}).ContentType(); got != "application/json" {
		t.Errorf(`want state codec content type "application/json", got %q`, got)
	}

	if got := (estoria.JSONDomainEventCodec[mockState]{}).ContentType(); got != "application/json" {
		t.Errorf(`want domain event codec content type "application/json", got %q`, got)
	}
}

// TestDomainEvent_ApplyTo covers the double satisfying the DomainEvent contract, so the
// interface's shape is exercised rather than only its marshaling.
func TestDomainEvent_ApplyTo(t *testing.T) {
	t.Parallel()

	var event estoria.DomainEvent[mockState] = &mockDomainEvent{Amount: 25}

	if got := event.EventType(); got != "mockdomainevent" {
		t.Errorf("want event type %q, got %q", "mockdomainevent", got)
	}

	if got := event.New(); got == nil {
		t.Error("want a new instance, got nil")
	}

	got := event.ApplyTo(mockState{Balance: 100})
	if got.Balance != 125 {
		t.Errorf("want balance 125, got %d", got.Balance)
	}
}

// TestStateFactory covers the factory signature aggregate stores are constructed with.
func TestStateFactory(t *testing.T) {
	t.Parallel()

	id := uuid.Must(uuid.NewV4())

	var factory estoria.StateFactory[mockState] = func(id uuid.UUID) mockState {
		return mockState{ID: id}
	}

	if got := factory(id); got.ID != id {
		t.Errorf("want the factory to build state carrying the given ID, got %s", got.ID)
	}
}
