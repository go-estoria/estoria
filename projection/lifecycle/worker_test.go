package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-estoria/estoria/eventstore"
	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/lifecycle"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// startWorkerForTest runs the worker in the background for the duration of
// the test; an exit other than the test context's cancellation is a test
// failure.
func startWorkerForTest(t *testing.T, worker *lifecycle.Worker) {
	t.Helper()

	runErr := make(chan error, 1)

	go func() { runErr <- worker.Run(t.Context()) }()

	t.Cleanup(func() {
		if err := <-runErr; !errors.Is(err, context.Canceled) {
			t.Errorf("worker exited unexpectedly: %v", err)
		}
	})
}

// waitReady waits for the worker's initialized-through-high-water signal.
func waitReady(t *testing.T, worker *lifecycle.Worker) {
	t.Helper()

	select {
	case <-worker.Ready():
	case <-time.After(waitTimeout):
		t.Fatal("worker never signaled readiness")
	}
}

// recordingSetter captures every cutover delivered to it, in order, failing
// while armed to fail. It deliberately does not de-duplicate: worker tests
// assert the exact delivery sequence, and contract semantics are pinned on
// MemoryRouter.
type recordingSetter struct {
	mu      sync.Mutex
	applied []lifecycle.Cutover
	fail    bool
}

func (s *recordingSetter) ApplyCutover(_ context.Context, cutover lifecycle.Cutover) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.fail {
		return errors.New("delivery failed")
	}

	s.applied = append(s.applied, cutover)

	return nil
}

func (s *recordingSetter) AppliedCutover(_ context.Context, name string) (lifecycle.Cutover, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := len(s.applied) - 1; i >= 0; i-- {
		if s.applied[i].Live.Name == name {
			return s.applied[i], nil
		}
	}

	return lifecycle.Cutover{}, lifecycle.ErrNoLiveVersion
}

func (s *recordingSetter) setFail(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.fail = fail
}

func (s *recordingSetter) seen() []lifecycle.Cutover {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]lifecycle.Cutover(nil), s.applied...)
}

// assertCutovers compares an exact delivery sequence.
func assertCutovers(t *testing.T, got, want []lifecycle.Cutover) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("want deliveries %v, got %v", want, got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want deliveries %v, got %v", want, got)
		}
	}
}

// TestWorker_InitialFoldConvergesOnFinals pins the worker's convergence
// shape: the initial fold applies nothing, and once it completes, each
// projection receives exactly its final cutover — intermediate flips are
// superseded, not delivered — in ascending name order, regardless of the
// order the history recorded them.
func TestWorker_InitialFoldConvergesOnFinals(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}
	customersV1 := projection.ID{Name: "customers", Version: 1}

	// Orders history first, customers second: event order is the reverse of
	// name order, so the two application orders are distinguishable.
	recordCutover(t, projections, ordersV1, projection.ID{}, false)
	recordCutover(t, projections, ordersV2, ordersV1, true)
	recordCutover(t, projections, customersV1, projection.ID{}, false)

	recorder := &recordingSetter{}
	router := lifecycle.NewMemoryRouter()

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithCutoverSetter(router),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)
	waitReady(t, worker)

	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{
		{Live: customersV1, Revision: 1},
		{Live: ordersV1, Revision: 3},
	})

	if live, err := router.Live(t.Context(), "customers"); err != nil || live != customersV1 {
		t.Errorf("want the router serving %s, got %s (%v)", customersV1, live, err)
	}

	if applied, err := router.AppliedCutover(t.Context(), "orders"); err != nil || applied != (lifecycle.Cutover{Live: ordersV1, Revision: 3}) {
		t.Errorf("want the router vouching for the final orders cutover, got %+v (%v)", applied, err)
	}
}

