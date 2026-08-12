package rebuild_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	cpmemory "github.com/go-estoria/estoria/projection/checkpointstore/memory"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/go-estoria/estoria/projection/rebuild"
	"github.com/go-estoria/estoria/typeid"
)

const waitTimeout = 5 * time.Second

// harness wires an orchestrator against shared in-memory stores: domain and
// rebuild events in one event store (the default deployment), a memory
// checkpoint store, and a MemoryRouter registered as the cutover LiveSetter.
type harness struct {
	t            *testing.T
	events       *esmemory.EventStore
	checkpoints  *cpmemory.CheckpointStore
	router       *rebuild.MemoryRouter
	rebuilds     aggregatestore.Store[rebuild.State]
	model        *readModel
	orchestrator *rebuild.Orchestrator
}

func newHarness(t *testing.T, opts ...rebuild.OrchestratorOption) *harness {
	t.Helper()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	rebuilds, err := rebuild.NewStore(events)
	if err != nil {
		t.Fatalf("creating rebuild store: %v", err)
	}

	checkpoints := cpmemory.NewCheckpointStore()
	router := rebuild.NewMemoryRouter()
	model := newReadModel()

	orchestrator, err := rebuild.NewOrchestrator(rebuild.Config{
		Events:      events,
		Checkpoints: checkpoints,
		Handler:     model.handler,
		Rebuilds:    rebuilds,
		Router:      router,
	}, append([]rebuild.OrchestratorOption{
		rebuild.WithLiveSetter(router),
		rebuild.WithProcessorOptions(processor.WithPollInterval(2 * time.Millisecond)),
	}, opts...)...)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

	return &harness{
		t:            t,
		events:       events,
		checkpoints:  checkpoints,
		router:       router,
		rebuilds:     rebuilds,
		model:        model,
		orchestrator: orchestrator,
	}
}

func (h *harness) appendDomain(n int) {
	h.t.Helper()

	events := make([]*eventstore.WritableEvent, 0, n)
	for range n {
		events = append(events, &eventstore.WritableEvent{Type: "ordertest", Data: []byte(`{}`)})
	}

	if _, err := h.events.AppendStream(h.t.Context(), typeid.NewV4("order"), events, eventstore.AppendStreamOptions{}); err != nil {
		h.t.Fatalf("appending %d domain events: %v", n, err)
	}
}

func (h *harness) begin(reason string) *rebuild.Rebuild {
	h.t.Helper()

	r, err := h.orchestrator.Begin(h.t.Context(), "orders", reason)
	if err != nil {
		h.t.Fatalf("beginning rebuild: %v", err)
	}

	return r
}

