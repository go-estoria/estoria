package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	cpmemory "github.com/go-estoria/estoria/projection/checkpointstore/memory"
	"github.com/go-estoria/estoria/projection/lifecycle"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

const waitTimeout = 5 * time.Second

// harness wires an orchestrator against shared in-memory stores: domain and
// lifecycle events in one event store (the default deployment), a memory
// checkpoint store, and an effect worker keeping a MemoryRouter current from
// the recorded cutovers.
type harness struct {
	t            *testing.T
	events       *esmemory.EventStore
	checkpoints  *cpmemory.CheckpointStore
	router       *lifecycle.MemoryRouter
	projections  aggregatestore.Store[lifecycle.State]
	model        *readModel
	orchestrator *lifecycle.Orchestrator
}

func newHarness(t *testing.T, opts ...lifecycle.OrchestratorOption) *harness {
	t.Helper()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	return buildHarness(t, events, projections, opts...)
}

func newEventStore(t *testing.T) *esmemory.EventStore {
	t.Helper()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	return events
}

// buildHarness wires the standard harness around the given stores, so tests
// can interpose failure-injecting wrappers at either layer.
func buildHarness(t *testing.T, events *esmemory.EventStore, projections aggregatestore.Store[lifecycle.State], opts ...lifecycle.OrchestratorOption) *harness {
	t.Helper()

	checkpoints := cpmemory.NewCheckpointStore()
	router := lifecycle.NewMemoryRouter()
	model := newReadModel()

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:      events,
		Checkpoints: checkpoints,
		Handler:     model.handler,
		Projections: projections,
	}, append([]lifecycle.OrchestratorOption{
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2 * time.Millisecond)),
		lifecycle.WithReconcileInterval(10 * time.Millisecond),
	}, opts...)...)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	h := &harness{
		t:            t,
		events:       events,
		checkpoints:  checkpoints,
		router:       router,
		projections:  projections,
		model:        model,
		orchestrator: orchestrator,
	}

	h.startWorker(events)

	return h
}

// bareOrchestrator wires an orchestrator with fast test intervals over the
// given collaborators, for tests that need a custom handler factory or
// checkpoint store and no harness worker.
func bareOrchestrator(t *testing.T, events *esmemory.EventStore, checkpoints checkpointstore.Store, handler func(projection.ID) (projection.EventHandler, error)) *lifecycle.Orchestrator {
	t.Helper()

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:      events,
		Checkpoints: checkpoints,
		Handler:     handler,
		Projections: projections,
	},
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
		lifecycle.WithReconcileInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	return orchestrator
}

// startWorker runs an effect worker over the given store for the duration of
// the test, applying recorded cutovers to the harness router. A worker exit
// other than the test context's cancellation is a test failure.
func (h *harness) startWorker(events eventstore.GlobalReader) {
	h.t.Helper()

	worker, err := lifecycle.NewWorker(events, h.checkpoints,
		lifecycle.WithLiveSetter(h.router),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		h.t.Fatalf("creating effect worker: %v", err)
	}

	runErr := make(chan error, 1)

	go func() { runErr <- worker.Run(h.t.Context()) }()

	h.t.Cleanup(func() {
		if err := <-runErr; !errors.Is(err, context.Canceled) {
			h.t.Errorf("effect worker exited unexpectedly: %v", err)
		}
	})
}

func (h *harness) appendDomain(n int) {
	h.t.Helper()
	appendDomainTo(h.t, h.events, n)
}

// appendDomainTo appends n domain events to the store, each on its own
// stream.
func appendDomainTo(t *testing.T, store *esmemory.EventStore, n int) {
	t.Helper()

	events := make([]*eventstore.WritableEvent, 0, n)
	for range n {
		events = append(events, &eventstore.WritableEvent{Type: "ordertest", Data: []byte(`{}`)})
	}

	if _, err := store.AppendStream(t.Context(), typeid.NewV4("order"), events, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending %d domain events: %v", n, err)
	}
}

func (h *harness) begin(reason string) *lifecycle.Rebuild {
	h.t.Helper()

	r, err := h.orchestrator.Begin(h.t.Context(), "orders", reason)
	if err != nil {
		h.t.Fatalf("beginning rebuild: %v", err)
	}

	return r
}

// promoteFirstVersion builds, promotes, and completes v1 of "orders" on a
// manual-promotion harness, leaving v1 live, the attempt slot vacant, and no
// processor running.
func (h *harness) promoteFirstVersion() projection.ID {
	h.t.Helper()

	return promoteAndComplete(h.t, h.orchestrator)
}

// promoteAndComplete drives the "orders" projection's first rebuild to a
// completed v1 on any manual-promotion orchestrator: begin, run to
// caught-up, promote, retire (trivially — a first rebuild has no previous
// version), and wait for Run to wind down nil.
func promoteAndComplete(t *testing.T, orchestrator *lifecycle.Orchestrator) projection.ID {
	t.Helper()

	r, err := orchestrator.Begin(t.Context(), "orders", "initial build")
	if err != nil {
		t.Fatalf("beginning rebuild: %v", err)
	}

	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v1: %v", err)
	}

	if err := r.Retire(t.Context()); err != nil {
		t.Fatalf("completing the first rebuild: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Fatalf("want Run to return nil after the rebuild completes, got %v", err)
	}

	return projection.ID{Name: "orders", Version: 1}
}

func (h *harness) waitLive(id projection.ID) {
	h.t.Helper()

	waitFor(h.t, func() bool {
		live, err := h.router.Live(h.t.Context(), id.Name)
		return err == nil && live == id
	})
}

// runAsync runs the rebuild in the background. The returned channel receives
// Run's result exactly once.
func runAsync(t *testing.T, r *lifecycle.Rebuild) (context.CancelFunc, <-chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	done := make(chan error, 1)

	go func() {
		done <- r.Run(ctx)
	}()

	return cancel, done
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}

		time.Sleep(time.Millisecond)
	}
}

func waitPhase(t *testing.T, r *lifecycle.Rebuild, phase lifecycle.Phase) {
	t.Helper()

	waitFor(t, func() bool { return r.State().Attempt.Phase == phase })
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for Run to return")
		return nil
	}
}

