package lifecycle

import (
	"context"
	"errors"
	"fmt"
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

// leafCancellationError is a cancellation-aware error whose unwrapping yields no
// children: Unwrap exists but returns nil, the shape errors.Is treats as a
// leaf. A tree walk that recurses into the nil child instead of matching the
// node misreads it as a non-cancellation.
type leafCancellationError struct{ cancel bool }

func (e leafCancellationError) Error() string { return "leaf cancellation" }

func (e leafCancellationError) Is(target error) bool { return e.cancel && target == context.Canceled }

func (e leafCancellationError) Unwrap() error { return nil }

// emptyJoinCancellationError is leafCancellationError's multi-error twin: its
// child list yields no children — nil by default, or the non-nil empty list
// a case supplies, which must read identically.
type emptyJoinCancellationError struct {
	cancel   bool
	children []error
}

func (e emptyJoinCancellationError) Error() string { return "empty-join cancellation" }

func (e emptyJoinCancellationError) Is(target error) bool {
	return e.cancel && target == context.Canceled
}

func (e emptyJoinCancellationError) Unwrap() []error { return e.children }

// TestCancellationOnly pins the leaf semantics of the reconcile loop's benign
// arm: every leaf must match the cancellation, a node whose unwrapping yields
// no children is itself a leaf, and a joined independent failure is never
// discarded. A custom cancellation with a childless Unwrap must read as
// benign — misreading it records a false terminal failure that exit
// classification then ranks above the processor's real error.
func TestCancellationOnly(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	for _, tt := range []struct {
		name   string
		err    error
		target error // matched against context.Canceled when nil
		want   bool
	}{
		{name: "nil error is not a cancellation", err: nil, want: false},
		{name: "the cancellation itself", err: context.Canceled, want: true},
		{name: "wrapped cancellation", err: fmt.Errorf("tick: %w", context.Canceled), want: true},
		{name: "joined cancellations", err: errors.Join(context.Canceled, context.Canceled), want: true},
		{name: "joined independent failure is not benign", err: errors.Join(context.Canceled, errBoom), want: false},
		{name: "plain failure", err: errBoom, want: false},
		{name: "cancellation-aware leaf with a nil Unwrap", err: leafCancellationError{cancel: true}, want: true},
		{name: "non-cancellation leaf with a nil Unwrap", err: leafCancellationError{}, want: false},
		{name: "cancellation-aware empty join", err: emptyJoinCancellationError{cancel: true}, want: true},
		{name: "non-cancellation empty join", err: emptyJoinCancellationError{}, want: false},
		{name: "cancellation-aware non-nil empty join", err: emptyJoinCancellationError{cancel: true, children: []error{}}, want: true},
		{name: "non-cancellation non-nil empty join", err: emptyJoinCancellationError{children: []error{}}, want: false},
		{name: "wrapped cancellation-aware leaf", err: fmt.Errorf("tick: %w", leafCancellationError{cancel: true}), want: true},
		{name: "cancellation-aware leaf joined with a failure", err: errors.Join(leafCancellationError{cancel: true}, errBoom), want: false},
		{name: "deadline exceeded matched against itself", err: context.DeadlineExceeded, target: context.DeadlineExceeded, want: true},
		{name: "wrapped deadline exceeded", err: fmt.Errorf("tick: %w", context.DeadlineExceeded), target: context.DeadlineExceeded, want: true},
		{name: "deadline exceeded is not the canceled target", err: context.DeadlineExceeded, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			target := tt.target
			if target == nil {
				target = context.Canceled
			}

			if got := cancellationOnly(tt.err, target); got != tt.want {
				t.Errorf("want %v, got %v", tt.want, got)
			}
		})
	}
}

// caughtUpRebuildForTest builds a handle over a real caught-up lifecycle
// aggregate, for pinning Promote's certificate arms directly: the arms guard
// against windows — a lifecycle version that advanced past the certificate,
// a processor that exited a moment ago — that behavioral tests cannot hold
// open deterministically.
func caughtUpRebuildForTest(t *testing.T) *Rebuild {
	t.Helper()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	at := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

	aggregate := store.New(StreamUUID("orders"))
	aggregate.Append(
		RebuildInitiated{Attempt: uuid.Must(uuid.NewV4()), Target: v1, Reason: "certificate arms", At: at},
		RunnerClaimed{Runner: uuid.Must(uuid.NewV4()), At: at},
		BuildStarted{},
		CaughtUp{Position: 7, At: at},
	)

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving the caught-up history: %v", err)
	}

	return &Rebuild{name: "orders", aggregate: aggregate, processorExited: make(chan struct{})}
}

// TestPromote_RefusesStaleCertificateVersion pins the certificate's version
// arm: a certificate cut against an older lifecycle version than the
// aggregate now holds is dead — the version only grows — and the refusal
// wraps ErrNotCertified before anything is appended.
func TestPromote_RefusesStaleCertificateVersion(t *testing.T) {
	t.Parallel()

	r := caughtUpRebuildForTest(t)
	r.certificate = &certification{runner: uuid.Must(uuid.NewV4()), position: 7, version: r.aggregate.Version() - 1}

	if err := r.Promote(t.Context()); !errors.Is(err, ErrNotCertified) {
		t.Fatalf("want the stale-version certificate refused with ErrNotCertified, got %v", err)
	}

	if r.certificate != nil {
		t.Error("want the dead certificate cleared by the refusal")
	}
}

// TestPromote_RefusesCertificateOfExitedProcessor pins the certificate's
// liveness arm: a certificate whose processor has already exited is refused
// even when the certificate itself is otherwise current — the in-process
// clear on exit and this check close the same window from both sides.
func TestPromote_RefusesCertificateOfExitedProcessor(t *testing.T) {
	t.Parallel()

	r := caughtUpRebuildForTest(t)
	r.certificate = &certification{runner: uuid.Must(uuid.NewV4()), position: 7, version: r.aggregate.Version()}
	close(r.processorExited)

	if err := r.Promote(t.Context()); !errors.Is(err, ErrNotCertified) {
		t.Fatalf("want the exited processor's certificate refused with ErrNotCertified, got %v", err)
	}

	if r.certificate != nil {
		t.Error("want the dead certificate cleared by the refusal")
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
		RunnerClaimed{Runner: uuid.Must(uuid.NewV4()), At: at},
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

// TestCheckLifecycleAggregate_RejectsMisderivedStream pins the address arm
// no public flow reaches — Begin, Resume, and Get derive the stream from the
// name they load, so only a direct check can hold an aggregate against a
// name it does not derive from — and pins its refusal to ErrInvalidState by
// identity.
func TestCheckLifecycleAggregate_RejectsMisderivedStream(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	aggregate := store.New(StreamUUID("orders"))

	err = checkLifecycleAggregate(aggregate, "customers")
	if err == nil {
		t.Fatal("want an aggregate held against a name it does not derive from refused, got nil")
	}

	if !errors.Is(err, ErrInvalidState) {
		t.Errorf("want the derivation refusal to wrap ErrInvalidState, got %v", err)
	}
}