// runAsync runs the rebuild in the background. The returned channel receives
// Run's result exactly once.
func runAsync(t *testing.T, r *rebuild.Rebuild) (context.CancelFunc, <-chan error) {
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

func waitPhase(t *testing.T, r *rebuild.Rebuild, phase rebuild.Phase) {
	t.Helper()

	waitFor(t, func() bool { return r.State().Phase == phase })
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

// TestBlueGreen_AutoPromote is the full happy path: v1 built and promoted
// from nothing, then v2 built alongside the still-tailing v1 under live
// appends, auto-promoted, and v1 retired.
func TestBlueGreen_AutoPromote(t *testing.T) {
	t.Parallel()

	h := newHarness(t, rebuild.WithAutoPromote(true))
	h.appendDomain(20)

	v1 := projection.ID{Name: "orders", Version: 1}
	v2 := projection.ID{Name: "orders", Version: 2}

	r1 := h.begin("initial build")

	if state := r1.State(); state.Next != v1 || state.Previous.Version != 0 || state.Reason != "initial build" {
		t.Fatalf("want a first rebuild targeting %s, got %+v", v1, state)
	}

	cancel1, done1 := runAsync(t, r1)
	waitPhase(t, r1, rebuild.PhasePromoted)

	if live, err := h.router.Live(t.Context(), "orders"); err != nil || live != v1 {
		t.Fatalf("want live version %s after auto-promote, got %s (%v)", v1, live, err)
	}

	waitFor(t, func() bool { return len(h.model.table(v1)) == 20 })

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

	if state := r2.State(); state.Next != v2 || state.Previous != v1 {
		t.Fatalf("want a rebuild of %s to %s, got %+v", v1, v2, state)
	}

	_, done2 := runAsync(t, r2)
	waitPhase(t, r2, rebuild.PhasePromoted)

	if live, _ := h.router.Live(t.Context(), "orders"); live != v2 {
		t.Fatalf("want live version %s after auto-promote, got %s", v2, live)
	}

	if err := <-appendErr; err != nil {
		t.Fatalf("appending concurrently: %v", err)
	}

	// Both versions converge on the full history: v2 catches up, and the
	// retained v1 keeps tailing until it is retired.
	waitFor(t, func() bool { return len(h.model.table(v2)) == 50 })
	waitFor(t, func() bool { return len(h.model.table(v1)) == 50 })

	state := r2.State()
	if state.CaughtUpPos == 0 || state.CreatedAt.IsZero() || state.CaughtUpAt.IsZero() || state.PromotedAt.IsZero() {
		t.Errorf("want audit datapoints populated, got %+v", state)
	}

	// The operator stops the old projector, then retires it.
	cancel1()

	if err := waitDone(t, done1); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping v1 projector: %v", err)
	}

	if err := r2.Retire(t.Context()); err != nil {
		t.Fatalf("retiring previous version: %v", err)
	}

	if got := r2.State().Phase; got != rebuild.PhaseRetired {
		t.Errorf("want phase %s, got %s", rebuild.PhaseRetired, got)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down, got %v", v1, dropped)
	}

	if _, err := h.checkpoints.Load(t.Context(), v1); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want the retired version's checkpoint deleted, got %v", err)
	}

	_ = done2
}