// TestBlueGreen_AutoPromote is the full happy path under the settled
// operating model: v1 built, promoted, and completed from nothing; a
// steady-state processor takes over the live version; v2 built alongside it
// under live appends, auto-promoted, and v1 retired.
func TestBlueGreen_AutoPromote(t *testing.T) {
	t.Parallel()

	h := newHarness(t, lifecycle.WithAutoPromote(true))
	h.appendDomain(20)

	v1 := projection.ID{Name: "orders", Version: 1}
	v2 := projection.ID{Name: "orders", Version: 2}

	r1 := h.begin("initial build")

	if state := r1.State(); state.Attempt.Target != v1 || state.Attempt.Previous.Version != 0 || state.Attempt.Reason != "initial build" {
		t.Fatalf("want a first rebuild targeting %s, got %+v", v1, state.Attempt)
	}

	_, done1 := runAsync(t, r1)
	waitPhase(t, r1, lifecycle.PhasePromoted)
	h.waitLive(v1)
	waitFor(t, func() bool { return len(h.model.table(v1)) == 20 })

	// Completing the first rebuild vacates the attempt slot; the reconcile
	// loop stops the build's processor.
	if err := r1.Retire(t.Context()); err != nil {
		t.Fatalf("completing the first rebuild: %v", err)
	}

	if err := waitDone(t, done1); err != nil {
		t.Fatalf("want Run to return nil after the rebuild completes, got %v", err)
	}

	// The documented handoff: steady-state processing of the live version is
	// a plain processor over the same checkpoint.
	steadyHandler, err := h.model.handler(v1)
	if err != nil {
		t.Fatalf("creating steady-state handler: %v", err)
	}

	steady, err := processor.New(h.events, h.checkpoints, v1, steadyHandler,
		processor.WithPollInterval(2*time.Millisecond))
	if err != nil {
		t.Fatalf("creating steady-state processor: %v", err)
	}

	steadyCtx, stopSteady := context.WithCancel(t.Context())
	steadyDone := make(chan error, 1)

	go func() { steadyDone <- steady.Run(steadyCtx) }()

	// Live writes continue while v2 rebuilds from scratch.
	appendErr := make(chan error, 1)

	go func() {
		for range 30 {
			if _, err := h.events.AppendStream(context.Background(), typeid.NewV4("order"),
				[]*eventstore.WritableEvent{{Type: "ordertest", Data: []byte(`{}`)}},
				eventstore.AppendStreamOptions{}); err != nil {
				appendErr <- err
				return
			}

			time.Sleep(time.Millisecond)
		}

		appendErr <- nil
	}()

	r2 := h.begin("add region column")

	if state := r2.State(); state.Attempt.Target != v2 || state.Attempt.Previous != v1 {
		t.Fatalf("want a rebuild of %s to %s, got %+v", v1, v2, state.Attempt)
	}

	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhasePromoted)
	h.waitLive(v2)

	if err := <-appendErr; err != nil {
		t.Fatalf("appending concurrently: %v", err)
	}

	// Both versions converge on the full history: v2 catches up, and the
	// steady-state processor keeps the rollback target current.
	waitFor(t, func() bool { return len(h.model.table(v2)) == 50 })
	waitFor(t, func() bool { return len(h.model.table(v1)) == 50 })

	attempt := r2.State().Attempt
	if attempt.CaughtUpPos == 0 || attempt.InitiatedAt.IsZero() || attempt.CaughtUpAt.IsZero() || attempt.PromotedAt.IsZero() {
		t.Errorf("want audit datapoints populated, got %+v", attempt)
	}

	// The operator stops the old version's processor, then retires it.
	stopSteady()
	<-steadyDone

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retiring previous version: %v", err)
	}

	if err := waitDone(t, done2); err != nil {
		t.Fatalf("want Run to return nil after the rebuild completes, got %v", err)
	}

	if state := r2.State(); state.Live != v2 || state.Attempt.Phase != lifecycle.PhaseNone {
		t.Errorf("want %s live with the attempt slot vacant, got %+v", v2, state)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down, got %v", v1, dropped)
	}

	if _, err := h.checkpoints.Load(t.Context(), v1); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want the retired version's checkpoint deleted, got %v", err)
	}
}

// TestBlueGreen_ManualPromote pins the default gate: caught-up does not flip
// reads until Promote is called.
func TestBlueGreen_ManualPromote(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(10)

	r := h.begin("manual promote")

	_, _ = runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	if _, err := h.router.Live(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrNoLiveVersion) {
		t.Fatalf("want no live version before Promote, got %v", err)
	}

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("promoting: %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	h.waitLive(v1)

	if err := r.Promote(t.Context()); err == nil {
		t.Error("want an error promoting twice, got nil")
	}
}

// TestRollback pins the rollback path: reads revert to the previous version,
// the attempt slot is vacated, and the rolled-back version number is dead.
func TestRollback(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(5)

	r1 := h.begin("initial build")
	_, done1 := runAsync(t, r1)
	waitPhase(t, r1, lifecycle.PhaseCaughtUp)

	if err := r1.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v1: %v", err)
	}

	if err := r1.Rollback(t.Context()); err == nil {
		t.Error("want an error rolling back a first version with no predecessor, got nil")
	}

	if err := r1.Retire(t.Context()); err != nil {
		t.Fatalf("completing the first rebuild: %v", err)
	}

	if err := waitDone(t, done1); err != nil {
		t.Fatalf("want Run to return nil after the rebuild completes, got %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	h.waitLive(v1)

	r2 := h.begin("bad mapping")
	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	if err := r2.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back: %v", err)
	}

	// Rollback is terminal for the attempt: the losing version's processor
	// is stopped, and its Run reports the deliberate stop as nil.
	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after Rollback, got %v", err)
	}

	h.waitLive(v1)

	if state := r2.State(); state.Live != v1 || state.Attempt.Phase != lifecycle.PhaseNone {
		t.Errorf("want %s live with the attempt slot vacant, got %+v", v1, state)
	}

	if err := r2.Rollback(t.Context()); err == nil {
		t.Error("want an error rolling back twice, got nil")
	}

	if err := r2.Retire(t.Context()); err == nil {
		t.Error("want an error retiring a rolled-back rebuild, got nil")
	}
}

// TestAbandon pins abandonment: the decision is recorded, the processor
// stops (Run returns nil), and the target version's storage and checkpoint
// are deliberately left in place — inert residue on a never-reused identity,
// awaiting explicit collection rather than an automatic cleanup no handle
// can prove it is safe to run.
func TestAbandon(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(5)

	v1 := projection.ID{Name: "orders", Version: 1}

	r := h.begin("will be abandoned")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)
	waitFor(t, func() bool { return len(h.model.table(v1)) == 5 })

	if err := r.Abandon(t.Context(), "wrong column mapping"); err != nil {
		t.Fatalf("abandoning: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after Abandon, got %v", err)
	}

	if got := r.State().Attempt.Phase; got != lifecycle.PhaseNone {
		t.Errorf("want the attempt slot vacated, got %s", got)
	}

	if got := countEventsOfType(t, h.events, lifecycle.Abandoned{}.EventType()); got != 1 {
		t.Errorf("want one Abandoned event recorded, got %d", got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Errorf("want no teardown from an abandonment, got %v", dropped)
	}

	if got := len(h.model.table(v1)); got != 5 {
		t.Errorf("want the abandoned version's storage left in place (5 rows), got %d", got)
	}

	if _, err := h.checkpoints.Load(t.Context(), v1); err != nil {
		t.Errorf("want the abandoned version's checkpoint left in place, got %v", err)
	}

	if err := r.Abandon(t.Context(), "again"); err == nil {
		t.Error("want an error abandoning twice, got nil")
	}
}

// TestAbandon_NeverEndsCaughtUp stresses the same-handle race between the
// caught-up transition and Abandon: because both share one aggregate, a lost
// race would let CaughtUp land on top of Abandoned without a version
// conflict. Whatever the interleaving, an abandoned attempt must leave the
// slot vacant.
func TestAbandon_NeverEndsCaughtUp(t *testing.T) {
	t.Parallel()

	for range 25 {
		h := newHarness(t)

		r := h.begin("raced abandon")
		_, done := runAsync(t, r)

		if err := r.Abandon(t.Context(), "raced"); err != nil {
			t.Fatalf("abandoning: %v", err)
		}

		_ = waitDone(t, done)

		state, err := h.orchestrator.Get(t.Context(), "orders")
		if err != nil {
			t.Fatalf("loading lifecycle state: %v", err)
		}

		if state.Attempt.Phase != lifecycle.PhaseNone {
			t.Fatalf("want the attempt slot vacant regardless of interleaving, got %s", state.Attempt.Phase)
		}
	}
}

