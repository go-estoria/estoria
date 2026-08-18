package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-estoria/estoria/eventstore"
	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	cpmemory "github.com/go-estoria/estoria/projection/checkpointstore/memory"
	"github.com/go-estoria/estoria/projection/lifecycle"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/go-estoria/estoria/typeid"
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

// TestWorker_AppliesCutoversInStreamOrder pins the worker's core guarantee:
// every promotion and rollback is delivered with its recorded revision, in
// the order the lifecycle streams recorded them, ending on the recorded
// truth.
func TestWorker_AppliesCutoversInStreamOrder(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)
	recordCutover(t, projections, ordersV2, ordersV1, true)

	recorder := &recordingSetter{}
	router := lifecycle.NewMemoryRouter()

	worker, err := lifecycle.NewWorker(events, cpmemory.NewCheckpointStore(),
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithCutoverSetter(router),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)

	waitFor(t, func() bool { return len(recorder.seen()) == 3 })

	want := []lifecycle.Cutover{
		{Live: ordersV1, Revision: 1},
		{Live: ordersV2, Revision: 2},
		{Live: ordersV1, Revision: 3},
	}
	for i, cutover := range recorder.seen() {
		if cutover != want[i] {
			t.Fatalf("want cutovers delivered in stream order %v, got %v", want, recorder.seen())
		}
	}

	if live, err := router.Live(t.Context(), "orders"); err != nil || live != ordersV1 {
		t.Errorf("want the router ending on the recorded truth %s, got %s (%v)", ordersV1, live, err)
	}

	if applied, err := router.AppliedCutover(t.Context(), "orders"); err != nil || applied != (lifecycle.Cutover{Live: ordersV1, Revision: 3}) {
		t.Errorf("want the router vouching for the final cutover, got %+v (%v)", applied, err)
	}
}

// TestWorker_FailedDeliveryStopsAndResumes pins stop-on-error delivery: a
// failed delivery stops the worker with the cutover still ahead of the
// checkpoint, and a fresh worker over the same checkpoint redelivers it.
func TestWorker_FailedDeliveryStopsAndResumes(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	checkpoints := cpmemory.NewCheckpointStore()
	setter := &recordingSetter{}
	setter.setFail(true)

	first, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	if err := first.Run(t.Context()); err == nil {
		t.Fatal("want the failed delivery to stop the worker, got nil")
	}

	if got := len(setter.seen()); got != 0 {
		t.Fatalf("want no cutover applied while failing, got %d", got)
	}

	// A fresh worker resumes from the checkpoint and redelivers the cutover.
	setter.setFail(false)

	second, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating second worker: %v", err)
	}

	startWorkerForTest(t, second)

	waitFor(t, func() bool { return len(setter.seen()) == 1 })

	if got := setter.seen()[0]; got != (lifecycle.Cutover{Live: ordersV1, Revision: 1}) {
		t.Errorf("want the redelivered cutover for %s at revision 1, got %+v", ordersV1, got)
	}
}

// TestWorker_StopOnErrorCannotBeDisabled pins the retry-contract pin: a
// caller-supplied WithContinueOnHandlerError(true) is overridden, so a
// failed delivery still stops the worker instead of being skipped, and the
// unadvanced checkpoint redelivers the cutover to a fresh worker.
func TestWorker_StopOnErrorCannotBeDisabled(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	checkpoints := cpmemory.NewCheckpointStore()
	setter := &recordingSetter{}
	setter.setFail(true)

	hostile, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithWorkerProcessorOptions(
			processor.WithContinueOnHandlerError(true),
			processor.WithPollInterval(2*time.Millisecond),
		),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	// The deadline bounds the failure mode: were the forwarded option to
	// win, the worker would skip the failed delivery and tail until the
	// deadline instead of returning the delivery error.
	runCtx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()

	if err := hostile.Run(runCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the failed delivery to stop the worker despite the forwarded option, got %v", err)
	}

	if got := len(setter.seen()); got != 0 {
		t.Fatalf("want no cutover applied while failing, got %d", got)
	}

	// The failed cutover stayed ahead of the checkpoint: a fresh worker
	// redelivers it rather than resuming past it.
	setter.setFail(false)

	second, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating second worker: %v", err)
	}

	startWorkerForTest(t, second)

	waitFor(t, func() bool { return len(setter.seen()) == 1 })

	if got := setter.seen()[0]; got != (lifecycle.Cutover{Live: ordersV1, Revision: 1}) {
		t.Errorf("want the redelivered cutover for %s at revision 1, got %+v", ordersV1, got)
	}
}