// TestBlueGreen_ManualPromote pins the default gate: caught-up does not flip
// reads until Promote is called.
func TestBlueGreen_ManualPromote(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(10)

	r := h.begin("manual promote")

	_, _ = runAsync(t, r)
	waitPhase(t, r, rebuild.PhaseCaughtUp)

	if _, err := h.router.Live(t.Context(), "orders"); !errors.Is(err, rebuild.ErrNoLiveVersion) {
		t.Fatalf("want no live version before Promote, got %v", err)
	}

	if err := r.Promote(t.Context()); err != nil {
		t.Fatalf("promoting: %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	if live, _ := h.router.Live(t.Context(), "orders"); live != v1 {
		t.Errorf("want live version %s, got %s", v1, live)
	}

	if err := r.Promote(t.Context()); err == nil {
		t.Error("want an error promoting twice, got nil")
	}
}

// TestRollback pins the rollback path: reads revert to the previous version
// and the rebuild is terminal.
func TestRollback(t *testing.T) {
	t.Parallel()

	h := newHarness(t, rebuild.WithAutoPromote(true))
	h.appendDomain(5)

	v1 := projection.ID{Name: "orders", Version: 1}

	r1 := h.begin("initial build")
	cancel1, done1 := runAsync(t, r1)
	waitPhase(t, r1, rebuild.PhasePromoted)

	if err := r1.Rollback(t.Context()); err == nil {
		t.Error("want an error rolling back a first version with no predecessor, got nil")
	}

	cancel1()
	_ = waitDone(t, done1)

	r2 := h.begin("bad mapping")
	_, _ = runAsync(t, r2)
	waitPhase(t, r2, rebuild.PhasePromoted)

	if err := r2.Rollback(t.Context()); err != nil {
		t.Fatalf("rolling back: %v", err)
	}

	if live, _ := h.router.Live(t.Context(), "orders"); live != v1 {
		t.Errorf("want live version %s after rollback, got %s", v1, live)
	}

	if got := r2.State().Phase; got != rebuild.PhaseRolledBack {
		t.Errorf("want phase %s, got %s", rebuild.PhaseRolledBack, got)
	}

	if err := r2.Rollback(t.Context()); err == nil {
		t.Error("want an error rolling back twice, got nil")
	}

	if err := r2.Retire(t.Context()); err == nil {
		t.Error("want an error retiring a rolled-back rebuild, got nil")
	}
}

// TestAbandon pins abandonment mid-build: the decision is recorded, the
// processor stops (Run returns nil), and the next version's storage and
// checkpoint are cleaned up.
func TestAbandon(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(5)
	h.model.armGate()

	v1 := projection.ID{Name: "orders", Version: 1}

	r := h.begin("will be abandoned")
	_, done := runAsync(t, r)
	waitPhase(t, r, rebuild.PhaseBuilding)

	if err := r.Abandon(t.Context(), "wrong column mapping"); err != nil {
		t.Fatalf("abandoning: %v", err)
	}

	if err := waitDone(t, done); err != nil {
		t.Errorf("want Run to return nil after Abandon, got %v", err)
	}

	state := r.State()
	if state.Phase != rebuild.PhaseAbandoned || state.AbandonCause != "wrong column mapping" {
		t.Errorf("want an abandoned rebuild with its cause, got %+v", state)
	}

	if dropped := h.model.droppedTables(); len(dropped) != 1 || dropped[0] != v1 {
		t.Errorf("want %s torn down, got %v", v1, dropped)
	}

	if err := r.Abandon(t.Context(), "again"); err == nil {
		t.Error("want an error abandoning twice, got nil")
	}
}

// TestResumeAfterCrash pins crash recovery: a new handle loaded via Resume
// records BuildResumed and completes the build from the checkpoint.
func TestResumeAfterCrash(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)
	h.model.armGate()

	r := h.begin("will crash")
	cancel, done := runAsync(t, r)
	waitPhase(t, r, rebuild.PhaseBuilding)

	// The "crash": the run's context dies mid-build.
	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the crashed run to report cancellation, got %v", err)
	}

	h.model.releaseGate()

	resumed, err := h.orchestrator.Resume(t.Context(), r.ID().UUID)
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if got := resumed.State().Phase; got != rebuild.PhaseBuilding {
		t.Fatalf("want a resumed rebuild still %s, got %s", rebuild.PhaseBuilding, got)
	}

	_, _ = runAsync(t, resumed)
	waitPhase(t, resumed, rebuild.PhaseCaughtUp)

	v1 := projection.ID{Name: "orders", Version: 1}
	waitFor(t, func() bool { return len(h.model.table(v1)) == 3 })

	// The stream records the full story: created, started, resumed, caught up.
	loaded, err := h.rebuilds.Load(t.Context(), r.ID().UUID, nil)
	if err != nil {
		t.Fatalf("loading rebuild aggregate: %v", err)
	}

	if got := loaded.Version(); got != 4 {
		t.Errorf("want 4 recorded transitions (created, started, resumed, caught up), got %d", got)
	}
}

// TestCompetingOrchestrators pins the coordination story: two handles racing
// to promote are arbitrated by optimistic concurrency on the aggregate
// stream, and the loser observes the winner's transition after reloading.
func TestCompetingOrchestrators(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.appendDomain(3)

	r := h.begin("competing operators")
	_, _ = runAsync(t, r)
	waitPhase(t, r, rebuild.PhaseCaughtUp)

	first, err := h.orchestrator.Resume(t.Context(), r.ID().UUID)
	if err != nil {
		t.Fatalf("resuming first handle: %v", err)
	}

	second, err := h.orchestrator.Resume(t.Context(), r.ID().UUID)
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

	reloaded, err := h.orchestrator.Resume(t.Context(), r.ID().UUID)
	if err != nil {
		t.Fatalf("reloading after losing: %v", err)
	}

	if got := reloaded.State().Phase; got != rebuild.PhasePromoted {
		t.Errorf("want the loser to observe %s after reloading, got %s", rebuild.PhasePromoted, got)
	}
}

