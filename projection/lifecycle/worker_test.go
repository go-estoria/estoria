package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// assertReadyWithheld asserts the worker never signaled readiness.
func assertReadyWithheld(t *testing.T, worker *lifecycle.Worker) {
	t.Helper()

	select {
	case <-worker.Ready():
		t.Error("want readiness withheld")
	default:
	}
}

// runWorker runs the worker in the background under a test-scoped cancelable
// context, returning its exit channel.
func runWorker(t *testing.T, worker *lifecycle.Worker) (<-chan error, context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	runErr := make(chan error, 1)

	go func() { runErr <- worker.Run(ctx) }()

	t.Cleanup(cancel)

	return runErr, cancel
}

// awaitExit waits for the worker's exit error.
func awaitExit(t *testing.T, runErr <-chan error) error {
	t.Helper()

	select {
	case err := <-runErr:
		return err
	case <-time.After(waitTimeout):
		t.Fatal("worker never exited")
		return nil
	}
}

// countingReader wraps a GlobalReader, recording the AfterPosition of every
// read issued through it once the read has been handed out.
type countingReader struct {
	inner eventstore.GlobalReader

	mu    sync.Mutex
	after []int64
}

func (r *countingReader) ReadAll(ctx context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	iter, err := r.inner.ReadAll(ctx, opts)

	r.mu.Lock()
	r.after = append(r.after, opts.AfterPosition)
	r.mu.Unlock()

	return iter, err
}

func (r *countingReader) reads() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]int64(nil), r.after...)
}

// failingReader fails every read.
type failingReader struct{ err error }

func (r failingReader) ReadAll(context.Context, eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	return nil, r.err
}

// failingNextReader yields a fixed number of events and then fails the read
// mid-stream.
type failingNextReader struct {
	inner eventstore.GlobalReader
	allow int
	err   error
}

func (r *failingNextReader) ReadAll(ctx context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	iter, err := r.inner.ReadAll(ctx, opts)
	if err != nil {
		return nil, err
	}

	return &failingNextIterator{StreamIterator: iter, reader: r}, nil
}

type failingNextIterator struct {
	eventstore.StreamIterator
	reader  *failingNextReader
	yielded int
}

func (i *failingNextIterator) Next(ctx context.Context) (*eventstore.Event, error) {
	if i.yielded >= i.reader.allow {
		return nil, i.reader.err
	}

	event, err := i.StreamIterator.Next(ctx)
	if err == nil {
		i.yielded++
	}

	return event, err
}

// failingCloseReader closes iterators with an error while armed — once when
// oneShot is set, so a test can prove nothing follows the failing close —
// and keeps an ordered history of the reads it hands out and the close
// failures it fires.
type failingCloseReader struct {
	inner   eventstore.GlobalReader
	err     error
	oneShot bool
	armed   atomic.Bool

	mu      sync.Mutex
	history []string
}

func (r *failingCloseReader) ReadAll(ctx context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	iter, err := r.inner.ReadAll(ctx, opts)
	if err != nil {
		return nil, err
	}

	r.record("read")

	return &failingCloseIterator{StreamIterator: iter, reader: r}, nil
}

func (r *failingCloseReader) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.history = append(r.history, event)
}

func (r *failingCloseReader) lastEvent() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.history) == 0 {
		return ""
	}

	return r.history[len(r.history)-1]
}

type failingCloseIterator struct {
	eventstore.StreamIterator
	reader *failingCloseReader
}

func (i *failingCloseIterator) Close(ctx context.Context) error {
	_ = i.StreamIterator.Close(ctx)

	fire := i.reader.armed.Load()
	if fire && i.reader.oneShot {
		fire = i.reader.armed.CompareAndSwap(true, false)
	}

	if fire {
		i.reader.record("close failed")
		return i.reader.err
	}

	return nil
}

// cancelingSetter cancels a context from inside its own application,
// modeling a setter that observes shutdown mid-fan-out.
type cancelingSetter struct {
	recordingSetter
	cancel context.CancelFunc
}

func (s *cancelingSetter) ApplyCutover(ctx context.Context, cutover lifecycle.Cutover) error {
	s.cancel()

	return s.recordingSetter.ApplyCutover(ctx, cutover)
}

// cancelingReader cancels a context from inside a chosen Next call, modeling
// cancellation that arrives while a reader that does not observe contexts is
// mid-drain. It counts the events it yields; cancelOn zero never fires.
type cancelingReader struct {
	inner    eventstore.GlobalReader
	cancel   context.CancelFunc
	cancelOn atomic.Int64

	yielded atomic.Int64
}

func (r *cancelingReader) ReadAll(ctx context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	iter, err := r.inner.ReadAll(ctx, opts)
	if err != nil {
		return nil, err
	}

	return &cancelingIterator{StreamIterator: iter, reader: r}, nil
}

type cancelingIterator struct {
	eventstore.StreamIterator
	reader *cancelingReader
}

