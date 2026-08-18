package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	cpmemory "github.com/go-estoria/estoria/projection/checkpointstore/memory"
	"github.com/go-estoria/estoria/typeid"
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

// nopHandler is the smallest projection.EventHandler, for wiring real
// orchestrators in white-box fixtures.
type nopHandler struct{}

func (nopHandler) Handle(context.Context, *eventstore.Event) error { return nil }

// gatedSaveStore delegates and, when armed, parks the next Save on a gate:
// entered closes when the parked save begins, and the save completes when
// the gate closes. It holds promotion's append open so exit-publication
// ordering can be observed.
type gatedSaveStore struct {
	aggregatestore.Store[State]

	mu      sync.Mutex
	entered chan struct{}
	gate    chan struct{}
}

func (s *gatedSaveStore) armSaveGate() (entered, gate chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entered = make(chan struct{})
	s.gate = make(chan struct{})

	return s.entered, s.gate
}

func (s *gatedSaveStore) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[State], opts *aggregatestore.SaveOptions) error {
	s.mu.Lock()
	entered, gate := s.entered, s.gate
	s.entered, s.gate = nil, nil
	s.mu.Unlock()

	if entered != nil {
		close(entered)
		<-gate
	}

	return s.Store.Save(ctx, aggregate, opts)
}

// caughtUpRebuildForTest builds a handle over a real caught-up lifecycle
// aggregate wired to a real orchestrator, for pinning Promote's certificate
// arms directly: the arms guard windows — a lifecycle version that advanced
// past the certificate, a binding that no longer matches, a processor that
// exited a moment ago — that behavioral tests cannot hold open
// deterministically. The real store makes a mutant that skips an arm
// observable as a durably recorded promotion rather than as a fixture
// panic.
func caughtUpRebuildForTest(t *testing.T, store aggregatestore.Store[State]) (*Rebuild, *certification) {
	t.Helper()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	if store == nil {
		inner, err := NewStore(events)
		if err != nil {
			t.Fatalf("creating lifecycle store: %v", err)
		}

		store = inner
	} else if wrapper, ok := store.(*gatedSaveStore); ok {
		inner, err := NewStore(events)
		if err != nil {
			t.Fatalf("creating lifecycle store: %v", err)
		}

		wrapper.Store = inner
	}

	orchestrator, err := NewOrchestrator(Config{
		Events:      events,
		Checkpoints: cpmemory.NewCheckpointStore(),
		Handler:     func(projection.ID) (projection.EventHandler, error) { return nopHandler{}, nil },
		Projections: store,
	})
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	at := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	attempt := uuid.Must(uuid.NewV4())
	runner := uuid.Must(uuid.NewV4())

	aggregate := store.New(StreamUUID("orders"))
	aggregate.Append(
		RebuildInitiated{Attempt: attempt, Target: v1, Reason: "certificate arms", At: at},
		RunnerClaimed{Attempt: attempt, Runner: runner, At: at},
		BuildStarted{},
		CaughtUp{Position: 7, At: at},
	)

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving the caught-up history: %v", err)
	}

	r := &Rebuild{
		orchestrator:    orchestrator,
		name:            "orders",
		aggregate:       aggregate,
		runner:          runner,
		processorExited: make(chan struct{}),
	}

	return r, &certification{
		attempt:  attempt,
		runner:   runner,
		position: 7,
		version:  aggregate.Version(),
		exited:   r.processorExited,
	}
}

