package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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

func TestMemoryRouter(t *testing.T) {
	t.Parallel()

	router := lifecycle.NewMemoryRouter()

	if _, err := router.Live(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrNoLiveVersion) {
		t.Errorf("want ErrNoLiveVersion for a never-promoted projection, got %v", err)
	}

	if _, err := router.AppliedCutover(t.Context(), "orders"); !errors.Is(err, lifecycle.ErrNoLiveVersion) {
		t.Errorf("want ErrNoLiveVersion before any cutover was applied, got %v", err)
	}

	v1 := projection.ID{Name: "orders", Version: 1}
	if err := router.ApplyCutover(t.Context(), lifecycle.Cutover{Live: v1, Revision: 1}); err != nil {
		t.Fatalf("applying cutover: %v", err)
	}

	if got, err := router.Live(t.Context(), "orders"); err != nil || got != v1 {
		t.Errorf("want live version %s, got %s (%v)", v1, got, err)
	}

	if got, err := router.AppliedCutover(t.Context(), "orders"); err != nil || got != (lifecycle.Cutover{Live: v1, Revision: 1}) {
		t.Errorf("want the applied cutover reported, got %+v (%v)", got, err)
	}

	v2 := projection.ID{Name: "orders", Version: 2}
	if err := router.ApplyCutover(t.Context(), lifecycle.Cutover{Live: v2, Revision: 2}); err != nil {
		t.Fatalf("applying cutover: %v", err)
	}

	if got, _ := router.Live(t.Context(), "orders"); got != v2 {
		t.Errorf("want live version %s after the newer cutover, got %s", v2, got)
	}
}

// TestMemoryRouter_ApplyCutoverContract pins the apply-if-newer semantics the
// CutoverSetter contract requires: higher revisions apply, redelivered and
// older cutovers are no-ops, an equal revision must carry the same live
// version, and malformed cutovers are refused without touching the route.
func TestMemoryRouter_ApplyCutoverContract(t *testing.T) {
	t.Parallel()

	v1 := projection.ID{Name: "orders", Version: 1}
	v2 := projection.ID{Name: "orders", Version: 2}
	served := lifecycle.Cutover{Live: v2, Revision: 2}

	for _, tt := range []struct {
		name    string
		deliver lifecycle.Cutover
		want    lifecycle.Cutover // the route after the delivery
		wantErr bool
	}{
		{name: "a higher revision applies", deliver: lifecycle.Cutover{Live: v1, Revision: 3}, want: lifecycle.Cutover{Live: v1, Revision: 3}},
		{name: "the served cutover redelivered is an idempotent no-op", deliver: served, want: served},
		{name: "an older cutover is a stale no-op", deliver: lifecycle.Cutover{Live: v1, Revision: 1}, want: served},
		{name: "a conflicting cutover at the served revision is corruption", deliver: lifecycle.Cutover{Live: v1, Revision: 2}, want: served, wantErr: true},
		{name: "an invalid live version is refused", deliver: lifecycle.Cutover{Live: projection.ID{Name: "Bad Name", Version: 1}, Revision: 9}, want: served, wantErr: true},
		{name: "a non-positive revision is refused", deliver: lifecycle.Cutover{Live: v1, Revision: 0}, want: served, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			router := lifecycle.NewMemoryRouter()
			if err := router.ApplyCutover(t.Context(), served); err != nil {
				t.Fatalf("seeding the served cutover: %v", err)
			}

			err := router.ApplyCutover(t.Context(), tt.deliver)
			if tt.wantErr && err == nil {
				t.Fatal("want the delivery refused, got nil")
			} else if !tt.wantErr && err != nil {
				t.Fatalf("want the delivery absorbed, got %v", err)
			}

			if got, err := router.AppliedCutover(t.Context(), "orders"); err != nil || got != tt.want {
				t.Errorf("want the route serving %+v, got %+v (%v)", tt.want, got, err)
			}
		})
	}
}