// TestAbandon_FromResumedHandle pins abandonment from a processor-less
// handle: the decision is recorded and nothing is torn down. With
// reconciliation effectively disabled, the concurrent builder stops when its
// next transition conflicts with Abandoned — same-stream arbitration, not
// cleanup, is what ends it.
func TestAbandon_FromResumedHandle(t *testing.T) {
	t.Parallel()

	h := newHarness(t, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("abandoned remotely")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	remote, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if err := remote.Abandon(t.Context(), "remote abandon"); err != nil {
		t.Fatalf("abandoning from the resumed handle: %v", err)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Errorf("want no teardown from a processor-less abandonment, got %v", dropped)
	}

	// The original builder proceeds, and its caught-up transition conflicts
	// with the recorded abandonment: it stops with a version mismatch.
	h.model.releaseGate()

	if err := waitDone(t, done); !errors.Is(err, eventstore.StreamVersionMismatchError{}) {
		t.Errorf("want the remote builder refused with a version mismatch, got %v", err)
	}

	state, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("loading lifecycle state: %v", err)
	}

	if state.Attempt.Phase != lifecycle.PhaseNone {
		t.Errorf("want the attempt slot vacant after the abandonment, got %s", state.Attempt.Phase)
	}
}

// TestReconcile_StopsRemotelyEndedBuild pins self-reconciliation: a tailing
// processor appends nothing that would surface a terminal transition
// recorded elsewhere, so the reconcile loop must observe the vacated slot
// and stop the build — Run returns nil without any operator intervention.
func TestReconcile_StopsRemotelyEndedBuild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("ended remotely")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	remote, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if err := remote.Abandon(t.Context(), "remote abandon"); err != nil {
		t.Fatalf("abandoning from the resumed handle: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want the builder to wind itself down with nil, got %v", err)
	}

	// The abandoner owned no processor and the builder only stopped; the
	// dead version's residue remains until explicitly collected.
	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Errorf("want no teardown from a remotely ended build, got %v", dropped)
	}
}

// TestReconcile_StopsReplacedAttempt pins reconciliation by attempt
// identity, not phase: when the running attempt ends and a replacement is
// admitted quickly, the superseded builder still winds itself down — a slot
// occupied by a different attempt is as terminal for it as a vacant one.
func TestReconcile_StopsReplacedAttempt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("will be replaced")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	remote, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if err := remote.Abandon(t.Context(), "replacing"); err != nil {
		t.Fatalf("abandoning: %v", err)
	}

	replacement := h.begin("the replacement")

	if err := waitDone(t, done); err != nil {
		t.Errorf("want the superseded builder to wind itself down with nil, got %v", err)
	}

	// The replacement is untouched by the wind-down.
	state, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("loading lifecycle state: %v", err)
	}

	if want := replacement.State().Attempt.ID; state.Attempt.ID != want || state.Attempt.Phase != lifecycle.PhaseCreated {
		t.Errorf("want the replacement attempt %s still admitted, got %+v", want, state.Attempt)
	}
}

// TestCommands_RefuseInvalidLifecycleState pins fail-closed hydration: the
// fold is total, so a decodable but semantically invalid lifecycle stream
// hydrates into state that violates the package's invariants — and no
// command acts on it. Without the refusal, a poisoned lineage could aim a
// retirement's teardown at a different projection's storage.
func TestCommands_RefuseInvalidLifecycleState(t *testing.T) {
	t.Parallel()

	t.Run("refused at load", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		model := newReadModel()
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), model.handler)

		// A poisoned admission: the target belongs to "orders", the lineage
		// to "customers".
		appendRawLifecycleEvent(t, events, "orders", lifecycle.RebuildInitiated{
			Attempt:  uuid.Must(uuid.NewV4()),
			Target:   projection.ID{Name: "orders", Version: 1},
			Previous: projection.ID{Name: "customers", Version: 1},
			Reason:   "poisoned",
			At:       time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC),
		})

		if _, err := orchestrator.Resume(t.Context(), "orders"); err == nil {
			t.Error("want Resume refused on invalid lifecycle state, got nil")
		}

		if _, err := orchestrator.Get(t.Context(), "orders"); err == nil {
			t.Error("want Get refused on invalid lifecycle state, got nil")
		}

		if _, err := orchestrator.Begin(t.Context(), "orders", "on poison"); err == nil {
			t.Error("want Begin refused on invalid lifecycle state, got nil")
		}
	})

	t.Run("retirement refused on poisoned lineage", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		model := newReadModel()
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), model.handler)

		appendDomainTo(t, events, 3)
		promoteAndComplete(t, orchestrator)

		r2, err := orchestrator.Begin(t.Context(), "orders", "second build")
		if err != nil {
			t.Fatalf("beginning v2: %v", err)
		}

		_, _ = runAsync(t, r2)
		waitPhase(t, r2, lifecycle.PhaseCaughtUp)

		if err := r2.Promote(t.Context()); err != nil {
			t.Fatalf("promoting v2: %v", err)
		}

		// A poisoned promotion lands on the stream: it decodes, the fold
		// applies it — history is truth — and the resulting state names a
		// different projection as live.
		appendRawLifecycleEvent(t, events, "orders", lifecycle.Promoted{
			Previous: projection.ID{Name: "orders", Version: 2},
			Next:     projection.ID{Name: "customers", Version: 7},
			At:       time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC),
		})

		if err := r2.Retire(t.Context()); err == nil {
			t.Fatal("want retirement refused on invalid lifecycle state, got nil")
		}

		if dropped := model.droppedTables(); len(dropped) != 0 {
			t.Errorf("want nothing torn down from the refused retirement, got %v", dropped)
		}
	})
}

