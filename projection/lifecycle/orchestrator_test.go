package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	model        *readModel
	orchestrator *lifecycle.Orchestrator
}

func newHarness(t *testing.T, opts ...lifecycle.OrchestratorOption) *harness {
	t.Helper()

	return buildHarness(t, newEventStore(t), opts...)
}

func newEventStore(t *testing.T) *esmemory.EventStore {
	t.Helper()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	return events
}

// buildHarness wires the standard harness over the given event store.
func buildHarness(t *testing.T, events *esmemory.EventStore, opts ...lifecycle.OrchestratorOption) *harness {
	t.Helper()

	return buildHarnessWithLifecycleEvents(t, events, events, opts...)
}

// buildHarnessWithLifecycleEvents additionally separates the lifecycle event
// store from the domain store, so tests can interpose on the one store every
// lifecycle fold and append flows through.
func buildHarnessWithLifecycleEvents(t *testing.T, events *esmemory.EventStore, lifecycleEvents eventstore.Store, opts ...lifecycle.OrchestratorOption) *harness {
	t.Helper()

	checkpoints := cpmemory.NewCheckpointStore()
	router := lifecycle.NewMemoryRouter()
	model := newReadModel()

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:          events,
		Checkpoints:     checkpoints,
		Handler:         model.handler,
		LifecycleEvents: lifecycleEvents,
	}, append([]lifecycle.OrchestratorOption{
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2 * time.Millisecond)),
		lifecycle.WithReconcileInterval(10 * time.Millisecond),
		lifecycle.WithRetirementWitness("router", router),
	}, opts...)...)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	h := &harness{
		t:            t,
		events:       events,
		checkpoints:  checkpoints,
		router:       router,
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

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:          events,
		Checkpoints:     checkpoints,
		Handler:         handler,
		LifecycleEvents: events,
	},
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
		lifecycle.WithReconcileInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	return orchestrator
}

// startWorker runs a cutover worker over the given store for the duration of
// the test, converging the harness router on recorded cutovers. A worker
// exit other than the test context's cancellation is a test failure.
func (h *harness) startWorker(events eventstore.GlobalReader) {
	h.t.Helper()

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(h.router),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		h.t.Fatalf("creating cutover worker: %v", err)
	}

	runErr := make(chan error, 1)

	go func() { runErr <- worker.Run(h.t.Context()) }()

	h.t.Cleanup(func() {
		if err := <-runErr; !errors.Is(err, context.Canceled) {
			h.t.Errorf("cutover worker exited unexpectedly: %v", err)
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

	if err := <-steadyDone; !errors.Is(err, context.Canceled) {
		t.Errorf("want the steady-state processor to report its cancellation, got %v", err)
	}

	// Retirement is gated on the durable policy: the router must vouch for
	// the exact live cutover before v1's storage is destroyed.
	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"router"}, Actor: "test operator", Reason: "gate retirements on the serving router",
	}); err != nil {
		t.Fatalf("setting retirement policy: %v", err)
	}

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

	cancel, done := runAsync(t, r)
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

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
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

		// Every interleaving is deliberate: a run stopped mid-flight reports
		// nil, and an abandonment that lands before Run's entry refresh makes
		// Run refuse the vacant slot outright.
		if err := waitDone(t, done); err != nil && !strings.Contains(err.Error(), "no rebuild in flight") {
			t.Fatalf("want nil or the vacant-slot refusal after Abandon, got %v", err)
		}

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
// cleanup, is what ends it — and the lost append is classified against the
// recorded truth: the attempt ended, so the builder winds down clean.
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
	// with the recorded abandonment: the loss reloads the truth, observes
	// the vacated slot, and winds down as a deliberate stop.
	h.model.releaseGate()

	if err := waitDone(t, done); err != nil {
		t.Errorf("want the superseded builder to wind down clean after the classified loss, got %v", err)
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
		appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{
			Attempt:  uuid.Must(uuid.NewV4()),
			Target:   projection.ID{Name: "orders", Version: 1},
			Previous: projection.ID{Name: "customers", Version: 1},
			Reason:   "poisoned",
			At:       time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC),
		})

		if _, err := orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want Resume refused with ErrInvalidState, got %v", err)
		}

		if _, err := orchestrator.Get(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want Get refused with ErrInvalidState, got %v", err)
		}

		if _, err := orchestrator.Begin(t.Context(), "orders", "on poison"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want Begin refused with ErrInvalidState, got %v", err)
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

		_, done2 := runAsync(t, r2)
		waitPhase(t, r2, lifecycle.PhaseCaughtUp)

		if err := r2.Promote(t.Context()); err != nil {
			t.Fatalf("promoting v2: %v", err)
		}

		// A poisoned promotion lands on the stream: it decodes, the fold
		// applies it — history is truth — and the resulting state names a
		// different projection as live.
		appendRawLifecycleEvent(t, events, lifecycle.Promoted{
			Previous: projection.ID{Name: "orders", Version: 2},
			Next:     projection.ID{Name: "customers", Version: 7},
			At:       time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC),
		})

		if err := r2.Retire(t.Context()); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Fatalf("want retirement refused with ErrInvalidState, got %v", err)
		}

		if dropped := model.droppedTables(); len(dropped) != 0 {
			t.Errorf("want nothing torn down from the refused retirement, got %v", dropped)
		}

		// The builder's reconcile loop observes the same invalidity and fails
		// closed rather than tailing the poisoned lifecycle.
		if err := waitDone(t, done2); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want Run to fail closed with ErrInvalidState on the poisoned lifecycle, got %v", err)
		}
	})
}

// TestResume_RefusesPoisonedAllocation pins sticky fold invalidity: a
// decodable admission that reuses or skips past the allocation high-water
// mark assigns both sides of every final-state equality, so only the mark
// left at the moment of observation can prove the history inconsistent —
// and no later event may clear it. Without the refusal, running the reused
// attempt would resume from the dead version's checkpoint and skip history.
func TestResume_RefusesPoisonedAllocation(t *testing.T) {
	t.Parallel()

	// burnV1 admits and abandons orders v1, leaving Allocated at 1 with the
	// slot vacant, and returns the store for raw poisoning.
	burnV1 := func(t *testing.T) (*esmemory.EventStore, *lifecycle.Orchestrator) {
		t.Helper()

		events := newEventStore(t)
		model := newReadModel()
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), model.handler)

		r, err := orchestrator.Begin(t.Context(), "orders", "will be abandoned")
		if err != nil {
			t.Fatalf("beginning v1: %v", err)
		}

		if err := r.Abandon(t.Context(), "burning v1"); err != nil {
			t.Fatalf("abandoning v1: %v", err)
		}

		return events, orchestrator
	}

	reusedAdmission := func() lifecycle.RebuildInitiated {
		return lifecycle.RebuildInitiated{
			Attempt: uuid.Must(uuid.NewV4()),
			Target:  projection.ID{Name: "orders", Version: 1},
			Reason:  "reusing the burned version",
			At:      time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
		}
	}

	t.Run("reused version refused", func(t *testing.T) {
		t.Parallel()

		events, orchestrator := burnV1(t)
		appendRawLifecycleEvent(t, events, reusedAdmission())

		if _, err := orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want Resume refused with ErrInvalidState after a version-reusing admission, got %v", err)
		}
	})

	t.Run("skipped version refused", func(t *testing.T) {
		t.Parallel()

		events, orchestrator := burnV1(t)
		appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{
			Attempt: uuid.Must(uuid.NewV4()),
			Target:  projection.ID{Name: "orders", Version: 5},
			Reason:  "skipping the allocation sequence",
			At:      time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
		})

		if _, err := orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want Resume refused with ErrInvalidState after an allocation-skipping admission, got %v", err)
		}
	})

	t.Run("poison survives later well-formed events", func(t *testing.T) {
		t.Parallel()

		events, orchestrator := burnV1(t)
		appendRawLifecycleEvent(t, events, reusedAdmission())

		// A well-formed abandonment vacates the slot, leaving a final state
		// whose shape alone would validate. The mark must survive it.
		appendRawLifecycleEvent(t, events, lifecycle.Abandoned{Cause: "covering the tracks"})

		if _, err := orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want the poisoned fold refused with ErrInvalidState despite later well-formed events, got %v", err)
		}
	})
}

// TestRetainedHandle_FailsClosedOnPoisonedStream pins revalidation against
// the handle's immutable address: a handle obtained before malformed events
// were appended hydrates incrementally, without Begin/Resume's load-time
// check, so its commands and its reconcile loop must re-run the full
// aggregate check themselves. Without it, a self-consistent foreign-name
// history could aim the retained handle's retirement at another projection's
// storage.
func TestRetainedHandle_FailsClosedOnPoisonedStream(t *testing.T) {
	t.Parallel()

	// customersTakeover is a decodable, internally consistent promoted
	// customers attempt, appended to the orders lifecycle stream.
	customersTakeover := func(t *testing.T, events *esmemory.EventStore) {
		t.Helper()

		customersV1 := projection.ID{Name: "customers", Version: 1}
		customersV2 := projection.ID{Name: "customers", Version: 2}
		at := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)

		appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{
			Attempt:  uuid.Must(uuid.NewV4()),
			Target:   customersV2,
			Previous: customersV1,
			Reason:   "takeover",
			At:       at,
		})
		appendRawLifecycleEvent(t, events, lifecycle.BuildStarted{})
		appendRawLifecycleEvent(t, events, lifecycle.CaughtUp{Position: 1, At: at})
		appendRawLifecycleEvent(t, events, lifecycle.Promoted{Previous: customersV1, Next: customersV2, Revision: 1, At: at})
	}

	t.Run("retire refused", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		model := newReadModel()
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), model.handler)

		r, err := orchestrator.Begin(t.Context(), "orders", "to be retained")
		if err != nil {
			t.Fatalf("beginning: %v", err)
		}

		if err := r.Abandon(t.Context(), "retaining the handle"); err != nil {
			t.Fatalf("abandoning: %v", err)
		}

		customersTakeover(t, events)

		if err := r.Retire(t.Context()); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Fatalf("want the retained handle's retirement refused with ErrInvalidState, got %v", err)
		}

		if dropped := model.droppedTables(); len(dropped) != 0 {
			t.Errorf("want no teardown through the poisoned stream, got %v", dropped)
		}
	})

	t.Run("rollback refused", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		model := newReadModel()
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), model.handler)

		r, err := orchestrator.Begin(t.Context(), "orders", "to be retained")
		if err != nil {
			t.Fatalf("beginning: %v", err)
		}

		if err := r.Abandon(t.Context(), "retaining the handle"); err != nil {
			t.Fatalf("abandoning: %v", err)
		}

		customersTakeover(t, events)

		// A rollback records a routing flip, so deciding from the retained
		// view instead of the poisoned fold would aim RolledBack's cutover
		// from a history this handle never validated.
		if err := r.Rollback(t.Context()); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Fatalf("want the retained handle's rollback refused with ErrInvalidState, got %v", err)
		}

		if got := countEventsOfType(t, events, lifecycle.RolledBack{}.EventType()); got != 0 {
			t.Errorf("want no rollback recorded through the poisoned stream, got %d", got)
		}
	})

	t.Run("run fails closed through reconciliation", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		model := newReadModel()
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), model.handler)

		appendDomainTo(t, events, 3)

		r, err := orchestrator.Begin(t.Context(), "orders", "running during the poisoning")
		if err != nil {
			t.Fatalf("beginning: %v", err)
		}

		_, done := runAsync(t, r)
		waitPhase(t, r, lifecycle.PhaseCaughtUp)

		// The poisoned admission replaces the running attempt in the fold.
		// Reconciliation must surface the invalidity, not report the
		// replacement as an ordinary nil wind-down.
		appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{
			Attempt: uuid.Must(uuid.NewV4()),
			Target:  projection.ID{Name: "orders", Version: 2},
			Reason:  "poisoning the running attempt",
			At:      time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC),
		})

		if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want Run to fail closed with ErrInvalidState on the poisoned lifecycle, got %v", err)
		}

		// The fail-closed stop revoked the handle: even though its own
		// aggregate still shows a caught-up attempt, its certification is
		// dead and promotion refuses with the recorded cause attached.
		promoteErr := r.Promote(t.Context())
		if !errors.Is(promoteErr, lifecycle.ErrNotCertified) {
			t.Errorf("want the revoked handle's promotion refused with ErrNotCertified, got %v", promoteErr)
		}

		if !errors.Is(promoteErr, lifecycle.ErrInvalidState) {
			t.Errorf("want the refusal to carry the recorded fail-closed cause, got %v", promoteErr)
		}
	})
}

// TestRetire_CompletionFailureIsRepairableAfterTeardown pins the last
// recovery window in retirement: teardown and checkpoint deletion succeed,
// the completion append fails, and the repair runs from a fresh handle —
// the crashed process's handle is gone; recovery is Resume by name. The
// repair re-resolves the handler for a version whose storage is observably
// already gone — the factory contract — re-drives the idempotent teardown
// on the instance it resolved, tolerates the already-deleted checkpoint,
// and records completion exactly once.
func TestRetire_CompletionFailureIsRepairableAfterTeardown(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	model := newReadModel()
	factory := &countingFactory{model: model}
	checkpoints := cpmemory.NewCheckpointStore()

	writer := &refusingWriter{Store: events}

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:          events,
		Checkpoints:     checkpoints,
		Handler:         factory.handler,
		LifecycleEvents: writer,
	},
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
		lifecycle.WithReconcileInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

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

	// The reservation append passes; the completion append fails.
	writer.armFailureAfterAppends(1)

	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "interrupted completion scenario")); err == nil {
		t.Fatal("want the completion failure reported, got nil")
	}

	// Everything before the completion happened: reservation durable,
	// storage torn down, checkpoint deleted.
	if got := r2.State().Attempt.Phase; got != lifecycle.PhaseRetiring {
		t.Fatalf("want phase %s after the interrupted retirement, got %s", lifecycle.PhaseRetiring, got)
	}

	if dropped := model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Fatalf("want %s torn down before the failed completion, got %v", v1, dropped)
	}

	if _, err := checkpoints.Load(t.Context(), v1); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Fatalf("want v1's checkpoint already deleted, got %v", err)
	}

	// The repair runs from a fresh handle resumed by name.
	fresh, err := orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming for the repair: %v", err)
	}

	preRepair := factory.resolutions(v1)

	if err := fresh.Retire(t.Context()); err != nil {
		t.Fatalf("repairing the retirement: %v", err)
	}

	// The repair re-resolved v1's handler exactly once, for storage that was
	// observably already gone — the factory contract the Handler doc states.
	if got := factory.resolutions(v1); got != preRepair+1 {
		t.Errorf("want exactly one more v1 resolution for the repair, got %d (had %d)", got, preRepair)
	}

	observations := factory.storageObservations(v1)
	if len(observations) < 2 {
		t.Fatalf("want at least the retirement and repair resolutions observed, got %v", observations)
	}

	if observations[len(observations)-2] != true || observations[len(observations)-1] != false {
		t.Errorf("want storage present at the first teardown resolution and absent at the repair's, got %v", observations)
	}

	// The repair re-drove the idempotent teardown on the exact instance it
	// resolved.
	if got := factory.lastResolved().teardownCount(); got != 1 {
		t.Errorf("want exactly one teardown through the repair's handler instance, got %d", got)
	}

	if dropped := model.droppedTables(); len(dropped) != 2 || dropped[0] != v1 || dropped[1] != v1 {
		t.Errorf("want the teardown re-driven on repair (v1 twice), got %v", dropped)
	}

	if got := countEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()); got != 1 {
		t.Errorf("want exactly one reservation, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.PreviousRetired{}.EventType()); got != 2 {
		t.Errorf("want one completion per finished rebuild (2 total), got %d", got)
	}

	if got := fresh.State().Attempt.Phase; got != lifecycle.PhaseNone {
		t.Errorf("want the attempt slot vacant after the repair, got %s", got)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
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

	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "interrupted teardown scenario")); err == nil {
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
	_, done2 := runAsync(t, r2)
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

	if err := waitDone(t, done2); err != nil {
		t.Fatalf("want Run to return nil after Rollback, got %v", err)
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

	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "handler resolution scenario")); err != nil {
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

	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "interrupted checkpoint delete scenario")); err == nil {
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

// TestRun_RefusesTruncatedPrefix pins that Run's entry decides from a fold
// built from scratch, not from the handle's retained prefix: with a retained
// version-1 handle, a valid version-2 claim, and the stream's prefix
// truncated through version 1, the from-scratch fold sees the break in
// continuity and refuses before appending. Hydrating the retained aggregate
// incrementally would instead accept the retained prefix as the missing
// history, absorb the claim, and record another claim plus BuildStarted over
// a stream this handle never validated.
func TestRun_RefusesTruncatedPrefix(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r := h.begin("truncated prefix")

	attemptID := r.State().Attempt.ID

	// A valid competing claim lands at version 2: it names the in-flight
	// attempt and carries a runner, so the fold accepts it in any runnable
	// phase, phase-preservingly.
	store, err := lifecycle.NewStore(h.events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	aggregate, err := store.Load(t.Context(), lifecycle.StreamUUID("orders"), nil)
	if err != nil {
		t.Fatalf("loading lifecycle aggregate: %v", err)
	}

	aggregate.Append(lifecycle.RunnerClaimed{
		Attempt: attemptID,
		Runner:  uuid.Must(uuid.NewV4()),
		At:      time.Now(),
	})
	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("recording the competing claim: %v", err)
	}

	// Truncate the prefix through version 1: reads now begin at the claim,
	// and the retained handle's version-1 view is the only witness to what
	// was removed.
	if err := h.events.DeleteStream(t.Context(), ordersLifecycleStreamID(), eventstore.DeleteStreamOptions{ToVersion: 1}); err != nil {
		t.Fatalf("truncating the lifecycle stream: %v", err)
	}

	_, done := runAsync(t, r)

	if err := waitDone(t, done); err == nil {
		t.Fatal("want the truncated prefix refused at entry, got a running rebuild")
	}

	// The refusal precedes any append: the stream still holds exactly the
	// surviving claim.
	iter, err := h.events.ReadStream(t.Context(), ordersLifecycleStreamID(), eventstore.ReadStreamOptions{})
	if err != nil {
		t.Fatalf("reading the lifecycle stream: %v", err)
	}

	events, err := eventstore.Collect(t.Context(), iter)
	if err != nil {
		t.Fatalf("collecting lifecycle events: %v", err)
	}

	if len(events) != 1 || events[0].StreamVersion != 2 {
		t.Errorf("want the stream unchanged (one event at version 2), got %d events", len(events))
	}
}

// TestPromote_RecordedDespiteSaveFailure pins the ErrEventsAppended
// contract: when the save fails after the event is durable, the flip
// happened — the effect worker observes it from the stream, and the returned
// error says the transition is recorded and the handle is stale.
func TestPromote_RecordedDespiteSaveFailure(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &truncatingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer)
	h.appendDomain(3)

	r := h.begin("events-appended failure")
	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	writer.armFailure()

	if err := r.Promote(t.Context()); !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want an error carrying ErrEventsAppended, got %v", err)
	}

	// The flip is durable, so the effect worker must apply it.
	v1 := projection.ID{Name: "orders", Version: 1}
	h.waitLive(v1)

	if got := countEventsOfType(t, h.events, lifecycle.Promoted{}.EventType()); got != 1 {
		t.Errorf("want exactly one Promoted event recorded, got %d", got)
	}

	// The stale handle is a command-side condition, not a processor fault:
	// the run keeps tailing the promoted version until it is canceled.
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