func (i *cancelingIterator) Next(ctx context.Context) (*eventstore.Event, error) {
	event, err := i.StreamIterator.Next(ctx)
	if err != nil {
		return event, err
	}

	if i.reader.yielded.Add(1) == i.reader.cancelOn.Load() {
		i.reader.cancel()
	}

	return event, nil
}

// cancelOnReadReader cancels a context from inside ReadAll — after the read
// is handed out, before any event is requested — and counts the Next calls
// its iterators receive.
type cancelOnReadReader struct {
	inner  eventstore.GlobalReader
	cancel context.CancelFunc

	nextCalls atomic.Int64
}

func (r *cancelOnReadReader) ReadAll(ctx context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	iter, err := r.inner.ReadAll(ctx, opts)
	if err != nil {
		return nil, err
	}

	r.cancel()

	return &nextCountingIterator{StreamIterator: iter, reader: r}, nil
}

type nextCountingIterator struct {
	eventstore.StreamIterator
	reader *cancelOnReadReader
}

func (i *nextCountingIterator) Next(ctx context.Context) (*eventstore.Event, error) {
	i.reader.nextCalls.Add(1)

	return i.StreamIterator.Next(ctx)
}

// cancelEOFReader hands out iterators that cancel a context while reporting
// the end of the stream, modeling a reader that treats cancellation as
// stream end.
type cancelEOFReader struct {
	cancel context.CancelFunc
}

func (r cancelEOFReader) ReadAll(context.Context, eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	return cancelEOFIterator(r), nil
}

type cancelEOFIterator struct {
	cancel context.CancelFunc
}

func (i cancelEOFIterator) Next(context.Context) (*eventstore.Event, error) {
	i.cancel()

	return nil, eventstore.ErrEndOfEventStream
}

func (i cancelEOFIterator) Close(context.Context) error { return nil }

// cancelFailReader hands out iterators that cancel a context and fail from
// the same Next call, modeling cancellation racing a read failure.
type cancelFailReader struct {
	cancel context.CancelFunc
	err    error
}

func (r cancelFailReader) ReadAll(context.Context, eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	return cancelFailIterator(r), nil
}

type cancelFailIterator struct {
	cancel context.CancelFunc
	err    error
}

func (i cancelFailIterator) Next(context.Context) (*eventstore.Event, error) {
	i.cancel()

	return nil, i.err
}

func (i cancelFailIterator) Close(context.Context) error { return nil }

// nextCountingReader counts the Next calls its iterators receive.
type nextCountingReader struct {
	inner     eventstore.GlobalReader
	nextCalls atomic.Int64
}

func (r *nextCountingReader) ReadAll(ctx context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	iter, err := r.inner.ReadAll(ctx, opts)
	if err != nil {
		return nil, err
	}

	return &countedIterator{StreamIterator: iter, reader: r}, nil
}

type countedIterator struct {
	eventstore.StreamIterator
	reader *nextCountingReader
}

func (i *countedIterator) Next(ctx context.Context) (*eventstore.Event, error) {
	i.reader.nextCalls.Add(1)

	return i.StreamIterator.Next(ctx)
}