// TestMemoryRouter_ConcurrentAppliesConverge exercises the setter's atomicity
// under contention: interleaved deliveries of every revision in arbitrary
// order must converge on the highest one, with the (Live, Revision) pair
// changing together — the race detector patrols the mutex discipline.
func TestMemoryRouter_ConcurrentAppliesConverge(t *testing.T) {
	t.Parallel()

	router := lifecycle.NewMemoryRouter()

	const revisions = 32

	var wg sync.WaitGroup
	for rev := int64(1); rev <= revisions; rev++ {
		wg.Go(func() {
			cutover := lifecycle.Cutover{Live: projection.ID{Name: "orders", Version: int(rev)}, Revision: rev}
			if err := router.ApplyCutover(t.Context(), cutover); err != nil {
				t.Errorf("applying cutover %+v: %v", cutover, err)
			}
		})
	}

	wg.Wait()

	want := lifecycle.Cutover{Live: projection.ID{Name: "orders", Version: revisions}, Revision: revisions}
	if got, err := router.AppliedCutover(t.Context(), "orders"); err != nil || got != want {
		t.Errorf("want the route converged on %+v, got %+v (%v)", want, got, err)
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

	// A promotion after the rollback continues the validated history:
	// revision 4 promoting v3 from the reverted v1.
	ordersV3 := projection.ID{Name: "orders", Version: 3}
	recordCutover(t, projections, ordersV3, ordersV1, false)

	if err := router.Refresh(t.Context()); err != nil {
		t.Fatalf("refreshing: %v", err)
	}

	if got, _ := router.Live(t.Context(), "orders"); got != ordersV3 {
		t.Errorf("want live version %s after the post-rollback promotion, got %s", ordersV3, got)
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

func TestNewStreamRouter_RejectsNilOption(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	if _, err := lifecycle.NewStreamRouter(events, nil); err == nil {
		t.Error("want an error for a nil option, got nil")
	}
}

// TestStreamRouter_RefreshInterval pins the two refresh modes: a router with
// an interval advances its fold on Live calls once the cache is older than
// the interval, and the default router never advances except through an
// explicit Refresh.
func TestStreamRouter_RefreshInterval(t *testing.T) {
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

	refreshing, err := lifecycle.NewStreamRouter(events, lifecycle.WithRefreshInterval(20*time.Millisecond))
	if err != nil {
		t.Fatalf("creating refreshing router: %v", err)
	}

	fixed, err := lifecycle.NewStreamRouter(events)
	if err != nil {
		t.Fatalf("creating no-refresh router: %v", err)
	}

	// Both fold v1 on first use.
	for _, router := range []*lifecycle.StreamRouter{refreshing, fixed} {
		if got, err := router.Live(t.Context(), "orders"); err != nil || got != ordersV1 {
			t.Fatalf("want live version %s from the initial fold, got %s (%v)", ordersV1, got, err)
		}
	}

	ordersV2 := projection.ID{Name: "orders", Version: 2}
	recordCutover(t, projections, ordersV2, ordersV1, false)

	// The refreshing router picks the promotion up once its interval lapses.
	waitFor(t, func() bool {
		live, err := refreshing.Live(t.Context(), "orders")
		return err == nil && live == ordersV2
	})

	// The default router still serves its cache, until an explicit Refresh.
	if got, err := fixed.Live(t.Context(), "orders"); err != nil || got != ordersV1 {
		t.Errorf("want the no-refresh router still serving %s, got %s (%v)", ordersV1, got, err)
	}

	if err := fixed.Refresh(t.Context()); err != nil {
		t.Fatalf("refreshing: %v", err)
	}

	if got, err := fixed.Live(t.Context(), "orders"); err != nil || got != ordersV2 {
		t.Errorf("want live version %s after the explicit refresh, got %s (%v)", ordersV2, got, err)
	}
}

// TestStreamRouter_RejectsInvalidCutovers pins the fold's semantic decode: a
// cutover recording an invalid projection ID or a non-positive revision, or
// living on a stream its projection's name does not derive, fails the fold
// on every attempt — and the failed fold commits nothing.
func TestStreamRouter_RejectsInvalidCutovers(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		streamID func() typeid.ID
		next     projection.ID
		revision int64
	}{
		{
			name:     "invalid live version",
			streamID: func() typeid.ID { return typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID("orders")} },
			next:     projection.ID{Name: "orders", Version: 0},
			revision: 1,
		},
		{
			name:     "invalid cutover revision",
			streamID: func() typeid.ID { return typeid.ID{Type: lifecycle.StreamType, UUID: lifecycle.StreamUUID("orders")} },
			next:     projection.ID{Name: "orders", Version: 1},
			revision: 0,
		},
		{
			name:     "foreignly addressed stream",
			streamID: func() typeid.ID { return typeid.NewV4(lifecycle.StreamType) },
			next:     projection.ID{Name: "orders", Version: 1},
			revision: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events, err := esmemory.NewEventStore()
			if err != nil {
				t.Fatalf("creating event store: %v", err)
			}

			data, err := json.Marshal(lifecycle.Promoted{Next: tt.next, Revision: tt.revision, At: promotedAt})
			if err != nil {
				t.Fatalf("marshaling promoted event: %v", err)
			}

			if _, err := events.AppendStream(t.Context(), tt.streamID(),
				[]*eventstore.WritableEvent{{Type: lifecycle.Promoted{}.EventType(), Data: data, DataContentType: "application/json"}},
				eventstore.AppendStreamOptions{}); err != nil {
				t.Fatalf("appending raw cutover: %v", err)
			}

			router, err := lifecycle.NewStreamRouter(events)
			if err != nil {
				t.Fatalf("creating stream router: %v", err)
			}

			if _, err := router.Live(t.Context(), "orders"); err == nil {
				t.Fatal("want the invalid cutover reported, got nil")
			}

			if _, err := router.Live(t.Context(), "orders"); err == nil {
				t.Error("want the invalid cutover reported again, not skipped, got nil")
			}
		})
	}
}