// TestPromoteFailure_DoesNotLeakIntoAbandon pins that a failed command save
// leaves nothing queued on the handle: without the discard, the failed
// Promote's event would ride along with a later Abandon's save, durably
// promoting the version and then tearing down the now-live model.
func TestPromoteFailure_DoesNotLeakIntoAbandon(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &refusingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer)
	h.appendDomain(3)

	r := h.begin("save will fail")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	writer.armFailure()

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

	h := buildHarnessWithLifecycleEvents(t, events, writer)
	h.appendDomain(3)

	v1 := h.promoteFirstVersion()

	r2 := h.begin("second build")
	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	writer.armFailure()

	if err := r2.Promote(t.Context()); !errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want an error carrying ErrEventsAppended, got %v", err)
	}

	// The uncertain save voided the certificate — the aggregate can no
	// longer vouch for the version it was cut against — so a promote retry
	// is refused as uncertified instead of reaching the stream again.
	if err := r2.Promote(t.Context()); !errors.Is(err, lifecycle.ErrNotCertified) {
		t.Fatalf("want the retried promotion refused with ErrNotCertified, got %v", err)
	}

	// The promotion is durable despite the failed save; Retire rehydrates,
	// observes it, and must record exactly one reservation and one
	// completion — not a replayed Promoted.
	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "stale handle scenario")); err != nil {
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

	h := buildHarnessWithLifecycleEvents(t, events, writer)

	writer.armFailure()

	if _, err := h.orchestrator.Begin(t.Context(), "orders", "durable admission"); !errors.Is(err, aggregatestore.ErrEventsAppended) {
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

// TestRun_RecertifiesCaughtUpAttempt pins re-certification: a rebuild that
// recorded CaughtUp but stopped before promoting is not promoted on the
// strength of the persisted phase — a run entering at PhaseCaughtUp claims,
// drains to the current head, records a fresh CaughtUp covering the events
// that arrived while nothing ran, and only then auto-promotes. The persisted
// phase never regresses along the way.
func TestRun_RecertifiesCaughtUpAttempt(t *testing.T) {
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

	// Events arrive while no processor runs: the stopped run's catch-up
	// position is stale the moment these land.
	h.appendDomain(4)

	// An auto-promoting orchestrator over the same stores resumes the
	// rebuild; entering at caught-up must re-certify against the current
	// head before promoting.
	auto, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:          h.events,
		Checkpoints:     h.checkpoints,
		Handler:         h.model.handler,
		LifecycleEvents: h.events,
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

	if got := resumed.State().Attempt.Phase; got != lifecycle.PhaseCaughtUp {
		t.Fatalf("want the resumed rebuild still %s, got %s", lifecycle.PhaseCaughtUp, got)
	}

	staleCertifiedPos := resumed.State().Attempt.CaughtUpPos

	cancel2, done2 := runAsync(t, resumed)
	waitPhase(t, resumed, lifecycle.PhasePromoted)

	v1 := projection.ID{Name: "orders", Version: 1}
	h.waitLive(v1)

	// The promotion was licensed by a fresh certification: a second CaughtUp
	// is durable, and its position covers the events that arrived while the
	// rebuild sat caught up.
	if got := countEventsOfType(t, h.events, lifecycle.CaughtUp{}.EventType()); got != 2 {
		t.Errorf("want the re-certification to record a second CaughtUp, got %d total", got)
	}

	if got := resumed.State().Attempt.CaughtUpPos; got <= staleCertifiedPos {
		t.Errorf("want the fresh certification past the stale position %d, got %d", staleCertifiedPos, got)
	}

	// The build drained everything before promoting.
	if got := len(h.model.table(v1)); got != 7 {
		t.Errorf("want all 7 domain events drained before promotion, got %d", got)
	}

	cancel2()

	if err := waitDone(t, done2); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping the resumed run: %v", err)
	}
}

// TestPromote_RequiresCertification pins the promotion license: persisted
// PhaseCaughtUp is a historical fact, not a standing license, so a handle
// that merely resumed the caught-up rebuild cannot promote it — only the
// run that drained it to the head holds the certificate.
func TestPromote_RequiresCertification(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("certified promotion only")
	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	resumed, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if err := resumed.Promote(t.Context()); !errors.Is(err, lifecycle.ErrNotCertified) {
		t.Fatalf("want the resumed handle's promotion refused with ErrNotCertified, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.Promoted{}.EventType()); got != 0 {
		t.Fatalf("want no promotion recorded through the uncertified handle, got %d", got)
	}

	// The certifying run itself promotes.
	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("promoting from the certifying run: %v", err)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

// TestPromote_CertificateDiesWithTheRun pins that a certificate from a
// stopped run is never reused: the same handle that certified catch-up loses
// its license the moment its processor exits, even though the persisted
// phase is still PhaseCaughtUp.
func TestPromote_CertificateDiesWithTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("certificate dies with the run")
	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping the run: %v", err)
	}

	if got := r.State().Attempt.Phase; got != lifecycle.PhaseCaughtUp {
		t.Fatalf("want the persisted phase still %s, got %s", lifecycle.PhaseCaughtUp, got)
	}

	if err := r.Promote(t.Context()); !errors.Is(err, lifecycle.ErrNotCertified) {
		t.Fatalf("want promotion refused after the certifying processor exited, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.Promoted{}.EventType()); got != 0 {
		t.Errorf("want no promotion recorded from the dead certificate, got %d", got)
	}
}

// TestRun_DisplacedBuilderWindsDown pins displacement through an attested
// takeover: a second run takes the standing claim over, and the first
// builder's reconcile loop observes the recorded runner change, stops its
// processor, and surfaces ErrRunnerDisplaced — with its certification
// revoked for good, so the displaced handle cannot promote what it no
// longer builds.
func TestRun_DisplacedBuilderWindsDown(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r1 := h.begin("will be displaced")
	_, done1 := runAsync(t, r1)
	waitPhase(t, r1, lifecycle.PhaseCaughtUp)

	r2, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming the second runner: %v", err)
	}

	// The incumbent's claim is standing, so the second runner takes it over
	// with an explicit attestation; the incumbent then observes the claim
	// and winds itself down.
	ctx2, cancel2 := context.WithCancel(t.Context())
	t.Cleanup(cancel2)

	done2 := make(chan error, 1)

	go func() {
		done2 <- r2.Run(ctx2, lifecycle.WithTakeover("op", "displacing the incumbent"))
	}()

	// The first builder observes the second runner's claim and winds down.
	if err := waitDone(t, done1); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Fatalf("want the displaced builder to surface ErrRunnerDisplaced, got %v", err)
	}

	// Sticky revocation: the displaced handle's certificate is gone.
	if err := r1.Promote(t.Context()); !errors.Is(err, lifecycle.ErrNotCertified) {
		t.Fatalf("want the displaced handle's promotion refused with ErrNotCertified, got %v", err)
	}

	// The claimant re-certifies against the current head and promotes.
	waitFor(t, func() bool {
		return countEventsOfType(t, h.events, lifecycle.CaughtUp{}.EventType()) == 2
	})

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting from the claimant: %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RunnerClaimed{}.EventType()); got != 2 {
		t.Errorf("want one claim per run (2 total), got %d", got)
	}

	cancel2()

	if err := waitDone(t, done2); !errors.Is(err, context.Canceled) {
		t.Errorf("want the claimant's tailing run to report cancellation, got %v", err)
	}
}

// TestRun_ClaimSaveFailureStartsNothing pins the first claim outcome: a
// pre-append failure recording the claim refuses the Run before any
// processor exists, and nothing is durable.
func TestRun_ClaimSaveFailureStartsNothing(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &refusingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer)
	h.appendDomain(3)

	r := h.begin("claim save refused")

	writer.armFailure()

	if err := r.Run(t.Context()); err == nil {
		t.Fatal("want the refused claim save to fail the run, got nil")
	}

	if got := countEventsOfType(t, events, lifecycle.RunnerClaimed{}.EventType()); got != 0 {
		t.Errorf("want no claim durable after the refused save, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.BuildStarted{}.EventType()); got != 0 {
		t.Errorf("want no start durable after the refused save, got %d", got)
	}
}

// TestRun_UnobservedClaimStartsWhenWon pins the second claim outcome: a
// claim that is durable but unobserved is reloaded, and the run proceeds
// because this exact runner won it — without re-appending the claim.
func TestRun_UnobservedClaimStartsWhenWon(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &truncatingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer)
	h.appendDomain(3)

	r := h.begin("unobserved claim, won")

	writer.armFailure()

	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	if got := countEventsOfType(t, events, lifecycle.RunnerClaimed{}.EventType()); got != 1 {
		t.Errorf("want exactly the one durable claim, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.BuildStarted{}.EventType()); got != 1 {
		t.Errorf("want exactly one durable start, got %d", got)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

// TestRun_UnobservedClaimRefusedWhenSuperseded pins the third claim outcome:
// a durable-but-unobserved claim that was superseded before the reload
// observes it refuses the Run with ErrRunnerDisplaced — the claim is in the
// stream, but this runner did not end up the recorded claimant, so it must
// not start a processor. Reconciliation is effectively disabled, so the
// refusal can only come from the reload check itself, before any processor
// exists — not from the running loop noticing the displacement later.
func TestRun_UnobservedClaimRefusedWhenSuperseded(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &truncatingWriter{Store: events}

	authority := &interceptingEventStore{Store: writer}
	h := buildHarnessWithLifecycleEvents(t, events, authority, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)
	h.model.armGate()

	r1 := h.begin("will crash mid-build")
	cancel1, done1 := runAsync(t, r1)
	waitPhase(t, r1, lifecycle.PhaseBuilding)
	cancel1()

	if err := waitDone(t, done1); !errors.Is(err, context.Canceled) {
		t.Fatalf("crashing the first run: %v", err)
	}

	h.model.releaseGate()

	resumed, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	// The resumed run's claim append is truncated — durable but unobserved —
	// and a competing claim lands before the recovery reload observes it:
	// the entry refresh consumes the first authority read, so skipping one
	// read lands the interception on the recovery reload.
	attemptID := resumed.State().Attempt.ID

	writer.armFailure()
	authority.armHydrateBefore(1, func(ctx context.Context) error {
		competing, err := json.Marshal(lifecycle.RunnerClaimed{
			Attempt:  attemptID,
			Runner:   uuid.Must(uuid.NewV4()),
			Takeover: lifecycle.RunnerTakeover{Actor: "op", Reason: "competing takeover"},
			At:       time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC),
		})
		if err != nil {
			return err
		}

		_, err = events.AppendStream(ctx, ordersLifecycleStreamID(), []*eventstore.WritableEvent{{
			Type:            lifecycle.RunnerClaimed{}.EventType(),
			Data:            competing,
			DataContentType: "application/json",
		}}, eventstore.AppendStreamOptions{})

		return err
	})

	_, done := runAsync(t, resumed)

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Fatalf("want the superseded claim to refuse the run with ErrRunnerDisplaced, got %v", err)
	}

	// Both claims are durable history — the refused runner appended nothing
	// further and started nothing.
	if got := countEventsOfType(t, events, lifecycle.RunnerClaimed{}.EventType()); got != 3 {
		t.Errorf("want the first run's claim plus both racing claims durable (3 total), got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.BuildStarted{}.EventType()); got != 1 {
		t.Errorf("want only the first run's start durable, got %d", got)
	}
}

// TestRun_ClaimRecoveryFailurePreservesEventsAppended pins the recovery
// error shape: when the claim is durable but unobserved and the fresh
// reload itself fails, the returned error must carry both the reload
// failure and ErrEventsAppended — hiding the durable claim would let the
// caller believe nothing changed ownership.
func TestRun_ClaimRecoveryFailurePreservesEventsAppended(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &truncatingWriter{Store: events}

	authority := &interceptingEventStore{Store: writer}
	h := buildHarnessWithLifecycleEvents(t, events, authority)
	h.appendDomain(3)

	r := h.begin("recovery load will fail")

	errRecovery := errors.New("recovery load refused")

	// The entry refresh consumes the first authority read; the interception
	// skips it and fails the recovery reload.
	writer.armFailure()
	authority.armHydrateIntercept(1, func(context.Context) error { return errRecovery })

	runErr := r.Run(t.Context())

	if !errors.Is(runErr, aggregatestore.ErrEventsAppended) {
		t.Errorf("want the failed recovery to keep carrying ErrEventsAppended, got %v", runErr)
	}

	if !errors.Is(runErr, errRecovery) {
		t.Errorf("want the reload failure carried alongside it, got %v", runErr)
	}
}

// TestRun_UnobservedClaimRecoversFromMisappliedSave pins the fresh-reload
// recovery contract: the claim save fails mid-application — the claim
// applied, the start left queued and unapplied — so the uncertain aggregate
// holds partially advanced state no incremental refresh repairs and a queued
// event a later save would duplicate. Recovery must discard it and load
// fresh, after which the run proceeds normally and each transition is
// durable exactly once.
func TestRun_UnobservedClaimRecoversFromMisappliedSave(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &misreportingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer)
	h.appendDomain(3)

	r := h.begin("misapplied claim save")

	writer.armFailure()

	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	if got := countEventsOfType(t, events, lifecycle.RunnerClaimed{}.EventType()); got != 1 {
		t.Errorf("want exactly the one durable claim, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.BuildStarted{}.EventType()); got != 1 {
		t.Errorf("want the queued start never replayed (1 durable), got %d", got)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

// TestRun_LostCatchUpToCompetingClaimIsDisplacement pins lost-append
// classification on the catch-up path: when a competing claim wins the
// stream ahead of this run's CaughtUp, Run surfaces ErrRunnerDisplaced —
// the documented displacement contract — not the raw version mismatch.
// Reconciliation is effectively disabled, so only the classification of the
// lost append can type the verdict.
func TestRun_LostCatchUpToCompetingClaimIsDisplacement(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("will lose catch-up to a claim")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	writer.armRaceAfter(0, writableLifecycleEvent(t, lifecycle.RunnerClaimed{
		Attempt:  r.State().Attempt.ID,
		Runner:   uuid.Must(uuid.NewV4()),
		Takeover: lifecycle.RunnerTakeover{Actor: "op", Reason: "competing takeover"},
		At:       time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC),
	}))
	h.model.releaseGate()

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Errorf("want the lost catch-up classified as displacement, got %v", err)
	}
}

// TestRun_LostCatchUpToClaimThenAbandonIsDisplacement pins the verdict to
// the exact event that defeated the append: a competing claim wins the slot
// and its claimant abandons immediately after, so a classification read from
// the stream's head would see only the vacated slot and report a clean
// wind-down — or displacement, depending on when it looked. The defeat was
// the claim; the verdict is displacement, deterministically.
func TestRun_LostCatchUpToClaimThenAbandonIsDisplacement(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("will lose to a claim that then abandons")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	writer.armRaceAfter(0,
		writableLifecycleEvent(t, lifecycle.RunnerClaimed{
			Attempt:  r.State().Attempt.ID,
			Runner:   uuid.Must(uuid.NewV4()),
			Takeover: lifecycle.RunnerTakeover{Actor: "op", Reason: "competing takeover"},
			At:       time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC),
		}),
		writableLifecycleEvent(t, lifecycle.Abandoned{Cause: "the winning claimant gave up"}),
	)
	h.model.releaseGate()

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Errorf("want the defeat classified from the claim that won the slot, not the abandonment after it, got %v", err)
	}
}

// TestRun_LostCatchUpDisplacementSurvivesReconciledEnd pins verdict
// precedence between the two observers: a reconcile tick already holding a
// read of the terminal head — the abandonment that followed the defeating
// claim — completes after the CaughtUp loss and records a clean terminal
// stop before classification reads the defeat at its slot. The exact defeat
// governs: the displacement verdict must upgrade the clean stop, so the
// same history reports ErrRunnerDisplaced no matter which observer landed
// first. One tick is parked as a barrier over the pre-defeat fold, the next
// parks holding the terminal head, and the classification read is parked
// until the reconciled end is provably recorded.
func TestRun_LostCatchUpDisplacementSurvivesReconciledEnd(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	authority := &interceptingEventStore{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, authority)
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("claim then abandon, reconciled first")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	// Barrier: park the next reconcile tick over the pre-defeat fold, so no
	// unparked tick can observe the terminal events below early and end the
	// run before its CaughtUp ever contends.
	release1 := make(chan struct{})
	entered1 := authority.armHydrateAfter(func(ctx context.Context) error {
		select {
		case <-release1:
		case <-ctx.Done():
		}

		return nil
	})

	select {
	case <-entered1:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the barrier tick to park")
	}

	// The competing claim and its claimant's abandonment land while the
	// barrier holds; the tick after the barrier reads this terminal head and
	// parks holding it — released only when the lost catch-up's wind-down
	// cancels the run, which is exactly the in-flight-read race.
	appendRawLifecycleEvent(t, events, lifecycle.RunnerClaimed{
		Attempt:  r.State().Attempt.ID,
		Runner:   uuid.Must(uuid.NewV4()),
		Takeover: lifecycle.RunnerTakeover{Actor: "op", Reason: "competing takeover"},
		At:       time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC),
	})
	appendRawLifecycleEvent(t, events, lifecycle.Abandoned{Cause: "the winning claimant gave up"})

	entered2 := authority.armHydrateAfter(func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})

	release := make(chan struct{})
	classifying := authority.armVersionedHydrateBefore(func(ctx context.Context) error {
		select {
		case <-release:
		case <-ctx.Done():
		}

		return nil
	})

	close(release1)

	select {
	case <-entered2:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the terminal-head tick to park")
	}

	// The build catches up and its CaughtUp loses to the claim; the parked
	// terminal-head tick completes on the wind-down's cancellation and
	// records the clean stop while classification is parked at its read.
	h.model.releaseGate()

	select {
	case <-classifying:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the classification read to park")
	}

	waitFor(t, func() bool { return r.State().Attempt.Phase == lifecycle.PhaseNone })

	close(release)

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Errorf("want the exact displacement to upgrade the reconciled clean stop, got %v", err)
	}
}

// TestRun_InitialClaimLostToClaimThenAbandonIsDisplacement pins the claim
// site's defeat classification to the exact slot: the competing claim wins
// the slot and its claimant abandons immediately after, so a head read
// would see only the vacated slot and report a clean wind-down. The defeat
// was the claim; the verdict is displacement, and the verified slot fold —
// the competitor's claim — is installed on the handle.
func TestRun_InitialClaimLostToClaimThenAbandonIsDisplacement(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)

	r := h.begin("claim lost to a claim that then abandons")

	competitor := uuid.Must(uuid.NewV4())

	writer.armRaceAfter(0,
		writableLifecycleEvent(t, lifecycle.RunnerClaimed{
			Attempt: r.State().Attempt.ID,
			Runner:  competitor,
			At:      time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC),
		}),
		writableLifecycleEvent(t, lifecycle.Abandoned{Cause: "the winning claimant gave up"}),
	)

	_, done := runAsync(t, r)

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Fatalf("want the lost claim classified from the claim that won the slot, not the abandonment after it, got %v", err)
	}

	if got := r.State().Attempt.Runner; got != competitor {
		t.Errorf("want the verified slot fold installed — the competitor as recorded claimant — got %s", got)
	}

	if got := countEventsOfType(t, events, lifecycle.BuildStarted{}.EventType()); got != 0 {
		t.Errorf("want nothing started by the defeated claimant, got %d", got)
	}
}

// TestRun_ProcessorDeathDuringLostCatchUpSurfacesItsError pins the catch-up
// append-failure path's exit attribution: the processor dies on its own
// while the CaughtUp append is parked — its return committed before the
// wind-down stops anything — and the append then loses its slot to a
// verdict-neutral raw CaughtUp. Two genuinely independent failures: the
// processor's own result must surface, joined with the unclassifiable loss,
// not be drained and discarded.
func TestRun_ProcessorDeathDuringLostCatchUpSurfacesItsError(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}
	parking := &parkingWriter{Store: writer}

	h := buildHarnessWithLifecycleEvents(t, events, parking, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)

	r := h.begin("processor dies during the lost catch-up")

	// The claim append passes; the CaughtUp append parks with the handle
	// lock held.
	entered, gate := parking.armAppendGateAfter(1)

	_, done := runAsync(t, r)

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the catch-up save to park")
	}

	// With the append parked, the processor dies on its own: its return is
	// committed — and has claimed its exit order — before any stop, proven
	// by the return observation rather than the in-handler signal, which
	// closes before the processor's Run returns and orders nothing.
	h.model.armHandleFailure()
	h.appendDomain(1)

	select {
	case <-h.model.handleFailed():
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor to fail")
	}

	select {
	case <-lifecycle.ProcessorReturnedForTest(r):
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor's return")
	}

	// The parked append loses its slot to a verdict-neutral competitor:
	// same attempt, same claimant, so classification records nothing.
	writer.armRaceAfter(0, writableLifecycleEvent(t, lifecycle.CaughtUp{
		Position: 4,
		At:       time.Date(2026, 8, 18, 13, 30, 0, 0, time.UTC),
	}))

	close(gate)

	runErr := waitDone(t, done)

	if runErr == nil || !strings.Contains(runErr.Error(), "handler failure") {
		t.Fatalf("want the processor's own failure surfaced, got %v", runErr)
	}

	if !errors.Is(runErr, eventstore.StreamVersionMismatchError{}) {
		t.Errorf("want the independent unclassifiable loss joined alongside the death, got %v", runErr)
	}
}

// TestRun_LateCancellationKeepsAnEarlierIndependentResult pins attribution's
// provenance against relabeling: the processor returns a cancellation-shaped
// result of its own while the run's context is live, and only then is the run
// canceled. The cancellation state that matters is the one the return
// happened under — captured with the return's exit-order claim — so the later
// cancellation must not subsume the result as the run's own wind-down: both
// the processor's result and the independent append loss survive. The parked
// append holds the exit publication open, so the cancellation is guaranteed
// to land between the return and the wind-down that attributes it.
func TestRun_LateCancellationKeepsAnEarlierIndependentResult(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}
	parking := &parkingWriter{Store: writer}

	h := buildHarnessWithLifecycleEvents(t, events, parking, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)

	r := h.begin("canceled after an independent cancellation-shaped death")

	// The claim append passes; the CaughtUp append parks with the handle
	// lock held.
	entered, gate := parking.armAppendGateAfter(1)

	cancel, done := runAsync(t, r)

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the catch-up save to park")
	}

	// With the append parked, the processor dies on its own — with a result
	// that is nothing but cancellation in shape, under a context that is
	// still alive. Its return claims the exit order before the cancellation
	// below can be observed by anything.
	h.model.armHandleFailureWith(fmt.Errorf("simulated shard shutdown: %w", context.Canceled))
	h.appendDomain(1)

	select {
	case <-h.model.handleFailed():
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor to fail")
	}

	select {
	case <-lifecycle.ProcessorReturnedForTest(r):
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor's return")
	}

	// The run is canceled only now, after the return committed: a wind-down
	// that samples the context late would misread the earlier result as the
	// run's own cancellation and discard it — and the append loss with it.
	cancel()

	// The parked append loses its slot to a verdict-neutral competitor:
	// same attempt, same claimant, so classification records nothing.
	writer.armRaceAfter(0, writableLifecycleEvent(t, lifecycle.CaughtUp{
		Position: 4,
		At:       time.Date(2026, 8, 18, 13, 30, 0, 0, time.UTC),
	}))

	close(gate)

	runErr := waitDone(t, done)

	if runErr == nil || !strings.Contains(runErr.Error(), "simulated shard shutdown") {
		t.Fatalf("want the processor's pre-cancellation result kept, got %v", runErr)
	}

	if !errors.Is(runErr, eventstore.StreamVersionMismatchError{}) {
		t.Errorf("want the independent append loss joined alongside it, got %v", runErr)
	}
}