// gatedSetter blocks one chosen delivery until released, holding the worker
// inside the delivery window so the test can act there.
type gatedSetter struct {
	recordingSetter
	gateOn      int32
	calls       atomic.Int32
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newGatedSetter(gateOn int32) *gatedSetter {
	return &gatedSetter{gateOn: gateOn, entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *gatedSetter) ApplyCutover(ctx context.Context, cutover lifecycle.Cutover) error {
	if s.calls.Add(1) == s.gateOn {
		close(s.entered)
		<-s.release
	}

	return s.recordingSetter.ApplyCutover(ctx, cutover)
}

func (s *gatedSetter) releaseGate() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func (s *gatedSetter) awaitEntered(t *testing.T) {
	t.Helper()

	select {
	case <-s.entered:
	case <-time.After(waitTimeout):
		t.Fatal("the gated delivery never started")
	}
}

// nameRefusingSetter refuses every cutover for one projection name.
type nameRefusingSetter struct {
	recordingSetter
	refuse string
}

func (s *nameRefusingSetter) ApplyCutover(ctx context.Context, cutover lifecycle.Cutover) error {
	if cutover.Live.Name == s.refuse {
		return errors.New("refusing " + s.refuse)
	}

	return s.recordingSetter.ApplyCutover(ctx, cutover)
}

// storeHead reports the last global position the store has recorded.
func storeHead(t *testing.T, events eventstore.GlobalReader) int64 {
	t.Helper()

	iter, err := events.ReadAll(t.Context(), eventstore.ReadAllOptions{})
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	defer func() { _ = iter.Close(t.Context()) }()

	var head int64

	for {
		event, err := iter.Next(t.Context())
		if errors.Is(err, eventstore.ErrEndOfEventStream) {
			return head
		} else if err != nil {
			t.Fatalf("reading event: %v", err)
		}

		if event.GlobalPosition != nil {
			head = *event.GlobalPosition
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
		wantErr string
	}{
		{
			name:    "invalid live version",
			history: rawPromoted(ordersStream, projection.ID{Name: "orders", Version: 0}, 1),
			wantErr: "records an invalid live version",
		},
		{
			// The name-derived stream and every fold arm accept this event;
			// only the live version's own validation refuses it.
			name:    "an unrepresentable projection name",
			history: rawPromoted(typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID("")}, projection.ID{Version: 1}, 1),
			wantErr: "records an invalid live version",
		},
		{
			name:    "invalid cutover revision",
			history: rawPromoted(ordersStream, v1, 0),
			wantErr: "records an invalid cutover revision",
		},
		{
			name:    "foreignly addressed stream",
			history: rawPromoted(typeid.NewV4(lifecycle.StreamType), v1, 1),
			wantErr: "want the name-derived stream",
		},
		{
			name: "a discontinuous history",
			history: func(t *testing.T, events *esmemory.EventStore) {
				t.Helper()
				appendRawCutoverEvent(t, events, lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt})
				appendRawCutoverEvent(t, events, lifecycle.Promoted{Previous: v1, Next: v2, Revision: 3, At: promotedAt})
			},
			wantErr: "records revision",
		},
		{
			name: "an opening rollback",
			history: func(t *testing.T, events *esmemory.EventStore) {
				t.Helper()
				appendRawCutoverEvent(t, events, lifecycle.RolledBack{From: v2, RevertedTo: v1, Revision: 1, At: promotedAt})
			},
			wantErr: "opens with a rollback",
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

			err = worker.Run(runCtx)
			if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want the invalid history refused with %q, got %v", tt.wantErr, err)
			}

			assertReadyWithheld(t, worker)

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

// TestWorker_RunAtMostOnce pins the restart contract: a Worker folds once —
// whether its first run is still going or has already terminated — and
// restarting means a new Worker refolding from zero.
func TestWorker_RunAtMostOnce(t *testing.T) {
	t.Parallel()

	t.Run("while the first run is going", func(t *testing.T) {
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
	})

	t.Run("after the first run terminated", func(t *testing.T) {
		t.Parallel()

		worker, err := lifecycle.NewWorker(newEventStore(t),
			lifecycle.WithCutoverSetter(&recordingSetter{}),
			lifecycle.WithPollInterval(2*time.Millisecond),
		)
		if err != nil {
			t.Fatalf("creating worker: %v", err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		runErr := make(chan error, 1)

		go func() { runErr <- worker.Run(ctx) }()

		waitReady(t, worker)
		cancel()

		if err := awaitExit(t, runErr); !errors.Is(err, context.Canceled) {
			t.Fatalf("want the first run canceled, got %v", err)
		}

		// The canceled context keeps a worker that wrongly accepted the rerun
		// from folding: the refusal must come from the run-once gate, not the
		// context.
		if err := worker.Run(ctx); err == nil || !strings.Contains(err.Error(), "already run") {
			t.Fatalf("want the rerun refused after termination, got %v", err)
		}
	})
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
		{"rejects a zero poll interval", []lifecycle.WorkerOption{
			lifecycle.WithCutoverSetter(&recordingSetter{}),
			lifecycle.WithPollInterval(0),
		}},
		{"rejects a negative poll interval", []lifecycle.WorkerOption{
			lifecycle.WithCutoverSetter(&recordingSetter{}),
			lifecycle.WithPollInterval(-time.Millisecond),
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

// TestWorker_InitializationCloseFailureWithholdsReadiness pins close-error
// propagation during the initial fold: an iterator that cannot vouch for a
// complete read stops the worker before any setter acts, and readiness is
// withheld.
func TestWorker_InitializationCloseFailureWithholdsReadiness(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	errClose := errors.New("the close failed")
	reader := &failingCloseReader{inner: events, err: errClose, oneShot: true}
	reader.armed.Store(true)

	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr, _ := runWorker(t, worker)

	err = awaitExit(t, runErr)
	if !errors.Is(err, errClose) || !strings.Contains(err.Error(), "closing event iterator") {
		t.Fatalf("want the close failure propagated, got %v", err)
	}

	// The one-shot failure proves the stop was immediate: no read follows
	// the failing close.
	if got := reader.lastEvent(); got != "close failed" {
		t.Errorf("want the failing close to be the reader's last event, got %q", got)
	}

	assertReadyWithheld(t, worker)
	assertCutovers(t, recorder.seen(), nil)
}

// TestWorker_TailCloseFailureStopsTheWorker pins close-error propagation past
// readiness: a tail read whose iterator fails to close stops the worker
// instead of allowing another poll.
func TestWorker_TailCloseFailureStopsTheWorker(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	errClose := errors.New("the close failed")
	reader := &failingCloseReader{inner: events, err: errClose, oneShot: true}
	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr, _ := runWorker(t, worker)
	waitReady(t, worker)
	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})

	reader.armed.Store(true)

	err = awaitExit(t, runErr)
	if !errors.Is(err, errClose) || !strings.Contains(err.Error(), "closing event iterator") {
		t.Fatalf("want the tail close failure to stop the worker, got %v", err)
	}

	// The failure fired once and disarmed, so only an immediate stop leaves
	// it as the reader's last event: a worker that read again before
	// returning would append that read to the history.
	if got := reader.lastEvent(); got != "close failed" {
		t.Errorf("want the failing close to be the reader's last event, got %q", got)
	}

	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})
}

// TestWorker_CancellationStopsInitialization pins the entry check: a worker
// started with a canceled context touches nothing — no read issued, no
// setter acting, readiness withheld — and Run returns the context's error.
func TestWorker_CancellationStopsInitialization(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	recordCutover(t, projections, projection.ID{Name: "orders", Version: 1}, projection.ID{}, false)

	reader := &countingReader{inner: events}
	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	if reads := reader.reads(); len(reads) != 0 {
		t.Errorf("want no reads issued after cancellation, got %v", reads)
	}

	assertCutovers(t, recorder.seen(), nil)
	assertReadyWithheld(t, worker)

	// The canceled run consumed the worker's single start: a retry with a
	// live context is refused by the run-once gate — the deadline bounds a
	// worker that wrongly accepted it.
	retryCtx, cancelRetry := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancelRetry()

	if err := worker.Run(retryCtx); err == nil || !strings.Contains(err.Error(), "already run") {
		t.Fatalf("want the retry refused after the consumed start, got %v", err)
	}

	if reads := reader.reads(); len(reads) != 0 {
		t.Errorf("want no reads from the refused retry, got %v", reads)
	}
}

// TestWorker_CancellationStopsTheFoldMidDrain pins the drain's per-event
// check: cancellation arriving mid-read stops the fold between events even
// when the reader does not observe contexts, before any setter acts.
func TestWorker_CancellationStopsTheFoldMidDrain(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	recordCutover(t, projections, projection.ID{Name: "orders", Version: 1}, projection.ID{}, false)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reader := &cancelingReader{inner: events, cancel: cancel}
	reader.cancelOn.Store(2)

	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	if got := reader.yielded.Load(); got != 2 {
		t.Errorf("want the drain stopped after the canceling event, read %d events", got)
	}

	assertCutovers(t, recorder.seen(), nil)
	assertReadyWithheld(t, worker)
}

// TestWorker_CancellationStopsFinalDeliveries pins the check between final
// deliveries: cancellation arriving while one projection's final is in
// flight stops the remaining projections' deliveries and withholds
// readiness.
func TestWorker_CancellationStopsFinalDeliveries(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	customersV1 := projection.ID{Name: "customers", Version: 1}
	ordersV1 := projection.ID{Name: "orders", Version: 1}

	recordCutover(t, projections, customersV1, projection.ID{}, false)
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	setter := newGatedSetter(1)
	t.Cleanup(setter.releaseGate)

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr, cancel := runWorker(t, worker)

	setter.awaitEntered(t)
	cancel()
	setter.releaseGate()

	if err := awaitExit(t, runErr); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	// The delivery in flight completed; the next projection's never started.
	assertCutovers(t, setter.seen(), []lifecycle.Cutover{{Live: customersV1, Revision: 1}})
	assertReadyWithheld(t, worker)
}

// TestWorker_CancellationWithholdsReadinessAfterFinals pins the readiness
// check: cancellation arriving during the last final delivery leaves every
// setter converged but never signals readiness.
func TestWorker_CancellationWithholdsReadinessAfterFinals(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	customersV1 := projection.ID{Name: "customers", Version: 1}
	ordersV1 := projection.ID{Name: "orders", Version: 1}

	recordCutover(t, projections, customersV1, projection.ID{}, false)
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	setter := newGatedSetter(2)
	t.Cleanup(setter.releaseGate)

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr, cancel := runWorker(t, worker)

	setter.awaitEntered(t)
	cancel()
	setter.releaseGate()

	if err := awaitExit(t, runErr); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	assertCutovers(t, setter.seen(), []lifecycle.Cutover{
		{Live: customersV1, Revision: 1},
		{Live: ordersV1, Revision: 1},
	})
	assertReadyWithheld(t, worker)
}

// TestWorker_TailsEventsRecordedDuringInitialization pins the high-water
// handoff: the tail starts at the completed initial read's position, so a
// cutover recorded while final deliveries were still in flight is delivered
// by the tail — exactly once, neither lost to a later mark nor duplicated.
func TestWorker_TailsEventsRecordedDuringInitialization(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	setter := newGatedSetter(1)

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)
	t.Cleanup(setter.releaseGate)

	setter.awaitEntered(t)

	// The initial read is complete and its position captured; this cutover
	// lands strictly after it, before the worker reaches the tail.
	recordCutover(t, projections, ordersV2, ordersV1, false)

	setter.releaseGate()
	waitReady(t, worker)

	waitFor(t, func() bool { return len(setter.seen()) == 2 })
	assertCutovers(t, setter.seen(), []lifecycle.Cutover{
		{Live: ordersV1, Revision: 1},
		{Live: ordersV2, Revision: 2},
	})
}

// TestWorker_CancellationDropsTheEventInHand pins the check after each read:
// a cutover whose read completes alongside cancellation is dropped
// unprocessed — neither folded nor delivered — rather than ridden to the
// setters on the way out.
func TestWorker_CancellationDropsTheEventInHand(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reader := &cancelingReader{inner: events, cancel: cancel}
	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr := make(chan error, 1)

	go func() { runErr <- worker.Run(ctx) }()

	waitReady(t, worker)
	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})

	// Arm the cancellation to fire from inside the read that yields the next
	// event — the raw append is a single cutover, so that read is it.
	initialized := reader.yielded.Load()
	reader.cancelOn.Store(initialized + 1)

	appendRawCutoverEvent(t, events, lifecycle.Promoted{Previous: ordersV1, Next: ordersV2, Revision: 2, At: promotedAt})

	if err := awaitExit(t, runErr); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	if got := reader.yielded.Load(); got != initialized+1 {
		t.Errorf("want the drain stopped with the canceling event in hand, read %d events past initialization", got-initialized)
	}

	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})
}

