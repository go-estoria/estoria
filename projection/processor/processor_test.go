package processor_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/eventstore"
	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	cpmemory "github.com/go-estoria/estoria/projection/checkpointstore/memory"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/go-estoria/estoria/typeid"
)

const waitTimeout = 5 * time.Second

// shortPoll keeps at-head tests fast; production default is 1s.
const shortPoll = 5 * time.Millisecond

func TestNew(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	checkpoints := cpmemory.NewCheckpointStore()
	handler := &collector{}
	id := projection.ID{Name: "orders", Version: 1}

	for _, tt := range []struct {
		name string
		make func() (*processor.Processor, error)
	}{
		{"rejects a nil global reader", func() (*processor.Processor, error) {
			return processor.New(nil, checkpoints, id, handler)
		}},
		{"rejects a nil checkpoint store", func() (*processor.Processor, error) {
			return processor.New(events, nil, id, handler)
		}},
		{"rejects a nil handler", func() (*processor.Processor, error) {
			return processor.New(events, checkpoints, id, nil)
		}},
		{"rejects an invalid projection ID", func() (*processor.Processor, error) {
			return processor.New(events, checkpoints, projection.ID{Name: "Orders", Version: 1}, handler)
		}},
		{"rejects a non-positive poll interval", func() (*processor.Processor, error) {
			return processor.New(events, checkpoints, id, handler, processor.WithPollInterval(0))
		}},
		{"rejects a negative batch size", func() (*processor.Processor, error) {
			return processor.New(events, checkpoints, id, handler, processor.WithBatchSize(-1))
		}},
		{"rejects a non-positive checkpoint interval", func() (*processor.Processor, error) {
			return processor.New(events, checkpoints, id, handler, processor.WithCheckpointEvery(0))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := tt.make(); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}

	t.Run("accepts valid configuration", func(t *testing.T) {
		t.Parallel()

		p, err := processor.New(events, checkpoints, id, handler)
		if err != nil {
			t.Fatalf("creating processor: %v", err)
		}

		if p == nil {
			t.Error("want a processor, got nil")
		}
	})
}

// TestProcessor_ColdStart pins the replay path: a projection with no
// checkpoint processes the entire history in order and checkpoints the head.
func TestProcessor_ColdStart(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 5)

	handler := &collector{}
	p := newProcessor(t, events, checkpoints, handler)

	cancel, done := start(t, p)
	waitCaughtUp(t, p)

	assertPositions(t, handler.snapshot(), 1, 2, 3, 4, 5)

	if got := p.Position(); got != 5 {
		t.Errorf("want position 5, got %d", got)
	}

	if got := loadPosition(t, checkpoints); got != 5 {
		t.Errorf("want checkpointed position 5, got %d", got)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want Run to return the context's error on cancellation, got %v", err)
	}
}

// TestProcessor_Resume pins that a new processor for the same projection ID
// picks up from the checkpoint: no skipped events, no double handling.
func TestProcessor_Resume(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 5)

	first := &collector{}
	p1 := newProcessor(t, events, checkpoints, first)

	cancel1, done1 := start(t, p1)
	waitCaughtUp(t, p1)
	cancel1()

	if err := waitDone(t, done1); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping first processor: %v", err)
	}

	appendEvents(t, events, 3)

	second := &collector{}
	p2 := newProcessor(t, events, checkpoints, second)

	_, _ = start(t, p2)
	waitCaughtUp(t, p2)

	assertPositions(t, second.snapshot(), 6, 7, 8)
}

// TestProcessor_RedeliversAfterCheckpointFailure pins the at-least-once crash
// window: an event whose handling succeeded but whose checkpoint save did not
// is redelivered on restart.
func TestProcessor_RedeliversAfterCheckpointFailure(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 3)

	failing := &failingSaveStore{Store: checkpoints, failAt: 2}

	first := &collector{}
	p1 := newProcessor(t, events, failing, first)

	_, done1 := start(t, p1)

	err := waitDone(t, done1)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("want Run to return the checkpoint save failure, got %v", err)
	}

	assertPositions(t, first.snapshot(), 1, 2)

	second := &collector{}
	p2 := newProcessor(t, events, failing, second)

	_, _ = start(t, p2)
	waitCaughtUp(t, p2)

	// Event 2 was handled before the failed save, and again after restart:
	// that is the documented at-least-once window.
	assertPositions(t, second.snapshot(), 2, 3)
}