// TestRun_OwnCancellationAtTheReturnSubsumesTheLostAppend pins the other side
// of return-time provenance: when the run is canceled first and the processor
// returns nothing but that cancellation, the documented context error is the
// whole story — the append loss the wind-down races into is downstream of the
// same cancellation and must not ride along as an independent failure.
func TestRun_OwnCancellationAtTheReturnSubsumesTheLostAppend(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}
	parking := &parkingWriter{Store: writer}

	h := buildHarnessWithLifecycleEvents(t, events, parking, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)

	r := h.begin("canceled before the processor returned")

	// The claim append passes; the CaughtUp append parks with the handle
	// lock held.
	entered, gate := parking.armAppendGateAfter(1)

	cancel, done := runAsync(t, r)

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the catch-up save to park")
	}

	// The run is canceled while the append is parked: the tailing processor
	// observes it and returns the run's own cancellation, under the already-
	// dead context.
	cancel()

	select {
	case <-lifecycle.ProcessorReturnedForTest(r):
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor's return")
	}

	// The parked append loses its slot to a verdict-neutral competitor.
	writer.armRaceAfter(0, writableLifecycleEvent(t, lifecycle.CaughtUp{
		Position: 4,
		At:       time.Date(2026, 8, 18, 13, 30, 0, 0, time.UTC),
	}))

	close(gate)

	runErr := waitDone(t, done)

	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("want the run's own cancellation surfaced, got %v", runErr)
	}

	if errors.Is(runErr, eventstore.StreamVersionMismatchError{}) {
		t.Errorf("want the downstream append loss subsumed by the cancellation, got %v", runErr)
	}
}

// TestRun_CancellationAfterProcessorDeathKeepsItsError pins that a
// cancellation arriving after the processor died with a result of its own
// does not displace that result. The already-ready result wins the catch-up
// select here; the cancellation arm's equivalent protection — a first-
// returned result surviving a dead context — is pinned directly on the
// shared wind-down helper, whose both-ready select window no external
// synchronization can hold open.
func TestRun_CancellationAfterProcessorDeathKeepsItsError(t *testing.T) {
	t.Parallel()

	h := newHarness(t, lifecycle.WithReconcileInterval(time.Hour))
	h.model.armGate()
	h.appendDomain(3)

	r := h.begin("canceled after the processor died")

	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	// The gated handlers resume onto an armed failure: the processor dies
	// mid-catch-up, on its own, with the run's context still alive.
	h.model.armHandleFailure()
	h.model.releaseGate()

	select {
	case <-h.model.handleFailed():
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor to fail")
	}

	select {
	case <-lifecycle.ProcessorReturnedForTest(r):
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor's return")
	}

	// Both signals may now be ready at the catch-up select; the processor's
	// own result must surface either way.
	cancel()

	runErr := waitDone(t, done)

	if runErr == nil || !strings.Contains(runErr.Error(), "handler failure") {
		t.Fatalf("want the dead processor's own result kept through the cancellation, got %v", runErr)
	}
}

// TestRun_UnobservedCatchUpSurfacesEventsAppended pins the classification
// gate's other side: a CaughtUp append that is durable but unobserved has no
// foreign winner — the event at the contended slot is this run's own — so
// nothing is classified, and Run surfaces the raw stale-handle error
// carrying ErrEventsAppended rather than misreading its own append as a
// competitor's transition or a clean end.
func TestRun_UnobservedCatchUpSurfacesEventsAppended(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &truncatingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("unobserved catch-up")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	// The next lifecycle append is this run's own CaughtUp.
	writer.armFailure()
	h.model.releaseGate()

	runErr := waitDone(t, done)

	if !errors.Is(runErr, aggregatestore.ErrEventsAppended) {
		t.Fatalf("want the unobserved catch-up surfaced with ErrEventsAppended, got %v", runErr)
	}

	if errors.Is(runErr, lifecycle.ErrRunnerDisplaced) {
		t.Errorf("want no displacement verdict from the run's own durable append, got %v", runErr)
	}

	// Classification was skipped entirely, not run to a neutral verdict: a
	// classifier that hydrated the durable state would have installed the
	// caught-up fold on the handle.
	if got := r.State().Attempt.Phase; got != lifecycle.PhaseBuilding {
		t.Errorf("want the handle's view untouched by the unclassified loss, got %s", got)
	}

	if got := countEventsOfType(t, events, lifecycle.CaughtUp{}.EventType()); got != 1 {
		t.Errorf("want the catch-up durable exactly once, got %d", got)
	}
}

// TestRun_LostAutoPromotionToCompetingClaimIsDisplacement pins the same
// classification on the auto-promotion append: the competing claim lands
// after this run's fresh CaughtUp and ahead of its Promoted, and Run
// surfaces ErrRunnerDisplaced. Reconciliation is effectively disabled, so
// only the classification of the lost append can type the verdict.
func TestRun_LostAutoPromotionToCompetingClaimIsDisplacement(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithAutoPromote(true), lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("will lose auto-promotion to a claim")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	// Skip this run's own CaughtUp append; race the Promoted that follows.
	writer.armRaceAfter(1, writableLifecycleEvent(t, lifecycle.RunnerClaimed{
		Attempt:  r.State().Attempt.ID,
		Runner:   uuid.Must(uuid.NewV4()),
		Takeover: lifecycle.RunnerTakeover{Actor: "op", Reason: "competing takeover"},
		At:       time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC),
	}))
	h.model.releaseGate()

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Errorf("want the lost auto-promotion classified as displacement, got %v", err)
	}

	if got := countEventsOfType(t, events, lifecycle.Promoted{}.EventType()); got != 0 {
		t.Errorf("want no promotion recorded by the displaced runner, got %d", got)
	}
}

// TestRun_InitialClaimLostToCompetingClaimIsDisplacement pins defeat
// classification on the claim itself: a Run whose claim append loses the
// stream to a competing runner's claim is displacement — the documented
// contract — not a raw version mismatch, and the defeated claimant starts
// nothing.
func TestRun_InitialClaimLostToCompetingClaimIsDisplacement(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)

	r := h.begin("will lose the claim to a competitor")

	writer.armRaceAfter(0, writableLifecycleEvent(t, lifecycle.RunnerClaimed{
		Attempt: r.State().Attempt.ID,
		Runner:  uuid.Must(uuid.NewV4()),
		At:      time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC),
	}))

	_, done := runAsync(t, r)

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Fatalf("want the lost claim classified as displacement, got %v", err)
	}

	if got := countEventsOfType(t, events, lifecycle.BuildStarted{}.EventType()); got != 0 {
		t.Errorf("want nothing started by the defeated claimant, got %d", got)
	}
}

// TestRun_InitialClaimLostToEndedAttemptWindsDownNil pins the claim defeat's
// other verdict: losing the claim append to a transition that ended the
// attempt is a terminal state observed — Run returns nil, per its contract
// for terminal transitions recorded elsewhere — and the defeated claimant
// records and starts nothing.
func TestRun_InitialClaimLostToEndedAttemptWindsDownNil(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)

	r := h.begin("will lose the claim to an abandonment")

	writer.armRaceAfter(0, writableLifecycleEvent(t, lifecycle.Abandoned{Cause: "abandoned ahead of the claim"}))

	_, done := runAsync(t, r)

	if err := waitDone(t, done); err != nil {
		t.Fatalf("want the claim lost to the attempt's end to wind down clean, got %v", err)
	}

	// The verified slot fold installs: the handle observed the end it
	// reported, rather than still showing the pre-defeat attempt in flight.
	if got := r.State().Attempt.Phase; got != lifecycle.PhaseNone {
		t.Errorf("want the handle's state to reflect the observed end, got %s", got)
	}

	if got := countEventsOfType(t, events, lifecycle.RunnerClaimed{}.EventType()); got != 0 {
		t.Errorf("want no claim recorded by the defeated run, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.BuildStarted{}.EventType()); got != 0 {
		t.Errorf("want nothing started by the defeated run, got %d", got)
	}
}

// TestAbandon_CompletesWhileReconcileLoadBlocked pins the reconcile loop's
// lock discipline: its fresh load runs outside the handle's lock, so a load
// that ends only when the processor is canceled cannot block the very
// command that performs the cancellation — with the load parked, Abandon
// must still complete promptly and wind the run down clean.
func TestAbandon_CompletesWhileReconcileLoadBlocked(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	authority := &interceptingEventStore{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, authority)
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("abandoned under a parked reconcile load")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	entered := authority.armHydrateIntercept(0, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the reconcile load to start")
	}

	abandoned := make(chan error, 1)

	go func() { abandoned <- r.Abandon(t.Context(), "abandoned mid-park") }()

	if err := waitDone(t, abandoned); err != nil {
		t.Fatalf("want Abandon to complete while the reconcile load is parked, got %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to wind down clean after the abandonment, got %v", err)
	}

	if got := r.State().Attempt.Phase; got != lifecycle.PhaseNone {
		t.Errorf("want the attempt slot vacated, got %s", got)
	}
}

// TestReconcile_StaleReadDoesNotOverwriteCertifiedState pins the reconcile
// loop's currentness guard: a reconcile read begun before this run's
// CaughtUp can return after the catch-up was certified, and installing that
// older fold would regress the handle's state and refuse a healthy
// promotion. The parked read here releases only after certification, with
// the following tick parked in turn so the promotion runs strictly after the
// stale view's verdict; the promotion must succeed.
func TestReconcile_StaleReadDoesNotOverwriteCertifiedState(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	authority := &interceptingEventStore{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, authority)
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("stale reconcile read")
	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	// The next reconcile tick reads the pre-catch-up fold, then parks before
	// returning it.
	release1 := make(chan struct{})
	entered1 := authority.armHydrateAfter(func(ctx context.Context) error {
		select {
		case <-release1:
		case <-ctx.Done():
		}

		return nil
	})

	select {
	case <-entered1:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the reconcile read to park")
	}

	// Pre-arm the tick after it, so the promotion below runs strictly
	// between the stale view's verdict and any fresher read.
	release2 := make(chan struct{})
	entered2 := authority.armHydrateAfter(func(ctx context.Context) error {
		select {
		case <-release2:
		case <-ctx.Done():
		}

		return nil
	})

	// The build catches up and certifies while the parked read holds its
	// stale fold.
	h.model.releaseGate()
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	close(release1)

	select {
	case <-entered2:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the tick after the stale read")
	}

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("want the certified promotion to succeed after the stale read returned, got %v", err)
	}

	close(release2)
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the promoted run to tail until canceled, got %v", err)
	}
}

// TestRun_ProcessorDeathAtCaughtUpSurfacesItsError pins auto-promotion's
// exit awareness: the processor dies on its own while the promotion append
// is parked, and the promotion then fails for a reason the defeat
// classifier leaves raw — a competing raw CaughtUp wins its slot, changing
// neither attempt nor claimant. The refusal is not the run's story; the
// death is, and its result queues strictly behind the held handle lock, so
// only the exit-aware failure path can surface it.
func TestRun_ProcessorDeathAtCaughtUpSurfacesItsError(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &racingWriter{Store: events}
	parking := &parkingWriter{Store: writer}

	h := buildHarnessWithLifecycleEvents(t, events, parking, lifecycle.WithAutoPromote(true), lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)

	r := h.begin("processor dies at auto-promotion")

	// Appends after arming: the claim and the CaughtUp certification pass;
	// the Promoted append parks with the handle lock held.
	entered, gate := parking.armAppendGateAfter(2)

	_, done := runAsync(t, r)

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the promotion save to park")
	}

	// With the promotion parked, the processor dies on its own. Its exit
	// publication queues behind the held lock, and its result is sent only
	// after that publication — the failure path below must wait for both.
	h.model.armHandleFailure()
	h.appendDomain(1)

	select {
	case <-h.model.handleFailed():
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor to fail")
	}

	// The in-handler signal precedes the processor's return; the return
	// observation orders the release strictly after the return has claimed
	// its exit order, so attribution provably sees a first-returned result.
	select {
	case <-lifecycle.ProcessorReturnedForTest(r):
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor's return")
	}

	// The parked promotion loses its append to a competing raw CaughtUp:
	// same attempt, same claimant, so the classifier records no verdict and
	// the refusal alone would surface as a bare version conflict.
	writer.armRaceAfter(0, writableLifecycleEvent(t, lifecycle.CaughtUp{
		Position: 4,
		At:       time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	}))

	close(gate)

	runErr := waitDone(t, done)

	if runErr == nil || !strings.Contains(runErr.Error(), "handler failure") {
		t.Fatalf("want the processor's own failure surfaced, got %v", runErr)
	}

	if !errors.Is(runErr, eventstore.StreamVersionMismatchError{}) {
		t.Errorf("want the independent unclassifiable refusal joined alongside the death, got %v", runErr)
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

	h := buildHarnessWithLifecycleEvents(t, events, writer)

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
// records a fresh runner claim and completes the build from the checkpoint.
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

	cancelResumed, doneResumed := runAsync(t, resumed)
	waitPhase(t, resumed, lifecycle.PhaseCaughtUp)

	v1 := projection.ID{Name: "orders", Version: 1}
	waitFor(t, func() bool { return len(h.model.table(v1)) == 3 })

	// The stream records the full story: initiated, the first run's claim,
	// start, and wind-down release, the resumed run's transparent claim, and
	// the catch-up.
	replay, err := lifecycle.NewStore(h.events)
	if err != nil {
		t.Fatalf("creating the replay store: %v", err)
	}

	loaded, err := replay.Load(t.Context(), lifecycle.StreamUUID("orders"), nil)
	if err != nil {
		t.Fatalf("loading lifecycle aggregate: %v", err)
	}

	if got := loaded.Version(); got != 6 {
		t.Errorf("want 6 recorded transitions (initiated, claimed, started, released, claimed again, caught up), got %d", got)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RunnerClaimed{}.EventType()); got != 2 {
		t.Errorf("want one claim per run (2 total), got %d", got)
	}

	cancelResumed()

	if err := waitDone(t, doneResumed); !errors.Is(err, context.Canceled) {
		t.Errorf("want the resumed run to report cancellation, got %v", err)
	}
}

// TestCompetingOrchestrators pins the coordination story: two handles racing
// to end the same attempt are arbitrated by the lifecycle stream, and the
// loser's command refolds the events first, observing the winner's
// transition and refusing before it can append anything.
func TestCompetingOrchestrators(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("competing operators")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	first, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming first handle: %v", err)
	}

	second, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming second handle: %v", err)
	}

	if err := first.Abandon(t.Context(), "the winning abandon"); err != nil {
		t.Fatalf("abandoning from the first handle: %v", err)
	}

	err = second.Abandon(t.Context(), "the losing abandon")
	if err == nil || !strings.Contains(err.Error(), "no rebuild in flight") {
		t.Fatalf("want the losing abandonment refused on the vacated slot, got %v", err)
	}

	state, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("reloading after losing: %v", err)
	}

	if got := state.Attempt.Phase; got != lifecycle.PhaseNone {
		t.Errorf("want the loser to observe the vacated slot after reloading, got %s", got)
	}

	if got := countEventsOfType(t, h.events, lifecycle.Abandoned{}.EventType()); got != 1 {
		t.Errorf("want exactly the winning abandonment recorded, got %d", got)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want the builder to wind itself down with nil, got %v", err)
	}
}

// TestSeparateLifecycleStore runs the lifecycle aggregates in their own event
// store, with the effect worker and a StreamRouter folding it: domain and
// infrastructure streams never interleave.
func TestSeparateLifecycleStore(t *testing.T) {
	t.Parallel()

	domainEvents := newEventStore(t)
	lifecycleEvents := newEventStore(t)

	checkpoints := cpmemory.NewCheckpointStore()
	router := lifecycle.NewMemoryRouter()
	model := newReadModel()

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:          domainEvents,
		Checkpoints:     checkpoints,
		Handler:         model.handler,
		LifecycleEvents: lifecycleEvents,
	},
		lifecycle.WithAutoPromote(true),
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
		lifecycle.WithReconcileInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	worker, err := lifecycle.NewWorker(lifecycleEvents,
		lifecycle.WithCutoverSetter(router),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating cutover worker: %v", err)
	}

	workerErr := make(chan error, 1)

	go func() { workerErr <- worker.Run(t.Context()) }()

	t.Cleanup(func() {
		if err := <-workerErr; !errors.Is(err, context.Canceled) {
			t.Errorf("cutover worker exited unexpectedly: %v", err)
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

	cancel, done := runAsync(t, r)
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

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

func TestBegin_InvalidName(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if _, err := h.orchestrator.Begin(t.Context(), "Bad Name", "reason"); err == nil {
		t.Error("want an error for an invalid projection name, got nil")
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

	model := newReadModel()
	config := lifecycle.Config{
		Events:          events,
		Checkpoints:     cpmemory.NewCheckpointStore(),
		Handler:         model.handler,
		LifecycleEvents: events,
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
		Events:          h.events,
		Checkpoints:     h.checkpoints,
		Handler:         h.model.handler,
		LifecycleEvents: h.events,
	}

	for _, tt := range []struct {
		name   string
		mutate func(*lifecycle.Config)
	}{
		{"rejects a nil global reader", func(c *lifecycle.Config) { c.Events = nil }},
		{"rejects a nil checkpoint store", func(c *lifecycle.Config) { c.Checkpoints = nil }},
		{"rejects a nil handler factory", func(c *lifecycle.Config) { c.Handler = nil }},
		{"rejects a nil lifecycle event store", func(c *lifecycle.Config) { c.LifecycleEvents = nil }},
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

	// The control: the unmutated config constructs, so each rejection above
	// is attributable to its own mutation.
	t.Run("accepts a complete config", func(t *testing.T) {
		t.Parallel()

		if _, err := lifecycle.NewOrchestrator(valid); err != nil {
			t.Errorf("want the complete config accepted, got %v", err)
		}
	})
}

// ordersLifecycleStreamID is the typeid of the "orders" projection's
// lifecycle stream, for raw stream operations — appends, reads, truncation —
// performed outside the aggregate.
func ordersLifecycleStreamID() typeid.ID {
	return typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID("orders")}
}

// readCutoverRevisions decodes every cutover event in the store in global
// order, returning each recorded (Live, Revision) pair.
func readCutoverRevisions(t *testing.T, events *esmemory.EventStore) []lifecycle.Cutover {
	t.Helper()

	iter, err := events.ReadAll(t.Context(), eventstore.ReadAllOptions{})
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	all, err := eventstore.Collect(t.Context(), iter)
	if err != nil {
		t.Fatalf("collecting events: %v", err)
	}

	var cutovers []lifecycle.Cutover

	for _, event := range all {
		switch event.ID.Type {
		case lifecycle.Promoted{}.EventType():
			var promoted lifecycle.Promoted
			if err := json.Unmarshal(event.Data, &promoted); err != nil {
				t.Fatalf("decoding promoted event: %v", err)
			}

			cutovers = append(cutovers, lifecycle.Cutover{Live: promoted.Next, Revision: promoted.Revision})
		case lifecycle.RolledBack{}.EventType():
			var rolledBack lifecycle.RolledBack
			if err := json.Unmarshal(event.Data, &rolledBack); err != nil {
				t.Fatalf("decoding rolledback event: %v", err)
			}

			cutovers = append(cutovers, lifecycle.Cutover{Live: rolledBack.RevertedTo, Revision: rolledBack.Revision})
		}
	}

	return cutovers
}

// TestCutoverRevisions_StampAndFoldAcrossAttempts pins the revision as a
// domain fact end to end: each promotion and rollback records the fold's
// revision plus one under the arbitrating append — across attempts, with
// rollback keeping the counter monotonic rather than the version — and the
// worker converges the routing setter on the recorded pairs.
func TestCutoverRevisions_StampAndFoldAcrossAttempts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(2)

	v1 := h.promoteFirstVersion()

	r := h.begin("second build, to be rolled back")

	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	if err := r.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back v2: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Fatalf("want the rolled-back run to wind down nil, got %v", err)
	}

	// A third attempt after the rollback: v3 promotes at revision 4, so the
	// version and the revision diverge — a stamp aliased to the version
	// would poison the fold here.
	r3 := h.begin("third build after the rollback")

	cancel3, done3 := runAsync(t, r3)
	waitPhase(t, r3, lifecycle.PhaseCaughtUp)

	if err := r3.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v3: %v", err)
	}

	v2 := projection.ID{Name: "orders", Version: 2}
	v3 := projection.ID{Name: "orders", Version: 3}

	want := []lifecycle.Cutover{
		{Live: v1, Revision: 1},
		{Live: v2, Revision: 2},
		{Live: v1, Revision: 3},
		{Live: v3, Revision: 4},
	}

	got := readCutoverRevisions(t, h.events)
	if len(got) != len(want) {
		t.Fatalf("want cutovers %v recorded, got %v", want, got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want cutovers %v recorded, got %v", want, got)
		}
	}

	state, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("reloading state: %v", err)
	}

	if state.Live != v3 || state.CutoverRevision != 4 {
		t.Errorf("want the fold at (live %s, revision 4), got (%s, %d)", v3, state.Live, state.CutoverRevision)
	}

	waitFor(t, func() bool {
		applied, err := h.router.AppliedCutover(t.Context(), "orders")
		return err == nil && applied == (lifecycle.Cutover{Live: v3, Revision: 4})
	})

	cancel3()

	if err := waitDone(t, done3); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the tailing third run to end on its cancellation, got %v", err)
	}
}