// TestWorker_TailsDeliveriesAfterReady pins the tail: cutovers recorded
// after initialization are folded against the initial fold's state — the
// continuity fold spans the high-water boundary — and delivered in stream
// order with their recorded revisions.
func TestWorker_TailsDeliveriesAfterReady(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}
	ordersV3 := projection.ID{Name: "orders", Version: 3}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	recorder := &recordingSetter{}
	router := lifecycle.NewMemoryRouter()

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithCutoverSetter(router),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)
	waitReady(t, worker)

	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})

	// Two sequential tail rounds: the second delivery is proof of a drain
	// after the first, so a tail that re-read or re-folded its own past
	// would have failed before delivering it.
	recordCutover(t, projections, ordersV2, ordersV1, false)
	waitFor(t, func() bool { return len(recorder.seen()) == 2 })

	recordCutover(t, projections, ordersV3, ordersV2, false)
	waitFor(t, func() bool { return len(recorder.seen()) == 3 })

	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{
		{Live: ordersV1, Revision: 1},
		{Live: ordersV2, Revision: 2},
		{Live: ordersV3, Revision: 3},
	})

	if applied, err := router.AppliedCutover(t.Context(), "orders"); err != nil || applied != (lifecycle.Cutover{Live: ordersV3, Revision: 3}) {
		t.Errorf("want the router on the tailed truth, got %+v (%v)", applied, err)
	}
}

// TestWorker_ReadyOnlyAfterEverySetterSucceeds pins the readiness gate: a
// setter refusing its final cutover stops the worker uninitialized — Run
// returns the delivery error and Ready never closes — and a fresh worker
// refolds from zero, redelivering to every setter.
func TestWorker_ReadyOnlyAfterEverySetterSucceeds(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	healthy := &recordingSetter{}
	failing := &recordingSetter{}
	failing.setFail(true)

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(healthy),
		lifecycle.WithCutoverSetter(failing),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	if err := worker.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "applying cutover") {
		t.Fatalf("want the failed delivery to stop initialization, got %v", err)
	}

	select {
	case <-worker.Ready():
		t.Fatal("want readiness withheld after a failed delivery")
	default:
	}

	// Registration order held: the healthy setter was applied before the
	// failing one refused.
	assertCutovers(t, healthy.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})
	assertCutovers(t, failing.seen(), nil)

	// Restarting is a fresh worker refolding from zero: the healthy setter
	// sees its final again, and redelivery is absorbed by contract.
	failing.setFail(false)

	second, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(healthy),
		lifecycle.WithCutoverSetter(failing),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating second worker: %v", err)
	}

	startWorkerForTest(t, second)
	waitReady(t, second)

	assertCutovers(t, failing.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})
	assertCutovers(t, healthy.seen(), []lifecycle.Cutover{
		{Live: ordersV1, Revision: 1},
		{Live: ordersV1, Revision: 1},
	})
}