// TestWorker_CancellationDuringTheReadIssuesNoNext pins the check between
// the read and its first event: cancellation arriving while the read is
// being handed out means no event is ever requested from it — a
// context-oblivious Next is never given the chance to block shutdown.
func TestWorker_CancellationDuringTheReadIssuesNoNext(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	recordCutover(t, projections, projection.ID{Name: "orders", Version: 1}, projection.ID{}, false)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reader := &cancelOnReadReader{inner: events, cancel: cancel}
	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	if got := reader.nextCalls.Load(); got != 0 {
		t.Errorf("want no events requested after cancellation, got %d Next calls", got)
	}

	assertCutovers(t, recorder.seen(), nil)
	assertReadyWithheld(t, worker)
}

// TestWorker_CancellationDominatesTheReadResult pins the check against
// every read result: a Next that reports end-of-stream alongside
// cancellation returns the cancellation, and an accompanying close failure
// joins it rather than replacing it.
func TestWorker_CancellationDominatesTheReadResult(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errClose := errors.New("the close failed")
	reader := &failingCloseReader{inner: cancelEOFReader{cancel: cancel}, err: errClose}
	reader.armed.Store(true)

	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	err = worker.Run(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errClose) {
		t.Fatalf("want the cancellation and the close failure both surfaced, got %v", err)
	}

	// The end of the stream is not a failure: nothing reports one.
	if strings.Contains(err.Error(), "reading event") {
		t.Errorf("want no read failure reported for the canceled end of stream, got %v", err)
	}

	assertCutovers(t, recorder.seen(), nil)
	assertReadyWithheld(t, worker)
}