// TestPromote_CertificateBindings pins every arm of the promotion license
// directly: a certificate is honored only when the handle is unrevoked and
// every binding — attempt, runner, position, lifecycle version, processor
// incarnation, and the exit signal itself — still holds. Each refusal wraps
// ErrNotCertified and appends nothing; the intact-certificate control proves
// the fixture promotes for real, so a skipped arm surfaces as a durably
// recorded promotion rather than a fixture artifact.
func TestPromote_CertificateBindings(t *testing.T) {
	t.Parallel()

	t.Run("intact certificate promotes", func(t *testing.T) {
		t.Parallel()

		r, certificate := caughtUpRebuildForTest(t, nil)
		r.certificate = certificate

		if err := r.Promote(t.Context()); err != nil {
			t.Fatalf("want the intact certificate honored, got %v", err)
		}

		if got := r.aggregate.State().Attempt.Phase; got != PhasePromoted {
			t.Errorf("want the promotion recorded, got %s", got)
		}

		if r.certificate != nil {
			t.Error("want the certificate consumed by the promotion")
		}
	})

	for _, tt := range []struct {
		name   string
		mutate func(r *Rebuild, c *certification)
	}{
		{"revoked handle", func(r *Rebuild, _ *certification) {
			r.failure = errors.New("recorded terminal cause")
		}},
		{"stopped run", func(r *Rebuild, _ *certification) {
			r.stopped = true
		}},
		{"different attempt", func(_ *Rebuild, c *certification) {
			c.attempt = uuid.Must(uuid.NewV4())
		}},
		{"superseded runner", func(_ *Rebuild, c *certification) {
			c.runner = uuid.Must(uuid.NewV4())
		}},
		{"different position", func(_ *Rebuild, c *certification) {
			c.position++
		}},
		{"stale lifecycle version", func(_ *Rebuild, c *certification) {
			c.version--
		}},
		{"different processor incarnation", func(_ *Rebuild, c *certification) {
			c.exited = make(chan struct{})
		}},
		{"exited processor", func(r *Rebuild, _ *certification) {
			close(r.processorExited)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, certificate := caughtUpRebuildForTest(t, nil)
			tt.mutate(r, certificate)
			r.certificate = certificate

			versionBefore := r.aggregate.Version()

			if err := r.Promote(t.Context()); !errors.Is(err, ErrNotCertified) {
				t.Fatalf("want the promotion refused with ErrNotCertified, got %v", err)
			}

			if got := r.aggregate.Version(); got != versionBefore {
				t.Errorf("want nothing appended by the refusal, got version %d (was %d)", got, versionBefore)
			}

			if r.certificate != nil {
				t.Error("want the dead certificate cleared by the refusal")
			}
		})
	}
}

// TestPromote_RevocationCarriesTheCause pins the revoked arm's error shape:
// the refusal carries both the typed not-certified sentinel and the recorded
// terminal cause, so a caller can tell a revoked handle from a merely
// uncertified one.
func TestPromote_RevocationCarriesTheCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("the recorded terminal cause")

	r, certificate := caughtUpRebuildForTest(t, nil)
	r.certificate = certificate
	r.stopped = true
	r.failure = cause

	err := r.Promote(t.Context())
	if !errors.Is(err, ErrNotCertified) {
		t.Fatalf("want the refusal to wrap ErrNotCertified, got %v", err)
	}

	if !errors.Is(err, cause) {
		t.Errorf("want the refusal to carry the recorded cause, got %v", err)
	}
}

// TestPublishProcessorExit_SerializesWithPromotion pins exit publication
// against the promotion window: publication takes the handle lock a running
// promotion holds through its append, so the exit signal cannot close
// between a certificate check and the append it authorized. The publication
// must block while the promotion's save is parked and land only after it —
// and a promotion attempted after publication is refused.
func TestPublishProcessorExit_SerializesWithPromotion(t *testing.T) {
	t.Parallel()

	store := &gatedSaveStore{}

	r, certificate := caughtUpRebuildForTest(t, store)
	r.certificate = certificate

	entered, gate := store.armSaveGate()

	promoted := make(chan error, 1)

	go func() { promoted <- r.Promote(t.Context()) }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the promotion save to start")
	}

	published := make(chan struct{})
	exited := r.processorExited

	go func() {
		r.publishProcessorExit()
		close(published)
	}()

	// One-sided by construction: an unserialized publication closes the exit
	// signal immediately, so observing the signal itself still open while the
	// save is parked is the whole assertion — helper completion alone would
	// miss a close that happened before the lock was taken.
	select {
	case <-exited:
		t.Fatal("want the exit signal unclosed while the promotion save is in flight")
	case <-time.After(150 * time.Millisecond):
	}

	close(gate)

	if err := <-promoted; err != nil {
		t.Fatalf("want the in-flight promotion to commit before the exit publishes, got %v", err)
	}

	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the exit publication after the save completed")
	}

	if got := r.aggregate.State().Attempt.Phase; got != PhasePromoted {
		t.Errorf("want the promotion durable, got %s", got)
	}
}