// appendRawCutoverEvent marshals a cutover event straight onto the "orders"
// lifecycle stream, bypassing the aggregate: the shape tampered or foreign
// histories arrive in.
func appendRawCutoverEvent(t *testing.T, events *esmemory.EventStore, event estoria.DomainEvent[lifecycle.State]) {
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

// TestStreamRouter_RejectsDiscontinuousHistories pins the fold's continuity
// validation: the authoritative record is one stream per name under
// optimistic concurrency, so a decoded history whose revisions skip, repeat,
// or regress, whose lineage misreports the previously live version, or that
// opens with a rollback is tampered or foreign — the fold fails closed on
// every attempt instead of serving last-write-wins.
func TestStreamRouter_RejectsDiscontinuousHistories(t *testing.T) {
	t.Parallel()

	v1 := projection.ID{Name: "orders", Version: 1}
	v2 := projection.ID{Name: "orders", Version: 2}
	v3 := projection.ID{Name: "orders", Version: 3}

	for _, tt := range []struct {
		name    string
		events  []estoria.DomainEvent[lifecycle.State]
		wantErr string
	}{
		{
			name: "a revision gap",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v2, Revision: 3, At: promotedAt},
			},
			wantErr: "records revision",
		},
		{
			name: "a duplicated revision",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v2, Revision: 1, At: promotedAt},
			},
			wantErr: "records revision",
		},
		{
			name: "a regressed revision",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v2, Revision: 2, At: promotedAt},
				lifecycle.Promoted{Previous: v2, Next: v3, Revision: 1, At: promotedAt},
			},
			wantErr: "records revision",
		},
		{
			name: "false lineage",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.Promoted{Previous: projection.ID{Name: "orders", Version: 9}, Next: v2, Revision: 2, At: promotedAt},
			},
			wantErr: "records a flip from",
		},
		{
			name: "an opening rollback",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.RolledBack{RevertedTo: v1, Revision: 1, At: promotedAt},
			},
			wantErr: "opens with a rollback",
		},
		{
			name: "rollback to the wrong predecessor",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v2, Revision: 2, At: promotedAt},
				lifecycle.RolledBack{From: v2, RevertedTo: v3, Revision: 3, At: promotedAt},
			},
			wantErr: "the promotion retained",
		},
		{
			name: "rollback after a first promotion",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.RolledBack{From: v1, RevertedTo: v1, Revision: 2, At: promotedAt},
			},
			wantErr: "no promotion to revert",
		},
		{
			name: "consecutive rollbacks",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v2, Revision: 2, At: promotedAt},
				lifecycle.RolledBack{From: v2, RevertedTo: v1, Revision: 3, At: promotedAt},
				lifecycle.RolledBack{From: v1, RevertedTo: v1, Revision: 4, At: promotedAt},
			},
			wantErr: "no promotion to revert",
		},
		{
			name: "a re-promoted old version",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v2, Revision: 2, At: promotedAt},
				lifecycle.RolledBack{From: v2, RevertedTo: v1, Revision: 3, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v2, Revision: 4, At: promotedAt},
			},
			wantErr: "never reused",
		},
		{
			name: "a promotion below the high-water",
			events: []estoria.DomainEvent[lifecycle.State]{
				lifecycle.Promoted{Next: v1, Revision: 1, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v3, Revision: 2, At: promotedAt},
				lifecycle.RolledBack{From: v3, RevertedTo: v1, Revision: 3, At: promotedAt},
				lifecycle.Promoted{Previous: v1, Next: v2, Revision: 4, At: promotedAt},
			},
			wantErr: "never reused",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events, err := esmemory.NewEventStore()
			if err != nil {
				t.Fatalf("creating event store: %v", err)
			}

			for _, event := range tt.events {
				appendRawCutoverEvent(t, events, event)
			}

			router, err := lifecycle.NewStreamRouter(events)
			if err != nil {
				t.Fatalf("creating stream router: %v", err)
			}

			if _, err := router.Live(t.Context(), "orders"); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want the discontinuity reported as %q, got %v", tt.wantErr, err)
			}

			if _, err := router.Live(t.Context(), "orders"); err == nil {
				t.Error("want the discontinuous history reported again, not skipped, got nil")
			}
		})
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