// TestWorker_CancellationJoinsTheIndependentReadFailure pins the dominance
// check's failure handling: cancellation prevents processing, but a read
// failure that is not itself the cancellation surfacing is joined with it
// rather than discarded — while a cancellation-shaped failure is not
// re-reported as a read failure.
func TestWorker_CancellationJoinsTheIndependentReadFailure(t *testing.T) {
	t.Parallel()

	errStore := errors.New("the store failed")

	for _, tt := range []struct {
		name     string
		readErr  func() error
		wantErr  error // additionally surfaced alongside the cancellation
		wantRead bool  // whether a read failure must be reported
	}{
		{
			name:     "an independent failure is joined",
			readErr:  func() error { return errStore },
			wantErr:  errStore,
			wantRead: true,
		},
		{
			name:    "a cancellation-shaped failure is not re-reported",
			readErr: func() error { return fmt.Errorf("waiting for events: %w", context.Canceled) },
		},
		{
			// Every leaf must be benign: an end of stream joined with a
			// failure is a failed read, not a finished one.
			name:     "a failure joined with the end of the stream is preserved",
			readErr:  func() error { return errors.Join(eventstore.ErrEndOfEventStream, errStore) },
			wantErr:  errStore,
			wantRead: true,
		},
		{
			// Whole-tree classification: the cancellation leaf must not let
			// the failure beside it ride along as benign.
			name:     "a failure riding the cancellation is still joined",
			readErr:  func() error { return errors.Join(context.Canceled, errStore) },
			wantErr:  errStore,
			wantRead: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			recorder := &recordingSetter{}

			worker, err := lifecycle.NewWorker(cancelFailReader{cancel: cancel, err: tt.readErr()},
				lifecycle.WithCutoverSetter(recorder),
				lifecycle.WithPollInterval(2*time.Millisecond),
			)
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			err = worker.Run(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("want the cancellation surfaced, got %v", err)
			}

			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("want the independent read failure joined with the cancellation, got %v", err)
			}

			if tt.wantRead && !strings.Contains(err.Error(), "reading event") {
				t.Errorf("want the read failure reported as one, got %v", err)
			}

			if !tt.wantRead && strings.Contains(err.Error(), "reading event") {
				t.Errorf("want the cancellation-shaped failure folded into the cancellation, got %v", err)
			}

			assertCutovers(t, recorder.seen(), nil)
			assertReadyWithheld(t, worker)
		})
	}
}