// TestRecordCatchUp_RefusesPublishedExit pins certification's exit check: the
// caught-up signal and the processor's exit can be ready together, and a
// certificate created after the exit published would outlive its processor —
// promotion would refuse on the dead signal, and the refusal would shadow the
// processor's real result. recordCatchUp must refuse instead: no append, no
// certificate.
func TestRecordCatchUp_RefusesPublishedExit(t *testing.T) {
	t.Parallel()

	r, _ := caughtUpRebuildForTest(t, nil)

	r.publishProcessorExit()

	versionBefore := r.aggregate.Version()

	stopped, exited, err := r.recordCatchUp(t.Context(), r.aggregate.State().Attempt.ID, 9, time.Second)

	switch {
	case err != nil:
		t.Fatalf("want the published exit refused without error, got %v", err)
	case stopped:
		t.Fatal("want the refusal reported as an exit, not a stop")
	case !exited:
		t.Fatal("want the published exit reported")
	}

	if got := r.aggregate.Version(); got != versionBefore {
		t.Errorf("want no catch-up appended after the exit published, got version %d (was %d)", got, versionBefore)
	}

	if r.certificate != nil {
		t.Error("want no certificate created against a published exit")
	}
}

// TestRecordCatchUp_CertifiesLiveProcessor is the control: with the run
// unstopped and the processor's exit unpublished, the catch-up is appended
// and the certificate is fully bound to it.
func TestRecordCatchUp_CertifiesLiveProcessor(t *testing.T) {
	t.Parallel()

	r, _ := caughtUpRebuildForTest(t, nil)

	attemptID := r.aggregate.State().Attempt.ID
	versionBefore := r.aggregate.Version()

	stopped, exited, err := r.recordCatchUp(t.Context(), attemptID, 9, time.Second)

	switch {
	case err != nil:
		t.Fatalf("want the catch-up recorded, got %v", err)
	case stopped || exited:
		t.Fatalf("want a live, unstopped certification, got stopped=%v exited=%v", stopped, exited)
	}

	if got := r.aggregate.Version(); got != versionBefore+1 {
		t.Fatalf("want the catch-up appended, got version %d (was %d)", got, versionBefore)
	}

	certificate := r.certificate
	if certificate == nil {
		t.Fatal("want the catch-up certified")
	}

	if certificate.attempt != attemptID || certificate.runner != r.runner || certificate.position != 9 ||
		certificate.version != r.aggregate.Version() || certificate.exited != (<-chan struct{})(r.processorExited) {
		t.Errorf("want the certificate fully bound to the fresh catch-up, got %+v", certificate)
	}
}

// TestRecordCatchUp_ReportsStopWithItsCause pins the stop recheck: a
// same-handle stop between the caught-up signal and the append shares the
// aggregate, so it surfaces here rather than as a version conflict, carrying
// the recorded cause.
func TestRecordCatchUp_ReportsStopWithItsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("recorded terminal cause")

	r, _ := caughtUpRebuildForTest(t, nil)
	r.stopped = true
	r.failure = cause

	versionBefore := r.aggregate.Version()

	stopped, exited, err := r.recordCatchUp(t.Context(), r.aggregate.State().Attempt.ID, 9, time.Second)

	switch {
	case !stopped:
		t.Fatal("want the stop reported")
	case exited:
		t.Fatal("want the stop reported as a stop, not an exit")
	case !errors.Is(err, cause):
		t.Fatalf("want the recorded cause carried, got %v", err)
	}

	if got := r.aggregate.Version(); got != versionBefore {
		t.Errorf("want no catch-up appended after the stop, got version %d (was %d)", got, versionBefore)
	}
}

