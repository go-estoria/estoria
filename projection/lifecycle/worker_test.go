package lifecycle_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	cpmemory "github.com/go-estoria/estoria/projection/checkpointstore/memory"
	"github.com/go-estoria/estoria/projection/lifecycle"
	"github.com/go-estoria/estoria/projection/processor"
)

// recordingEffect captures every live version it is applied with, failing
// while armed to fail.
type recordingEffect struct {
	mu      sync.Mutex
	applied []projection.ID
	fail    bool
}

func (e *recordingEffect) apply(_ context.Context, live projection.ID) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.fail {
		return errors.New("effect failed")
	}

	e.applied = append(e.applied, live)

	return nil
}

func (e *recordingEffect) setFail(fail bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.fail = fail
}

func (e *recordingEffect) seen() []projection.ID {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]projection.ID(nil), e.applied...)
}

// TestWorker_AppliesCutoversInStreamOrder pins the worker's core guarantee:
// every promotion and rollback is applied, in the order the lifecycle
// streams recorded them, ending on the recorded truth.
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

	effect := &recordingEffect{}
	router := lifecycle.NewMemoryRouter()

	worker, err := lifecycle.NewWorker(events, cpmemory.NewCheckpointStore(),
		lifecycle.WithEffect(effect.apply),
		lifecycle.WithLiveSetter(router),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = worker.Run(t.Context())
	}()

	t.Cleanup(func() { <-done })

	waitFor(t, func() bool { return len(effect.seen()) == 3 })

	want := []projection.ID{ordersV1, ordersV2, ordersV1}
	for i, id := range effect.seen() {
		if id != want[i] {
			t.Fatalf("want cutovers applied in stream order %v, got %v", want, effect.seen())
		}
	}

	if live, err := router.Live(t.Context(), "orders"); err != nil || live != ordersV1 {
		t.Errorf("want the router ending on the recorded truth %s, got %s (%v)", ordersV1, live, err)
	}
}

// TestWorker_FailedEffectStopsAndResumes pins stop-on-error delivery: a
// failed effect stops the worker with the cutover still ahead of the
// checkpoint, and a fresh worker over the same checkpoint redelivers it.
func TestWorker_FailedEffectStopsAndResumes(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	checkpoints := cpmemory.NewCheckpointStore()
	effect := &recordingEffect{}
	effect.setFail(true)

	first, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithEffect(effect.apply),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	if err := first.Run(t.Context()); err == nil {
		t.Fatal("want the failed effect to stop the worker, got nil")
	}

	if got := len(effect.seen()); got != 0 {
		t.Fatalf("want no effect applied while failing, got %d", got)
	}

	// A fresh worker resumes from the checkpoint and redelivers the cutover.
	effect.setFail(false)

	second, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithEffect(effect.apply),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating second worker: %v", err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = second.Run(t.Context())
	}()

	t.Cleanup(func() { <-done })

	waitFor(t, func() bool { return len(effect.seen()) == 1 })

	if got := effect.seen()[0]; got != ordersV1 {
		t.Errorf("want the redelivered cutover for %s, got %s", ordersV1, got)
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

	// An admitted, started build — no cutover yet — alongside domain events.
	aggregate := projections.New(lifecycle.StreamUUID("orders"))
	aggregate.Append(
		lifecycle.RebuildInitiated{Target: projection.ID{Name: "orders", Version: 1}, Reason: "no cutover", At: initiatedAt},
		lifecycle.BuildStarted{},
	)

	if err := projections.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving lifecycle aggregate: %v", err)
	}

	effect := &recordingEffect{}
	checkpoints := cpmemory.NewCheckpointStore()

	worker, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithEffect(effect.apply),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = worker.Run(t.Context())
	}()

	t.Cleanup(func() { <-done })

	// The worker's checkpoint appearing means it drained past the events.
	waitFor(t, func() bool {
		_, err := checkpoints.Load(t.Context(), lifecycle.DefaultWorkerCheckpointID)
		return err == nil
	})

	if got := effect.seen(); len(got) != 0 {
		t.Errorf("want no effects for non-cutover events, got %v", got)
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
	effect := &recordingEffect{}

	worker, err := lifecycle.NewWorker(events, checkpoints,
		lifecycle.WithEffect(effect.apply),
		lifecycle.WithCheckpointIdentity(custom),
		lifecycle.WithWorkerProcessorOptions(processor.WithPollInterval(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = worker.Run(t.Context())
	}()

	t.Cleanup(func() { <-done })

	waitFor(t, func() bool { return len(effect.seen()) == 1 })

	if _, err := checkpoints.Load(t.Context(), custom); err != nil {
		t.Errorf("want the worker checkpointed under %s, got %v", custom, err)
	}

	if _, err := checkpoints.Load(t.Context(), lifecycle.DefaultWorkerCheckpointID); !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		t.Errorf("want no checkpoint under the default identity, got %v", err)
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
		{"rejects a worker with no effects", nil},
		{"rejects a nil effect", []lifecycle.WorkerOption{lifecycle.WithEffect(nil)}},
		{"rejects a nil live setter", []lifecycle.WorkerOption{lifecycle.WithLiveSetter(nil)}},
		{"rejects a nil processor option", []lifecycle.WorkerOption{
			lifecycle.WithEffect((&recordingEffect{}).apply),
			lifecycle.WithWorkerProcessorOptions(nil),
		}},
		{"rejects an invalid checkpoint identity", []lifecycle.WorkerOption{
			lifecycle.WithEffect((&recordingEffect{}).apply),
			lifecycle.WithCheckpointIdentity(projection.ID{Name: "Bad Name", Version: 1}),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := lifecycle.NewWorker(events, checkpoints, tt.opts...); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}