// completeFirstBuild drives the orchestrator's "orders" projection through a
// complete first build: begun, run to catch-up, promoted, and completed, so
// v1 is live with the attempt slot vacant.
func completeFirstBuild(t *testing.T, orchestrator *lifecycle.Orchestrator) {
	t.Helper()

	r, err := orchestrator.Begin(t.Context(), "orders", "first build")
	if err != nil {
		t.Fatalf("beginning first build: %v", err)
	}

	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("promoting first build: %v", err)
	}

	if err := r.Retire(t.Context()); err != nil {
		t.Fatalf("completing first build: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Fatalf("first build run: %v", err)
	}
}

// TestSetRetirementPolicy_RefusesAPoisonedStream pins the policy
// transition's fold validation: a lifecycle whose event-only fold is
// poisoned must refuse the transition with ErrInvalidState instead of
// recording a generation over an unusable history.
func TestSetRetirementPolicy_RefusesAPoisonedStream(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_ = h.begin("to be poisoned")

	// A second admission over the occupied slot poisons the fold.
	appendRawLifecycleEvent(t, h.events, lifecycle.RebuildInitiated{
		Attempt: uuid.Must(uuid.NewV4()),
		Target:  projection.ID{Name: "orders", Version: 2},
		Reason:  "poisoning admission",
		At:      time.Now(),
	})

	err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"router"}, Actor: "op", Reason: "over poison",
	})
	if !errors.Is(err, lifecycle.ErrInvalidState) {
		t.Fatalf("want the transition refused with ErrInvalidState, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetirementPolicySet{}.EventType()); got != 0 {
		t.Errorf("want no transition recorded over the poisoned fold, got %d", got)
	}
}

// TestResume_RefusesCoveredUpSequences pins the fold's cover-up poisons
// end to end: each sequence finishes in a shape that validates without the
// mark — the terminal event vacates the slot that held the evidence — so
// only the mark left at the moment of observation can refuse the history.
func TestResume_RefusesCoveredUpSequences(t *testing.T) {
	t.Parallel()

	v1 := projection.ID{Name: "orders", Version: 1}
	at := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)

	// promotedV1 lands a well-formed first-version history through Promoted:
	// admitted, claimed, built, caught up, and promoted, all consistent with
	// a projection that had never been live.
	promotedV1 := func(t *testing.T, events *esmemory.EventStore) {
		t.Helper()

		attempt := uuid.Must(uuid.NewV4())

		appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{
			Attempt: attempt,
			Target:  v1,
			Reason:  "first build",
			At:      at,
		})
		appendRawLifecycleEvent(t, events, lifecycle.RunnerClaimed{Attempt: attempt, Runner: uuid.Must(uuid.NewV4()), At: at})
		appendRawLifecycleEvent(t, events, lifecycle.BuildStarted{})
		appendRawLifecycleEvent(t, events, lifecycle.CaughtUp{Position: 1, At: at})
		appendRawLifecycleEvent(t, events, lifecycle.Promoted{Next: v1, Revision: 1, At: at})
	}

	t.Run("nil-attempt admission then abandoned", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), newReadModel().handler)

		// The admission carries no attempt ID; the abandonment vacates the
		// slot, leaving nothing a final-state validator could object to.
		appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{Target: v1, Reason: "no identity", At: at})
		appendRawLifecycleEvent(t, events, lifecycle.Abandoned{Cause: "covering the tracks"})

		if _, err := orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want the nil-attempt admission refused with ErrInvalidState, got %v", err)
		}
	})

	t.Run("first-version rollback", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), newReadModel().handler)

		promotedV1(t, events)

		// No previous version exists, so RevertedTo's zero value equals the
		// attempt's zero Previous, and the rollback vacates the slot; every
		// other field is coherent, so only the dedicated arm can refuse it.
		appendRawLifecycleEvent(t, events, lifecycle.RolledBack{From: v1, Revision: 2, At: at})

		if _, err := orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want the first-version rollback refused with ErrInvalidState, got %v", err)
		}
	})

	t.Run("first-version retirement reservation", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), newReadModel().handler)

		promotedV1(t, events)

		// The reservation names nothing to retire — zero Retiring equals the
		// zero Previous — and the completion vacates the slot the invariant
		// would have been checked against.
		appendRawLifecycleEvent(t, events, lifecycle.RetireStarted{At: at})
		appendRawLifecycleEvent(t, events, lifecycle.PreviousRetired{})

		if _, err := orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want the first-version reservation refused with ErrInvalidState, got %v", err)
		}
	})

	t.Run("empty-target admission then abandonment", func(t *testing.T) {
		t.Parallel()

		events := newEventStore(t)
		orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), newReadModel().handler)

		// The first admission's target has no name, so it records none; the
		// abandonment vacates the slot, and a well-formed second admission
		// supplies the name — erasing every trace the malformed prefix left
		// in the final shape.
		appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{
			Attempt: uuid.Must(uuid.NewV4()),
			Target:  projection.ID{Version: 1},
			Reason:  "nameless target",
			At:      at,
		})
		appendRawLifecycleEvent(t, events, lifecycle.Abandoned{Cause: "covering the tracks"})
		appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{
			Attempt: uuid.Must(uuid.NewV4()),
			Target:  projection.ID{Name: "orders", Version: 2},
			Reason:  "supplies the name",
			At:      at,
		})
		appendRawLifecycleEvent(t, events, lifecycle.Abandoned{Cause: "covering the tracks again"})

		if _, err := orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
			t.Errorf("want the empty-target admission refused with ErrInvalidState, got %v", err)
		}
	})
}

// TestRun_ReconcileHydrationFailureIsTerminal pins the terminal contract for
// reconcile rehydration: any failure other than this run's own cancellation
// stops the processor and surfaces the cause. A fresh read either vouches
// for the whole lifecycle or for none of it — the retrying alternative would
// tail the processor forever over a stream that never reads back clean.
func TestRun_ReconcileHydrationFailureIsTerminal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("hydration failure mid-run")

	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	// One atomic append lands a decodable poisoning event and an undecodable
	// payload, so the next reconcile hydration sees both together: it folds
	// the first and fails on the second.
	poison, err := json.Marshal(lifecycle.RebuildInitiated{
		Attempt: uuid.Must(uuid.NewV4()),
		Target:  projection.ID{Name: "orders", Version: 9},
		Reason:  "displaces the running attempt",
		At:      time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshaling the poisoning event: %v", err)
	}

	if _, err := h.events.AppendStream(t.Context(), ordersLifecycleStreamID(), []*eventstore.WritableEvent{
		{Type: lifecycle.RebuildInitiated{}.EventType(), Data: poison, DataContentType: "application/json"},
		{Type: lifecycle.BuildStarted{}.EventType(), Data: []byte(`{`), DataContentType: "application/json"},
	}, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending the poisoned tail: %v", err)
	}

	runErr := waitDone(t, done)

	switch {
	case runErr == nil:
		t.Fatal("want the hydration failure to end the run, got nil")
	case errors.Is(runErr, context.Canceled):
		t.Fatalf("want the hydration failure surfaced, got cancellation: %v", runErr)
	}

	var hydrateErr aggregatestore.HydrateError
	if !errors.As(runErr, &hydrateErr) {
		t.Errorf("want the run's error to carry the hydration failure, got %v", runErr)
	}
}

// TestRun_ProcessorFailureSurfacesDespiteHeldReconcile pins the exit path's
// join discipline: the reconcile loop exits only on the processor context,
// so Run must cancel it before joining and taking the final status
// snapshot. Here the reconcile load ends only on cancellation; a join
// before canceling would wait on it forever, and an independent processor
// failure would hang the Run until its caller gave up.
func TestRun_ProcessorFailureSurfacesDespiteHeldReconcile(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	authority := &interceptingEventStore{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, authority)
	h.appendDomain(3)

	r := h.begin("processor failure under a held reconciliation")

	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	// The next reconcile load parks inside the store, releasing only when
	// the run's wind-down cancels it.
	entered := authority.armHydrateIntercept(0, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the reconcile hydration to start")
	}

	// With reconciliation parked, the processor fails on its own.
	h.model.armHandleFailure()
	h.appendDomain(1)

	runErr := waitDone(t, done)

	switch {
	case runErr == nil:
		t.Fatal("want the processor's own failure surfaced, got nil")
	case errors.Is(runErr, context.Canceled):
		t.Fatalf("want the processor's own failure surfaced, got cancellation: %v", runErr)
	}

	if !strings.Contains(runErr.Error(), "handler failure") {
		t.Errorf("want the handler failure as the cause, got %v", runErr)
	}
}

// TestRun_HydrationFailureNotLaunderedByCancellation pins the terminal
// contract's discrimination: a reconcile hydration failure is benign only
// when it IS this run's own cancellation. A real failure that merely races
// the wind-down must surface as the run's result, not vanish behind the
// context error.
func TestRun_HydrationFailureNotLaunderedByCancellation(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	authority := &interceptingEventStore{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, authority)
	h.appendDomain(3)

	r := h.begin("failure racing cancellation")

	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	// The hydration cancels the run itself, waits for the cancellation to
	// land, and then fails with a distinct error: the exact race the
	// discrimination must not launder.
	errWindDown := errors.New("store failure during wind-down")
	entered := authority.armHydrateIntercept(0, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()

		return errWindDown
	})

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the reconcile hydration to start")
	}

	if runErr := waitDone(t, done); !errors.Is(runErr, errWindDown) {
		t.Fatalf("want the hydration failure surfaced despite the cancellation, got %v", runErr)
	}
}

// TestRun_CancellationDuringCatchUpSurfacesRecordedFailure pins exit
// classification on the catch-up path: cancellation must not outrank a
// fail-closed cause recorded during the wind-down, which requires the
// cancellation arm to join reconciliation and classify instead of
// returning the bare context error.
func TestRun_CancellationDuringCatchUpSurfacesRecordedFailure(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	authority := &interceptingEventStore{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, authority)

	// The gate holds the build mid-replay, so the run is still catching up
	// when the failure and the cancellation race.
	h.model.armGate()
	h.appendDomain(3)

	r := h.begin("cancellation during catch-up")

	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	errWindDown := errors.New("store failure during wind-down")
	entered := authority.armHydrateIntercept(0, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()

		return errWindDown
	})

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the reconcile hydration to start")
	}

	if runErr := waitDone(t, done); !errors.Is(runErr, errWindDown) {
		t.Fatalf("want the recorded failure to win over the cancellation, got %v", runErr)
	}
}

// TestRun_JoinedCancellationDoesNotLaunderFailure pins the benign arm's
// exact scope: only a failure that is nothing but this run's cancellation
// is benign. A joined chain carrying an independent cause alongside the
// cancellation must stay terminal — errors.Is alone would find the
// cancellation somewhere in the tree and discard the store failure with it.
func TestRun_JoinedCancellationDoesNotLaunderFailure(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	authority := &interceptingEventStore{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, authority)
	h.appendDomain(3)

	r := h.begin("joined failure racing cancellation")

	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	errStoreFailure := errors.New("store failure joined to the cancellation")
	entered := authority.armHydrateIntercept(0, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()

		return errors.Join(ctx.Err(), errStoreFailure)
	})

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the reconcile hydration to start")
	}

	if runErr := waitDone(t, done); !errors.Is(runErr, errStoreFailure) {
		t.Fatalf("want the joined store failure surfaced, got %v", runErr)
	}
}

// TestRun_ProcessorFailureDuringCatchUpSurfacesDespiteHeldReconcile pins the
// exit discipline on the catch-up path's processor-exit arm, the same way
// the tailing variant pins the final exit: reconciliation is parked inside
// a load that ends only on cancellation, and the processor then fails on
// its own before ever catching up. The arm must cancel before joining and
// classifying, or the join would wait forever on the parked loop.
func TestRun_ProcessorFailureDuringCatchUpSurfacesDespiteHeldReconcile(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	authority := &interceptingEventStore{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, authority)

	// The gate holds the build mid-replay: the run never reaches catch-up.
	h.model.armGate()
	h.appendDomain(3)

	r := h.begin("processor failure while catching up")

	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseBuilding)

	entered := authority.armHydrateIntercept(0, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the reconcile hydration to start")
	}

	// With reconciliation parked, releasing the gate onto a failing handler
	// makes the processor exit on its own, still catching up.
	h.model.armHandleFailure()
	h.model.releaseGate()

	runErr := waitDone(t, done)

	switch {
	case runErr == nil:
		t.Fatal("want the processor's own failure surfaced, got nil")
	case errors.Is(runErr, context.Canceled):
		t.Fatalf("want the processor's own failure surfaced, got cancellation: %v", runErr)
	}

	if !strings.Contains(runErr.Error(), "handler failure") {
		t.Errorf("want the handler failure as the cause, got %v", runErr)
	}
}

//
// read model test double
//

// readModel is the versioned read-side: one "table" of handled global
// positions per projection version, with Teardown dropping a version's table.
type readModel struct {
	mu            sync.Mutex
	tables        map[projection.ID][]int64
	dropped       []projection.ID
	gate          chan struct{}
	failTeardown  bool
	failHandle    bool
	failHandleErr error
	failedOnce    sync.Once
	failed        chan struct{}
}

func newReadModel() *readModel {
	return &readModel{tables: map[projection.ID][]int64{}, failed: make(chan struct{})}
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

// hasTable reports whether the version's storage currently exists: created
// by its first handled event, removed by teardown.
func (m *readModel) hasTable(id projection.ID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.tables[id]

	return ok
}

// setTeardownFailure arms or disarms teardown failures.
func (m *readModel) setTeardownFailure(fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.failTeardown = fail
}

// handleFailed returns a channel closed the first time an armed handler
// failure is reported to a processor: from that point the processor's drain
// fails with the handler's error, regardless of any stop that follows.
func (m *readModel) handleFailed() <-chan struct{} {
	return m.failed
}

// armHandleFailure arms handler failures on domain events, so tests can make
// a running processor fail independently of the lifecycle.
func (m *readModel) armHandleFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.failHandle = true
}

// armHandleFailureWith arms handler failures that fail with the given error,
// so tests can shape the processor's own result — including one that merely
// looks like a cancellation the run never issued.
func (m *readModel) armHandleFailureWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.failHandle = true
	m.failHandleErr = err
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

	if h.model.failHandle {
		h.model.failedOnce.Do(func() { close(h.model.failed) })

		if h.model.failHandleErr != nil {
			return h.model.failHandleErr
		}

		return errors.New("handler failure")
	}

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

// interceptingEventStore delegates and lets tests hook the authority-level
// stream read every command entry except Promote's — which performs none —
// and every runtime verdict flows through: command entry refolds, the
// reconcile loop's fresh view, claim recovery, defeat classification, and
// retirement authority each hydrate through exactly one read of the
// lifecycle stream. A hook fires once, after a configured number of
// untouched calls: replacing the read, running ahead of it, or running
// after the inner read returned its frozen view but before the fold
// consumes it. Versioned hydrates — the defeat-classification read, the
// only one that bounds its version — arrive with a positive event count.
type interceptingEventStore struct {
	eventstore.Store

	mu             sync.Mutex
	replace        func(context.Context) error
	replaceEntered chan struct{}
	replaceSkip    int
	before         func(context.Context) error
	beforeSkip     int
	after          func(context.Context) error
	afterEntered   chan struct{}

	versionedBefore        func(context.Context) error
	versionedBeforeEntered chan struct{}
}

// armHydrateIntercept arms fn to run in place of the first authority read
// after skip untouched unversioned reads — the intercepted read fails with
// fn's result; the returned channel closes when it begins.
func (s *interceptingEventStore) armHydrateIntercept(skip int, fn func(context.Context) error) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.replace = fn
	s.replaceSkip = skip
	s.replaceEntered = make(chan struct{})

	return s.replaceEntered
}

// armHydrateBefore arms fn to run ahead of the first delegated authority
// read after skip untouched unversioned reads: that read then disarms the
// hook and proceeds for real.
func (s *interceptingEventStore) armHydrateBefore(skip int, fn func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.before = fn
	s.beforeSkip = skip
}

// armHydrateAfter arms fn to run after the next delegated Hydrate completes,
// before the call returns; the returned channel closes when fn begins — the
// inner hydrate's view is settled by then, so a parked fn holds exactly that
// view open.
func (s *interceptingEventStore) armHydrateAfter(fn func(context.Context) error) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.after = fn
	s.afterEntered = make(chan struct{})

	return s.afterEntered
}

// armVersionedHydrateBefore arms fn to run ahead of the next VERSIONED
// hydrate (ToVersion > 0) — the defeat-classification read, the only
// versioned read the handle performs — leaving the unversioned reconcile,
// entry, and recovery hydrates untouched; the returned channel closes when
// fn begins.
func (s *interceptingEventStore) armVersionedHydrateBefore(fn func(context.Context) error) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.versionedBefore = fn
	s.versionedBeforeEntered = make(chan struct{})

	return s.versionedBeforeEntered
}

func (s *interceptingEventStore) ReadStream(ctx context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	versioned := opts.Count > 0

	s.mu.Lock()

	var replace, before, after func(context.Context) error

	var replaceEntered, afterEntered chan struct{}

	if versioned && s.versionedBefore != nil {
		fn, entered := s.versionedBefore, s.versionedBeforeEntered
		s.versionedBefore, s.versionedBeforeEntered = nil, nil
		s.mu.Unlock()

		close(entered)

		if err := fn(ctx); err != nil {
			return nil, err
		}

		return s.Store.ReadStream(ctx, streamID, opts)
	}

	if s.replace != nil {
		if s.replaceSkip > 0 {
			s.replaceSkip--
		} else {
			replace, replaceEntered = s.replace, s.replaceEntered
			s.replace, s.replaceEntered = nil, nil
		}
	}

	if replace == nil && s.before != nil {
		if s.beforeSkip > 0 {
			s.beforeSkip--
		} else {
			before = s.before
			s.before = nil
		}
	}

	if replace == nil && s.after != nil {
		after, afterEntered = s.after, s.afterEntered
		s.after, s.afterEntered = nil, nil
	}
	s.mu.Unlock()

	if replace != nil {
		close(replaceEntered)

		if err := replace(ctx); err != nil {
			return nil, err
		}

		return s.Store.ReadStream(ctx, streamID, opts)
	}

	if before != nil {
		if err := before(ctx); err != nil {
			return nil, err
		}
	}

	iter, err := s.Store.ReadStream(ctx, streamID, opts)

	if after != nil {
		close(afterEntered)

		if afterErr := after(ctx); afterErr != nil && err == nil {
			return nil, afterErr
		}
	}

	return iter, err
}

// truncatingWriter delegates appends and, when armed, truncates the next
// append's result: the events are durable in the store, but the caller cannot
// observe them. Driving the real aggregate store over this writer produces
// the faithful ErrEventsAppended shape — queue intact, state not advanced.
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

// racingWriter delegates appends and, when armed, first lands competing
// lifecycle events on the same stream — after letting a configured number of
// lifecycle appends pass — so the caller's own append, sent with the
// expected version it read before the race, reports a version mismatch.
// This is the deterministic form of two writers racing one projection's
// lifecycle stream.
type racingWriter struct {
	eventstore.Store
	mu          sync.Mutex
	competitor  []byte
	competitors []*eventstore.WritableEvent
	armed       bool
	skip        int
}

// armRace arms the construction-time competitor against the next lifecycle
// append.
func (w *racingWriter) armRace() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.armed = true
	w.skip = 0
	w.competitors = []*eventstore.WritableEvent{{
		Type:            lifecycle.RebuildInitiated{}.EventType(),
		Data:            w.competitor,
		DataContentType: "application/json",
	}}
}

// armRaceAfter lets skip lifecycle appends pass, then lands the given
// competitors — one append, in order — ahead of the next one.
func (w *racingWriter) armRaceAfter(skip int, competitors ...*eventstore.WritableEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.armed = true
	w.skip = skip
	w.competitors = competitors
}