// TestRetire_FirstVersionCompletesWithoutTeardown pins the no-previous form
// of retirement: nothing to tear down and nothing to reserve against —
// rollback is impossible without a target — so Retire records completion
// directly and vacates the slot.
func TestRetire_FirstVersionCompletesWithoutTeardown(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("first version")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("promoting: %v", err)
	}

	if err := r.Retire(t.Context()); err != nil {
		t.Fatalf("completing the first rebuild: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after completion, got %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}

	if state := r.State(); state.Live != v1 || state.Attempt.Phase != lifecycle.PhaseNone {
		t.Errorf("want %s live with the attempt slot vacant, got %+v", v1, state)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Errorf("want no reservation for a retirement with nothing to contend for, got %d", got)
	}

	if got := countEventsOfType(t, h.events, lifecycle.PreviousRetired{}.EventType()); got != 1 {
		t.Errorf("want one completion event, got %d", got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Errorf("want nothing torn down, got %v", dropped)
	}
}

// TestRetire_ReservationBlocksRollbackAndRepairs pins the reserved
// transition end to end: RetireStarted lands before any destruction; from
// PhaseRetiring a rollback is refused (the reservation forfeits the rollback
// target); and a Retire interrupted by a teardown failure is repaired by
// calling Retire again, without a second reservation.
func TestRetire_ReservationBlocksRollbackAndRepairs(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	v1 := h.promoteFirstVersion()
	v2 := projection.ID{Name: "orders", Version: 2}

	r2 := h.begin("second build")
	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	h.model.setTeardownFailure(true)

	if err := r2.Retire(t.Context()); err == nil {
		t.Fatal("want the teardown failure reported, got nil")
	}

	// The reservation is durable even though the teardown failed.
	if got := r2.State().Attempt.Phase; got != lifecycle.PhaseRetiring {
		t.Fatalf("want phase %s after the interrupted retirement, got %s", lifecycle.PhaseRetiring, got)
	}

	if err := r2.Rollback(t.Context()); err == nil {
		t.Fatal("want rollback refused once retirement has started, got nil")
	}

	// Repair: re-running the retirement skips the reservation and re-runs
	// the idempotent teardown.
	h.model.setTeardownFailure(false)

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("repairing the retirement: %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 1 {
		t.Errorf("want exactly one reservation, got %d", got)
	}

	if got := countEventsOfType(t, h.events, lifecycle.PreviousRetired{}.EventType()); got != 2 {
		t.Errorf("want one completion per finished rebuild (2 total), got %d", got)
	}

	if state := r2.State(); state.Live != v2 || state.Attempt.Phase != lifecycle.PhaseNone {
		t.Errorf("want %s live with the attempt slot vacant, got %+v", v2, state)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down, got %v", v1, dropped)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_RefusesAfterStaleRollback pins that retirement acts on a fresh
// view: a handle that still believes the rebuild is promoted refreshes at
// Retire and observes the vacated slot instead of tearing down the now-live
// previous version.
func TestRetire_RefusesAfterStaleRollback(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	h.promoteFirstVersion()

	r2 := h.begin("to be rolled back")
	_, _ = runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	// A stale handle loaded while the rebuild is still promoted.
	stale, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if err := r2.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back: %v", err)
	}

	if err := stale.Retire(t.Context()); err == nil {
		t.Fatal("want retirement refused after the rollback, got nil")
	}

	// The now-live previous version's storage must be untouched.
	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Errorf("want no teardown from the refused retirement, got %v", dropped)
	}
}

// TestRetire_RequiresTeardowner pins the retirement capability gate: a
// handler that cannot tear down its storage is refused before RetireStarted
// is reserved, so the rollback target is not forfeited — rollback still
// works after the refusal. A first rebuild is exempt: with no previous
// version there is nothing to tear down.
func TestRetire_RequiresTeardowner(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	model := newReadModel()

	orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(),
		func(id projection.ID) (projection.EventHandler, error) {
			handler, err := model.handler(id)
			if err != nil {
				return nil, err
			}

			return noTeardownHandler{inner: handler}, nil
		})

	appendDomainTo(t, events, 3)

	// The first rebuild completes without the capability: nothing to remove.
	v1 := promoteAndComplete(t, orchestrator)

	r2, err := orchestrator.Begin(t.Context(), "orders", "second build")
	if err != nil {
		t.Fatalf("beginning v2: %v", err)
	}

	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	if err := r2.Retire(t.Context()); err == nil {
		t.Fatal("want retirement refused without projection.Teardowner, got nil")
	}

	// The refusal preceded the reservation: nothing was reserved, and the
	// rollback target is intact.
	if got := countEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Fatalf("want no reservation from the refused retirement, got %d", got)
	}

	if got := r2.State().Attempt.Phase; got != lifecycle.PhasePromoted {
		t.Fatalf("want the rebuild still %s after the refusal, got %s", lifecycle.PhasePromoted, got)
	}

	if err := r2.Rollback(t.Context()); err != nil {
		t.Errorf("want rollback to %s still possible after the refusal, got %v", v1, err)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rollback, got %v", err)
	}
}

// TestRetire_ResolvesHandlerBeforeReserving pins two retirement contracts: a
// handler-factory failure refuses the retirement before RetireStarted is
// reserved, and a successful retirement resolves the previous version's
// handler exactly once, with that same instance performing the teardown.
func TestRetire_ResolvesHandlerBeforeReserving(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	factory := &countingFactory{model: newReadModel()}
	orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), factory.handler)

	appendDomainTo(t, events, 3)

	v1 := promoteAndComplete(t, orchestrator)

	r2, err := orchestrator.Begin(t.Context(), "orders", "second build")
	if err != nil {
		t.Fatalf("beginning v2: %v", err)
	}

	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	factory.setFail(v1, true)

	if err := r2.Retire(t.Context()); err == nil {
		t.Fatal("want the handler-factory failure reported, got nil")
	}

	if got := countEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Fatalf("want no reservation after the pre-reservation failure, got %d", got)
	}

	factory.setFail(v1, false)

	before := factory.resolutions(v1)

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retiring: %v", err)
	}

	if got := factory.resolutions(v1) - before; got != 1 {
		t.Errorf("want the previous version's handler resolved exactly once during Retire, got %d resolutions", got)
	}

	if got := factory.lastResolved().teardownCount(); got != 1 {
		t.Errorf("want the teardown performed by the instance the capability check resolved, got %d teardowns on it", got)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_CheckpointDeleteFailureIsRepairable pins the act-then-record
// ordering inside retirement: a checkpoint delete failing after a successful
// teardown leaves the reservation durable and the completion unrecorded, and
// re-running Retire repairs it — the idempotent teardown re-runs, the delete
// retries, and completion lands exactly once.
func TestRetire_CheckpointDeleteFailureIsRepairable(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	model := newReadModel()
	checkpoints := &failingDeleteCheckpoints{Store: cpmemory.NewCheckpointStore()}
	orchestrator := bareOrchestrator(t, events, checkpoints, model.handler)

	appendDomainTo(t, events, 3)

	v1 := promoteAndComplete(t, orchestrator)

	r2, err := orchestrator.Begin(t.Context(), "orders", "second build")
	if err != nil {
		t.Fatalf("beginning v2: %v", err)
	}

	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	checkpoints.setFail(true)

	if err := r2.Retire(t.Context()); err == nil {
		t.Fatal("want the checkpoint delete failure reported, got nil")
	}

	// The teardown ran and the reservation is durable; the checkpoint — the
	// durable marker that v1's build existed — survives the failed delete.
	if got := r2.State().Attempt.Phase; got != lifecycle.PhaseRetiring {
		t.Fatalf("want phase %s after the interrupted retirement, got %s", lifecycle.PhaseRetiring, got)
	}

	if dropped := model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Fatalf("want %s torn down before the delete failure, got %v", v1, dropped)
	}

	if _, err := checkpoints.Load(t.Context(), v1); err != nil {
		t.Fatalf("want v1's checkpoint retained after the failed delete, got %v", err)
	}

	checkpoints.setFail(false)

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("repairing the retirement: %v", err)
	}

	if got := countEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()); got != 1 {
		t.Errorf("want exactly one reservation, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.PreviousRetired{}.EventType()); got != 2 {
		t.Errorf("want one completion per finished rebuild (2 total), got %d", got)
	}

	if _, err := checkpoints.Load(t.Context(), v1); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want v1's checkpoint deleted after the repair, got %v", err)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRun_RefusesStaleTerminalRebuild pins that Run decides from a fresh
// view: entering at a caught-up phase appends nothing, so without the
// refresh a handle loaded before an Abandon would start a processor that
// tails forever without ever surfacing the conflict.
func TestRun_RefusesStaleTerminalRebuild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("will be abandoned under a stale handle")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	// Loaded while the rebuild is still caught up; goes stale at the abandon.
	stale, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if err := r.Abandon(t.Context(), "abandoned before the stale run"); err != nil {
		t.Fatalf("abandoning: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Fatalf("want Run to return nil after Abandon, got %v", err)
	}

	if err := stale.Run(t.Context()); err == nil {
		t.Fatal("want the stale handle's Run refused after the abandon, got nil")
	}
}

// TestPromote_RecordedDespiteSaveFailure pins the ErrEventsAppended
// contract: when the save fails after the event is durable, the flip
// happened — the effect worker observes it from the stream, and the returned
// error says the transition is recorded and the handle is stale.
func TestPromote_RecordedDespiteSaveFailure(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	inner, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	projections := &eventsAppendedStore{Store: inner}
	h := buildHarness(t, events, projections)
	h.appendDomain(3)

	r := h.begin("events-appended failure")
	_, _ = runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	projections.armFailure()

	err = r.Promote(t.Context())
	if !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want an error carrying ErrEventsAppended, got %v", err)
	}

	// The flip is durable, so the effect worker must apply it.
	v1 := projection.ID{Name: "orders", Version: 1}
	h.waitLive(v1)

	if got := countEventsOfType(t, h.events, lifecycle.Promoted{}.EventType()); got != 1 {
		t.Errorf("want exactly one Promoted event recorded, got %d", got)
	}
}