// defeatedRebuildForTest builds a handle over a lifecycle history whose
// runner was displaced and whose attempt then ended: initiated, claimed by
// this handle's runner, started, claimed by a competitor (version 4), then
// abandoned (version 5). The handle's own view is hydrated to toVersion, so
// tests can hold views on either side of the defeat.
func defeatedRebuildForTest(t *testing.T, toVersion int64) (*Rebuild, uuid.UUID, uuid.UUID) {
	t.Helper()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	orchestrator, err := NewOrchestrator(Config{
		Events:      events,
		Checkpoints: cpmemory.NewCheckpointStore(),
		Handler:     func(projection.ID) (projection.EventHandler, error) { return nopHandler{}, nil },
		Projections: store,
	})
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	at := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	attempt := uuid.Must(uuid.NewV4())
	runner := uuid.Must(uuid.NewV4())
	competitor := uuid.Must(uuid.NewV4())

	history := store.New(StreamUUID("orders"))
	history.Append(
		RebuildInitiated{Attempt: attempt, Target: v1, Reason: "defeat classification", At: at},
		RunnerClaimed{Attempt: attempt, Runner: runner, At: at},
		BuildStarted{},
		RunnerClaimed{Attempt: attempt, Runner: competitor, At: at},
		Abandoned{Cause: "the competitor gave up"},
	)

	if err := store.Save(t.Context(), history, nil); err != nil {
		t.Fatalf("saving the defeat history: %v", err)
	}

	aggregate := store.New(StreamUUID("orders"))
	if err := store.Hydrate(t.Context(), aggregate, &aggregatestore.HydrateOptions{ToVersion: toVersion}); err != nil {
		t.Fatalf("hydrating the handle's view to version %d: %v", toVersion, err)
	}

	r := &Rebuild{
		orchestrator:    orchestrator,
		name:            "orders",
		aggregate:       aggregate,
		runner:          runner,
		processorExited: make(chan struct{}),
	}

	return r, attempt, runner
}

// ordersMismatch fabricates the version-mismatch error an "orders" lifecycle
// append loses with, expecting the given version.
func ordersMismatch(expected int64) error {
	return fmt.Errorf("recording caughtup: %w", eventstore.StreamVersionMismatchError{
		StreamID:        typeid.New(StreamType, StreamUUID("orders")),
		ExpectedVersion: expected,
		ActualVersion:   5,
	})
}

// TestRecordLostAppend_ClassifiesFromTheDefeatedSlot pins lost-append
// classification: the verdict comes from the exact slot the append contended
// for — here a competing claim, even though the stream's head shows the
// attempt ended — the slot fold installs only when it is at least as new as
// the handle's current view, a fold that never reaches the slot renders no
// verdict at all, and verdict precedence between observers is explicit: an
// exact displacement upgrades a clean terminal stop but never overwrites a
// recorded cause.
func TestRecordLostAppend_ClassifiesFromTheDefeatedSlot(t *testing.T) {
	t.Parallel()

	t.Run("verdict is the slot's claim, not the head's end", func(t *testing.T) {
		t.Parallel()

		// The handle's view is its own pre-defeat fold at version 3.
		r, attempt, runner := defeatedRebuildForTest(t, 3)

		r.recordLostAppend(t.Context(), attempt, runner, ordersMismatch(3))

		if !r.stopped {
			t.Fatal("want the defeat recorded as a stop")
		}

		if !errors.Is(r.failure, ErrRunnerDisplaced) {
			t.Fatalf("want the slot's competing claim recorded as displacement, got %v", r.failure)
		}

		// The slot fold is newer than the handle's view, so it installs.
		if got := r.aggregate.Version(); got != 4 {
			t.Errorf("want the slot fold installed over the older view, got version %d", got)
		}
	})

	t.Run("an older slot fold renders its verdict without regressing state", func(t *testing.T) {
		t.Parallel()

		// The handle's view is already past the slot — a fresher install,
		// as the reconcile loop performs.
		r, attempt, runner := defeatedRebuildForTest(t, 5)

		r.recordLostAppend(t.Context(), attempt, runner, ordersMismatch(3))

		if !r.stopped {
			t.Fatal("want the defeat recorded as a stop")
		}

		if !errors.Is(r.failure, ErrRunnerDisplaced) {
			t.Fatalf("want the slot's competing claim recorded as displacement, got %v", r.failure)
		}

		if got := r.aggregate.Version(); got != 5 {
			t.Errorf("want the newer view kept over the older slot fold, got version %d", got)
		}
	})

	t.Run("a fold short of the slot renders no verdict", func(t *testing.T) {
		t.Parallel()

		// The fabricated expectation points past the stream's head, so the
		// hydrate cannot reach the slot: classifying the head fold in its
		// place would read the abandonment as a clean end and mask the raw
		// loss.
		r, attempt, runner := defeatedRebuildForTest(t, 3)

		r.recordLostAppend(t.Context(), attempt, runner, ordersMismatch(9))

		if r.stopped || r.failure != nil {
			t.Fatalf("want no verdict from an unreachable slot, got stopped=%v failure=%v", r.stopped, r.failure)
		}

		if got := r.aggregate.Version(); got != 3 {
			t.Errorf("want the handle's view untouched, got version %d", got)
		}
	})

	t.Run("displacement upgrades a reconciled clean end", func(t *testing.T) {
		t.Parallel()

		// Reconciliation observed the head's abandonment first and recorded
		// a clean terminal stop; the exact defeat still governs.
		r, attempt, runner := defeatedRebuildForTest(t, 5)
		r.stopped = true

		r.recordLostAppend(t.Context(), attempt, runner, ordersMismatch(3))

		if !errors.Is(r.failure, ErrRunnerDisplaced) {
			t.Fatalf("want the exact displacement to upgrade the clean stop, got %v", r.failure)
		}

		if got := r.aggregate.Version(); got != 5 {
			t.Errorf("want no install through the stopped path, got version %d", got)
		}
	})

	t.Run("a recorded cause is never overwritten", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("recorded terminal cause")

		r, attempt, runner := defeatedRebuildForTest(t, 5)
		r.stopped = true
		r.failure = cause

		r.recordLostAppend(t.Context(), attempt, runner, ordersMismatch(3))

		if !errors.Is(r.failure, cause) || errors.Is(r.failure, ErrRunnerDisplaced) {
			t.Fatalf("want the recorded cause kept over the displacement verdict, got %v", r.failure)
		}
	})
}

