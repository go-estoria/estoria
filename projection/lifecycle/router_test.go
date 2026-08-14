package lifecycle_test

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
	"github.com/go-estoria/estoria/projection/lifecycle"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

func TestMemoryRouter(t *testing.T) {
	t.Parallel()

	router := lifecycle.NewMemoryRouter()

	if _, err := router.Live(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrNoLiveVersion) {
		t.Errorf("want ErrNoLiveVersion for a never-promoted projection, got %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	if err := router.SetLive(t.Context(), v1); err != nil {
		t.Fatalf("setting live version: %v", err)
	}

	if got, err := router.Live(t.Context(), "orders"); err != nil || got != v1 {
		t.Errorf("want live version %s, got %s (%v)", v1, got, err)
	}

	v2 := projection.ID{Name: "orders", Version: 2}
	if err := router.SetLive(t.Context(), v2); err != nil {
		t.Fatalf("setting live version: %v", err)
	}

	if got, _ := router.Live(t.Context(), "orders"); got != v2 {
		t.Errorf("want live version %s after overwrite, got %s", v2, got)
	}
}

// TestStreamRouter pins the fold: Promoted and RolledBack events in
// estoria.projection streams determine the live version, domain streams are
// ignored, and Refresh advances the fold incrementally.
func TestStreamRouter(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	// A domain event in the same store, which the fold must ignore.
	if _, err := events.AppendStream(t.Context(), typeid.NewV4("order"),
		[]*eventstore.WritableEvent{{Type: "ordertest", Data: []byte(`{}`)}},
		eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending domain event: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	router, err := lifecycle.NewStreamRouter(events)
	if err != nil {
		t.Fatalf("creating stream router: %v", err)
	}

	if got, err := router.Live(t.Context(), "orders"); err != nil || got != ordersV1 {
		t.Errorf("want live version %s, got %s (%v)", ordersV1, got, err)
	}

	if _, err := router.Live(t.Context(), "carts"); !errors.Is(err, lifecycle.ErrNoLiveVersion) {
		t.Errorf("want ErrNoLiveVersion for a never-promoted projection, got %v", err)
	}

	// Promotions recorded after the fold are invisible until a refresh.
	cartsV1 := projection.ID{Name: "carts", Version: 1}
	recordCutover(t, projections, cartsV1, projection.ID{}, false)

	if _, err := router.Live(t.Context(), "carts"); !errors.Is(err, lifecycle.ErrNoLiveVersion) {
		t.Errorf("want ErrNoLiveVersion before a refresh, got %v", err)
	}

	if err := router.Refresh(t.Context()); err != nil {
		t.Fatalf("refreshing: %v", err)
	}

	if got, err := router.Live(t.Context(), "carts"); err != nil || got != cartsV1 {
		t.Errorf("want live version %s after refresh, got %s (%v)", cartsV1, got, err)
	}

	// A rollback reverts the fold to the previous version.
	ordersV2 := projection.ID{Name: "orders", Version: 2}
	recordCutover(t, projections, ordersV2, ordersV1, true)

	if err := router.Refresh(t.Context()); err != nil {
		t.Fatalf("refreshing: %v", err)
	}

	if got, _ := router.Live(t.Context(), "orders"); got != ordersV1 {
		t.Errorf("want live version %s after rollback, got %s", ordersV1, got)
	}
}

func TestNewStreamRouter_RequiresReader(t *testing.T) {
	t.Parallel()

	if _, err := lifecycle.NewStreamRouter(nil); err == nil {
		t.Error("want an error for a nil reader, got nil")
	}
}

func TestNewStreamRouter_RejectsNegativeRefreshInterval(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	if _, err := lifecycle.NewStreamRouter(events, lifecycle.WithRefreshInterval(-time.Second)); err == nil {
		t.Error("want an error for a negative refresh interval, got nil")
	}
}

// flakyReader fails ReadAll a fixed number of times before delegating.
type flakyReader struct {
	inner eventstore.GlobalReader
	mu    sync.Mutex
	fails int
}

func (f *flakyReader) ReadAll(ctx context.Context, opts eventstore.ReadAllOptions) (eventstore.StreamIterator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fails > 0 {
		f.fails--
		return nil, errors.New("transient read failure")
	}

	return f.inner.ReadAll(ctx, opts)
}

// TestStreamRouter_RetriesFailedInitialFold pins that a failed fold commits
// nothing: the next call retries instead of serving a partial cache forever.
func TestStreamRouter_RetriesFailedInitialFold(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	projections, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	ordersV1 := projection.ID{Name: "orders", Version: 1}
	recordCutover(t, projections, ordersV1, projection.ID{}, false)

	router, err := lifecycle.NewStreamRouter(&flakyReader{inner: events, fails: 1})
	if err != nil {
		t.Fatalf("creating stream router: %v", err)
	}

	if _, err := router.Live(t.Context(), "orders"); err == nil {
		t.Fatal("want the failed fold reported, got nil")
	}

	if got, err := router.Live(t.Context(), "orders"); err != nil || got != ordersV1 {
		t.Errorf("want the fold retried and %s live, got %s (%v)", ordersV1, got, err)
	}
}

// TestStreamRouter_ReportsCorruptCutoverEvent pins that a cutover event that
// cannot be decoded is reported on every fold attempt rather than silently
// skipped past.
func TestStreamRouter_ReportsCorruptCutoverEvent(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	if _, err := events.AppendStream(t.Context(), typeid.NewV4(lifecycle.StreamType),
		[]*eventstore.WritableEvent{{Type: "promoted", Data: []byte("not json")}},
		eventstore.AppendStreamOptions{}); err != nil {
		t.Fatalf("appending corrupt event: %v", err)
	}

	router, err := lifecycle.NewStreamRouter(events)
	if err != nil {
		t.Fatalf("creating stream router: %v", err)
	}

	if _, err := router.Live(t.Context(), "orders"); err == nil {
		t.Fatal("want the corrupt event reported, got nil")
	}

	if _, err := router.Live(t.Context(), "orders"); err == nil {
		t.Error("want the corrupt event reported again, not skipped, got nil")
	}
}

// recordCutover appends a full promoted attempt to next's lifecycle stream —
// creating the aggregate on the projection's first rebuild, loading it
// afterwards — optionally rolled back to previous.
func recordCutover(t *testing.T, store aggregatestore.Store[lifecycle.State], next, previous projection.ID, rollBack bool) {
	t.Helper()

	aggregate, err := store.Load(t.Context(), lifecycle.StreamUUID(next.Name), nil)
	if errors.Is(err, aggregatestore.ErrAggregateNotFound) {
		aggregate = store.New(lifecycle.StreamUUID(next.Name))
	} else if err != nil {
		t.Fatalf("loading lifecycle aggregate: %v", err)
	}

	aggregate.Append(
		lifecycle.RebuildInitiated{Attempt: uuid.Must(uuid.NewV4()), Target: next, Previous: previous, Reason: "router test", At: initiatedAt},
		lifecycle.BuildStarted{},
		lifecycle.CaughtUp{Position: 1, At: caughtUpAt},
		lifecycle.Promoted{Previous: previous, Next: next, At: promotedAt},
	)

	if rollBack {
		aggregate.Append(lifecycle.RolledBack{From: next, RevertedTo: previous, At: promotedAt})
	}

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving lifecycle aggregate: %v", err)
	}
}