func (w *racingWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	w.mu.Lock()

	race := false

	if w.armed && streamID.Type == lifecycle.StreamType {
		if w.skip > 0 {
			w.skip--
		} else {
			w.armed = false
			race = true
		}
	}

	competitors := w.competitors
	w.mu.Unlock()

	if race {
		if _, err := w.Store.AppendStream(ctx, streamID, competitors, eventstore.AppendStreamOptions{}); err != nil {
			return nil, fmt.Errorf("landing competing events: %w", err)
		}
	}

	return w.Store.AppendStream(ctx, streamID, events, opts)
}

// writableLifecycleEvent marshals a lifecycle event into the raw writable
// form racingWriter lands competitors as.
func writableLifecycleEvent(t *testing.T, event estoria.DomainEvent[lifecycle.State]) *eventstore.WritableEvent {
	t.Helper()

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshaling competing %s event: %v", event.EventType(), err)
	}

	return &eventstore.WritableEvent{
		Type:            event.EventType(),
		Data:            data,
		DataContentType: "application/json",
	}
}

// misreportingWriter delegates appends and, when armed, reports the next
// append's events back with inflated stream versions on every event after
// the first: the events are durable as written, the first applies cleanly,
// and the next fails to apply — the partial-application ErrEventsAppended
// shape, with state partially advanced and the unapplied remainder left
// queued on the aggregate.
type misreportingWriter struct {
	eventstore.Store
	mu    sync.Mutex
	armed bool
}

func (w *misreportingWriter) armFailure() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.armed = true
}

func (w *misreportingWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	w.mu.Lock()
	armed := w.armed
	w.armed = false
	w.mu.Unlock()

	written, err := w.Store.AppendStream(ctx, streamID, events, opts)
	if err != nil || !armed {
		return written, err
	}

	misreported := make([]*eventstore.Event, len(written))
	for i, event := range written {
		clone := *event
		if i > 0 {
			clone.StreamVersion += 1000
		}

		misreported[i] = &clone
	}

	return misreported, nil
}

// appendRawLifecycleEvent writes a lifecycle event to the "orders"
// projection's stream through the raw event store, bypassing the aggregate —
// the shape of tampering the reserved namespace does not prevent.
func appendRawLifecycleEvent(t *testing.T, events *esmemory.EventStore, event estoria.DomainEvent[lifecycle.State]) {
	t.Helper()

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshaling %s event: %v", event.EventType(), err)
	}

	streamID := typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID("orders")}

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
// resolutions per ID, recording whether the version's storage existed at
// each resolution, failing on demand, and tracing each resolved handler so
// a teardown can be attributed to the exact instance that performed it.
type countingFactory struct {
	model *readModel

	mu             sync.Mutex
	calls          map[projection.ID]int
	storagePresent map[projection.ID][]bool
	fail           map[projection.ID]bool
	last           *tracedHandler
}

func (f *countingFactory) handler(id projection.ID) (projection.EventHandler, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.calls == nil {
		f.calls = map[projection.ID]int{}
	}

	if f.storagePresent == nil {
		f.storagePresent = map[projection.ID][]bool{}
	}

	f.calls[id]++
	f.storagePresent[id] = append(f.storagePresent[id], f.model.hasTable(id))

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

// storageObservations returns, per resolution of the ID in order, whether
// the version's storage existed when the factory was called.
func (f *countingFactory) storageObservations(id projection.ID) []bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]bool(nil), f.storagePresent[id]...)
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

	mu      sync.Mutex
	failure error
}

func (s *failingDeleteCheckpoints) setFail(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if fail {
		s.failure = errors.New("simulated checkpoint delete failure")
	} else {
		s.failure = nil
	}
}

// failWith arms Delete to return exactly the given error.
func (s *failingDeleteCheckpoints) failWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failure = err
}

func (s *failingDeleteCheckpoints) Delete(ctx context.Context, id projection.ID) error {
	s.mu.Lock()
	failure := s.failure
	s.mu.Unlock()

	if failure != nil {
		return failure
	}

	return s.Store.Delete(ctx, id)
}

// stubWitness reports a fixed cutover attestation.
type stubWitness struct {
	cutover lifecycle.Cutover
	err     error
}

func (w stubWitness) AppliedCutover(context.Context, string) (lifecycle.Cutover, error) {
	return w.cutover, w.err
}

// sequenceWitness reports queued cutovers in order, holding the last one
// once the queue drains to it.
type sequenceWitness struct {
	mu    sync.Mutex
	queue []lifecycle.Cutover
}

func (w *sequenceWitness) AppliedCutover(context.Context, string) (lifecycle.Cutover, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.queue) == 0 {
		return lifecycle.Cutover{}, lifecycle.ErrNoLiveVersion
	}

	cutover := w.queue[0]
	if len(w.queue) > 1 {
		w.queue = w.queue[1:]
	}

	return cutover, nil
}

// injectingEventStore delegates to an inner store and, once armed, runs
// its injection exactly once, immediately before serving the first read that
// can observe a recorded retirement reservation: the window between a
// reservation's save and the retirement protocol's post-reservation refold.
type injectingEventStore struct {
	eventstore.Store
	mu     sync.Mutex
	inject func()
	fired  bool
}

func (s *injectingEventStore) arm(inject func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.inject = inject
	s.fired = false
}

func (s *injectingEventStore) ReadStream(ctx context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	s.mu.Lock()
	pending := s.inject != nil && !s.fired
	s.mu.Unlock()

	if pending && s.holdsReservation(ctx, streamID) {
		s.mu.Lock()
		inject := s.inject
		fire := !s.fired
		s.fired = true
		s.mu.Unlock()

		if fire {
			inject()
		}
	}

	return s.Store.ReadStream(ctx, streamID, opts)
}

// holdsReservation reports whether the stream already records a retirement
// reservation, so the injection fires only after a reservation's save.
func (s *injectingEventStore) holdsReservation(ctx context.Context, streamID typeid.ID) bool {
	iter, err := s.Store.ReadStream(ctx, streamID, eventstore.ReadStreamOptions{})
	if err != nil {
		return false
	}
	defer iter.Close(ctx)

	reservation := lifecycle.RetireStarted{}.EventType()

	for {
		event, err := iter.Next(ctx)
		if err != nil {
			return false
		}

		if event.ID.Type == reservation {
			return true
		}
	}
}

// recordedReservations decodes every recorded RetireStarted event in order.
func recordedReservations(t *testing.T, events *esmemory.EventStore) []lifecycle.RetireStarted {
	t.Helper()

	var decoded []lifecycle.RetireStarted

	for _, data := range rawEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()) {
		var event lifecycle.RetireStarted
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("decoding reservation: %v", err)
		}

		decoded = append(decoded, event)
	}

	return decoded
}

// recordedCompletions decodes every recorded PreviousRetired event in order.
func recordedCompletions(t *testing.T, events *esmemory.EventStore) []lifecycle.PreviousRetired {
	t.Helper()

	var decoded []lifecycle.PreviousRetired

	for _, data := range rawEventsOfType(t, events, lifecycle.PreviousRetired{}.EventType()) {
		var event lifecycle.PreviousRetired
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("decoding completion: %v", err)
		}

		decoded = append(decoded, event)
	}

	return decoded
}

// recordedPromotions decodes every recorded Promoted event in order.
func recordedPromotions(t *testing.T, events *esmemory.EventStore) []lifecycle.Promoted {
	t.Helper()

	var decoded []lifecycle.Promoted

	for _, data := range rawEventsOfType(t, events, lifecycle.Promoted{}.EventType()) {
		var event lifecycle.Promoted
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("decoding promotion: %v", err)
		}

		decoded = append(decoded, event)
	}

	return decoded
}

// recordedPolicies decodes every recorded RetirementPolicySet event in order.
func recordedPolicies(t *testing.T, events *esmemory.EventStore) []lifecycle.RetirementPolicySet {
	t.Helper()

	var decoded []lifecycle.RetirementPolicySet

	for _, data := range rawEventsOfType(t, events, lifecycle.RetirementPolicySet{}.EventType()) {
		var event lifecycle.RetirementPolicySet
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("decoding policy transition: %v", err)
		}

		decoded = append(decoded, event)
	}

	return decoded
}

// rawEventsOfType returns the payloads of every recorded event of the given
// type, in global order.
func rawEventsOfType(t *testing.T, events *esmemory.EventStore, eventType string) [][]byte {
	t.Helper()

	iter, err := events.ReadAll(t.Context(), eventstore.ReadAllOptions{})
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	all, err := eventstore.Collect(t.Context(), iter)
	if err != nil {
		t.Fatalf("collecting events: %v", err)
	}

	var payloads [][]byte

	for _, event := range all {
		if event.ID.Type == eventType {
			payloads = append(payloads, event.Data)
		}
	}

	return payloads
}

// promotedSecondVersion drives a v2-over-v1 rebuild to PhasePromoted on the
// harness, returning the handle, both versions, and Run's done channel.
func promotedSecondVersion(t *testing.T, h *harness) (*lifecycle.Rebuild, projection.ID, projection.ID, <-chan error) {
	t.Helper()

	v1 := h.promoteFirstVersion()
	h.appendDomain(3)

	r2 := h.begin("witness gate build")

	_, done := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	return r2, v1, projection.ID{Name: "orders", Version: 2}, done
}

// TestSetRetirementPolicy pins the audited policy command: transitions
// require an actor and reason, exactly one mode, and canonicalizable
// witness IDs; recorded policies canonicalize the set and count
// generations; and a projection with no recorded lifecycle refuses.
func TestSetRetirementPolicy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.promoteFirstVersion()

	for _, tt := range []struct {
		name    string
		change  lifecycle.RetirementPolicyChange
		wantErr string
	}{
		{
			name:    "requires an actor",
			change:  lifecycle.RetirementPolicyChange{Witnesses: []string{"router"}, Reason: "gate"},
			wantErr: "requires an actor and a reason",
		},
		{
			name:    "requires a reason",
			change:  lifecycle.RetirementPolicyChange{Witnesses: []string{"router"}, Actor: "op"},
			wantErr: "requires an actor and a reason",
		},
		{
			name:    "refuses witnesses alongside the unwitnessed mode",
			change:  lifecycle.RetirementPolicyChange{Witnesses: []string{"router"}, Unwitnessed: true, Actor: "op", Reason: "gate"},
			wantErr: "not both",
		},
		{
			name:    "requires a mode",
			change:  lifecycle.RetirementPolicyChange{Actor: "op", Reason: "gate"},
			wantErr: "at least one witness or is explicitly unwitnessed",
		},
		{
			name:    "refuses duplicate witness IDs",
			change:  lifecycle.RetirementPolicyChange{Witnesses: []string{"router", "router"}, Actor: "op", Reason: "gate"},
			wantErr: "unique and sorted",
		},
		{
			name:    "refuses an empty witness ID",
			change:  lifecycle.RetirementPolicyChange{Witnesses: []string{""}, Actor: "op", Reason: "gate"},
			wantErr: "must not be empty",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", tt.change)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want the transition refused with %q, got %v", tt.wantErr, err)
			}
		})
	}

	t.Run("refuses a projection with no recorded lifecycle", func(t *testing.T) {
		t.Parallel()

		if err := h.orchestrator.SetRetirementPolicy(t.Context(), "carts", lifecycle.RetirementPolicyChange{
			Unwitnessed: true, Actor: "op", Reason: "gate",
		}); err == nil {
			t.Fatal("want a projection with no recorded lifecycle refused, got nil")
		}
	})

	// The refusal rows record nothing, so the generation math here is
	// independent of them.
	t.Run("records canonically and counts generations", func(t *testing.T) {
		t.Parallel()

		if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
			Witnesses: []string{"router", "auditor"}, Actor: "op", Reason: "gate retirements",
		}); err != nil {
			t.Fatalf("recording the policy: %v", err)
		}

		state, err := h.orchestrator.Get(t.Context(), "orders")
		if err != nil {
			t.Fatalf("getting state: %v", err)
		}

		want := lifecycle.RetirementPolicy{Generation: 1, Witnesses: []string{"auditor", "router"}}
		if !reflect.DeepEqual(state.RetirementPolicy, want) {
			t.Fatalf("want the policy recorded canonically as %+v, got %+v", want, state.RetirementPolicy)
		}

		if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
			Unwitnessed: true, Actor: "op", Reason: "single-route deployment",
		}); err != nil {
			t.Fatalf("superseding the policy: %v", err)
		}

		state, err = h.orchestrator.Get(t.Context(), "orders")
		if err != nil {
			t.Fatalf("getting state: %v", err)
		}

		want = lifecycle.RetirementPolicy{Generation: 2, Unwitnessed: true}
		if !reflect.DeepEqual(state.RetirementPolicy, want) {
			t.Fatalf("want the superseding policy at generation 2, got %+v", state.RetirementPolicy)
		}

		// The audit fields never reach the fold, so only the recorded
		// payloads prove who authorized what and when.
		policies := recordedPolicies(t, h.events)
		if len(policies) != 2 {
			t.Fatalf("want both transitions recorded, got %d", len(policies))
		}

		if p := policies[0]; p.Actor != "op" || p.Reason != "gate retirements" || p.At.IsZero() {
			t.Errorf("want the first transition's audit trail recorded verbatim, got %+v", p)
		}

		if p := policies[1]; p.Actor != "op" || p.Reason != "single-route deployment" || p.At.IsZero() {
			t.Errorf("want the superseding transition's audit trail recorded verbatim, got %+v", p)
		}
	})
}

// TestRetire_RefusesWithoutPolicy pins the gate's default: a projection with
// no recorded retirement policy and no audited override refuses to retire —
// before anything is reserved, so rollback remains available.
func TestRetire_RefusesWithoutPolicy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r2, v1, _, done := promotedSecondVersion(t, h)

	err := r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), "no retirement policy") {
		t.Fatalf("want the ungoverned retirement refused, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Fatalf("want no reservation from the refused retirement, got %d", got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Fatalf("want nothing destroyed by the refused retirement, got %v", dropped)
	}

	if err := r2.Rollback(t.Context()); err != nil {
		t.Errorf("want rollback to %s still possible after the refusal, got %v", v1, err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rollback, got %v", err)
	}
}

// TestRetire_WitnessedProtocolRecordsReceipts pins the full witnessed path:
// the registered router vouches for the live cutover, and both the
// reservation and the completion record the policy binding, the captured
// membership, and the receipts.
func TestRetire_WitnessedProtocolRecordsReceipts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r2, v1, v2, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"router"}, Actor: "op", Reason: "gate retirements on the serving router",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	h.waitLive(v2)

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retiring: %v", err)
	}

	receipts := []lifecycle.WitnessReceipt{{Witness: "router", Cutover: lifecycle.Cutover{Live: v2, Revision: 2}}}

	reservations := recordedReservations(t, h.events)
	if len(reservations) != 1 {
		t.Fatalf("want exactly one reservation, got %d", len(reservations))
	}

	reservation := reservations[0]
	if reservation.PolicyGeneration != 1 || !reflect.DeepEqual(reservation.Witnesses, []string{"router"}) ||
		!reflect.DeepEqual(reservation.Receipts, receipts) || reservation.Override != (lifecycle.RetirementOverride{}) {
		t.Errorf("want the reservation to capture the policy binding and preflight receipts, got %+v", reservation)
	}

	completions := recordedCompletions(t, h.events)
	if len(completions) != 2 { // v1's trivial first-version completion, then this one
		t.Fatalf("want two completions, got %d", len(completions))
	}

	if completion := completions[1]; !reflect.DeepEqual(completion.Receipts, receipts) {
		t.Errorf("want the completion to re-attest the captured witnesses, got %+v", completion)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down, got %v", v1, dropped)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_RefusesUnattestedWitness pins the preflight: a required witness
// serving anything but the exact live (version, revision) pair refuses the
// retirement before anything is reserved, and rollback remains available.
func TestRetire_RefusesUnattestedWitness(t *testing.T) {
	t.Parallel()

	v1 := projection.ID{Name: "orders", Version: 1}
	v2 := projection.ID{Name: "orders", Version: 2}

	for _, tt := range []struct {
		name    string
		serving lifecycle.Cutover
	}{
		{name: "a stale version", serving: lifecycle.Cutover{Live: v1, Revision: 1}},
		{name: "the right version at a stale revision", serving: lifecycle.Cutover{Live: v2, Revision: 1}},
		{name: "the wrong version at the live revision", serving: lifecycle.Cutover{Live: v1, Revision: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, lifecycle.WithRetirementWitness("gate", stubWitness{cutover: tt.serving}))
			r2, _, _, done := promotedSecondVersion(t, h)

			if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
				Witnesses: []string{"gate"}, Actor: "op", Reason: "gate retirements",
			}); err != nil {
				t.Fatalf("recording the policy: %v", err)
			}

			err := r2.Retire(t.Context())
			if err == nil || !strings.Contains(err.Error(), `witness "gate" serves`) {
				t.Fatalf("want the unattested retirement refused, got %v", err)
			}

			if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 0 {
				t.Fatalf("want no reservation from the refused preflight, got %d", got)
			}

			if dropped := h.model.droppedTables(); len(dropped) != 0 {
				t.Fatalf("want nothing destroyed, got %v", dropped)
			}

			if err := r2.Rollback(t.Context()); err != nil {
				t.Errorf("want rollback still possible after the refusal, got %v", err)
			}

			if err := waitDone(t, done); err != nil {
				t.Errorf("want Run to return nil after the rollback, got %v", err)
			}
		})
	}
}

// TestRetire_RefusesUnregisteredWitness pins configuration resolution: a
// policy-required witness with no registered implementation refuses the
// retirement rather than weakening it.
func TestRetire_RefusesUnregisteredWitness(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r2, _, _, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"auditor"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	err := r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), `"auditor"`) || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("want the unregistered witness named in the refusal, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Fatalf("want no reservation, got %d", got)
	}

	if err := r2.Rollback(t.Context()); err != nil {
		t.Errorf("want rollback still possible after the refusal, got %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rollback, got %v", err)
	}
}

// TestRetire_UnwitnessedPolicy pins the explicit opt-out: an unwitnessed
// policy retires without attestation, recording the policy binding and no
// receipts.
func TestRetire_UnwitnessedPolicy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r2, v1, _, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Unwitnessed: true, Actor: "op", Reason: "single-route deployment",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retiring: %v", err)
	}

	reservations := recordedReservations(t, h.events)
	if len(reservations) != 1 {
		t.Fatalf("want exactly one reservation, got %d", len(reservations))
	}

	if r := reservations[0]; r.PolicyGeneration != 1 || len(r.Witnesses) != 0 || len(r.Receipts) != 0 {
		t.Errorf("want an unwitnessed reservation under generation 1, got %+v", r)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down, got %v", v1, dropped)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_RetryUsesCapturedMembership pins the reservation's authority: a
// retry from PhaseRetiring re-attests exactly the membership the reservation
// captured — a process configured without the witness refuses, and a policy
// superseded to unwitnessed still re-attests the captured router.
func TestRetire_RetryUsesCapturedMembership(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r2, v1, v2, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"router"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	h.waitLive(v2)
	h.model.setTeardownFailure(true)

	if err := r2.Retire(t.Context()); err == nil {
		t.Fatal("want the teardown failure reported, got nil")
	}

	if got := r2.State().Attempt.Phase; got != lifecycle.PhaseRetiring {
		t.Fatalf("want the reservation durable in %s, got %s", lifecycle.PhaseRetiring, got)
	}

	// A process without the router registered cannot weaken the reservation:
	// the captured membership still governs the retry.
	bare, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:          h.events,
		Checkpoints:     h.checkpoints,
		Handler:         h.model.handler,
		LifecycleEvents: h.events,
	})
	if err != nil {
		t.Fatalf("creating witnessless orchestrator: %v", err)
	}

	stale, err := bare.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming on the witnessless orchestrator: %v", err)
	}

	err = stale.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), `"router"`) || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("want the captured membership to refuse the witnessless retry, got %v", err)
	}

	// A policy superseded to unwitnessed cannot weaken it either: the retry
	// still re-attests the captured router, and the completion's receipts
	// prove which membership governed.
	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Unwitnessed: true, Actor: "op", Reason: "attempted weakening",
	}); err != nil {
		t.Fatalf("superseding the policy: %v", err)
	}

	h.model.setTeardownFailure(false)

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retrying the retirement: %v", err)
	}

	completions := recordedCompletions(t, h.events)

	want := []lifecycle.WitnessReceipt{{Witness: "router", Cutover: lifecycle.Cutover{Live: v2, Revision: 2}}}
	if last := completions[len(completions)-1]; !reflect.DeepEqual(last.Receipts, want) {
		t.Errorf("want the retry's completion to re-attest the captured router, got %+v", last)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down once, got %v", v1, dropped)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_RecheckLeavesReservationRetryable pins the post-reservation
// recheck: a witness that vouched at preflight but not after the
// reservation stops the retirement before the teardown, leaving the
// reservation standing for a retry.
func TestRetire_RecheckLeavesReservationRetryable(t *testing.T) {
	t.Parallel()

	v2 := projection.ID{Name: "orders", Version: 2}
	live := lifecycle.Cutover{Live: v2, Revision: 2}

	// Vouches at preflight, wavers at the recheck, vouches thereafter.
	gate := &sequenceWitness{queue: []lifecycle.Cutover{live, {Live: projection.ID{Name: "orders", Version: 1}, Revision: 1}, live}}

	h := newHarness(t, lifecycle.WithRetirementWitness("gate", gate))
	r2, v1, _, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"gate"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	err := r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), "recheck") || !strings.Contains(err.Error(), "the reservation stands") {
		t.Fatalf("want the failed recheck to stop the retirement retryably, got %v", err)
	}

	if got := r2.State().Attempt.Phase; got != lifecycle.PhaseRetiring {
		t.Fatalf("want the reservation durable in %s, got %s", lifecycle.PhaseRetiring, got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Fatalf("want nothing destroyed before the recheck passed, got %v", dropped)
	}

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retrying after the witness converged: %v", err)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down by the retry, got %v", v1, dropped)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_OverrideValidation pins the override's audit requirement: an
// override without an actor or reason is refused before anything is
// touched.
func TestRetire_OverrideValidation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r2, _, _, done := promotedSecondVersion(t, h)

	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("", "reason")); err == nil ||
		!strings.Contains(err.Error(), "requires an actor and a reason") {
		t.Fatalf("want the actorless override refused, got %v", err)
	}

	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("op", "")); err == nil ||
		!strings.Contains(err.Error(), "requires an actor and a reason") {
		t.Fatalf("want the reasonless override refused, got %v", err)
	}

	if err := r2.Retire(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("want the nil option refused, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Fatalf("want no reservation, got %d", got)
	}

	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("op", "audited emergency")); err != nil {
		t.Fatalf("retiring with the audited override: %v", err)
	}

	reservations := recordedReservations(t, h.events)
	if len(reservations) != 1 || reservations[0].Override != (lifecycle.RetirementOverride{Actor: "op", Reason: "audited emergency"}) {
		t.Fatalf("want the override recorded in the reservation, got %+v", reservations)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestPromote_BindsPolicyGeneration pins the promotion's policy binding: a
// flip recorded under an active policy carries its generation durably.
func TestPromote_BindsPolicyGeneration(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.promoteFirstVersion()

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"router"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	h.appendDomain(3)

	r2 := h.begin("bound build")

	_, done := runAsync(t, r2)
	waitPhase(t, r2, lifecycle.PhaseCaughtUp)

	if err := r2.Promote(t.Context()); err != nil {
		t.Fatalf("promoting v2: %v", err)
	}

	promotions := recordedPromotions(t, h.events)
	if len(promotions) != 2 {
		t.Fatalf("want two promotions, got %d", len(promotions))
	}

	if promotions[0].PolicyGeneration != 0 || promotions[1].PolicyGeneration != 1 {
		t.Errorf("want the flips bound to generations 0 and 1, got %d and %d",
			promotions[0].PolicyGeneration, promotions[1].PolicyGeneration)
	}

	if err := r2.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back to unwind the fixture: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rollback, got %v", err)
	}
}