// TestPromoteFailure_DoesNotLeakIntoAbandon pins that a failed command save
// leaves nothing queued on the handle: without the discard, the failed
// Promote's event would ride along with a later Abandon's save, durably
// promoting the version and then tearing down the now-live model.
func TestPromoteFailure_DoesNotLeakIntoAbandon(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	inner, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	projections := &refusingStore{Store: inner}
	h := buildHarness(t, events, projections)
	h.appendDomain(3)

	r := h.begin("save will fail")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	projections.armFailure()

	if err := r.Promote(t.Context()); err == nil {
		t.Fatal("want the armed save failure, got nil")
	}

	if err := r.Abandon(t.Context(), "giving up after failed promote"); err != nil {
		t.Fatalf("abandoning after a failed promote: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Fatalf("want Run to return nil after Abandon, got %v", err)
	}

	// The failed promotion must not have become durable alongside the
	// abandonment: no Promoted in the history, so reads can never flip.
	if got := countEventsOfType(t, events, lifecycle.Promoted{}.EventType()); got != 0 {
		t.Errorf("want no Promoted events recorded, got %d", got)
	}

	if _, err := h.router.Live(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrNoLiveVersion) {
		t.Errorf("want no live version after the abandoned rebuild, got %v", err)
	}

	if got := r.State().Attempt.Phase; got != lifecycle.PhaseNone {
		t.Errorf("want the attempt slot vacant, got %s", got)
	}
}

// TestStaleHandle_DoesNotReplayDurableTransition pins the durable-append
// variant of the same defect: after a save fails with the event already
// durable, a later command on the same handle must not re-append that event.
// Retire rehydrates before acting, which restores version freshness —
// without the discard, the leftover queued Promoted would append cleanly as
// a duplicate ahead of the retirement.
func TestStaleHandle_DoesNotReplayDurableTransition(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &truncatingWriter{Store: events}

	projections, err := lifecycle.NewStore(writer)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	h := buildHarness(t, events, projections)
	h.appendDomain(3)

	v1 := h.promoteFirstVersion()

	r2 := h.begin("second build")
	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	writer.armFailure()

	err = r2.Promote(t.Context())
	if !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want an error carrying ErrEventsAppended, got %v", err)
	}

	// The promotion is durable despite the failed save; Retire rehydrates,
	// observes it, and must record exactly one reservation and one
	// completion — not a replayed Promoted.
	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retiring after a stale promote: %v", err)
	}

	if got := countEventsOfType(t, events, lifecycle.Promoted{}.EventType()); got != 2 {
		t.Errorf("want one Promoted per rebuild (2 total), got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()); got != 1 {
		t.Errorf("want exactly one reservation, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.PreviousRetired{}.EventType()); got != 2 {
		t.Errorf("want one completion per finished rebuild (2 total), got %d", got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down exactly once, got %v", v1, dropped)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestBegin_DurableAdmissionIsResumableByName pins Begin's durable-append
// error: the name is the address, so an admission that was recorded but not
// observed is reached by resuming the projection — no internally assigned ID
// to lose, no orphaned rebuild to carry in the error.
func TestBegin_DurableAdmissionIsResumableByName(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &truncatingWriter{Store: events}

	projections, err := lifecycle.NewStore(writer)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	h := buildHarness(t, events, projections)

	writer.armFailure()

	_, err = h.orchestrator.Begin(t.Context(), "orders", "durable admission")
	if !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want the error to carry ErrEventsAppended, got %v", err)
	}

	r, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming the projection: %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	if state := r.State(); state.Attempt.Phase != lifecycle.PhaseCreated || state.Attempt.Target != v1 {
		t.Errorf("want the admitted rebuild resumable at %s targeting %s, got %+v", lifecycle.PhaseCreated, v1, state.Attempt)
	}
}

// TestRun_SingleUse pins the handle contract: Run may be called at most once
// per handle, so a second call cannot start a competing processor whose
// ownership silently overwrites the first's.
func TestRun_SingleUse(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("single use")

	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	// The deadline bounds the failure mode: without the guard, a repeated
	// call starts a second processor and blocks tailing until its context
	// dies, so a deadline expiry means the call was not refused.
	secondCtx, cancelSecond := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancelSecond()

	if err := r.Run(secondCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want a second Run refused immediately, got %v", err)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping the first Run: %v", err)
	}

	thirdCtx, cancelThird := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancelThird()

	if err := r.Run(thirdCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want Run refused after the handle already ran, got %v", err)
	}
}

// TestRun_ResumesAutoPromotion pins append-then-act reconciliation for the
// caught-up window: a rebuild that recorded CaughtUp but stopped before its
// auto-promotion is promoted when Run resumes it, rather than tailing
// unpromoted forever.
func TestRun_ResumesAutoPromotion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("will stop at caught-up")
	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping the build: %v", err)
	}

	// An auto-promoting orchestrator over the same stores resumes the
	// rebuild; entering at caught-up must retry the promotion.
	auto, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:      h.events,
		Checkpoints: h.checkpoints,
		Handler:     h.model.handler,
		Projections: h.projections,
	},
		lifecycle.WithAutoPromote(true),
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
		lifecycle.WithReconcileInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating auto-promoting orchestrator: %v", err)
	}

	resumed, err := auto.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	cancel2, done2 := runAsync(t, resumed)
	waitPhase(t, resumed, lifecycle.PhasePromoted)

	v1 := projection.ID{Name: "orders", Version: 1}
	h.waitLive(v1)

	cancel2()

	if err := waitDone(t, done2); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping the resumed run: %v", err)
	}
}

// TestBegin_RefusesSecondInFlight pins single-in-flight admission: while an
// attempt occupies the slot, a second Begin is refused outright.
func TestBegin_RefusesSecondInFlight(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_ = h.begin("first")

	if _, err := h.orchestrator.Begin(t.Context(), "orders", "second"); err == nil {
		t.Error("want a second Begin refused while an attempt is in flight, got nil")
	}
}