// TestWorker_RejectsInvalidCutovers pins the worker's semantic decode: a
// cutover event that decodes but records an invalid projection ID or a
// non-positive revision, or that lives on a stream its projection's name
// does not derive, stops the worker — the reserved namespace is a guardrail,
// and setters must not act on infrastructure state that fails its own
// scheme.
func TestWorker_RejectsInvalidCutovers(t *testing.T) {
	t.Parallel()

	appendRawCutover := func(t *testing.T, events *esmemory.EventStore, streamID typeid.ID, next projection.ID, revision int64) {
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

	for _, tt := range []struct {
		name     string
		streamID typeid.ID
		next     projection.ID
		revision int64
	}{
		{
			name:     "invalid live version",
			streamID: typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID("orders")},
			next:     projection.ID{Name: "orders", Version: 0},
			revision: 1,
		},
		{
			name:     "invalid cutover revision",
			streamID: typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID("orders")},
			next:     projection.ID{Name: "orders", Version: 1},
			revision: 0,
		},
		{
			name:     "foreignly addressed stream",
			streamID: typeid.NewV4(lifecycle.StreamType),
			next:     projection.ID{Name: "orders", Version: 1},
			revision: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events := newEventStore(t)
			appendRawCutover(t, events, tt.streamID, tt.next, tt.revision)

			setter := &recordingSetter{}

			worker, err := lifecycle.NewWorker(events, cpmemory.NewCheckpointStore(),
				lifecycle.WithCutoverSetter(setter),
				lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
			)
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			// The deadline bounds the failure mode: a worker that accepted the
			// invalid cutover would apply it and tail until the deadline
			// instead of returning a decode error.
			runCtx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()

			if err := worker.Run(runCtx); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want the invalid cutover to stop the worker, got %v", err)
			}

			if got := setter.seen(); len(got) != 0 {
				t.Errorf("want no deliveries from an invalid cutover, got %v", got)
			}
		})
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

	// An admitted, started build — no cutover yet — alongside a domain event.
	aggregate := projections.New(lifecycle.StreamUUID("orders"))
	aggregate.Append(
		lifecycle.RebuildInitiated{Target: projection.ID{Name: "orders", Version: 1}, Reason: "no cutover", At: initiatedAt},
		lifecycle.BuildStarted{},
	)

	if err := projections.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving lifecycle aggregate: %v", err)
	}

	if _, err := events.AppendStream(t.Context(), typeid.NewV4("order"),
		[]*eventstore.WritableEvent{{Type: "ordertest", Data: []byte(`{}`)}},
		eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending domain event: %v", err)
	}

	setter := &recordingSetter{}
	checkpoints := cpmemory.NewCheckpointStore()

	worker, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)

	// The worker's checkpoint appearing means it drained past the events.
	waitFor(t, func() bool {
		_, err := checkpoints.Load(t.Context(), lifecycle.DefaultWorkerCheckpointID)
		return err == nil
	})

	if got := setter.seen(); len(got) != 0 {
		t.Errorf("want no deliveries for non-cutover events, got %v", got)
	}
}

// TestWorker_CustomCheckpointIdentity pins WithCheckpointIdentity: progress
// is keyed under the configured ID, so distinct workers can track distinct
// effect sets.
func TestWorker_CustomCheckpointIdentity(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	custom := projection.ID{Name: "alias_swapper", Version: 1}
	checkpoints := cpmemory.NewCheckpointStore()
	setter := &recordingSetter{}

	worker, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithCheckpointIdentity(custom),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)

	waitFor(t, func() bool { return len(setter.seen()) == 1 })

	if _, err := checkpoints.Load(t.Context(), custom); err != nil {
		t.Errorf("want the worker checkpointed under %s, got %v", custom, err)
	}

	if _, err := checkpoints.Load(t.Context(), lifecycle.DefaultWorkerCheckpointID); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want no checkpoint under the default identity, got %v", err)
	}
}

// TestWorker_RedeliveryConvergesWithoutError pins the delivery half of the
// apply-if-newer contract end to end: a second worker with no progress of
// its own replays the entire cutover history against an already-converged
// setter, every delivery is a stale or idempotent no-op, and the worker
// tails on rather than wedging — redelivery is never an error.
func TestWorker_RedeliveryConvergesWithoutError(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)
	recordCutover(t, projections, ordersV2, ordersV1, true)

	router := lifecycle.NewMemoryRouter()
	recorder := &recordingSetter{}

	first, err := lifecycle.NewWorker(events, cpmemory.NewCheckpointStore(),
		lifecycle.WithCutoverSetter(router),
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, first)

	waitFor(t, func() bool { return len(recorder.seen()) == 3 })

	converged, err := router.AppliedCutover(t.Context(), "orders")
	if err != nil || converged != (lifecycle.Cutover{Live: ordersV1, Revision: 3}) {
		t.Fatalf("want the router converged on the final cutover, got %+v (%v)", converged, err)
	}

	// The second worker has a fresh checkpoint store: it replays the whole
	// history against the converged router. A delivery error would stop it
	// before the tally below completes.
	tally := &recordingSetter{}

	second, err := lifecycle.NewWorker(events, cpmemory.NewCheckpointStore(),
		lifecycle.WithCutoverSetter(router),
		lifecycle.WithCutoverSetter(tally),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating second worker: %v", err)
	}

	startWorkerForTest(t, second)

	waitFor(t, func() bool { return len(tally.seen()) == 3 })

	if applied, err := router.AppliedCutover(t.Context(), "orders"); err != nil || applied != converged {
		t.Errorf("want the redelivered history to leave the converged route %+v untouched, got %+v (%v)", converged, applied, err)
	}
}

func TestNewWorker_Validation(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)
	checkpoints := cpmemory.NewCheckpointStore()

	for _, tt := range []struct {
		name string
		opts []lifecycle.WorkerOption
	}{
		{"rejects a worker with no setters", nil},
		{"rejects a nil cutover setter", []lifecycle.WorkerOption{lifecycle.WithCutoverSetter(nil)}},
		{"rejects a nil processor option", []lifecycle.WorkerOption{
			lifecycle.WithCutoverSetter(&recordingSetter{}),
			lifecycle.WithWorkerProcessorOptions(nil),
		}},
		{"rejects an invalid checkpoint identity", []lifecycle.WorkerOption{
			lifecycle.WithCutoverSetter(&recordingSetter{}),
			lifecycle.WithCheckpointIdentity(projection.ID{Name: "Bad Name", Version: 1}),
		}},
		{"rejects a nil option", []lifecycle.WorkerOption{nil}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := lifecycle.NewWorker(events, checkpoints, tt.opts...); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}