// TestState_MutationCannotWeakenThePolicy pins that State hands out a
// detached copy: writing through a returned state's policy membership must
// not change which witnesses a later retirement resolves.
func TestState_MutationCannotWeakenThePolicy(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r2, _, v2, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"auditor"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	// The registered router serves the live cutover, so a membership swapped
	// to name it would attest cleanly and destroy storage.
	h.waitLive(v2)

	// Fold the policy into the handle: this refusal hydrates the policy
	// event, so the mutation below targets the folded slice.
	if err := r2.Retire(t.Context()); err == nil || !strings.Contains(err.Error(), `"auditor"`) {
		t.Fatalf("want the unregistered auditor refused, got %v", err)
	}

	state := r2.State()
	state.RetirementPolicy.Witnesses[0] = "router"

	err := r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), `"auditor"`) || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("want the recorded membership still governing after the mutation, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Errorf("want no reservation through the mutated view, got %d", got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Fatalf("want nothing destroyed through the mutated view, got %v", dropped)
	}

	if err := r2.Rollback(t.Context()); err != nil {
		t.Errorf("want rollback still possible, got %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rollback, got %v", err)
	}
}

// TestState_MutationCannotAmendTheCapturedMembership pins the same
// detachment for the reservation's captured witnesses: a retry re-attests
// what the reservation recorded, not what a caller wrote into a returned
// state.
func TestState_MutationCannotAmendTheCapturedMembership(t *testing.T) {
	t.Parallel()

	v1 := projection.ID{Name: "orders", Version: 1}
	live := lifecycle.Cutover{Live: projection.ID{Name: "orders", Version: 2}, Revision: 2}
	stale := lifecycle.Cutover{Live: v1, Revision: 1}

	// Vouches at preflight, wavers at the recheck and the mutated retry,
	// vouches thereafter.
	gate := &sequenceWitness{queue: []lifecycle.Cutover{live, stale, stale, live}}

	h := newHarness(t, lifecycle.WithRetirementWitness("gate", gate))
	r2, _, v2, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"gate"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	// The router the mutation will name is registered and converged.
	h.waitLive(v2)

	err := r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), "recheck") {
		t.Fatalf("want the failed recheck to park the reservation, got %v", err)
	}

	state := r2.State()
	state.Attempt.RetiringWitnesses[0] = "router"

	err = r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), `witness "gate"`) {
		t.Fatalf("want the captured membership still governing the retry, got %v", err)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Fatalf("want nothing destroyed through the mutated view, got %v", dropped)
	}

	// The gate converges; the unmutated retry completes and re-attests the
	// captured membership.
	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retrying after the witness converged: %v", err)
	}

	completions := recordedCompletions(t, h.events)

	want := []lifecycle.WitnessReceipt{{Witness: "gate", Cutover: live}}
	if last := completions[len(completions)-1]; !reflect.DeepEqual(last.Receipts, want) {
		t.Errorf("want the completion to re-attest the captured gate, got %+v", last)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// reservationWindowHarness wires the standard harness with the lifecycle
// authority reads intercepted, so a test can inject a stream event into the
// window between a retirement reservation's save and the protocol's
// post-reservation refold.
func reservationWindowHarness(t *testing.T, live lifecycle.Cutover) (*harness, *injectingEventStore) {
	t.Helper()

	events := newEventStore(t)
	hooked := &injectingEventStore{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, hooked,
		lifecycle.WithRetirementWitness("gate_a", stubWitness{cutover: live}),
		lifecycle.WithRetirementWitness("gate_b", stubWitness{cutover: live}),
	)

	return h, hooked
}

// TestRetire_AdvanceDuringReservationFailsClosed pins the post-reservation
// binding check: a concurrent repair's completion lands between this call's
// reservation and its refold, so the refold no longer holds the reservation
// — the call must refuse loudly and destroy nothing, rather than re-run a
// teardown it no longer owns and record a second completion over a vacated
// slot.
func TestRetire_AdvanceDuringReservationFailsClosed(t *testing.T) {
	t.Parallel()

	live := lifecycle.Cutover{Live: projection.ID{Name: "orders", Version: 2}, Revision: 2}

	h, hooked := reservationWindowHarness(t, live)
	r2, v1, _, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"gate_a", "gate_b"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	hooked.arm(func() {
		appendRawLifecycleEvent(t, h.events, lifecycle.PreviousRetired{
			Retired: v1,
			Receipts: []lifecycle.WitnessReceipt{
				{Witness: "gate_a", Cutover: live},
				{Witness: "gate_b", Cutover: live},
			},
		})
	})

	err := r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), "advanced past this call's reservation") {
		t.Fatalf("want the outrun retirement refused loudly, got %v", err)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Errorf("want nothing destroyed by the outrun call, got %v", dropped)
	}

	// v1's own first-version completion is already on the stream; the
	// injected completion must be the only other one.
	completions := recordedCompletions(t, h.events)
	if len(completions) != 2 || completions[len(completions)-1].Retired != v1 {
		t.Errorf("want exactly the injected completion beside v1's own, got %+v", completions)
	}

	replay, err := lifecycle.NewStore(h.events)
	if err != nil {
		t.Fatalf("creating the replay store: %v", err)
	}

	refolded, err := replay.Load(t.Context(), lifecycle.StreamUUID("orders"), nil)
	if err != nil {
		t.Fatalf("replaying the lifecycle stream: %v", err)
	}

	if reason := refolded.State().InvalidReason; reason != "" {
		t.Errorf("want the stream clean after the refusal, got poisoned: %s", reason)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the completion, got %v", err)
	}
}

// TestRetire_RecheckReattestsTheCapturedMembership pins what the
// post-reservation refold re-derives: a policy transition landing between
// the reservation and its refold supersedes the policy, but the recheck and
// the completion must follow the membership the reservation captured — the
// completion's receipts attest the captured witnesses, and an event-only
// replay accepts them.
func TestRetire_RecheckReattestsTheCapturedMembership(t *testing.T) {
	t.Parallel()

	live := lifecycle.Cutover{Live: projection.ID{Name: "orders", Version: 2}, Revision: 2}

	h, hooked := reservationWindowHarness(t, live)
	r2, v1, _, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"gate_a", "gate_b"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	hooked.arm(func() {
		appendRawLifecycleEvent(t, h.events, lifecycle.RetirementPolicySet{
			Generation: 2,
			Witnesses:  []string{"gate_a"},
			Actor:      "op",
			Reason:     "supersede mid-retirement",
			At:         time.Now(),
		})
	})

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retiring across the policy transition: %v", err)
	}

	want := []lifecycle.WitnessReceipt{{Witness: "gate_a", Cutover: live}, {Witness: "gate_b", Cutover: live}}

	completions := recordedCompletions(t, h.events)
	if last := completions[len(completions)-1]; !reflect.DeepEqual(last.Receipts, want) {
		t.Errorf("want the completion re-attesting the captured membership, got %+v", last)
	}

	replay, err := lifecycle.NewStore(h.events)
	if err != nil {
		t.Fatalf("creating the replay store: %v", err)
	}

	refolded, err := replay.Load(t.Context(), lifecycle.StreamUUID("orders"), nil)
	if err != nil {
		t.Fatalf("replaying the lifecycle stream: %v", err)
	}

	if reason := refolded.State().InvalidReason; reason != "" {
		t.Errorf("want the event-only replay to accept the completion, got poisoned: %s", reason)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down exactly once, got %v", v1, dropped)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_FirstVersionRefusesOverride pins the override's scope: a first
// rebuild's completion destroys nothing and is not gated, so an override —
// which no reservation exists to record — is refused rather than silently
// accepted as unaudited authorization.
func TestRetire_FirstVersionRefusesOverride(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("first version")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("promoting: %v", err)
	}

	err := r.Retire(t.Context(), lifecycle.WithRetirementOverride("op", "not applicable"))
	if err == nil || !strings.Contains(err.Error(), "override does not apply") {
		t.Fatalf("want the inapplicable override refused, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.PreviousRetired{}.EventType()); got != 0 {
		t.Fatalf("want no completion recorded under the refused override, got %d", got)
	}

	if err := r.Retire(t.Context()); err != nil {
		t.Fatalf("completing the first rebuild: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after completion, got %v", err)
	}
}

// TestRetire_RecordsEveryWitnessReceipt pins the successful multi-witness
// protocol: every required witness is attested and every ordered receipt is
// recorded, at the reservation and again at the completion.
func TestRetire_RecordsEveryWitnessReceipt(t *testing.T) {
	t.Parallel()

	live := lifecycle.Cutover{Live: projection.ID{Name: "orders", Version: 2}, Revision: 2}

	h := newHarness(t,
		lifecycle.WithRetirementWitness("gate_a", stubWitness{cutover: live}),
		lifecycle.WithRetirementWitness("gate_b", stubWitness{cutover: live}),
	)
	r2, v1, _, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"gate_a", "gate_b"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retiring: %v", err)
	}

	want := []lifecycle.WitnessReceipt{{Witness: "gate_a", Cutover: live}, {Witness: "gate_b", Cutover: live}}

	reservations := recordedReservations(t, h.events)
	if len(reservations) != 1 {
		t.Fatalf("want exactly one reservation, got %d", len(reservations))
	}

	if r := reservations[0]; !reflect.DeepEqual(r.Witnesses, []string{"gate_a", "gate_b"}) || !reflect.DeepEqual(r.Receipts, want) {
		t.Errorf("want both ordered attestations captured by the reservation, got %+v", r)
	}

	completions := recordedCompletions(t, h.events)
	if last := completions[len(completions)-1]; !reflect.DeepEqual(last.Receipts, want) {
		t.Errorf("want both ordered attestations re-recorded by the completion, got %+v", last)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down, got %v", v1, dropped)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestGet_ReturnsDetachedState pins the observation boundary: the state Get
// returns must not share memory with anything a later read is served from —
// writing through one caller's view can never alter another's.
func TestGet_ReturnsDetachedState(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.promoteFirstVersion()

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"auditor"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	tampered, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("getting state: %v", err)
	}

	if len(tampered.RetirementPolicy.Witnesses) != 1 {
		t.Fatalf("fixture: want the policy visible through Get, got %+v", tampered.RetirementPolicy)
	}

	tampered.RetirementPolicy.Witnesses[0] = "tampered"

	state, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("getting state again: %v", err)
	}

	if !reflect.DeepEqual(state.RetirementPolicy.Witnesses, []string{"auditor"}) {
		t.Errorf("want the recorded membership unaffected by the mutation, got %v", state.RetirementPolicy.Witnesses)
	}
}

// TestRetire_CheckpointDeleteJoinedFailureSurfaces pins the delete's error
// classification: a checkpoint already absent is benign only when absence is
// the whole story, so a failure joined alongside the not-found sentinel must
// stop the retirement before completion records.
func TestRetire_CheckpointDeleteJoinedFailureSurfaces(t *testing.T) {
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

	errBackend := errors.New("simulated checkpoint backend failure")
	checkpoints.failWith(errors.Join(checkpointstore.ErrCheckpointNotFound, errBackend))

	err = r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "joined delete failure scenario"))
	if err == nil || !errors.Is(err, errBackend) {
		t.Fatalf("want the joined delete failure surfaced, got %v", err)
	}

	if got := countEventsOfType(t, events, lifecycle.PreviousRetired{}.EventType()); got != 1 {
		t.Fatalf("want no completion recorded past the failed delete, got %d", got)
	}

	if _, err := checkpoints.Load(t.Context(), v1); err != nil {
		t.Fatalf("want v1's checkpoint retained after the failed delete, got %v", err)
	}

	checkpoints.setFail(false)

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("repairing the retirement: %v", err)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_ReservationSaveFailureDestroysNothing pins the reservation as
// the destruction boundary: a retirement whose reservation never became
// durable must not tear anything down, and retrying it reserves afresh.
func TestRetire_ReservationSaveFailureDestroysNothing(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	model := newReadModel()
	checkpoints := cpmemory.NewCheckpointStore()

	writer := &refusingWriter{Store: events}

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:          events,
		Checkpoints:     checkpoints,
		Handler:         model.handler,
		LifecycleEvents: writer,
	},
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
		lifecycle.WithReconcileInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

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

	writer.armFailure()

	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "refused reservation scenario")); err == nil {
		t.Fatal("want the refused reservation save reported, got nil")
	}

	if dropped := model.droppedTables(); len(dropped) != 0 {
		t.Fatalf("want nothing destroyed without a durable reservation, got %v", dropped)
	}

	if _, err := checkpoints.Load(t.Context(), v1); err != nil {
		t.Fatalf("want v1's checkpoint intact without a durable reservation, got %v", err)
	}

	if got := countEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Fatalf("want no reservation recorded, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.PreviousRetired{}.EventType()); got != 1 {
		t.Fatalf("want no completion recorded, got %d", got)
	}

	// The failure was pre-append: nothing was reserved, so the retry
	// reserves afresh under the same authorization.
	if err := r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "refused reservation scenario")); err != nil {
		t.Fatalf("retrying the retirement: %v", err)
	}

	if dropped := model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down by the retry, got %v", v1, dropped)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_UnobservedReservationDestroysNothing pins the stale-handle
// boundary: a reservation that is durable but unobserved (the append landed,
// the save could not confirm it — the faithful ErrEventsAppended shape, with
// the handle's state genuinely not advanced) stops the retirement before any
// destruction, and the repair runs from a fresh handle against the recorded
// reservation.
func TestRetire_UnobservedReservationDestroysNothing(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &truncatingWriter{Store: events}

	model := newReadModel()
	checkpoints := cpmemory.NewCheckpointStore()

	orchestrator, err := lifecycle.NewOrchestrator(lifecycle.Config{
		Events:          events,
		Checkpoints:     checkpoints,
		Handler:         model.handler,
		LifecycleEvents: writer,
	},
		lifecycle.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
		lifecycle.WithReconcileInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

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

	writer.armFailure()

	err = r2.Retire(t.Context(), lifecycle.WithRetirementOverride("test", "unobserved reservation scenario"))
	if err == nil || !errors.Is(err, aggregatestore.ErrEventsAppended) || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("want the unobserved reservation to stop the stale handle, got %v", err)
	}

	if got := r2.State().Attempt.Phase; got != lifecycle.PhasePromoted {
		t.Fatalf("want the stale handle genuinely unadvanced in %s, got %s", lifecycle.PhasePromoted, got)
	}

	if dropped := model.droppedTables(); len(dropped) != 0 {
		t.Fatalf("want nothing destroyed through the stale handle, got %v", dropped)
	}

	if _, err := checkpoints.Load(t.Context(), v1); err != nil {
		t.Fatalf("want v1's checkpoint intact behind the stale handle, got %v", err)
	}

	if got := countEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()); got != 1 {
		t.Fatalf("want the reservation durable despite the stale view, got %d", got)
	}

	if got := countEventsOfType(t, events, lifecycle.PreviousRetired{}.EventType()); got != 1 {
		t.Fatalf("want no completion through the stale handle, got %d", got)
	}

	fresh, err := orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming for the repair: %v", err)
	}

	if err := fresh.Retire(t.Context()); err != nil {
		t.Fatalf("repairing the retirement: %v", err)
	}

	if dropped := model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down by the repair, got %v", v1, dropped)
	}

	if got := countEventsOfType(t, events, lifecycle.RetireStarted{}.EventType()); got != 1 {
		t.Errorf("want the repair to reuse the recorded reservation, got %d", got)
	}

	if err := waitDone(t, done2); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// TestRetire_ChecksEveryWitness pins the gate over a multi-witness policy:
// every required witness attests, so a stale one after a vouching one still
// refuses the retirement.
func TestRetire_ChecksEveryWitness(t *testing.T) {
	t.Parallel()

	v1 := projection.ID{Name: "orders", Version: 1}
	live := lifecycle.Cutover{Live: projection.ID{Name: "orders", Version: 2}, Revision: 2}

	h := newHarness(t,
		lifecycle.WithRetirementWitness("gate_a", stubWitness{cutover: live}),
		lifecycle.WithRetirementWitness("gate_b", stubWitness{cutover: lifecycle.Cutover{Live: v1, Revision: 1}}),
	)
	r2, _, _, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"gate_a", "gate_b"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	err := r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), `witness "gate_b"`) {
		t.Fatalf("want the stale second witness to refuse the retirement, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 0 {
		t.Fatalf("want no reservation from the refused preflight, got %d", got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Fatalf("want nothing destroyed, got %v", dropped)
	}

	if err := r2.Rollback(t.Context()); err != nil {
		t.Errorf("want rollback still possible after the refusal, got %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rollback, got %v", err)
	}
}

// TestRetire_RetryCannotBeOverridden pins the reservation's authority against
// per-call config: once a retirement is reserved, a retry re-attests the
// captured membership, and an override on the retry is refused rather than
// silently recorded nowhere.
func TestRetire_RetryCannotBeOverridden(t *testing.T) {
	t.Parallel()

	v1 := projection.ID{Name: "orders", Version: 1}
	live := lifecycle.Cutover{Live: projection.ID{Name: "orders", Version: 2}, Revision: 2}
	stale := lifecycle.Cutover{Live: v1, Revision: 1}

	// Vouches at preflight, wavers at the recheck, vouches thereafter.
	gate := &sequenceWitness{queue: []lifecycle.Cutover{live, stale, live}}

	h := newHarness(t, lifecycle.WithRetirementWitness("gate", gate))
	r2, _, _, done := promotedSecondVersion(t, h)

	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Witnesses: []string{"gate"}, Actor: "op", Reason: "gate retirements",
	}); err != nil {
		t.Fatalf("recording the policy: %v", err)
	}

	err := r2.Retire(t.Context())
	if err == nil || !strings.Contains(err.Error(), "recheck") {
		t.Fatalf("want the failed recheck to park the reservation, got %v", err)
	}

	err = r2.Retire(t.Context(), lifecycle.WithRetirementOverride("op", "forcing past the captured gate"))
	if err == nil || !strings.Contains(err.Error(), "cannot be overridden") {
		t.Fatalf("want the overridden retry refused, got %v", err)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 0 {
		t.Fatalf("want nothing destroyed by the refused override, got %v", dropped)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RetireStarted{}.EventType()); got != 1 {
		t.Fatalf("want the original reservation untouched, got %d", got)
	}

	// The unoverridden retry still re-attests the captured gate and completes.
	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retrying without the override: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after the rebuild completes, got %v", err)
	}
}