// TestWorker_ReadFailureJoinedWithEOFStopsTheWorker pins whole-tree EOF
// classification without cancellation in play: a read failure joined with
// the end of the stream is a failed read, not a finished one — the worker
// stops with the failure instead of initializing over a read it cannot
// trust.
func TestWorker_ReadFailureJoinedWithEOFStopsTheWorker(t *testing.T) {
	t.Parallel()

	errStore := errors.New("the store failed")

	reader := &failingNextReader{
		inner: newEventStore(t),
		allow: 0,
		err:   errors.Join(eventstore.ErrEndOfEventStream, errStore),
	}

	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	// The deadline bounds the failure mode: a worker that read the joined
	// failure as a clean end would initialize and tail until the deadline.
	runCtx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()

	err = worker.Run(runCtx)
	if !errors.Is(err, errStore) || !strings.Contains(err.Error(), "reading event") {
		t.Fatalf("want the joined read failure to stop the worker, got %v", err)
	}

	assertReadyWithheld(t, worker)
	assertCutovers(t, recorder.seen(), nil)
}

// TestWorker_CancellationDuringDeliveryRequestsNoFurtherEvents pins the
// pre-request check on every iteration: cancellation arriving while an
// event is being delivered means no further event is requested from the
// context-oblivious iterator — not merely that a requested one is dropped.
func TestWorker_CancellationDuringDeliveryRequestsNoFurtherEvents(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	reader := &nextCountingReader{inner: events}
	setter := newGatedSetter(2)
	t.Cleanup(setter.releaseGate)

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr, cancel := runWorker(t, worker)
	waitReady(t, worker)

	// The tail delivery blocks; the drain is parked inside it, so the count
	// taken after canceling is the last event ever requested.
	recordCutover(t, projections, ordersV2, ordersV1, false)
	setter.awaitEntered(t)

	cancel()

	requested := reader.nextCalls.Load()
	setter.releaseGate()

	if err := awaitExit(t, runErr); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	if got := reader.nextCalls.Load(); got != requested {
		t.Errorf("want no events requested after the mid-delivery cancellation, got %d more", got-requested)
	}

	assertCutovers(t, setter.seen(), []lifecycle.Cutover{
		{Live: ordersV1, Revision: 1},
		{Live: ordersV2, Revision: 2},
	})
}

// TestWorker_CancellationPreemptsTheEventInHand pins that cancellation is
// applied to a read's result before the result is acted on: a malformed
// cutover arriving alongside cancellation is never decoded — the worker
// reports the cancellation, not the decode failure.
func TestWorker_CancellationPreemptsTheEventInHand(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reader := &cancelingReader{inner: events, cancel: cancel}
	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	runErr := make(chan error, 1)

	go func() { runErr <- worker.Run(ctx) }()

	waitReady(t, worker)
	reader.cancelOn.Store(reader.yielded.Load() + 1)

	// An invalid revision the decoder must refuse — unless the cancellation
	// riding the same read preempts the decode entirely.
	appendRawCutoverEvent(t, events, lifecycle.Promoted{Previous: ordersV1, Next: ordersV2, Revision: 0, At: promotedAt})

	if err := awaitExit(t, runErr); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the cancellation reported instead of the decode failure, got %v", err)
	}

	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})
}

// TestWorker_CancellationStopsSetterFanout pins the check before each
// setter: a setter observing shutdown mid-application stops the fan-out —
// later context-oblivious setters are never invoked.
func TestWorker_CancellationStopsSetterFanout(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	first := &cancelingSetter{cancel: cancel}
	second := &recordingSetter{}

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(first),
		lifecycle.WithCutoverSetter(second),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	if err := worker.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want the canceled context's error, got %v", err)
	}

	assertCutovers(t, first.seen(), []lifecycle.Cutover{{Live: ordersV1, Revision: 1}})
	assertCutovers(t, second.seen(), nil)
	assertReadyWithheld(t, worker)
}

// TestWorker_ReadAndCloseFailuresBothSurface pins the join: when a read
// fails mid-stream and its iterator then fails to close, the worker's error
// carries both causes.
func TestWorker_ReadAndCloseFailuresBothSurface(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	recordCutover(t, projections, projection.ID{Name: "orders", Version: 1}, projection.ID{}, false)

	errRead := errors.New("the read failed")
	errClose := errors.New("the close failed")

	reader := &failingCloseReader{
		inner: &failingNextReader{inner: events, allow: 1, err: errRead},
		err:   errClose,
	}
	reader.armed.Store(true)

	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	err = worker.Run(t.Context())
	if !errors.Is(err, errRead) || !errors.Is(err, errClose) {
		t.Fatalf("want both the read and close failures surfaced, got %v", err)
	}

	assertReadyWithheld(t, worker)
	assertCutovers(t, recorder.seen(), nil)
}

