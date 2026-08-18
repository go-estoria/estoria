package lifecycle

import (
	"errors"
	"testing"
	"time"

	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	"github.com/gofrs/uuid/v5"
)

// TestProcessorExit_Classification pins the exit mapping and its precedence:
// a recorded fail-closed cause always wins, a deliberate stop is nil, and
// anything else is the processor's own error. The classification reads both
// fields in one critical section; this table pins what each joint state must
// map to.
func TestProcessorExit_Classification(t *testing.T) {
	t.Parallel()

	procErr := errors.New("processor exit error")
	failure := errors.New("fail-closed cause")

	for _, tt := range []struct {
		name    string
		stopped bool
		failure error
		want    error
	}{
		{name: "running exit surfaces the processor error", stopped: false, failure: nil, want: procErr},
		{name: "deliberate stop is clean", stopped: true, failure: nil, want: nil},
		{name: "fail-closed stop surfaces its cause", stopped: true, failure: failure, want: failure},
		{name: "a recorded cause wins even without the stop flag", stopped: false, failure: failure, want: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &Rebuild{stopped: tt.stopped, failure: tt.failure}

			if got := r.processorExit(procErr); !errors.Is(got, tt.want) {
				t.Errorf("want %v, got %v", tt.want, got)
			}
		})
	}
}

// TestCheckLifecycleAggregate_RejectsForeignAggregate is the supplemental
// direct proof that the helper refuses a self-consistent foreign history: a
// fresh fold that never knew its address accepts a foreign name without
// poisoning, so State.validate alone passes it, and only the check against
// the addressing name refuses. This proves the helper's behavior, not that
// commands call it — the behavioral snapshot-hydration tests prove the call
// sites.
func TestCheckLifecycleAggregate_RejectsForeignAggregate(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	customersV1 := projection.ID{Name: "customers", Version: 1}
	at := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	// A full, internally consistent customers history lands at the stream
	// addressed by "orders". Folding from empty state, the first admission
	// sets the name, so nothing poisons.
	aggregate := store.New(StreamUUID("orders"))
	aggregate.Append(
		RebuildInitiated{Attempt: uuid.Must(uuid.NewV4()), Target: customersV1, Reason: "takeover", At: at},
		BuildStarted{},
		CaughtUp{Position: 1, At: at},
		Promoted{Next: customersV1, At: at},
	)

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving the foreign history: %v", err)
	}

	state := aggregate.State()
	if state.InvalidReason != "" {
		t.Fatalf("want a clean foreign fold for this proof, got mark %q", state.InvalidReason)
	}

	if err := state.validate(); err != nil {
		t.Fatalf("want the foreign state structurally valid — validate alone cannot catch it — got %v", err)
	}

	err = checkLifecycleAggregate(aggregate, "orders")
	if err == nil {
		t.Fatal("want the foreign aggregate refused against the addressing name, got nil")
	}

	if !errors.Is(err, ErrInvalidState) {
		t.Errorf("want the refusal to wrap ErrInvalidState, got %v", err)
	}
}