// refusingWriter delegates appends and, when armed, refuses one append
// outright — after letting a configured number pass — before anything
// reaches the store: the pre-append failure shape, which leaves the
// command's event queued on the aggregate and nothing durable.
type refusingWriter struct {
	eventstore.Store
	mu    sync.Mutex
	armed bool
	skip  int
}

func (w *refusingWriter) armFailure() {
	w.armFailureAfterAppends(0)
}

// armFailureAfterAppends arms the writer to refuse one append after allowing n.
func (w *refusingWriter) armFailureAfterAppends(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.armed = true
	w.skip = n
}

func (w *refusingWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	w.mu.Lock()

	fail := false

	if w.armed {
		if w.skip > 0 {
			w.skip--
		} else {
			w.armed = false
			fail = true
		}
	}
	w.mu.Unlock()

	if fail {
		return nil, errors.New("simulated append refusal")
	}

	return w.Store.AppendStream(ctx, streamID, events, opts)
}

// parkingWriter delegates appends and, when armed, parks an append on a gate
// after a configured number of untouched appends: entered closes when the
// parked append begins, and the append proceeds when the gate closes.
// Parking a command's append holds the handle lock open, so exit publication
// must queue behind it — the window auto-promotion's exit awareness is
// pinned against.
type parkingWriter struct {
	eventstore.Store

	mu      sync.Mutex
	entered chan struct{}
	gate    chan struct{}
	skip    int
}

func (w *parkingWriter) armAppendGateAfter(skip int) (entered, gate chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.entered = make(chan struct{})
	w.gate = make(chan struct{})
	w.skip = skip

	return w.entered, w.gate
}

func (w *parkingWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	w.mu.Lock()

	var entered, gate chan struct{}

	if w.entered != nil {
		if w.skip > 0 {
			w.skip--
		} else {
			entered, gate = w.entered, w.gate
			w.entered, w.gate = nil, nil
		}
	}
	w.mu.Unlock()

	if entered != nil {
		close(entered)
		<-gate
	}

	return w.Store.AppendStream(ctx, streamID, events, opts)
}

// TestPromote_PerformsNoAuthorityRead pins Promote's deliberate exception to
// the entry-refold rule: promotion decides from the retained,
// certificate-anchored state and performs no authority read at all. The
// retained version is load-bearing — the flip's append carries it as the
// expected version, so the append itself arbitrates against every
// transition recorded since certification. A refold inserted after the
// certificate checks would absorb a competing claim those checks never saw
// and let the flip commit over it; any authority read here fails the armed
// intercept, so that mutation cannot survive this test.
func TestPromote_PerformsNoAuthorityRead(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	authority := &interceptingEventStore{Store: events}

	h := buildHarnessWithLifecycleEvents(t, events, authority, lifecycle.WithReconcileInterval(time.Hour))
	h.appendDomain(3)

	r := h.begin("promotion reads nothing")
	cancel, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	reads := authority.armHydrateIntercept(0, func(context.Context) error {
		return errors.New("promotion must not read the lifecycle stream")
	})

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("want the certified promotion to decide without reading, got %v", err)
	}

	select {
	case <-reads:
		t.Fatal("want no authority read during Promote")
	default:
	}

	if got := r.State().Attempt.Phase; got != lifecycle.PhasePromoted {
		t.Errorf("want the flip recorded on the handle, got %s", got)
	}

	if got := countEventsOfType(t, events, lifecycle.Promoted{}.EventType()); got != 1 {
		t.Errorf("want exactly one Promoted event recorded, got %d", got)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the promoted run to tail until canceled, got %v", err)
	}
}

// TestStateJSONRoundTrip_PreservesPoison pins the poison mark's persistence
// through the state codec: lifecycle state serialized outside the
// orchestrator — a user-wired snapshot over the exported store, a
// diagnostic dump — feeds State back through JSON, and a mark that did not
// persist would launder a poisoned fold into a clean one. The fold here is
// a cover-up whose final shape validates clean; only the mark carries the
// verdict.
func TestStateJSONRoundTrip_PreservesPoison(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	v1 := projection.ID{Name: "orders", Version: 1}
	at := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)

	appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{Attempt: uuid.Must(uuid.NewV4()), Target: v1, Reason: "first build", At: at})
	appendRawLifecycleEvent(t, events, lifecycle.Abandoned{Cause: "burning v1"})
	appendRawLifecycleEvent(t, events, lifecycle.RebuildInitiated{Attempt: uuid.Must(uuid.NewV4()), Target: v1, Reason: "reusing the burned version", At: at})
	appendRawLifecycleEvent(t, events, lifecycle.Abandoned{Cause: "covering the tracks"})

	replay, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating the replay store: %v", err)
	}

	folded, err := replay.Load(t.Context(), lifecycle.StreamUUID("orders"), nil)
	if err != nil {
		t.Fatalf("folding the covered-up stream: %v", err)
	}

	poisoned := folded.State()
	if poisoned.InvalidReason == "" {
		t.Fatal("fixture: want the cover-up fold poisoned")
	}

	data, err := json.Marshal(poisoned)
	if err != nil {
		t.Fatalf("marshaling the poisoned state: %v", err)
	}

	var restored lifecycle.State
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}

	if restored.InvalidReason != poisoned.InvalidReason {
		t.Errorf("want the poison mark to survive the round trip, got %q (was %q)", restored.InvalidReason, poisoned.InvalidReason)
	}
}

// TestStateJSONRoundTrip_PreservesCutoverRevision pins the revision's
// persistence through the state codec: the revision is the routing fencing
// token, and external serialization that dropped it would restart fencing
// at zero.
func TestStateJSONRoundTrip_PreservesCutoverRevision(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(2)

	completeFirstBuild(t, h.orchestrator)

	state, err := h.orchestrator.Get(t.Context(), "orders")
	if err != nil {
		t.Fatalf("getting state: %v", err)
	}

	if state.CutoverRevision != 1 {
		t.Fatalf("fixture: want cutover revision 1 after the first build, got %d", state.CutoverRevision)
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	var restored lifecycle.State
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}

	if restored.CutoverRevision != 1 {
		t.Errorf("want cutover revision 1 to survive the round trip, got %d", restored.CutoverRevision)
	}
}

// TestObservation_RefusesAForeignNamedStream pins the address check on the
// one load path: a stream whose events name another projection — written
// there by a bug or by tampering — folds clean on its own terms, so only
// the check against the addressing name can refuse it. Observation and
// every command entry run the same check; a foreign fold must never be
// served or acted on as "orders".
func TestObservation_RefusesAForeignNamedStream(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	appendRawLifecycleEvent(t, h.events, lifecycle.RebuildInitiated{
		Attempt: uuid.Must(uuid.NewV4()),
		Target:  projection.ID{Name: "customers", Version: 1},
		Reason:  "foreign history at the orders address",
		At:      time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
	})

	if _, err := h.orchestrator.Get(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
		t.Errorf("want Get refused with ErrInvalidState, got %v", err)
	}

	if _, err := h.orchestrator.Resume(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrInvalidState) {
		t.Errorf("want Resume refused with ErrInvalidState, got %v", err)
	}
}

// ambiguousPromotedWriter delegates appends and, once, loses the response of
// the append recording Promoted: the event is durable, the caller sees an
// unmarked error.
type ambiguousPromotedWriter struct {
	eventstore.Store
	mu    sync.Mutex
	fired bool
}

func (w *ambiguousPromotedWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	written, err := w.Store.AppendStream(ctx, streamID, events, opts)
	if err != nil {
		return written, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.fired {
		return written, nil
	}

	for _, event := range events {
		if event.Type == (lifecycle.Promoted{}).EventType() {
			w.fired = true
			return nil, errors.New("append response lost")
		}
	}

	return written, nil
}

// droppingPromotedWriter swallows appends that record Promoted — nothing
// lands, the caller sees an unmarked error — until the configured number of
// drops is spent, and counts the attempts.
type droppingPromotedWriter struct {
	eventstore.Store
	mu       sync.Mutex
	drops    int
	attempts int
}

func (w *droppingPromotedWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	w.mu.Lock()
	promoted := false
	for _, event := range events {
		if event.Type == (lifecycle.Promoted{}).EventType() {
			promoted = true
		}
	}

	if promoted {
		w.attempts++
		if w.drops != 0 {
			w.drops--
			w.mu.Unlock()

			return nil, errors.New("append response lost")
		}
	}
	w.mu.Unlock()

	return w.Store.AppendStream(ctx, streamID, events, opts)
}

func (w *droppingPromotedWriter) attemptCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.attempts
}

// TestRun_UnknownPromotionOutcomeKeepsTailing pins the reconciliation half
// of auto-promotion: an append that is durable but loses its response
// resolves to no outcome, and the run must not stop the live projection's
// only processor over it — it keeps tailing, proves the promotion from the
// lifecycle stream, and installs the observed history in the handle.
func TestRun_UnknownPromotionOutcomeKeepsTailing(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &ambiguousPromotedWriter{Store: events}
	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("ambiguous auto-promotion")
	cancel, done := runAsync(t, r)

	v1 := projection.ID{Name: "orders", Version: 1}

	// The Promoted event is durable regardless of the lost response: reads
	// flip to v1.
	h.waitLive(v1)

	// The handle reconciles the durable promotion from history.
	waitPhase(t, r, lifecycle.PhasePromoted)

	// The processor is still tailing: new domain events keep flowing into
	// the live table.
	h.appendDomain(2)
	waitFor(t, func() bool { return len(h.model.table(v1)) == 5 })

	if got := countEventsOfType(t, h.events, (lifecycle.Promoted{}).EventType()); got != 1 {
		t.Errorf("want exactly one Promoted event recorded, got %d", got)
	}

	select {
	case err := <-done:
		t.Fatalf("want the run still tailing through the unknown promotion outcome, got exit: %v", err)
	default:
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

// TestRun_UnknownPromotionOutcomeRetriesThePromotion pins the arbitration
// half: while the contended slot stays empty, an empty fold proves nothing —
// the lost append could still land — so reconciliation retries the promotion
// at the certified version and lets the stream decide. Here the first append
// vanished entirely, and the retry promotes.
func TestRun_UnknownPromotionOutcomeRetriesThePromotion(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: 1}
	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("vanished auto-promotion")
	cancel, done := runAsync(t, r)

	v1 := projection.ID{Name: "orders", Version: 1}

	// The retried promotion lands and reads flip.
	h.waitLive(v1)
	waitPhase(t, r, lifecycle.PhasePromoted)

	if got := countEventsOfType(t, h.events, (lifecycle.Promoted{}).EventType()); got != 1 {
		t.Errorf("want exactly one Promoted event recorded, got %d", got)
	}

	select {
	case err := <-done:
		t.Fatalf("want the run still tailing after the reconciled promotion, got exit: %v", err)
	default:
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

// TestRun_AmbiguousPromotionCedesToAbandon pins reconciliation's exit: it
// holds the run alive only while the promotion is unproven, and an Abandon
// ending the attempt is proof — the run winds down deliberately, reporting
// the recorded abandonment as a clean stop, with no Promoted ever recorded.
func TestRun_AmbiguousPromotionCedesToAbandon(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: -1}
	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("promotion that never lands")
	_, done := runAsync(t, r)

	// Reconciliation is live: the promotion has been attempted more than
	// once, every attempt swallowed without an outcome.
	waitFor(t, func() bool { return writer.attemptCount() >= 2 })

	if err := r.Abandon(t.Context(), "giving up on the ambiguous promotion"); err != nil {
		t.Fatalf("abandoning during promotion reconciliation: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Fatalf("want Run to wind down nil after the abandon, got %v", err)
	}

	if got := countEventsOfType(t, events, (lifecycle.Promoted{}).EventType()); got != 0 {
		t.Errorf("want no Promoted event recorded, got %d", got)
	}

	if got := r.State().Attempt.Phase; got != lifecycle.PhaseNone {
		t.Errorf("want the attempt slot vacant after the abandon, got %s", got)
	}
}

// pausingOutcomeError blocks its Nth Unwrap call until released, so a test
// can interleave work into the window where a save error's outcome is being
// resolved outside the handle lock.
type pausingOutcomeError struct {
	err     error
	calls   *atomic.Int32
	pauseAt int32
	paused  chan struct{}
	resume  chan struct{}
}

func (e *pausingOutcomeError) Error() string { return e.err.Error() }

func (e *pausingOutcomeError) Unwrap() error {
	if e.calls.Add(1) == e.pauseAt {
		close(e.paused)
		<-e.resume
	}

	return e.err
}

// pausingPromotedWriter lands the append recording Promoted, once, then
// returns the configured lost-response error in place of the result.
type pausingPromotedWriter struct {
	eventstore.Store
	mu    sync.Mutex
	fired bool
	errFn func() error
}

func (w *pausingPromotedWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	written, err := w.Store.AppendStream(ctx, streamID, events, opts)
	if err != nil {
		return written, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.fired {
		return written, nil
	}

	for _, event := range events {
		if event.Type == (lifecycle.Promoted{}).EventType() {
			w.fired = true
			return nil, w.errFn()
		}
	}

	return written, nil
}

// countingVersionedStore records the Count bound of every versioned read of
// the lifecycle stream — exactly the slots reconciliation folds to.
type countingVersionedStore struct {
	eventstore.Store
	mu     sync.Mutex
	counts []int64
}

func (s *countingVersionedStore) ReadStream(ctx context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	if streamID == ordersLifecycleStreamID() && opts.Count > 0 {
		s.mu.Lock()
		s.counts = append(s.counts, opts.Count)
		s.mu.Unlock()
	}

	return s.Store.ReadStream(ctx, streamID, opts)
}

func (s *countingVersionedStore) versionedReads() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.counts)
}

// streamVersionOfType returns the stream version of the first lifecycle
// event of the given type.
func streamVersionOfType(t *testing.T, events *esmemory.EventStore, eventType string) int64 {
	t.Helper()

	iter, err := events.ReadStream(t.Context(), ordersLifecycleStreamID(), eventstore.ReadStreamOptions{})
	if err != nil {
		t.Fatalf("reading lifecycle stream: %v", err)
	}
	defer iter.Close(t.Context())

	for {
		event, err := iter.Next(t.Context())
		if err != nil {
			t.Fatalf("no %s event recorded", eventType)
		}

		if event.ID.Type == eventType {
			return event.StreamVersion
		}
	}
}

// TestRun_ReconcileFoldsTheContendedSlot pins where promotion reconciliation
// gets its slot: from the append itself, not from handle state that may have
// moved since. While the lost append's outcome is being resolved, a policy
// transition advances the stream and the head reconcile loop installs the
// newer view — and the reconcile fold must still be bounded at the Promoted
// event's own version, prove the promotion, and keep the run tailing.
func TestRun_ReconcileFoldsTheContendedSlot(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	paused := make(chan struct{})
	resume := make(chan struct{})
	calls := &atomic.Int32{}

	writer := &pausingPromotedWriter{Store: events, errFn: func() error {
		return &pausingOutcomeError{
			err:     errors.New("append response lost"),
			calls:   calls,
			pauseAt: 4,
			paused:  paused,
			resume:  resume,
		}
	}}
	reader := &countingVersionedStore{Store: writer}
	h := buildHarnessWithLifecycleEvents(t, events, reader, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("promotion slot arbitration")
	cancel, done := runAsync(t, r)

	// The promotion lands durably, its response is lost, and resolution of
	// the save's outcome pauses outside the handle lock.
	select {
	case <-paused:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for outcome resolution to pause")
	}

	// While resolution is paused, an audited policy transition advances the
	// lifecycle stream, and the head reconcile loop installs the newer view
	// into the handle.
	if err := h.orchestrator.SetRetirementPolicy(t.Context(), "orders", lifecycle.RetirementPolicyChange{
		Unwitnessed: true, Actor: "test", Reason: "advance the stream",
	}); err != nil {
		t.Fatalf("setting retirement policy: %v", err)
	}

	waitFor(t, func() bool { return r.State().RetirementPolicy.Generation == 1 })

	close(resume)

	// The reconcile fold is bounded at the slot the promotion contended for
	// — the Promoted event's own version — and proves the promotion.
	waitFor(t, func() bool { return len(reader.versionedReads()) > 0 })

	promotedAt := streamVersionOfType(t, events, (lifecycle.Promoted{}).EventType())
	if got := reader.versionedReads()[0]; got != promotedAt {
		t.Errorf("want the reconcile fold bounded at the promotion's slot %d, got %d", promotedAt, got)
	}

	waitPhase(t, r, lifecycle.PhasePromoted)

	if got := countEventsOfType(t, events, (lifecycle.Promoted{}).EventType()); got != 1 {
		t.Errorf("want exactly one Promoted event recorded, got %d", got)
	}

	select {
	case err := <-done:
		t.Fatalf("want the run still tailing after the reconciled promotion, got exit: %v", err)
	default:
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

// parkAfterVersionedStore parks the first unversioned lifecycle read that
// begins after a versioned lifecycle read has been served, holding the
// stream's state as of park time and serving exactly that snapshot once
// released.
type parkAfterVersionedStore struct {
	eventstore.Store
	mu        sync.Mutex
	versioned bool
	done      bool
	parked    chan struct{}
	release   chan struct{}
}

func (s *parkAfterVersionedStore) ReadStream(ctx context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	if streamID != ordersLifecycleStreamID() {
		return s.Store.ReadStream(ctx, streamID, opts)
	}

	s.mu.Lock()
	if opts.Count > 0 {
		s.versioned = true
		s.mu.Unlock()

		return s.Store.ReadStream(ctx, streamID, opts)
	}

	park := s.versioned && !s.done
	if park {
		s.done = true
	}
	s.mu.Unlock()

	if !park {
		return s.Store.ReadStream(ctx, streamID, opts)
	}

	// Snapshot the stream as of now, then hold the fold until released.
	iter, err := s.Store.ReadStream(ctx, streamID, opts)
	if err != nil {
		return nil, err
	}

	var snapshot []*eventstore.Event

	for {
		event, err := iter.Next(ctx)
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			break
		}

		if err != nil {
			return nil, err
		}

		snapshot = append(snapshot, event)
	}

	_ = iter.Close(ctx)

	close(s.parked)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return &sliceIterator{events: snapshot}, nil
}

type sliceIterator struct {
	events []*eventstore.Event
}

func (i *sliceIterator) Next(context.Context) (*eventstore.Event, error) {
	if len(i.events) == 0 {
		return nil, eventstore.ErrEndOfEventStream
	}

	event := i.events[0]
	i.events = i.events[1:]

	return event, nil
}

func (i *sliceIterator) Close(context.Context) error { return nil }

// TestRun_ReconciledObservationNeverRegressesTheHandle pins the install
// guard on observing a reconciled promotion: the full fold runs outside the
// lock, and a retirement completing while it is parked leaves the handle
// holding newer state than the fold — the stale observation must not
// overwrite it.
func TestRun_ReconciledObservationNeverRegressesTheHandle(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &ambiguousPromotedWriter{Store: events}
	parker := &parkAfterVersionedStore{Store: writer, parked: make(chan struct{}), release: make(chan struct{})}
	h := buildHarnessWithLifecycleEvents(t, events, parker,
		lifecycle.WithAutoPromote(true),
		lifecycle.WithReconcileInterval(time.Hour),
	)
	h.appendDomain(3)

	r := h.begin("observation racing a retirement")
	cancel, done := runAsync(t, r)

	// The slot fold proves the durable promotion; the observation fold that
	// follows parks with the stream as of the promotion.
	select {
	case <-parker.parked:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the observation fold to park")
	}

	// Retirement completes durably while the observation is parked: the
	// attempt slot is vacated, and the handle observes it.
	if err := r.Retire(t.Context()); err != nil {
		t.Fatalf("retiring while the observation fold is parked: %v", err)
	}

	if got := r.State().Attempt.Phase; got != lifecycle.PhaseNone {
		t.Fatalf("want the attempt vacated after retirement, got %s", got)
	}

	close(parker.release)

	// The released observation reflects only the promotion; installing it
	// would regress the handle behind durable history.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := r.State().Attempt.Phase; got == lifecycle.PhasePromoted {
			t.Fatal("handle state regressed to PhasePromoted after durable retirement")
		}

		time.Sleep(2 * time.Millisecond)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the tailing run to report cancellation, got %v", err)
	}
}

// TestRun_AbandonWakesParkedReconciliation pins promotion reconciliation's
// wake condition: every stop that must end the run stops and joins the
// processor, and the exit wakes a reconciliation parked on its interval —
// Run winds down promptly no matter how long the interval is.
func TestRun_AbandonWakesParkedReconciliation(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: -1}
	h := buildHarnessWithLifecycleEvents(t, events, writer,
		lifecycle.WithAutoPromote(true),
		lifecycle.WithReconcileInterval(time.Hour),
	)
	h.appendDomain(3)

	r := h.begin("abandon during parked reconciliation")
	_, done := runAsync(t, r)

	// Reconciliation is live — the entry append and the first retry both
	// swallowed — and now waits out its interval.
	waitFor(t, func() bool { return writer.attemptCount() >= 2 })

	if err := r.Abandon(t.Context(), "give up on the ambiguous promotion"); err != nil {
		t.Fatalf("abandoning: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want a prompt clean wind-down after the abandon, got %v", err)
	}
}

// TestRun_ParkedReconciliationReportsCancellation pins what a canceled run
// reports from inside promotion reconciliation: the context's error, on
// every schedule — never the stale lost-append error, whichever side of the
// exit-order race the cancellation lands on.
func TestRun_ParkedReconciliationReportsCancellation(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: -1}
	h := buildHarnessWithLifecycleEvents(t, events, writer,
		lifecycle.WithAutoPromote(true),
		lifecycle.WithReconcileInterval(time.Hour),
	)
	h.appendDomain(3)

	r := h.begin("cancellation during parked reconciliation")
	cancel, done := runAsync(t, r)

	waitFor(t, func() bool { return writer.attemptCount() >= 2 })

	cancel()

	err := waitDone(t, done)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want cancellation to surface the context's error, got %v", err)
	}

	if err != nil && strings.Contains(err.Error(), "append response lost") {
		t.Errorf("want the canceled run not to surface the stale lost-append error, got %v", err)
	}
}