// TestWorker_TailEnforcesInitializedFoldState pins that the tail folds
// against the full state the initial fold established — the recorded
// rollback target and the promoted high-water — not merely the live
// revision.
func TestWorker_TailEnforcesInitializedFoldState(t *testing.T) {
	t.Parallel()

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}
	ordersV3 := projection.ID{Name: "orders", Version: 3}

	promotions := func(t *testing.T, projections aggregatestore.Store[lifecycle.State]) lifecycle.Cutover {
		t.Helper()

		recordCutover(t, projections, ordersV1, projection.ID{}, false)
		recordCutover(t, projections, ordersV2, ordersV1, false)

		return lifecycle.Cutover{Live: ordersV2, Revision: 2}
	}

	for _, tt := range []struct {
		name    string
		seed    func(*testing.T, aggregatestore.Store[lifecycle.State]) lifecycle.Cutover
		tail    estoria.DomainEvent[lifecycle.State]
		tailed  lifecycle.Cutover // delivered when wantErr is empty
		wantErr string
	}{
		{
			name:   "a rollback lands on the target initialization recorded",
			seed:   promotions,
			tail:   lifecycle.RolledBack{From: ordersV2, RevertedTo: ordersV1, Revision: 3, At: promotedAt},
			tailed: lifecycle.Cutover{Live: ordersV1, Revision: 3},
		},
		{
			name:    "a rollback to a version the promotion did not retain",
			seed:    promotions,
			tail:    lifecycle.RolledBack{From: ordersV2, RevertedTo: ordersV3, Revision: 3, At: promotedAt},
			wantErr: "the promotion retained",
		},
		{
			name: "a promotion below the initialized high-water",
			seed: func(t *testing.T, projections aggregatestore.Store[lifecycle.State]) lifecycle.Cutover {
				t.Helper()

				recordCutover(t, projections, ordersV1, projection.ID{}, false)
				recordCutover(t, projections, ordersV2, ordersV1, true)

				return lifecycle.Cutover{Live: ordersV1, Revision: 3}
			},
			tail:    lifecycle.Promoted{Previous: ordersV1, Next: ordersV2, Revision: 4, At: promotedAt},
			wantErr: "never reused",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events := newEventStore(t)

			projections, err := lifecycle.NewStore(events)
			if err != nil {
				t.Fatalf("creating lifecycle store: %v", err)
			}

			final := tt.seed(t, projections)

			recorder := &recordingSetter{}

			worker, err := lifecycle.NewWorker(events,
				lifecycle.WithCutoverSetter(recorder),
				lifecycle.WithPollInterval(2*time.Millisecond),
			)
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			runErr, _ := runWorker(t, worker)
			waitReady(t, worker)
			assertCutovers(t, recorder.seen(), []lifecycle.Cutover{final})

			appendRawCutoverEvent(t, events, tt.tail)

			if tt.wantErr == "" {
				waitFor(t, func() bool { return len(recorder.seen()) == 2 })
				assertCutovers(t, recorder.seen(), []lifecycle.Cutover{final, tt.tailed})

				return
			}

			if err := awaitExit(t, runErr); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want the tailed cutover refused with %q, got %v", tt.wantErr, err)
			}

			assertCutovers(t, recorder.seen(), []lifecycle.Cutover{final})
		})
	}
}

// TestWorker_ReadFailuresStopInitialization pins fail-closed reads: a read
// that cannot start or cannot finish stops the worker with the reader's
// error, readiness withheld, before any setter acts.
func TestWorker_ReadFailuresStopInitialization(t *testing.T) {
	t.Parallel()

	errRead := errors.New("the read failed")

	for _, tt := range []struct {
		name    string
		reader  func(*testing.T) eventstore.GlobalReader
		wantErr string
	}{
		{
			name:    "the read cannot start",
			reader:  func(*testing.T) eventstore.GlobalReader { return failingReader{err: errRead} },
			wantErr: "reading events",
		},
		{
			name: "the read fails mid-stream",
			reader: func(t *testing.T) eventstore.GlobalReader {
				t.Helper()

				events := newEventStore(t)

				projections, err := lifecycle.NewStore(events)
				if err != nil {
					t.Fatalf("creating lifecycle store: %v", err)
				}

				recordCutover(t, projections, projection.ID{Name: "orders", Version: 1}, projection.ID{}, false)

				return &failingNextReader{inner: events, allow: 1, err: errRead}
			},
			wantErr: "reading event",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			recorder := &recordingSetter{}

			worker, err := lifecycle.NewWorker(tt.reader(t),
				lifecycle.WithCutoverSetter(recorder),
				lifecycle.WithPollInterval(2*time.Millisecond),
			)
			if err != nil {
				t.Fatalf("creating worker: %v", err)
			}

			// The deadline bounds the failure mode: a worker that swallowed
			// the read failure would initialize and tail until the deadline.
			runCtx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()

			err = worker.Run(runCtx)
			if !errors.Is(err, errRead) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want the read failure propagated as %q, got %v", tt.wantErr, err)
			}

			assertReadyWithheld(t, worker)
			assertCutovers(t, recorder.seen(), nil)
		})
	}
}