// TestProcessor_CaughtUpUnderConcurrentAppends pins the caught-up definition:
// a drain cycle that reaches the end of its read signals caught-up even while
// writers keep appending, and the processor goes on to consume what they wrote.
func TestProcessor_CaughtUpUnderConcurrentAppends(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 10)

	handler := &collector{}
	p := newProcessor(t, events, checkpoints, handler)

	appendErr := make(chan error, 1)
	go func() {
		for range 20 {
			if _, err := events.AppendStream(context.Background(), typeid.NewV4("processortest"),
				[]*eventstore.WritableEvent{{Type: "processortest", Data: []byte(`{}`)}},
				eventstore.AppendStreamOptions{}); err != nil {
				appendErr <- err
				return
			}

			time.Sleep(time.Millisecond)
		}

		appendErr <- nil
	}()

	_, _ = start(t, p)
	waitCaughtUp(t, p)

	if err := <-appendErr; err != nil {
		t.Fatalf("appending concurrently: %v", err)
	}

	waitForHandled(t, handler, 30)

	want := make([]int64, 0, 30)
	for i := int64(1); i <= 30; i++ {
		want = append(want, i)
	}

	assertPositions(t, handler.snapshot(), want...)
}

// TestProcessor_BatchSize pins that the batch size is passed through as the
// per-read Count and that draining spans multiple reads without losing events.
func TestProcessor_BatchSize(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 5)

	recorder := &readRecorder{inner: events}
	handler := &collector{}
	p := newProcessor(t, recorder, checkpoints, handler, processor.WithBatchSize(2))

	_, _ = start(t, p)
	waitCaughtUp(t, p)

	assertPositions(t, handler.snapshot(), 1, 2, 3, 4, 5)

	for i, count := range recorder.snapshot() {
		if count != 2 {
			t.Errorf("read %d: want Count 2, got %d", i, count)
		}
	}
}

// TestProcessor_CheckpointEvery pins the checkpoint cadence: every n handled
// events, plus the unconditional save at the end of the drain cycle.
func TestProcessor_CheckpointEvery(t *testing.T) {
	t.Parallel()

	events, _ := newStores(t)
	appendEvents(t, events, 5)

	recorder := &saveRecorder{Store: cpmemory.NewCheckpointStore()}
	handler := &collector{}
	p := newProcessor(t, events, recorder, handler, processor.WithCheckpointEvery(3))

	_, _ = start(t, p)
	waitCaughtUp(t, p)

	// Both the cadence save and the head save precede the caught-up signal.
	saves := recorder.snapshot()
	if len(saves) < 2 || saves[0] != 3 || saves[1] != 5 {
		t.Errorf("want saves at positions [3 5], got %v", saves)
	}
}

// TestProcessor_StopsOnHandlerError pins the default error behavior: the
// failed event stays ahead of the checkpoint, so a restart redelivers it.
func TestProcessor_StopsOnHandlerError(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 3)

	wantErr := errors.New("handler failed")
	handler := &collector{failAt: 2, failWith: wantErr}
	p := newProcessor(t, events, checkpoints, handler)

	_, done := start(t, p)

	if err := waitDone(t, done); !errors.Is(err, wantErr) {
		t.Errorf("want the handler error wrapped, got %v", err)
	}

	if got := loadPosition(t, checkpoints); got != 1 {
		t.Errorf("want the checkpoint held at 1, got %d", got)
	}
}

// TestProcessor_ContinuesOnHandlerError pins the opt-in: the failed event is
// advanced past and checkpointed, so it is not redelivered on restart.
func TestProcessor_ContinuesOnHandlerError(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 3)

	handler := &collector{failAt: 2, failWith: errors.New("handler failed")}
	p := newProcessor(t, events, checkpoints, handler,
		processor.WithContinueOnHandlerError(true),
		processor.WithLogger(discardLogger{}),
	)

	_, _ = start(t, p)
	waitCaughtUp(t, p)

	assertPositions(t, handler.snapshot(), 1, 2, 3)

	if got := loadPosition(t, checkpoints); got != 3 {
		t.Errorf("want the checkpoint advanced past the failed event to 3, got %d", got)
	}

	second := &collector{}
	p2 := newProcessor(t, events, checkpoints, second)

	_, _ = start(t, p2)
	waitCaughtUp(t, p2)

	if got := second.snapshot(); len(got) != 0 {
		t.Errorf("want no events redelivered after a continue-past failure, got positions %v", got)
	}
}