// TestWorker_StopsOnTailDeliveryFailure pins stop-on-error past readiness: a
// failed tail delivery stops the worker before later setters act, and the
// route keeps serving the last converged truth.
func TestWorker_StopsOnTailDeliveryFailure(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	recorder := &recordingSetter{}
	router := lifecycle.NewMemoryRouter()

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithCutoverSetter(router),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr := make(chan error, 1)

	go func() { runErr <- worker.Run(t.Context()) }()

	waitReady(t, worker)
	recorder.setFail(true)

	recordCutover(t, projections, ordersV2, ordersV1, false)

	select {
	case err := <-runErr:
		if err == nil || !strings.Contains(err.Error(), "applying cutover") {
			t.Fatalf("want the failed tail delivery to stop the worker, got %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("worker kept running past a failed delivery")
	}

	// The failing setter sat ahead of the router, so the flip never reached
	// it: the route still serves the converged truth.
	if applied, err := router.AppliedCutover(t.Context(), "orders"); err != nil || applied != (lifecycle.Cutover{Live: ordersV1, Revision: 1}) {
		t.Errorf("want the router untouched by the failed delivery, got %+v (%v)", applied, err)
	}
}

// TestWorker_RejectsInvalidCutovers pins the worker's semantic decode and
// its from-zero continuity fold: a cutover event that fails its own scheme —
// an invalid live version, a non-positive revision, a stream its
// projection's name does not derive — or that extends no legal history stops
// the worker before any setter acts. The reserved namespace is a guardrail,
// and setters must not act on infrastructure state that fails it.
func TestWorker_RejectsInvalidCutovers(t *testing.T) {
	t.Parallel()

	ordersStream := typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID("orders")}

	rawPromoted := func(streamID typeid.ID, next projection.ID, revision int64) func(*testing.T, *esmemory.EventStore) {
		return func(t *testing.T, events *esmemory.EventStore) {
			t.Helper()

			data, err := json.Marshal(lifecycle.Promoted{Next: next, Revision: revision, At: promotedAt})
			if err != nil {
				t.Fatalf("marshaling promoted event: %v", err)
			}

			if _, err := events.AppendStream(t.Context(), streamID, []*eventstore.WritableEvent{{
				Type:            lifecycle.Promoted{}.EventType(),
				Data:            data,
				DataContentType: "application/json",
			}}, eventstore.AppendStreamOptions{}); err != nil {
				t.Fatalf("appending raw cutover: %v", err)
			}
		}
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	v2 := projection.ID{Name: "orders", Version: 2}

	for _, tt := range []struct {
		name    string
		history func(*testing.T, *esmemory.EventStore)
	}{
		{
			name:    "invalid live version",
			history: rawPromoted(ordersStream, projection.ID{Name: "orders", Version: 0}, 1),
		},
		{
			name:    "invalid cutover revision",
			history: rawPromoted(ordersStream, v1, 0),
		},
		{
			name:    "foreignly addressed stream",
			history: rawPromoted(typeid.NewV4(lifecycle.StreamType), v1, 1),
		},
		{
			name: "a discontinuous history",
			history: func(t *testing.T, events *esmemory.EventStore) {
				t.Helper()
				appendRawCutoverEvent(t, events, lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt})
				appendRawCutoverEvent(t, events, lifecycle.Promoted{Previous: v1, Next: v2, Revision: 3, At: promotedAt})
			},
		},
		{
			name: "an opening rollback",
			history: func(t *testing.T, events *esmemory.EventStore) {
				t.Helper()
				appendRawCutoverEvent(t, events, lifecycle.RolledBack{From: v2, RevertedTo: v1, Revision: 1, At: promotedAt})
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events := newEventStore(t)
			tt.history(t, events)

			setter := &recordingSetter{}

			worker, err := lifecycle.NewWorker(events,
				lifecycle.WithCutoverSetter(setter),
				lifecycle.WithPollInterval(2*time.Millisecond),
			)
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			// The deadline bounds the failure mode: a worker that accepted the
			// history would initialize and tail until the deadline instead of
			// returning the decode or continuity error.
			runCtx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()

			if err := worker.Run(runCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want the invalid history to stop the worker, got %v", err)
			}

			if got := setter.seen(); len(got) != 0 {
				t.Errorf("want no deliveries from an invalid history, got %v", got)
			}
		})
	}
}

// TestWorker_TailContinuityFailsClosed pins the tail's validation ordering:
// a tailed cutover that extends no legal history stops the worker before any
// setter acts on it, and the continuity check runs against the initial
// fold's state, not a fresh one.
func TestWorker_TailContinuityFailsClosed(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	recorder := &recordingSetter{}
	router := lifecycle.NewMemoryRouter()

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithCutoverSetter(router),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr := make(chan error, 1)

	go func() { runErr <- worker.Run(t.Context()) }()

	waitReady(t, worker)

	// Revision 4 after the folded revision 1: a gap only the prefix-aware
	// fold can see.
	appendRawCutoverEvent(t, events, lifecycle.Promoted{Previous: ordersV1, Next: ordersV2, Revision: 4, At: promotedAt})

	select {
	case err := <-runErr:
		if err == nil || !strings.Contains(err.Error(), "records revision") {
			t.Fatalf("want the discontinuous tail to stop the worker, got %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("worker kept running past a discontinuous tail")
	}

	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})

	if applied, err := router.AppliedCutover(t.Context(), "orders"); err != nil || applied != (lifecycle.Cutover{Live: ordersV1, Revision: 1}) {
		t.Errorf("want the route untouched by the refused flip, got %+v (%v)", applied, err)
	}
}

// TestWorker_IgnoresNonCutoverEvents pins the fold's scope: domain events
// and non-cutover lifecycle transitions invoke no effects.
func TestWorker_IgnoresNonCutoverEvents(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	// An admitted, claimed, started build — no cutover yet — alongside a
	// domain event.
	attempt := uuid.Must(uuid.NewV4())

	aggregate := projections.New(lifecycle.StreamUUID("orders"))
	aggregate.Append(
		lifecycle.RebuildInitiated{Attempt: attempt, Target: projection.ID{Name: "orders", Version: 1}, Reason: "no cutover", At: initiatedAt},
		lifecycle.RunnerClaimed{Attempt: attempt, Runner: uuid.Must(uuid.NewV4()), At: initiatedAt},
		lifecycle.BuildStarted{},
	)

	if err := projections.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving lifecycle aggregate: %v", err)
	}

	if state := aggregate.State(); state.InvalidReason != "" {
		t.Fatalf("fixture produced a poisoned history: %s", state.InvalidReason)
	}

	if _, err := events.AppendStream(t.Context(), typeid.NewV4("order"),
		[]*eventstore.WritableEvent{{Type: "ordertest", Data: []byte(`{}`)}},
		eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending domain event: %v", err)
	}

	setter := &recordingSetter{}

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)

	// Readiness means the initial fold drained past every recorded event.
	waitReady(t, worker)

	if got := setter.seen(); len(got) != 0 {
		t.Errorf("want no deliveries for non-cutover events, got %v", got)
	}
}

