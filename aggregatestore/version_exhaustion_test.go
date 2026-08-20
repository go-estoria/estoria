package aggregatestore_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// recordingEventStore records appends and reports them written, assigning
// stream versions exactly as a store does: one past the expected version.
// Reads report no stream. It stands in for a store whose stream has grown to
// an extreme tip, which no real fixture can reach event by event.
type recordingEventStore struct {
	appends []int
	expects []int64
}

func (s *recordingEventStore) ReadStream(context.Context, typeid.ID, eventstore.ReadStreamOptions) (eventstore.StreamIterator, error) {
	return nil, eventstore.ErrStreamNotFound
}

func (s *recordingEventStore) AppendStream(_ context.Context, streamID typeid.ID, events []*eventstore.WritableEvent, opts eventstore.AppendStreamOptions) ([]*eventstore.Event, error) {
	expect := int64(0)
	if opts.ExpectVersion != nil {
		expect = *opts.ExpectVersion
	}

	s.appends = append(s.appends, len(events))
	s.expects = append(s.expects, expect)

	written := make([]*eventstore.Event, len(events))
	for i, event := range events {
		written[i] = &eventstore.Event{
			ID:              typeid.NewV4(event.Type),
			StreamID:        streamID,
			StreamVersion:   expect + int64(i) + 1,
			Timestamp:       time.Now(),
			Data:            event.Data,
			DataContentType: event.DataContentType,
		}
	}

	return written, nil
}

// TestAggregate_ApplyRefusesTheVersionCeiling pins the version arithmetic at
// the top of the space: an aggregate at the maximum version applies nothing
// further — its next-version computation would wrap negative and a corrupt
// event carrying the wrapped version would false-match — while the final
// representable version itself is reached cleanly.
func TestAggregate_ApplyRefusesTheVersionCeiling(t *testing.T) {
	t.Parallel()

	t.Run("at the ceiling nothing applies", func(t *testing.T) {
		t.Parallel()

		id := uuid.Must(uuid.NewV4())
		aggregate := aggregatestore.NewAggregateForTest(typeid.New("mockentity", id), newMockEntity(id), math.MaxInt64)

		aggregate.TestOnlyWillApply(&aggregatestore.Event[mockEntity]{
			Version:     math.MinInt64,
			DomainEvent: mockEntityEventA{},
		})

		err := aggregate.TestOnlyApplyNext()
		if err == nil || !strings.Contains(err.Error(), "maximum version") {
			t.Fatalf("want the apply refused at the version ceiling, got %v", err)
		}

		if got := aggregate.Version(); got != math.MaxInt64 {
			t.Errorf("want the version unmoved at the ceiling, got %d", got)
		}
	})

	t.Run("the final version applies cleanly", func(t *testing.T) {
		t.Parallel()

		id := uuid.Must(uuid.NewV4())
		aggregate := aggregatestore.NewAggregateForTest(typeid.New("mockentity", id), newMockEntity(id), math.MaxInt64-1)

		aggregate.TestOnlyWillApply(&aggregatestore.Event[mockEntity]{
			Version:     math.MaxInt64,
			DomainEvent: mockEntityEventA{},
		})

		if err := aggregate.TestOnlyApplyNext(); err != nil {
			t.Fatalf("want the final representable version applied cleanly, got %v", err)
		}

		if got := aggregate.Version(); got != math.MaxInt64 {
			t.Errorf("want the aggregate at the final version, got %d", got)
		}
	})
}

// TestEventSourcedStoreSave_RefusesVersionExhaustion pins the central append
// guard: a save that would grow the stream past the maximum representable
// version, or from a negative version no legitimate hydration produces, is
// refused before anything reaches the event store — while the final
// representable slot itself remains appendable.
func TestEventSourcedStoreSave_RefusesVersionExhaustion(t *testing.T) {
	t.Parallel()

	newStore := func(t *testing.T) (*aggregatestore.EventSourcedStore[mockEntity], *recordingEventStore) {
		t.Helper()

		events := &recordingEventStore{}

		store, err := aggregatestore.New(events, "mockentity", newMockEntity)
		if err != nil {
			t.Fatalf("creating store: %v", err)
		}

		return store, events
	}

	t.Run("at the ceiling nothing appends", func(t *testing.T) {
		t.Parallel()

		store, events := newStore(t)
		id := uuid.Must(uuid.NewV4())

		aggregate := store.New(id)
		aggregate.TestOnlySetStateAtVersion(newMockEntity(id), math.MaxInt64)
		aggregate.Append(mockEntityEventA{})

		err := store.Save(t.Context(), aggregate, nil)
		if err == nil || !strings.Contains(err.Error(), "aggregate versions end at") {
			t.Fatalf("want the exhausted save refused, got %v", err)
		}

		if len(events.appends) != 0 {
			t.Errorf("want nothing appended to the event store, got %d appends", len(events.appends))
		}

		if got := aggregate.Version(); got != math.MaxInt64 {
			t.Errorf("want the version unmoved, got %d", got)
		}
	})

	t.Run("a negative version never appends", func(t *testing.T) {
		t.Parallel()

		store, events := newStore(t)
		id := uuid.Must(uuid.NewV4())

		aggregate := store.New(id)
		aggregate.TestOnlySetStateAtVersion(newMockEntity(id), -3)
		aggregate.Append(mockEntityEventA{})

		err := store.Save(t.Context(), aggregate, nil)
		if err == nil || !strings.Contains(err.Error(), "is invalid") {
			t.Fatalf("want the negative-version save refused, got %v", err)
		}

		if len(events.appends) != 0 {
			t.Errorf("want nothing appended to the event store, got %d appends", len(events.appends))
		}
	})

	t.Run("the final slot appends", func(t *testing.T) {
		t.Parallel()

		store, events := newStore(t)
		id := uuid.Must(uuid.NewV4())

		aggregate := store.New(id)
		aggregate.TestOnlySetStateAtVersion(newMockEntity(id), math.MaxInt64-1)
		aggregate.Append(mockEntityEventA{})

		if err := store.Save(t.Context(), aggregate, nil); err != nil {
			t.Fatalf("want the final representable slot appendable, got %v", err)
		}

		if len(events.appends) != 1 || events.expects[0] != math.MaxInt64-1 {
			t.Fatalf("want one append expecting version %d, got appends %v expecting %v",
				int64(math.MaxInt64-1), events.appends, events.expects)
		}

		if got := aggregate.Version(); got != math.MaxInt64 {
			t.Errorf("want the aggregate at the final version, got %d", got)
		}
	})
}