// TestProcessor_IdleTouchRefreshesCheckpoint pins the liveness mechanic: at
// the head, each poll cycle re-saves the unchanged position with a fresh
// UpdatedAt.
func TestProcessor_IdleTouchRefreshesCheckpoint(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 2)

	handler := &collector{}
	p := newProcessor(t, events, checkpoints, handler)

	_, _ = start(t, p)
	waitCaughtUp(t, p)

	first := loadCheckpoint(t, checkpoints)

	waitFor(t, func() bool {
		return loadCheckpoint(t, checkpoints).UpdatedAt.After(first.UpdatedAt)
	})

	refreshed := loadCheckpoint(t, checkpoints)
	if refreshed.Position != first.Position {
		t.Errorf("want the idle touch to keep position %d, got %d", first.Position, refreshed.Position)
	}
}

// TestProcessor_EmptyStore pins cold start against an empty store: the
// processor is immediately caught up and establishes a checkpoint at 0, so
// liveness is visible before any event exists.
func TestProcessor_EmptyStore(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)

	handler := &collector{}
	p := newProcessor(t, events, checkpoints, handler)

	cancel, done := start(t, p)
	waitCaughtUp(t, p)

	// The head save precedes the caught-up signal, so the position-0
	// checkpoint is guaranteed to exist by now.
	if got := loadPosition(t, checkpoints); got != 0 {
		t.Errorf("want a checkpoint at position 0, got %d", got)
	}

	cancel()

	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("want Run to return the context's error on cancellation, got %v", err)
	}
}

// TestProcessor_CaughtUpRequiresDurableCheckpoint pins the promotion-gating
// order: the head checkpoint save precedes the caught-up signal, so a failed
// head save leaves the processor not caught up and Run reports the failure.
func TestProcessor_CaughtUpRequiresDurableCheckpoint(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)

	failing := &failingSaveStore{Store: checkpoints, failAt: 0}
	handler := &collector{}
	p := newProcessor(t, events, failing, handler)

	_, done := start(t, p)

	err := waitDone(t, done)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("want Run to return the head checkpoint save failure, got %v", err)
	}

	select {
	case <-p.CaughtUp():
		t.Error("want the processor not caught up after a failed head checkpoint save")
	default:
	}
}

// TestProcessor_CaughtUpPosition pins that the first caught-up position is
// captured immutably: Position keeps advancing as the processor tails, while
// CaughtUpPosition stays at the position the caught-up signal certified.
func TestProcessor_CaughtUpPosition(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)
	appendEvents(t, events, 3)

	handler := &collector{}
	p := newProcessor(t, events, checkpoints, handler)

	if got := p.CaughtUpPosition(); got != 0 {
		t.Errorf("want CaughtUpPosition 0 before catch-up, got %d", got)
	}

	_, _ = start(t, p)
	waitCaughtUp(t, p)

	if got := p.CaughtUpPosition(); got != 3 {
		t.Errorf("want CaughtUpPosition 3, got %d", got)
	}

	appendEvents(t, events, 2)
	waitForHandled(t, handler, 5)

	if got := p.Position(); got != 5 {
		t.Errorf("want Position 5 after tailing, got %d", got)
	}

	if got := p.CaughtUpPosition(); got != 3 {
		t.Errorf("want CaughtUpPosition to stay 3 after tailing, got %d", got)
	}
}

func TestProcessor_RunTwice(t *testing.T) {
	t.Parallel()

	events, checkpoints := newStores(t)

	handler := &collector{}
	p := newProcessor(t, events, checkpoints, handler)

	_, _ = start(t, p)
	waitCaughtUp(t, p)

	if err := p.Run(t.Context()); err == nil {
		t.Error("want an error running a processor twice, got nil")
	}
}

//
// helpers
//

func newStores(t *testing.T) (*esmemory.EventStore, *cpmemory.CheckpointStore) {
	t.Helper()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	return events, cpmemory.NewCheckpointStore()
}

func newProcessor(
	t *testing.T,
	events eventstore.GlobalReader,
	checkpoints checkpointstore.Store,
	handler projection.EventHandler,
	opts ...processor.Option,
) *processor.Processor {
	t.Helper()

	p, err := processor.New(events, checkpoints, testID(), handler,
		append([]processor.Option{processor.WithPollInterval(shortPoll)}, opts...)...)
	if err != nil {
		t.Fatalf("creating processor: %v", err)
	}

	return p
}