// TestBegin_NeverReusesVersions pins the allocation rule: every admission
// targets one past the highest version ever allocated, regardless of how the
// prior attempts ended.
func TestBegin_NeverReusesVersions(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	v1 := h.promoteFirstVersion()

	// An abandoned attempt burns its version number.
	r2 := h.begin("will be abandoned")

	if got := r2.State().Attempt.Target; got != (projection.ID{Name: "orders", Version: 2}) {
		t.Fatalf("want the second attempt to target v2, got %s", got)
	}

	if err := r2.Abandon(t.Context(), "burned"); err != nil {
		t.Fatalf("abandoning: %v", err)
	}

	// So does a rolled-back one.
	r3 := h.begin("will be rolled back")

	if got := r3.State().Attempt.Target; got != (projection.ID{Name: "orders", Version: 3}) {
		t.Fatalf("want the third attempt to target v3, got %s", got)
	}

	_, done3 := runAsync(t, r3)
	waitPhase(t, r3, lifecycle.PhaseCaughtUp)

	if err := r3.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v3: %v", err)
	}

	if err := r3.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back v3: %v", err)
	}

	if err := waitDone(t, done3); err != nil {
		t.Fatalf("want Run to return nil after Rollback, got %v", err)
	}

	r4 := h.begin("fourth attempt")

	if state := r4.State(); state.Attempt.Target != (projection.ID{Name: "orders", Version: 4}) || state.Attempt.Previous != v1 {
		t.Errorf("want the fourth attempt to target v4 from live %s, got %+v", v1, state.Attempt)
	}
}

// TestBegin_DoesNotDisturbResidue pins that Begin is non-destructive: a dead
// attempt's checkpoint and storage belong to a permanently dead identity,
// and admitting the next rebuild neither probes nor removes them.
func TestBegin_DoesNotDisturbResidue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	h.promoteFirstVersion()

	r2 := h.begin("will be ended remotely")
	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	v2 := projection.ID{Name: "orders", Version: 2}
	waitFor(t, func() bool { return len(h.model.table(v2)) == 3 })

	// A processor-less abandonment leaves the build's residue in place.
	remote, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if err := remote.Abandon(t.Context(), "remote abandon"); err != nil {
		t.Fatalf("abandoning remotely: %v", err)
	}

	if err := waitDone(t, done2); err != nil {
		t.Fatalf("want the builder to wind itself down with nil, got %v", err)
	}

	// Admitting the next rebuild disturbs nothing: v2's checkpoint and
	// storage survive, unread and unwritten, until explicitly collected.
	r3 := h.begin("after residue")

	if got := r3.State().Attempt.Target; got != (projection.ID{Name: "orders", Version: 3}) {
		t.Fatalf("want the next attempt to target v3, got %s", got)
	}

	if _, err := h.checkpoints.Load(t.Context(), v2); err != nil {
		t.Errorf("want the dead attempt's checkpoint untouched, got %v", err)
	}

	if got := len(h.model.table(v2)); got != 3 {
		t.Errorf("want the dead attempt's storage untouched (3 rows), got %d", got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Errorf("want nothing torn down, got %v", dropped)
	}
}

// TestRacingBegins_LoserRefused pins first-admission arbitration: two Begins
// racing to create the same projection's lifecycle land on the same
// name-derived stream, and the loser's admission is refused with a version
// mismatch rather than creating a second rebuild.
func TestRacingBegins_LoserRefused(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	competitorAttempt := uuid.Must(uuid.NewV4())
	competitorData, err := json.Marshal(lifecycle.RebuildInitiated{
		Attempt: competitorAttempt,
		Target:  projection.ID{Name: "orders", Version: 1},
		Reason:  "the competing Begin",
		At:      time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshaling competitor event: %v", err)
	}

	writer := &racingWriter{Store: events, competitor: competitorData}

	projections, err := lifecycle.NewStore(writer)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	h := buildHarness(t, events, projections)

	writer.armRace()

	_, err = h.orchestrator.Begin(t.Context(), "orders", "the losing Begin")
	if !errors.Is(err, eventstore.StreamVersionMismatchError{}) {
		t.Fatalf("want the losing admission refused with a version mismatch, got %v", err)
	}

	// The loser resumes by name and observes the winner's attempt.
	r, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if got := r.State().Attempt.ID; got != competitorAttempt {
		t.Errorf("want the winning attempt %s in flight, got %s", competitorAttempt, got)
	}
}

// TestResumeAfterCrash pins crash recovery: a new handle resumed by name
// records BuildResumed and completes the build from the checkpoint.
func TestResumeAfterCrash(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("will crash")
	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	// The "crash": the run's context dies mid-build.
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the crashed run to report cancellation, got %v", err)
	}

	h.model.releaseGate()

	resumed, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if got := resumed.State().Attempt.Phase; got != lifecycle.PhaseBuilding {
		t.Fatalf("want a resumed rebuild still %s, got %s", lifecycle.PhaseBuilding, got)
	}

	_, _ = runAsync(t, resumed)
	waitPhase(t, resumed, lifecycle.PhaseCaughtUp)

	v1 := projection.ID{Name: "orders", Version: 1}
	waitFor(t, func() bool { return len(h.model.table(v1)) == 3 })

	// The stream records the full story: initiated, started, resumed, caught up.
	loaded, err := h.projections.Load(t.Context(), lifecycle.StreamUUID("orders"), nil)
	if err != nil {
		t.Fatalf("loading lifecycle aggregate: %v", err)
	}

	if got := loaded.Version(); got != 4 {
		t.Errorf("want 4 recorded transitions (initiated, started, resumed, caught up), got %d", got)
	}
}

// TestCompetingOrchestrators pins the coordination story: two handles racing
// to promote are arbitrated by optimistic concurrency on the lifecycle
// stream, and the loser observes the winner's transition after reloading.
func TestCompetingOrchestrators(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("competing operators")
	_, _ = runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	first, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming first handle: %v", err)
	}

	second, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming second handle: %v", err)
	}

	if err := first.Promote(t.Context()); err != nil {
		t.Fatalf("promoting from the first handle: %v", err)
	}

	err = second.Promote(t.Context())
	if !errors.Is(err, eventstore.StreamVersionMismatchError{}) {
		t.Fatalf("want the losing promotion refused with a version mismatch, got %v", err)
	}

	state, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("reloading after losing: %v", err)
	}

	if got := state.Attempt.Phase; got != lifecycle.PhasePromoted {
		t.Errorf("want the loser to observe %s after reloading, got %s", lifecycle.PhasePromoted, got)
	}
}