// TestClaimDefeat_RequiresTheSlotFold pins the claim site's slot proof: when
// the hydrate cannot reach the contended slot, the raw loss surfaces — a
// head read would misreport the abandonment at the head as a terminal
// observation — and nothing installs.
func TestClaimDefeat_RequiresTheSlotFold(t *testing.T) {
	t.Parallel()

	r, _, _ := defeatedRebuildForTest(t, 3)
	base := r.aggregate.State().Attempt

	lost := ordersMismatch(9)

	got := r.claimDefeat(t.Context(), base, lost)

	if !errors.Is(got, eventstore.StreamVersionMismatchError{}) {
		t.Fatalf("want the raw loss surfaced for an unreachable slot, got %v", got)
	}

	if errors.Is(got, ErrRunnerDisplaced) {
		t.Fatalf("want no displacement verdict from an unreachable slot, got %v", got)
	}

	if version := r.aggregate.Version(); version != 3 {
		t.Errorf("want the handle's view untouched, got version %d", version)
	}
}

// TestDefeatSlot pins which losses identify an exact defeated slot: only a
// version mismatch for this stream, with a representable expectation, on an
// error chain that does not also report the append as durable — a retried
// save can carry a stale mismatch alongside ErrEventsAppended, and the
// durable append means the contended slots hold this run's own events.
func TestDefeatSlot(t *testing.T) {
	t.Parallel()

	stream := typeid.New(StreamType, StreamUUID("orders"))
	foreign := typeid.New(StreamType, StreamUUID("customers"))

	valid := eventstore.StreamVersionMismatchError{StreamID: stream, ExpectedVersion: 4, ActualVersion: 7}

	for _, tt := range []struct {
		name     string
		lost     error
		wantSlot int64
		wantOK   bool
	}{
		{name: "mismatch for this stream", lost: valid, wantSlot: 5, wantOK: true},
		{name: "wrapped mismatch", lost: fmt.Errorf("recording caughtup: %w", valid), wantSlot: 5, wantOK: true},
		{name: "no mismatch", lost: errors.New("save refused")},
		{name: "durable append alone", lost: aggregatestore.ErrEventsAppended},
		{name: "durable append with a stale mismatch riding along", lost: errors.Join(aggregatestore.ErrEventsAppended, valid)},
		{name: "mismatch for a different stream", lost: eventstore.StreamVersionMismatchError{StreamID: foreign, ExpectedVersion: 4}},
		{name: "mismatch with no stream identity", lost: eventstore.StreamVersionMismatchError{ExpectedVersion: 4}},
		{name: "negative expectation", lost: eventstore.StreamVersionMismatchError{StreamID: stream, ExpectedVersion: -1}},
		{name: "expectation at the version ceiling", lost: eventstore.StreamVersionMismatchError{StreamID: stream, ExpectedVersion: math.MaxInt64}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			slot, ok := defeatSlot(tt.lost, stream)

			if ok != tt.wantOK || slot != tt.wantSlot {
				t.Errorf("want (%d, %v), got (%d, %v)", tt.wantSlot, tt.wantOK, slot, ok)
			}
		})
	}
}