func testID() projection.ID {
	return projection.ID{Name: "processortest", Version: 1}
}

func appendEvents(t *testing.T, store *esmemory.EventStore, n int) {
	t.Helper()

	events := make([]*eventstore.WritableEvent, 0, n)
	for range n {
		events = append(events, &eventstore.WritableEvent{Type: "processortest", Data: []byte(`{}`)})
	}

	if _, err := store.AppendStream(t.Context(), typeid.NewV4("processortest"), events, eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending %d events: %v", n, err)
	}
}

// start runs the processor in the background. The returned channel receives
// Run's result exactly once; the cleanup cancellation guarantees the
// goroutine exits even if the test never reads it.
func start(t *testing.T, p *processor.Processor) (context.CancelFunc, <-chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	done := make(chan error, 1)

	go func() {
		done <- p.Run(ctx)
	}()

	return cancel, done
}

func waitCaughtUp(t *testing.T, p *processor.Processor) {
	t.Helper()

	select {
	case <-p.CaughtUp():
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the processor to catch up")
	}
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

// waitFor polls until the condition holds, failing the test at the deadline.
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

func waitForHandled(t *testing.T, c *collector, n int) {
	t.Helper()

	waitFor(t, func() bool { return len(c.snapshot()) >= n })
}

func loadCheckpoint(t *testing.T, store checkpointstore.Store) checkpointstore.Checkpoint {
	t.Helper()

	checkpoint, err := store.Load(t.Context(), testID())
	if err != nil {
		t.Fatalf("loading checkpoint: %v", err)
	}

	return checkpoint
}

func loadPosition(t *testing.T, store checkpointstore.Store) int64 {
	t.Helper()

	return loadCheckpoint(t, store).Position
}

func assertPositions(t *testing.T, got []int64, want ...int64) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("want %d handled events at positions %v, got %d at %v", len(want), want, len(got), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want positions %v, got %v", want, got)
			return
		}
	}
}

// collector records the global position of each event it handles, failing
// at failAt if set.
type collector struct {
	mu        sync.Mutex
	positions []int64
	failAt    int64
	failWith  error
}

func (c *collector) Handle(_ context.Context, event *eventstore.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.positions = append(c.positions, *event.GlobalPosition)

	if c.failAt != 0 && *event.GlobalPosition == c.failAt {
		return c.failWith
	}

	return nil
}

func (c *collector) snapshot() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]int64(nil), c.positions...)
}

// failingSaveStore fails the save at one position, once, then behaves normally.
type failingSaveStore struct {
	checkpointstore.Store
	mu     sync.Mutex
	failAt int64
	failed bool
}

func (s *failingSaveStore) Save(ctx context.Context, id projection.ID, position int64) error {
	s.mu.Lock()
	shouldFail := position == s.failAt && !s.failed
	if shouldFail {
		s.failed = true
	}
	s.mu.Unlock()

	if shouldFail {
		return errors.New("save failed")
	}

	return s.Store.Save(ctx, id, position)
}

// readRecorder records the Count of every ReadAll it forwards.
type readRecorder struct {
	inner  eventstore.GlobalReader
	mu     sync.Mutex
	counts []int64
}

func (r *readRecorder) ReadAll(ctx context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	r.mu.Lock()
	r.counts = append(r.counts, opts.Count)
	r.mu.Unlock()

	return r.inner.ReadAll(ctx, opts)
}

func (r *readRecorder) snapshot() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]int64(nil), r.counts...)
}

// saveRecorder records the position of every save it forwards.
type saveRecorder struct {
	checkpointstore.Store
	mu        sync.Mutex
	positions []int64
}

func (s *saveRecorder) Save(ctx context.Context, id projection.ID, position int64) error {
	s.mu.Lock()
	s.positions = append(s.positions, position)
	s.mu.Unlock()

	return s.Store.Save(ctx, id, position)
}

func (s *saveRecorder) snapshot() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]int64(nil), s.positions...)
}

// discardLogger keeps the error path in "continue on handler error" from
// writing to the test's output, where it reads as a real failure.
type discardLogger struct{}

var _ estoria.Logger = discardLogger{}

func (discardLogger) Debug(string, ...any)              {}
func (discardLogger) Info(string, ...any)               {}
func (discardLogger) Warn(string, ...any)               {}
func (discardLogger) Error(string, ...any)              {}
func (l discardLogger) With(...any) estoria.Logger      { return l }
func (l discardLogger) WithGroup(string) estoria.Logger { return l }