// TestSeparateLifecycleStore runs the lifecycle aggregates in their own event
// store, with the effect worker and a StreamRouter folding it: domain and
// infrastructure streams never interleave.
func TestSeparateLifecycleStore(t *testing.T) {
	t.Parallel()

	domainEvents := newEventStore(t)
	lifecycleEvents := newEventStore(t)

	projections, err := lifecycle.NewStore(lifecycleEvents)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	checkpoints := cpmemory.NewCheckpointStore()
	router := lifecycle.NewMemoryRouter()
	model := newReadModel()

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:      domainEvents,
		Checkpoints: checkpoints,
		Handler:     model.handler,
		Projections: projections,
	},
		lifecycle.WithAutoPromote(true),
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
		lifecycle.WithReconcileInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	worker, err := lifecycle.NewWorker(lifecycleEvents, checkpoints,
		lifecycle.WithLiveSetter(router),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating effect worker: %v", err)
	}

	workerErr := make(chan error, 1)

	go func() { workerErr <- worker.Run(t.Context()) }()

	t.Cleanup(func() {
		if err := <-workerErr; !errors.Is(err, context.Canceled) {
			t.Errorf("effect worker exited unexpectedly: %v", err)
		}
	})

	events := make([]*eventstore.WritableEvent, 0, 5)
	for range 5 {
		events = append(events, &eventstore.WritableEvent{Type: "ordertest", Data: []byte(`{}`)})
	}

	if _, err := domainEvents.AppendStream(t.Context(), typeid.NewV4("order"), events, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending domain events: %v", err)
	}

	r, err := orchestrator.Begin(t.Context(), "orders", "separate store")
	if err != nil {
		t.Fatalf("beginning rebuild: %v", err)
	}

	_, _ = runAsync(t, r)
	waitPhase(t, r, lifecycle.PhasePromoted)

	v1 := projection.ID{Name: "orders", Version: 1}

	waitFor(t, func() bool {
		live, err := router.Live(t.Context(), "orders")
		return err == nil && live == v1
	})

	// The recorded cutover is also derivable directly from the lifecycle
	// store's history.
	streamRouter, err := lifecycle.NewStreamRouter(lifecycleEvents)
	if err != nil {
		t.Fatalf("creating stream router: %v", err)
	}

	if live, err := streamRouter.Live(t.Context(), "orders"); err != nil || live != v1 {
		t.Fatalf("want live version %s from the stream router, got %s (%v)", v1, live, err)
	}

	// The projection saw exactly the domain events: no infrastructure
	// streams interleave when the lifecycle store is separate.
	waitFor(t, func() bool { return len(model.table(v1)) == 5 })
}

func TestBegin_InvalidName(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if _, err := h.orchestrator.Begin(t.Context(), "Bad Name", "reason"); err == nil {
		t.Error("want an error for an invalid projection name, got nil")
	}
}

// TestBegin_RejectsForeignStoreType pins that a projection store not
// managing estoria.projection streams is refused before anything is
// recorded: its cutover events would be invisible to the effect worker and
// StreamRouter folds.
func TestBegin_RejectsForeignStoreType(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	foreign, err := aggregatestore.New(events, "ordertest", lifecycle.NewState)
	if err != nil {
		t.Fatalf("creating foreign-typed store: %v", err)
	}

	h := buildHarness(t, events, foreign)

	if _, err := h.orchestrator.Begin(t.Context(), "orders", "wrong store"); err == nil {
		t.Error("want Begin to reject a store with a foreign stream type, got nil")
	}
}

// TestGet pins read-only inspection: absent lifecycles report not-found, and
// present ones return the folded state.
func TestGet(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if _, err := h.orchestrator.Get(t.Context(), "orders"); !errors.Is(err, aggregatestore.ErrAggregateNotFound) {
		t.Errorf("want ErrAggregateNotFound for a projection never rebuilt, got %v", err)
	}

	_ = h.begin("inspection")

	state, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("getting lifecycle state: %v", err)
	}

	if state.Name != "orders" || state.Allocated != 1 || state.Attempt.Phase != lifecycle.PhaseCreated {
		t.Errorf("want the admitted attempt visible, got %+v", state)
	}
}

func TestNewOrchestrator_RejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	model := newReadModel()
	config := lifecycle.Config{
		Events:      events,
		Checkpoints: cpmemory.NewCheckpointStore(),
		Handler:     model.handler,
		Projections: projections,
	}

	for _, tt := range []struct {
		name string
		opt  lifecycle.OrchestratorOption
	}{
		{"rejects a nil processor option", lifecycle.WithProcessorOptions(nil)},
		{"rejects a nil logger", lifecycle.WithLogger(nil)},
		{"rejects a zero reconcile interval", lifecycle.WithReconcileInterval(0)},
		{"rejects a negative reconcile interval", lifecycle.WithReconcileInterval(-time.Second)},
		{"rejects a nil option", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := lifecycle.NewOrchestrator(config, tt.opt); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}

func TestNewOrchestrator_Validation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	valid := lifecycle.Config{
		Events:      h.events,
		Checkpoints: h.checkpoints,
		Handler:     h.model.handler,
		Projections: h.projections,
	}

	for _, tt := range []struct {
		name   string
		mutate func(*lifecycle.Config)
	}{
		{"rejects a nil global reader", func(c *lifecycle.Config) { c.Events = nil }},
		{"rejects a nil checkpoint store", func(c *lifecycle.Config) { c.Checkpoints = nil }},
		{"rejects a nil handler factory", func(c *lifecycle.Config) { c.Handler = nil }},
		{"rejects a nil projection store", func(c *lifecycle.Config) { c.Projections = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			tt.mutate(&config)

			if _, err := lifecycle.NewOrchestrator(config); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}

//
// read model test double
//

// readModel is the versioned read-side: one "table" of handled global
// positions per projection version, with Teardown dropping a version's table.
type readModel struct {
	mu           sync.Mutex
	tables       map[projection.ID][]int64
	dropped      []projection.ID
	gate         chan struct{}
	failTeardown bool
}

func newReadModel() *readModel {
	return &readModel{tables: map[projection.ID][]int64{}}
}

// handler is the orchestrator's handler factory: the versioned ID flows in so
// the handler targets versioned storage.
func (m *readModel) handler(id projection.ID) (projection.EventHandler, error) {
	return &readModelHandler{model: m, id: id}, nil
}

// armGate makes handlers block on domain events until releaseGate, so tests
// can hold a build mid-replay deterministically.
func (m *readModel) armGate() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.gate = make(chan struct{})
}

func (m *readModel) releaseGate() {
	m.mu.Lock()
	defer m.mu.Unlock()

	close(m.gate)
}

func (m *readModel) table(id projection.ID) []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]int64(nil), m.tables[id]...)
}

func (m *readModel) droppedTables() []projection.ID {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]projection.ID(nil), m.dropped...)
}

// setTeardownFailure arms or disarms teardown failures.
func (m *readModel) setTeardownFailure(fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.failTeardown = fail
}

type readModelHandler struct {
	model *readModel
	id    projection.ID
}

func (h *readModelHandler) Handle(ctx context.Context, event *eventstore.Event) error {
	// Lifecycle events interleave with domain events on a shared store; a
	// projection handler filters by stream type.
	if event.StreamID.Type == lifecycle.StreamType {
		return nil
	}

	h.model.mu.Lock()
	gate := h.model.gate
	h.model.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	h.model.mu.Lock()
	defer h.model.mu.Unlock()

	h.model.tables[h.id] = append(h.model.tables[h.id], *event.GlobalPosition)

	return nil
}

// Teardown implements projection.Teardowner: it drops the version's table.
func (h *readModelHandler) Teardown(_ context.Context, id projection.ID) error {
	h.model.mu.Lock()
	defer h.model.mu.Unlock()

	if h.model.failTeardown {
		return errors.New("teardown failed")
	}

	delete(h.model.tables, id)
	h.model.dropped = append(h.model.dropped, id)

	return nil
}