// TestWorker_ReadyOnlyAfterEveryProjectionConverges pins the readiness
// gate's scope across projections: a setter refusing one projection's final
// stops the worker uninitialized even after another projection's final was
// applied.
func TestWorker_ReadyOnlyAfterEveryProjectionConverges(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	customersV1 := projection.ID{Name: "customers", Version: 1}
	ordersV1 := projection.ID{Name: "orders", Version: 1}

	recordCutover(t, projections, customersV1, projection.ID{}, false)
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	setter := &nameRefusingSetter{refuse: "orders"}

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(setter),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	if err := worker.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "applying cutover") {
		t.Fatalf("want the refused final to stop initialization, got %v", err)
	}

	assertReadyWithheld(t, worker)
	assertCutovers(t, setter.seen(), []lifecycle.Cutover{{Live: customersV1, Revision: 1}})
}

// TestWorker_IgnoresForeignStreamTypes pins the stream-type filter: a
// cutover-shaped event on a non-lifecycle stream is not routing truth, even
// when every other field would decode — the worker neither delivers it nor
// folds it into any projection's continuity.
func TestWorker_IgnoresForeignStreamTypes(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	customersV1 := projection.ID{Name: "customers", Version: 1}
	customersV2 := projection.ID{Name: "customers", Version: 2}
	customersV3 := projection.ID{Name: "customers", Version: 3}

	recordCutover(t, projections, customersV1, projection.ID{}, false)

	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(events,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)
	waitReady(t, worker)

	// A decoy on a foreign stream type, maximally confusable otherwise: the
	// event type, payload, and UUID all match what the lifecycle would write.
	data, err := json.Marshal(lifecycle.Promoted{Previous: customersV1, Next: customersV2, Revision: 2, At: promotedAt})
	if err != nil {
		t.Fatalf("marshaling decoy event: %v", err)
	}

	if _, err := events.AppendStream(t.Context(),
		typeid.ID{Type: "decoystream", UUID: lifecycle.StreamUUID("customers")},
		[]*eventstore.WritableEvent{{Type: lifecycle.Promoted{}.EventType(), Data: data, DataContentType: "application/json"}},
		eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending decoy event: %v", err)
	}

	// The real flips continue the real continuity: a worker that had folded
	// or delivered the decoy would refuse the repeated revision 2 here and
	// never reach revision 3.
	recordCutover(t, projections, customersV2, customersV1, false)
	recordCutover(t, projections, customersV3, customersV2, false)

	waitFor(t, func() bool { return len(recorder.seen()) == 3 })
	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{
		{Live: customersV1, Revision: 1},
		{Live: customersV2, Revision: 2},
		{Live: customersV3, Revision: 3},
	})
}

// TestWorker_AdvancesTheMarkPastNonCutoverEvents pins the high-water mark's
// scope: it tracks the last observed event, cutover or not, so tail reads
// resume from the head instead of re-reading trailing non-cutover events on
// every poll.
func TestWorker_AdvancesTheMarkPastNonCutoverEvents(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	recordCutover(t, projections, projection.ID{Name: "orders", Version: 1}, projection.ID{}, false)

	if _, err := events.AppendStream(t.Context(), typeid.NewV4("order"),
		[]*eventstore.WritableEvent{{Type: "ordertest", Data: []byte(`{}`)}},
		eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending domain event: %v", err)
	}

	head := storeHead(t, events)
	reader := &countingReader{inner: events}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(&recordingSetter{}),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)
	waitReady(t, worker)

	waitFor(t, func() bool { return len(reader.reads()) >= 3 })

	reads := reader.reads()
	if reads[0] != 0 {
		t.Errorf("want the initial fold read from zero, got %d", reads[0])
	}

	for i, after := range reads[1:] {
		if after != head {
			t.Errorf("want tail read %d to start after the head %d, got %d", i+1, head, after)
		}
	}
}

// TestWorker_HonorsConfiguredPollInterval pins that the configured interval
// drives the tail: a cutover recorded after a tail read's snapshot is
// delivered within a fraction of the default interval, which only the
// configured one allows.
func TestWorker_HonorsConfiguredPollInterval(t *testing.T) {
	t.Parallel()

	events := newEventStore(t)

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	ordersV2 := projection.ID{Name: "orders", Version: 2}

	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	reader := &countingReader{inner: events}
	recorder := &recordingSetter{}

	worker, err := lifecycle.NewWorker(reader,
		lifecycle.WithCutoverSetter(recorder),
		lifecycle.WithPollInterval(2*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("creating worker: %v", err)
	}

	startWorkerForTest(t, worker)
	waitReady(t, worker)

	// Once the first tail read is handed out its snapshot is fixed, so this
	// cutover is delivered no sooner than the poll after it.
	waitFor(t, func() bool { return len(reader.reads()) >= 2 })
	recordCutover(t, projections, ordersV2, ordersV1, false)

	deadline := time.Now().Add(600 * time.Millisecond)

	for len(recorder.seen()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no tail delivery within a fraction of the default poll interval")
		}

		time.Sleep(time.Millisecond)
	}

	assertCutovers(t, recorder.seen(), []lifecycle.Cutover{
		{Live: ordersV1, Revision: 1},
		{Live: ordersV2, Revision: 2},
	})
}