// TestWorker_DuplicateWorkersConverge pins duplicate-safety by construction:
// stateless workers share nothing, so any number may fold the same store and
// converge the same setters — through initialization and the tail — with
// every repeated delivery absorbed by the apply-if-newer contract.
func TestWorker_DuplicateWorkersConverge(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}
	ordersV3 := projection.ID{Name: "orders", Version: 3}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)
	recordCutover(t, projections, ordersV2, ordersV1, true)

	router := lifecycle.NewMemoryRouter()
	first := &recordingSetter{}
	second := &recordingSetter{}

	for _, setter := range []*recordingSetter{first, second} {
		worker, err := lifecycle.NewWorker(events,
			lifecycle.WithCutoverSetter(setter),
			lifecycle.WithCutoverSetter(router),
			lifecycle.WithPollInterval(2*time.Millisecond),
		)
		if err != nil {
			t.Fatalf("creating worker: %v", err)
		}

		startWorkerForTest(t, worker)
		waitReady(t, worker)
	}

	converged := lifecycle.Cutover{Live: ordersV1, Revision: 3}

	assertCutovers(t, first.seen(), []lifecycle.Cutover{converged})
	assertCutovers(t, second.seen(), []lifecycle.Cutover{converged})

	if applied, err := router.AppliedCutover(t.Context(), "orders"); err != nil || applied != converged {
		t.Fatalf("want the shared router converged on %+v, got %+v (%v)", converged, applied, err)
	}

	// Both workers tail the same new cutover; the shared router absorbs the
	// duplicate delivery.
	recordCutover(t, projections, ordersV3, ordersV1, false)

	waitFor(t, func() bool { return len(first.seen()) == 2 && len(second.seen()) == 2 })

	tailed := lifecycle.Cutover{Live: ordersV3, Revision: 4}

	assertCutovers(t, first.seen(), []lifecycle.Cutover{converged, tailed})
	assertCutovers(t, second.seen(), []lifecycle.Cutover{converged, tailed})

	if applied, err := router.AppliedCutover(t.Context(), "orders"); err != nil || applied != tailed {
		t.Errorf("want the shared router on the tailed truth, got %+v (%v)", applied, err)
	}
}

// TestWorker_RunAtMostOnce pins the restart contract: a Worker folds once,
// and restarting means a new Worker refolding from zero.
func TestWorker_RunAtMostOnce(t *testing.T) {
	t.Parallel()

	worker, err := lifecycle.NewWorker(newEventStore(t),
		lifecycle.WithCutoverSetter(&recordingSetter{}),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)
	waitReady(t, worker)

	if err := worker.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "already run") {
		t.Fatalf("want the second run refused, got %v", err)
	}
}

func TestNewWorker_Validation(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	t.Run("rejects a nil event reader", func(t *testing.T) {
		t.Parallel()

		if _, err := lifecycle.NewWorker(nil, lifecycle.WithCutoverSetter(&recordingSetter{})); err == nil {
			t.Error("want an error, got nil")
		}
	})

	for _, tt := range []struct {
		name string
		opts []lifecycle.WorkerOption
	}{
		{"rejects a worker with no setters", nil},
		{"rejects a nil cutover setter", []lifecycle.WorkerOption{lifecycle.WithCutoverSetter(nil)}},
		{"rejects a non-positive poll interval", []lifecycle.WorkerOption{
			lifecycle.WithCutoverSetter(&recordingSetter{}),
			lifecycle.WithPollInterval(0),
		}},
		{"rejects a nil option", []lifecycle.WorkerOption{nil}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := lifecycle.NewWorker(events, tt.opts...); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}