var _ projection.Teardowner = (*readModelHandler)(nil)

// eventsAppendedStore delegates saves and, when armed, reports the next save
// as failed-after-append: the events are durable in the store, but the error
// carries aggregatestore.ErrEventsAppended.
type eventsAppendedStore struct {
	aggregatestore.Store[lifecycle.State]
	mu    sync.Mutex
	armed bool
}

func (s *eventsAppendedStore) armFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.armed = true
}

func (s *eventsAppendedStore) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[lifecycle.State], opts *aggregatestore.SaveOptions) error {
	s.mu.Lock()
	armed := s.armed
	s.armed = false
	s.mu.Unlock()

	if err := s.Store.Save(ctx, aggregate, opts); err != nil {
		return err
	}

	if armed {
		return fmt.Errorf("%w: simulated read-back failure", aggregatestore.ErrEventsAppended)
	}

	return nil
}

// refusingStore delegates saves and, when armed, refuses the next save
// outright, before anything reaches the event store — the pre-append failure
// shape, which leaves the command's event queued on the aggregate.
type refusingStore struct {
	aggregatestore.Store[lifecycle.State]
	mu    sync.Mutex
	armed bool
}

func (s *refusingStore) armFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.armed = true
}

func (s *refusingStore) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[lifecycle.State], opts *aggregatestore.SaveOptions) error {
	s.mu.Lock()
	armed := s.armed
	s.armed = false
	s.mu.Unlock()

	if armed {
		return errors.New("simulated save refusal")
	}

	return s.Store.Save(ctx, aggregate, opts)
}

// truncatingWriter delegates appends and, when armed, truncates the next
// append's result: the events are durable in the store, but the caller cannot
// observe them. Driving the real aggregate store over this writer produces
// the faithful ErrEventsAppended shape — queue intact, state not advanced —
// unlike eventsAppendedStore, whose inner save fully applies first.
type truncatingWriter struct {
	eventstore.Store
	mu    sync.Mutex
	armed bool
}

func (w *truncatingWriter) armFailure() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.armed = true
}

func (w *truncatingWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	w.mu.Lock()
	armed := w.armed
	w.armed = false
	w.mu.Unlock()

	written, err := w.Store.AppendStream(ctx, streamID, events, opts)
	if err != nil || !armed {
		return written, err
	}

	return written[:0], nil
}

// racingWriter delegates appends and, when armed, first lands a competing
// admission on the same lifecycle stream, so the caller's own append — sent
// with the expected version it read before the race — reports a version
// mismatch. This is the deterministic form of two Begins racing to create
// one projection's lifecycle.
type racingWriter struct {
	eventstore.Store
	competitor []byte
	mu         sync.Mutex
	armed      bool
}

func (w *racingWriter) armRace() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.armed = true
}

func (w *racingWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	w.mu.Lock()
	armed := w.armed && streamID.Type == lifecycle.StreamType
	if armed {
		w.armed = false
	}
	w.mu.Unlock()

	if armed {
		competing := []*eventstore.WritableEvent{{
			Type:            lifecycle.RebuildInitiated{}.EventType(),
			Data:            w.competitor,
			DataContentType: "application/json",
		}}

		if _, err := w.Store.AppendStream(ctx, streamID, competing, eventstore.AppendStreamOptions{}); err != nil {
			return nil, fmt.Errorf("landing competing admission: %w", err)
		}
	}

	return w.Store.AppendStream(ctx, streamID, events, opts)
}

// appendRawLifecycleEvent writes a lifecycle event to the named projection's
// stream through the raw event store, bypassing the aggregate — the shape of
// tampering the reserved namespace does not prevent.
func appendRawLifecycleEvent(t *testing.T, events *esmemory.EventStore, name string, event estoria.DomainEvent[lifecycle.State]) {
	t.Helper()

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshaling %s event: %v", event.EventType(), err)
	}

	streamID := typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID(name)}

	if _, err := events.AppendStream(t.Context(), streamID, []*eventstore.WritableEvent{{
		Type:            event.EventType(),
		Data:            data,
		DataContentType: "application/json",
	}}, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending raw %s event: %v", event.EventType(), err)
	}
}

// countEventsOfType counts events of the given type across the entire store,
// for asserting what a command sequence durably recorded.
func countEventsOfType(t *testing.T, events *esmemory.EventStore, eventType string) int {
	t.Helper()

	iter, err := events.ReadAll(t.Context(), eventstore.ReadAllOptions{})
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	all, err := eventstore.Collect(t.Context(), iter)
	if err != nil {
		t.Fatalf("collecting events: %v", err)
	}

	count := 0

	for _, event := range all {
		if event.ID.Type == eventType {
			count++
		}
	}

	return count
}

// noTeardownHandler hides the read model's Teardowner capability, modeling a
// handler whose storage removal the library does not manage.
type noTeardownHandler struct{ inner projection.EventHandler }

func (h noTeardownHandler) Handle(ctx context.Context, event *eventstore.Event) error {
	return h.inner.Handle(ctx, event)
}

// countingFactory wraps the read model's handler factory, counting
// resolutions per ID, failing on demand, and tracing each resolved handler
// so a teardown can be attributed to the exact instance that performed it.
type countingFactory struct {
	model *readModel

	mu    sync.Mutex
	calls map[projection.ID]int
	fail  map[projection.ID]bool
	last  *tracedHandler
}

func (f *countingFactory) handler(id projection.ID) (projection.EventHandler, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.calls == nil {
		f.calls = map[projection.ID]int{}
	}

	f.calls[id]++

	if f.fail[id] {
		return nil, errors.New("handler factory refused")
	}

	inner, err := f.model.handler(id)
	if err != nil {
		return nil, err
	}

	traced := &tracedHandler{inner: inner.(*readModelHandler)}
	f.last = traced

	return traced, nil
}

func (f *countingFactory) setFail(id projection.ID, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fail == nil {
		f.fail = map[projection.ID]bool{}
	}

	f.fail[id] = fail
}

func (f *countingFactory) resolutions(id projection.ID) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls[id]
}

func (f *countingFactory) lastResolved() *tracedHandler {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.last
}

// tracedHandler counts the teardowns performed through this exact instance.
type tracedHandler struct {
	inner *readModelHandler

	mu        sync.Mutex
	teardowns int
}

func (h *tracedHandler) Handle(ctx context.Context, event *eventstore.Event) error {
	return h.inner.Handle(ctx, event)
}

func (h *tracedHandler) Teardown(ctx context.Context, id projection.ID) error {
	h.mu.Lock()
	h.teardowns++
	h.mu.Unlock()

	return h.inner.Teardown(ctx, id)
}

func (h *tracedHandler) teardownCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.teardowns
}

// failingDeleteCheckpoints delegates to the wrapped store and, while armed,
// fails every Delete.
type failingDeleteCheckpoints struct {
	checkpointstore.Store

	mu    sync.Mutex
	armed bool
}

func (s *failingDeleteCheckpoints) setFail(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.armed = fail
}

func (s *failingDeleteCheckpoints) Delete(ctx context.Context, id projection.ID) error {
	s.mu.Lock()
	armed := s.armed
	s.mu.Unlock()

	if armed {
		return errors.New("simulated checkpoint delete failure")
	}

	return s.Store.Delete(ctx, id)
}