// TestSeparateRebuildStore runs the rebuild aggregates in their own event
// store, with a StreamRouter over it as both the orchestrator's router and
// (via a Refresh cutover hook) the live-version answer: domain and
// infrastructure streams never interleave.
func TestSeparateRebuildStore(t *testing.T) {
	t.Parallel()

	domainEvents, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating domain event store: %v", err)
	}

	rebuildEvents, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating rebuild event store: %v", err)
	}

	rebuilds, err := rebuild.NewStore(rebuildEvents)
	if err != nil {
		t.Fatalf("creating rebuild store: %v", err)
	}

	router, err := rebuild.NewStreamRouter(rebuildEvents)
	if err != nil {
		t.Fatalf("creating stream router: %v", err)
	}

	model := newReadModel()

	orchestrator, err := rebuild.NewOrchestrator(rebuild.Config{
		Events:      domainEvents,
		Checkpoints: cpmemory.NewCheckpointStore(),
		Handler:     model.handler,
		Rebuilds:    rebuilds,
		Router:      router,
	},
		rebuild.WithAutoPromote(true),
		rebuild.WithCutoverHook(func(ctx context.Context, _ projection.ID) error {
			return router.Refresh(ctx)
		}),
		rebuild.WithProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating orchestrator: %v", err)
	}

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
	waitPhase(t, r, rebuild.PhasePromoted)

	v1 := projection.ID{Name: "orders", Version: 1}

	if live, err := router.Live(t.Context(), "orders"); err != nil || live != v1 {
		t.Fatalf("want live version %s from the stream router, got %s (%v)", v1, live, err)
	}

	// The projection saw exactly the domain events: no infrastructure
	// streams interleave when the rebuild store is separate.
	waitFor(t, func() bool { return len(model.table(v1)) == 5 })
}

func TestBegin_InvalidName(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if _, err := h.orchestrator.Begin(t.Context(), "Bad Name", "reason"); err == nil {
		t.Error("want an error for an invalid projection name, got nil")
	}
}

func TestNewOrchestrator_Validation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	valid := rebuild.Config{
		Events:      h.events,
		Checkpoints: h.checkpoints,
		Handler:     h.model.handler,
		Rebuilds:    h.rebuilds,
		Router:      h.router,
	}

	for _, tt := range []struct {
		name   string
		mutate func(*rebuild.Config)
	}{
		{"rejects a nil global reader", func(c *rebuild.Config) { c.Events = nil }},
		{"rejects a nil checkpoint store", func(c *rebuild.Config) { c.Checkpoints = nil }},
		{"rejects a nil handler factory", func(c *rebuild.Config) { c.Handler = nil }},
		{"rejects a nil rebuild store", func(c *rebuild.Config) { c.Rebuilds = nil }},
		{"rejects a nil router", func(c *rebuild.Config) { c.Router = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			tt.mutate(&config)

			if _, err := rebuild.NewOrchestrator(config); err == nil {
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
	mu      sync.Mutex
	tables  map[projection.ID][]int64
	dropped []projection.ID
	gate    chan struct{}
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

type readModelHandler struct {
	model *readModel
	id    projection.ID
}

func (h *readModelHandler) Handle(ctx context.Context, event *eventstore.Event) error {
	// Rebuild lifecycle events interleave with domain events on a shared
	// store; a projection handler filters by stream type.
	if event.StreamID.Type == rebuild.StreamType {
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

	delete(h.model.tables, id)
	h.model.dropped = append(h.model.dropped, id)

	return nil
}

var _ projection.Teardowner = (*readModelHandler)(nil)