// TestAttributeExit pins failure attribution's ordering rule: a processor
// stopped by the wind-down has no story of its own no matter what shape its
// handler gave the stop, an early return that is purely the run's
// cancellation subsumes the local failure, and an early real result is an
// independent failure joined with the local one.
func TestAttributeExit(t *testing.T) {
	t.Parallel()

	errExit := errors.New("the processor's own failure")
	errLocal := errors.New("the local failure")

	for _, tt := range []struct {
		name          string
		returnedEarly bool
		exitErr       error
		local         error
		want          []error
		wantAbsent    []error
	}{
		{
			name:    "not returned early: the local failure governs",
			exitErr: errExit, local: errLocal,
			want: []error{errLocal}, wantAbsent: []error{errExit},
		},
		{
			name:          "returned early with a real result: independent failures join",
			returnedEarly: true, exitErr: errExit, local: errLocal,
			want: []error{errExit, errLocal},
		},
		{
			name:          "early pure cancellation subsumes the local failure",
			returnedEarly: true, exitErr: fmt.Errorf("processing: %w", context.Canceled), local: errLocal,
			want: []error{context.Canceled}, wantAbsent: []error{errLocal},
		},
		{
			name:          "early deadline expiry subsumes the local failure",
			returnedEarly: true, exitErr: context.DeadlineExceeded, local: errLocal,
			want: []error{context.DeadlineExceeded}, wantAbsent: []error{errLocal},
		},
		{
			name:          "early cancellation joined with a real cause is not pure",
			returnedEarly: true, exitErr: errors.Join(context.Canceled, errExit), local: errLocal,
			want: []error{errExit, errLocal},
		},
		{
			name:          "early nil result: the local failure governs",
			returnedEarly: true, local: errLocal,
			want: []error{errLocal},
		},
		{
			name:          "early real result with no local failure",
			returnedEarly: true, exitErr: errExit,
			want: []error{errExit},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := attributeExit(tt.returnedEarly, tt.exitErr, tt.local)

			for _, want := range tt.want {
				if !errors.Is(got, want) {
					t.Errorf("want the result to carry %v, got %v", want, got)
				}
			}

			for _, absent := range tt.wantAbsent {
				if errors.Is(got, absent) {
					t.Errorf("want the result free of %v, got %v", absent, got)
				}
			}
		})
	}
}

// TestPromoteAfterCatchUp_SamplesReturnOrderBeforeStopping pins the sampling
// discipline behind attribution: the processor's return must be observed
// BEFORE the wind-down initiates its stop. The fake stop here closes the
// return signal — exactly what a real stop eventually causes — so a path
// that sampled after stopping would misread its own stop as an early return
// and let the fabricated result hijack the genuine local refusal.
func TestPromoteAfterCatchUp_SamplesReturnOrderBeforeStopping(t *testing.T) {
	t.Parallel()

	r, certificate := caughtUpRebuildForTest(t, nil)

	// No certificate installed: the promotion refuses locally.
	r.processorReturned = make(chan struct{})

	fabricated := errors.New("laundered cancellation")
	done := make(chan error, 1)
	done <- fabricated

	reconcileExited := make(chan struct{})
	close(reconcileExited)

	stop := func() { close(r.processorReturned) }

	keepTailing, err := r.promoteAfterCatchUp(t.Context(), certificate.attempt, certificate.runner, stop, done, reconcileExited)

	if keepTailing {
		t.Fatal("want the failed promotion to end the run")
	}

	if !errors.Is(err, ErrNotCertified) {
		t.Fatalf("want the local refusal to govern a stop-caused exit, got %v", err)
	}

	if errors.Is(err, fabricated) {
		t.Errorf("want the stopped processor's fabricated result ignored, got %v", err)
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
	foreignAttempt := uuid.Must(uuid.NewV4())

	aggregate := store.New(StreamUUID("orders"))
	aggregate.Append(
		RebuildInitiated{Attempt: foreignAttempt, Target: customersV1, Reason: "takeover", At: at},
		RunnerClaimed{Attempt: foreignAttempt, Runner: uuid.Must(uuid.NewV4()), At: at},
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