// recordCutover appends a full attempt to next's lifecycle stream — creating
// the aggregate on the projection's first rebuild, loading it afterwards —
// promoted and completed, or rolled back to previous when rollBack is set.
// Every form ends with the attempt slot vacant, so successive calls admit
// cleanly; revisions continue the loaded fold's cutover sequence, as the
// commands stamp them. The fixture refuses to record a history the fold
// itself marks inconsistent.
func recordCutover(t *testing.T, store aggregatestore.Store[lifecycle.State], next, previous projection.ID, rollBack bool) {
	t.Helper()

	aggregate, err := store.Load(t.Context(), lifecycle.StreamUUID(next.Name), nil)
	if errors.Is(err, aggregatestore.ErrAggregateNotFound) {
		aggregate = store.New(lifecycle.StreamUUID(next.Name))
	} else if err != nil {
		t.Fatalf("loading lifecycle aggregate: %v", err)
	}

	attempt := uuid.Must(uuid.NewV4())
	revision := aggregate.State().CutoverRevision

	aggregate.Append(
		lifecycle.RebuildInitiated{Attempt: attempt, Target: next, Previous: previous, Reason: "router test", At: initiatedAt},
		lifecycle.RunnerClaimed{Attempt: attempt, Runner: uuid.Must(uuid.NewV4()), At: initiatedAt},
		lifecycle.BuildStarted{},
		lifecycle.CaughtUp{Position: 1, At: caughtUpAt},
		lifecycle.Promoted{Previous: previous, Next: next, Revision: revision + 1, At: promotedAt},
	)

	switch {
	case rollBack:
		aggregate.Append(lifecycle.RolledBack{From: next, RevertedTo: previous, Revision: revision + 2, At: promotedAt})
	case previous == (projection.ID{}):
		aggregate.Append(lifecycle.PreviousRetired{})
	default:
		aggregate.Append(
			lifecycle.RetireStarted{Retiring: previous, At: promotedAt},
			lifecycle.PreviousRetired{Retired: previous},
		)
	}

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving lifecycle aggregate: %v", err)
	}

	state := aggregate.State()
	if state.InvalidReason != "" {
		t.Fatalf("recordCutover produced a poisoned history: %s", state.InvalidReason)
	}

	if state.Attempt != (lifecycle.AttemptState{}) {
		t.Fatalf("recordCutover left the attempt slot occupied in phase %s", state.Attempt.Phase)
	}
}