// headBlockingStore, once armed, blocks unversioned reads of the lifecycle
// stream until their context ends, and counts the reads it blocked.
type headBlockingStore struct {
	eventstore.Store
	mu      sync.Mutex
	armed   bool
	blocked int
}

func (s *headBlockingStore) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.armed = true
}

func (s *headBlockingStore) blockedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.blocked
}

func (s *headBlockingStore) ReadStream(ctx context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	s.mu.Lock()
	block := s.armed && streamID == ordersLifecycleStreamID() && opts.Count == 0
	if block {
		s.blocked++
	}
	s.mu.Unlock()

	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	return s.Store.ReadStream(ctx, streamID, opts)
}

// TestRun_ForeignSlotWinnerWindsDownClean pins the verdict a foreign winner
// in the contested slot renders: an abandonment ending the attempt is a
// terminal state observed — the run winds down clean, through the same
// slot-defeat classification as any other lost append, even with ordinary
// head reconciliation unable to observe it.
func TestRun_ForeignSlotWinnerWindsDownClean(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: -1}
	blocker := &headBlockingStore{Store: writer}
	h := buildHarnessWithLifecycleEvents(t, events, blocker, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("foreign abandonment in the contested slot")
	_, done := runAsync(t, r)

	// Promotion reconciliation is live; ordinary head reconciliation is then
	// blocked, so only the slot fold can observe what follows.
	waitFor(t, func() bool { return writer.attemptCount() >= 1 })
	blocker.arm()
	waitFor(t, func() bool { return blocker.blockedCount() >= 1 })

	// A foreign abandonment durably wins the contested slot.
	abandoned, err := json.Marshal(lifecycle.Abandoned{Cause: "operator gave up elsewhere"})
	if err != nil {
		t.Fatalf("marshaling the abandonment: %v", err)
	}

	if _, err := events.AppendStream(t.Context(), ordersLifecycleStreamID(), []*eventstore.WritableEvent{{
		Type: lifecycle.Abandoned{}.EventType(), Data: abandoned, DataContentType: "application/json",
	}}, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending the foreign abandonment: %v", err)
	}

	// The attempt ended in the exact slot the promotion contended for: the
	// run classifies the terminal state and winds down clean.
	if err := waitDone(t, done); err != nil {
		t.Errorf("want a clean wind-down after the foreign abandonment won the slot, got %v", err)
	}

	if got := r.State().Attempt.Phase; got != lifecycle.PhaseNone {
		t.Errorf("want the attempt slot vacant in the handle's view, got %s", got)
	}
}

// TestRun_ForeignClaimInSlotIsDisplacement pins the other verdict a foreign
// slot winner can render: a competing runner's claim over the same attempt
// is displacement, surfaced as ErrRunnerDisplaced — not the stale
// lost-append error.
func TestRun_ForeignClaimInSlotIsDisplacement(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: -1}
	blocker := &headBlockingStore{Store: writer}
	h := buildHarnessWithLifecycleEvents(t, events, blocker, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("foreign claim in the contested slot")
	_, done := runAsync(t, r)

	waitFor(t, func() bool { return writer.attemptCount() >= 1 })
	blocker.arm()
	waitFor(t, func() bool { return blocker.blockedCount() >= 1 })

	// A competing runner's claim over the same attempt durably wins the
	// contested slot.
	claim, err := json.Marshal(lifecycle.RunnerClaimed{
		Attempt:  r.State().Attempt.ID,
		Runner:   uuid.Must(uuid.NewV4()),
		Takeover: lifecycle.RunnerTakeover{Actor: "op", Reason: "competing takeover"},
		At:       time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("marshaling the competing claim: %v", err)
	}

	if _, err := events.AppendStream(t.Context(), ordersLifecycleStreamID(), []*eventstore.WritableEvent{{
		Type: lifecycle.RunnerClaimed{}.EventType(), Data: claim, DataContentType: "application/json",
	}}, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending the competing claim: %v", err)
	}

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Errorf("want the foreign claim surfaced as displacement, got %v", err)
	}
}

// versionedBlockingStore blocks versioned reads of the lifecycle stream
// until their context ends, and counts the reads it blocked: the slot fold
// hangs exactly as a stalled authority would leave it.
type versionedBlockingStore struct {
	eventstore.Store
	mu      sync.Mutex
	blocked int
}

func (s *versionedBlockingStore) blockedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.blocked
}

func (s *versionedBlockingStore) ReadStream(ctx context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	if streamID == ordersLifecycleStreamID() && opts.Count > 0 {
		s.mu.Lock()
		s.blocked++
		s.mu.Unlock()

		<-ctx.Done()

		return nil, ctx.Err()
	}

	return s.Store.ReadStream(ctx, streamID, opts)
}

// TestRun_ProcessorExitUnblocksAParkedSlotFold pins the other half of
// promotion reconciliation's wake condition: its folds run on the same
// processor-exit-canceled context as its waits, so a fold hung on a stalled
// authority cannot outlive the processor the reconciliation serves.
func TestRun_ProcessorExitUnblocksAParkedSlotFold(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: -1}
	blocker := &versionedBlockingStore{Store: writer}
	h := buildHarnessWithLifecycleEvents(t, events, blocker, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("slot fold hung on a stalled authority")
	_, done := runAsync(t, r)

	// The first reconcile fold is parked on the stalled authority.
	waitFor(t, func() bool { return blocker.blockedCount() >= 1 })

	if err := r.Abandon(t.Context(), "give up while the fold hangs"); err != nil {
		t.Fatalf("abandoning: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want a prompt clean wind-down after the abandon, got %v", err)
	}
}

// retryParkingPromotedWriter swallows the first append recording Promoted —
// the caller sees an unmarked error, nothing lands — and parks every later
// Promoted append on its context, honoring cancellation the way a real store
// does. parked signals once the first retry is parked.
type retryParkingPromotedWriter struct {
	eventstore.Store
	mu     sync.Mutex
	fired  bool
	once   sync.Once
	parked chan struct{}
}

func (w *retryParkingPromotedWriter) AppendStream(ctx context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	promoted := false
	for _, event := range events {
		if event.Type == (lifecycle.Promoted{}).EventType() {
			promoted = true
		}
	}

	if !promoted {
		return w.Store.AppendStream(ctx, streamID, events, opts)
	}

	w.mu.Lock()
	first := !w.fired
	w.fired = true
	w.mu.Unlock()

	if first {
		return nil, errors.New("append response lost")
	}

	w.once.Do(func() { close(w.parked) })
	<-ctx.Done()

	return nil, ctx.Err()
}

// TestRun_ParkedPromotionRetryUnblocksOnProcessorReturn pins the retry
// loop's cancellation source: the retrying Promote holds the handle lock
// while its append honors the reconciliation context, and exit publication
// needs that same lock — so the cancellation must come from the lock-free
// return signal. Waiting on the published exit instead deadlocks the run:
// the append waits for the cancellation, the cancellation waits for the
// published exit, and the publication waits for the lock the append holds.
func TestRun_ParkedPromotionRetryUnblocksOnProcessorReturn(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &retryParkingPromotedWriter{Store: events, parked: make(chan struct{})}
	h := buildHarnessWithLifecycleEvents(t, events, writer, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("promotion retry parked on a context-honoring store")
	_, done := runAsync(t, r)

	// The first Promoted append vanished without an outcome, and the retry
	// is parked inside the store, holding the handle lock.
	select {
	case <-writer.parked:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the promotion retry to park")
	}

	// The processor dies on its own while the retry holds the lock.
	errHandler := errors.New("handler died during the parked retry")
	h.model.armHandleFailureWith(errHandler)
	h.appendDomain(1)

	if err := waitDone(t, done); !errors.Is(err, errHandler) {
		t.Errorf("want the processor's own failure surfaced after the parked retry unblocked, got %v", err)
	}
}

// gatedVersionedBlockingStore, once armed, parks versioned reads of the
// lifecycle stream on their context — counting the reads it parked — and
// disarms without releasing them: reads issued after disarm pass through.
type gatedVersionedBlockingStore struct {
	eventstore.Store
	mu      sync.Mutex
	armed   bool
	blocked int
}

func (s *gatedVersionedBlockingStore) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.armed = true
}

func (s *gatedVersionedBlockingStore) disarm() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.armed = false
}

func (s *gatedVersionedBlockingStore) blockedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.blocked
}

func (s *gatedVersionedBlockingStore) ReadStream(ctx context.Context, streamID typeid.ID, opts eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	s.mu.Lock()
	block := s.armed && streamID == ordersLifecycleStreamID() && opts.Count > 0
	if block {
		s.blocked++
	}
	s.mu.Unlock()

	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	return s.Store.ReadStream(ctx, streamID, opts)
}

// TestRun_CededPromotionLossStillClassifiesTheSlot pins the loss verdict
// when promotion reconciliation cedes before ever reading its slot: a
// competing claim wins the contested slot and its claimant abandons, head
// reconciliation sees only the terminal end and records a clean stop, and
// the parked slot fold dies with the processor. The lost append's own
// captured slot must still classify the loss — the run reports the
// displacement, not a clean wind-down.
func TestRun_CededPromotionLossStillClassifiesTheSlot(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: -1}
	blocker := &gatedVersionedBlockingStore{Store: writer}
	h := buildHarnessWithLifecycleEvents(t, events, blocker, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("displacement hidden behind a reconciled end")
	_, done := runAsync(t, r)

	waitFor(t, func() bool { return writer.attemptCount() >= 1 })
	blocker.arm()
	waitFor(t, func() bool { return blocker.blockedCount() >= 1 })

	// The competing claim wins the contested slot and its claimant abandons
	// immediately after; only the captured slot still shows the defeat.
	appendRawLifecycleEvent(t, events, lifecycle.RunnerClaimed{
		Attempt:  r.State().Attempt.ID,
		Runner:   uuid.Must(uuid.NewV4()),
		Takeover: lifecycle.RunnerTakeover{Actor: "op", Reason: "competing takeover"},
		At:       time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
	})
	appendRawLifecycleEvent(t, events, lifecycle.Abandoned{Cause: "the winning claimant gave up"})
	blocker.disarm()

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrRunnerDisplaced) {
		t.Errorf("want the loss classified from the captured slot after ceding, got %v", err)
	}
}

// TestRun_PoisonedSlotHistoryFailsClosed pins how promotion reconciliation
// treats a slot fold that reads back but does not validate: the exact
// prefix is immutable, so the poison can never heal, and retrying it as a
// transient read failure spins forever. The run must fail closed with the
// validation verdict instead.
func TestRun_PoisonedSlotHistoryFailsClosed(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	writer := &droppingPromotedWriter{Store: events, drops: -1}
	blocker := &headBlockingStore{Store: writer}
	h := buildHarnessWithLifecycleEvents(t, events, blocker, lifecycle.WithAutoPromote(true))
	h.appendDomain(3)

	r := h.begin("poisoned history in the contested slot")
	_, done := runAsync(t, r)

	waitFor(t, func() bool { return writer.attemptCount() >= 1 })
	blocker.arm()
	waitFor(t, func() bool { return blocker.blockedCount() >= 1 })

	// A fold-poisoning event wins the contested slot; with head reads
	// blocked, only the slot fold can render the verdict.
	appendRawLifecycleEvent(t, events, lifecycle.BuildStarted{})

	if err := waitDone(t, done); !errors.Is(err, lifecycle.ErrInvalidState) {
		t.Errorf("want the immutable poisoned prefix to fail the run closed, got %v", err)
	}
}

// TestRun_StandingClaimRefusesTransparentSupersession pins the takeover
// gate: while the incumbent's recorded claim is standing — no processor
// exit recorded — a second Run must refuse rather than claim transparently.
// Nothing fences data-plane writes within one attempt: both runners would
// write the same target storage and checkpoint, and an order-sensitive
// handler could apply an older event over a newer one, permanently
// regressing a promoted projection. The refusal appends nothing and leaves
// the incumbent untouched.
func TestRun_StandingClaimRefusesTransparentSupersession(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r1 := h.begin("incumbent with a live processor")
	cancel1, done1 := runAsync(t, r1)
	waitPhase(t, r1, lifecycle.PhaseCaughtUp)

	r2, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming the second runner: %v", err)
	}

	if err := r2.Run(t.Context()); !errors.Is(err, lifecycle.ErrClaimStanding) {
		t.Fatalf("want the standing claim to refuse the second run with ErrClaimStanding, got %v", err)
	}

	// The refusal precedes any append: the incumbent's claim is the only
	// one, and no second build ever started.
	if got := countEventsOfType(t, h.events, lifecycle.RunnerClaimed{}.EventType()); got != 1 {
		t.Errorf("want only the incumbent's claim durable, got %d", got)
	}

	// The incumbent is untouched: still certified, still promotable.
	if err := r1.Promote(t.Context()); err != nil {
		t.Errorf("want the incumbent still promotable after the refused takeover, got %v", err)
	}

	cancel1()

	if err := waitDone(t, done1); !errors.Is(err, context.Canceled) {
		t.Errorf("want the incumbent's tailing run to report cancellation, got %v", err)
	}
}

// TestRun_TakeoverClaimsACrashedRunnersAttempt pins the operator recovery
// path: a crashed runner records no release, so its claim stands and a
// plain Run is refused — and an explicitly attested takeover claims the
// attempt, recording the attestation durably in the claim that won.
func TestRun_TakeoverClaimsACrashedRunnersAttempt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("claimed by a runner that then crashes")
	attemptID := r.State().Attempt.ID

	// The crashed incumbent: a durable claim and start with no release —
	// exactly what a process that died mid-build leaves behind.
	appendRawLifecycleEvent(t, h.events, lifecycle.RunnerClaimed{
		Attempt: attemptID,
		Runner:  uuid.Must(uuid.NewV4()),
		At:      time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
	})
	appendRawLifecycleEvent(t, h.events, lifecycle.BuildStarted{})

	plain, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming for the plain run: %v", err)
	}

	if err := plain.Run(t.Context()); !errors.Is(err, lifecycle.ErrClaimStanding) {
		t.Fatalf("want the crashed runner's standing claim to refuse a plain run, got %v", err)
	}

	resumed, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming for the takeover: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	done := make(chan error, 1)

	go func() {
		done <- resumed.Run(ctx, lifecycle.WithTakeover("op", "incumbent process crashed"))
	}()

	waitPhase(t, resumed, lifecycle.PhaseCaughtUp)

	// The takeover's attestation is durable in the claim that won.
	var attested int

	for _, raw := range rawEventsOfType(t, h.events, lifecycle.RunnerClaimed{}.EventType()) {
		var claim lifecycle.RunnerClaimed
		if err := json.Unmarshal(raw, &claim); err != nil {
			t.Fatalf("decoding claim: %v", err)
		}

		if claim.Takeover != (lifecycle.RunnerTakeover{}) {
			attested++

			if claim.Takeover.Actor != "op" || claim.Takeover.Reason != "incumbent process crashed" {
				t.Errorf("want the takeover's actor and reason recorded, got %+v", claim.Takeover)
			}
		}
	}

	if attested != 1 {
		t.Errorf("want exactly the takeover claim attested, got %d", attested)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the takeover run to report cancellation, got %v", err)
	}
}

// TestRun_ProcessorFailureReleasesTheClaim pins the wind-down release on the
// failure path: a run whose processor dies still releases its claim once the
// processor has fully exited, so a successor is admitted transparently
// instead of needing an attested takeover.
func TestRun_ProcessorFailureReleasesTheClaim(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("processor will die")
	_, done := runAsync(t, r)
	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	errHandler := errors.New("handler died mid-tail")
	h.model.armHandleFailureWith(errHandler)
	h.appendDomain(1)

	if err := waitDone(t, done); !errors.Is(err, errHandler) {
		t.Fatalf("want the processor's failure surfaced, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RunnerReleased{}.EventType()); got != 1 {
		t.Fatalf("want the failed run's claim released, got %d releases", got)
	}

	// The release admits the successor transparently: its run fails on the
	// still-failing handler, never on the claim gate.
	resumed, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming after the failure: %v", err)
	}

	if err := resumed.Run(t.Context()); errors.Is(err, lifecycle.ErrClaimStanding) || !errors.Is(err, errHandler) {
		t.Errorf("want the successor admitted past the released claim and failed by its handler, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RunnerClaimed{}.EventType()); got != 2 {
		t.Errorf("want the successor's transparent claim recorded (2 total), got %d", got)
	}
}

// TestRun_RefusedStartReleasesTheClaim pins the release on Run's own
// refusal paths after the claim is durable: a handler factory failure
// starts no processor, and the claim it strands must still be released so
// a successor is admitted transparently.
func TestRun_RefusedStartReleasesTheClaim(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	appendDomainTo(t, events, 3)

	errFactory := errors.New("factory refused")
	failing := &atomic.Bool{}
	failing.Store(true)

	model := newReadModel()
	orchestrator := bareOrchestrator(t, events, cpmemory.NewCheckpointStore(), func(id projection.ID) (projection.EventHandler, error) {
		if failing.Load() {
			return nil, errFactory
		}

		return model.handler(id)
	})

	r, err := orchestrator.Begin(t.Context(), "orders", "factory will refuse")
	if err != nil {
		t.Fatalf("beginning rebuild: %v", err)
	}

	if err := r.Run(t.Context()); !errors.Is(err, errFactory) {
		t.Fatalf("want the factory failure surfaced, got %v", err)
	}

	if got := countEventsOfType(t, events, lifecycle.RunnerReleased{}.EventType()); got != 1 {
		t.Fatalf("want the refused start's claim released, got %d releases", got)
	}

	// The release admits the successor transparently once the factory heals.
	failing.Store(false)

	resumed, err := orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming after the refused start: %v", err)
	}

	cancel, done := runAsync(t, resumed)
	waitPhase(t, resumed, lifecycle.PhaseCaughtUp)

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the successor's run to report cancellation, got %v", err)
	}
}

// TestRun_TakeoverOptionValidation pins WithTakeover's refusals: a takeover
// without an actor and reason is refused before anything is read or
// claimed, as is a nil option.
func TestRun_TakeoverOptionValidation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	r := h.begin("takeover option validation")

	if err := r.Run(t.Context(), lifecycle.WithTakeover("", "reason without an actor")); err == nil ||
		!strings.Contains(err.Error(), "actor and a reason") {
		t.Errorf("want the actorless takeover refused, got %v", err)
	}

	second, err := h.orchestrator.Resume(t.Context(), "orders")
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if err := second.Run(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "must not be nil") {
		t.Errorf("want the nil option refused, got %v", err)
	}

	if got := countEventsOfType(t, h.events, lifecycle.RunnerClaimed{}.EventType()); got != 0 {
		t.Errorf("want no claim recorded by refused runs, got %d", got)
	}
}

// TestRun_TakeoverAppliesOnlyToAStandingClaim pins the attestation's scope:
// a Run carrying WithTakeover but finding no standing claim records a plain
// claim — over a vacant claim there is nothing the attestation could
// attest, and recording it would poison the fold.
func TestRun_TakeoverAppliesOnlyToAStandingClaim(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("takeover over a vacant claim")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	done := make(chan error, 1)

	go func() {
		done <- r.Run(ctx, lifecycle.WithTakeover("op", "mistaken precaution"))
	}()

	waitPhase(t, r, lifecycle.PhaseCaughtUp)

	for _, raw := range rawEventsOfType(t, h.events, lifecycle.RunnerClaimed{}.EventType()) {
		var claim lifecycle.RunnerClaimed
		if err := json.Unmarshal(raw, &claim); err != nil {
			t.Fatalf("decoding claim: %v", err)
		}

		if claim.Takeover != (lifecycle.RunnerTakeover{}) {
			t.Errorf("want the vacant claim recorded plain, got attestation %+v", claim.Takeover)
		}
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want the run to report cancellation, got %v", err)
	}
}
