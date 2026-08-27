package aggregatestore_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/snapshotstore"
	snapshotmemory "github.com/go-estoria/estoria/snapshotstore/memory"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// These tests pin the plumbing between an aggregate's events and the wire events
// the store persists: metadata attached at append time is what the event store
// receives, every payload is stamped with its codec's declared content type, and
// the reserved "estoria." metadata prefix is enforced before anything is written.

func newAccountStore(t *testing.T, eventStore *memory.EventStore) *aggregatestore.EventSourcedStore[account] {
	t.Helper()

	store, err := aggregatestore.New(eventStore, "account", newAccount,
		aggregatestore.WithEventTypes[account](fundsDeposited{}, fundsWithdrawn{}))
	if err != nil {
		t.Fatalf("creating aggregate store: %v", err)
	}

	return store
}

func readStreamEvents(t *testing.T, eventStore *memory.EventStore, streamID typeid.ID) []*eventstore.Event {
	t.Helper()

	iter, err := eventStore.ReadStream(t.Context(), streamID, eventstore.ReadStreamOptions{})
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}

	t.Cleanup(func() { _ = iter.Close(t.Context()) })

	events, err := eventstore.Collect(t.Context(), iter)
	if err != nil {
		t.Fatalf("reading events: %v", err)
	}

	return events
}

func TestAppendWithMetadata_MetadataIsPerCall(t *testing.T) {
	t.Parallel()

	eventStore, _ := memory.NewEventStore()
	store := newAccountStore(t, eventStore)

	first := map[string]string{"correlation_id": "corr-1"}
	second := map[string]string{"actor": "user-7"}

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.AppendWithMetadata(first, fundsDeposited{Amount: 10})
	aggregate.Append(fundsDeposited{Amount: 5})
	aggregate.AppendWithMetadata(second, fundsDeposited{Amount: 1}, fundsDeposited{Amount: 2})

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	events := readStreamEvents(t, eventStore, aggregate.ID())
	if len(events) != 4 {
		t.Fatalf("want 4 events, got %d", len(events))
	}

	if got := events[0].Metadata; !maps.Equal(got, first) {
		t.Errorf("event 0: want metadata %v, got %v", first, got)
	}

	if got := events[1].Metadata; len(got) != 0 {
		t.Errorf("event 1: want no metadata on a plain append, got %v", got)
	}

	for i := 2; i < 4; i++ {
		if got := events[i].Metadata; !maps.Equal(got, second) {
			t.Errorf("event %d: want metadata %v, got %v", i, second, got)
		}
	}
}

// TestAppendWithMetadata_CopiesTheCallersMap pins that metadata is captured at
// append time. A caller that reuses one map across appends — the natural way to
// write it — must not retroactively rewrite events appended earlier.
func TestAppendWithMetadata_CopiesTheCallersMap(t *testing.T) {
	t.Parallel()

	eventStore, _ := memory.NewEventStore()
	store := newAccountStore(t, eventStore)

	want := map[string]string{"causation_id": "cause-1"}

	metadata := maps.Clone(want)

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.AppendWithMetadata(metadata, fundsDeposited{Amount: 10})

	metadata["causation_id"] = "cause-2"
	metadata["injected"] = "late"

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	events := readStreamEvents(t, eventStore, aggregate.ID())
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}

	if got := events[0].Metadata; !maps.Equal(got, want) {
		t.Errorf("want the metadata as it was at append time %v, got %v", want, got)
	}
}

func TestMergeEventMetadata(t *testing.T) {
	t.Parallel()

	t.Run("merges into every unsaved event", func(t *testing.T) {
		t.Parallel()

		eventStore, _ := memory.NewEventStore()
		store := newAccountStore(t, eventStore)

		aggregate := store.New(uuid.Must(uuid.NewV4()))
		aggregate.Append(fundsDeposited{Amount: 10})
		aggregate.AppendWithMetadata(map[string]string{"actor": "user-7"}, fundsDeposited{Amount: 5})

		aggregate.MergeEventMetadata(map[string]string{"trace_id": "trace-1"})

		if err := store.Save(t.Context(), aggregate, nil); err != nil {
			t.Fatalf("saving aggregate: %v", err)
		}

		events := readStreamEvents(t, eventStore, aggregate.ID())
		if len(events) != 2 {
			t.Fatalf("want 2 events, got %d", len(events))
		}

		if want := map[string]string{"trace_id": "trace-1"}; !maps.Equal(events[0].Metadata, want) {
			t.Errorf("event 0: want metadata %v, got %v", want, events[0].Metadata)
		}

		if want := map[string]string{"actor": "user-7", "trace_id": "trace-1"}; !maps.Equal(events[1].Metadata, want) {
			t.Errorf("event 1: want metadata %v, got %v", want, events[1].Metadata)
		}
	})

	t.Run("the latest write wins for a colliding key", func(t *testing.T) {
		t.Parallel()

		eventStore, _ := memory.NewEventStore()
		store := newAccountStore(t, eventStore)

		aggregate := store.New(uuid.Must(uuid.NewV4()))
		aggregate.AppendWithMetadata(map[string]string{"actor": "alice"}, fundsDeposited{Amount: 10})

		aggregate.MergeEventMetadata(map[string]string{"actor": "bob"})

		if err := store.Save(t.Context(), aggregate, nil); err != nil {
			t.Fatalf("saving aggregate: %v", err)
		}

		events := readStreamEvents(t, eventStore, aggregate.ID())
		if len(events) != 1 {
			t.Fatalf("want 1 event, got %d", len(events))
		}

		if want := map[string]string{"actor": "bob"}; !maps.Equal(events[0].Metadata, want) {
			t.Errorf("want metadata %v, got %v", want, events[0].Metadata)
		}
	})

	t.Run("merging nothing changes nothing", func(t *testing.T) {
		t.Parallel()

		eventStore, _ := memory.NewEventStore()
		store := newAccountStore(t, eventStore)

		aggregate := store.New(uuid.Must(uuid.NewV4()))
		aggregate.Append(fundsDeposited{Amount: 10})

		aggregate.MergeEventMetadata(nil)

		if err := store.Save(t.Context(), aggregate, nil); err != nil {
			t.Fatalf("saving aggregate: %v", err)
		}

		events := readStreamEvents(t, eventStore, aggregate.ID())
		if len(events) != 1 {
			t.Fatalf("want 1 event, got %d", len(events))
		}

		if got := events[0].Metadata; len(got) != 0 {
			t.Errorf("want no metadata, got %v", got)
		}
	})
}

// TestSave_RejectsReservedMetadataKeys pins that the reserved-prefix rule is
// enforced as a pre-append failure: nothing reaches the event store, so the
// error carries no ErrEventsAppended and a retry after fixing the key is safe.
func TestSave_RejectsReservedMetadataKeys(t *testing.T) {
	t.Parallel()

	eventStore, _ := memory.NewEventStore()
	store := newAccountStore(t, eventStore)

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.AppendWithMetadata(map[string]string{"estoria.actor": "user-7"}, fundsDeposited{Amount: 10})

	err := store.Save(t.Context(), aggregate, nil)
	if err == nil {
		t.Fatal("want an error saving an event with a reserved metadata key, got nil")
	}

	if !strings.Contains(err.Error(), `"estoria.actor"`) {
		t.Errorf("want the error to name the offending key, got %q", err.Error())
	}

	if errors.Is(err, aggregatestore.ErrEventsAppended) {
		t.Error("want a pre-append failure without ErrEventsAppended, got an error carrying it")
	}

	if _, err := eventStore.ReadStream(t.Context(), aggregate.ID(), eventstore.ReadStreamOptions{}); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Errorf("want no events appended for the aggregate, got stream read error %v", err)
	}
}

// metadataInjectingStore decorates an aggregate store, merging ambient metadata
// into the events queued on an aggregate before delegating the save.
type metadataInjectingStore struct {
	aggregatestore.Store[account]
	metadata map[string]string
}

func (s metadataInjectingStore) Save(ctx context.Context, aggregate *aggregatestore.Aggregate[account], opts *aggregatestore.SaveOptions) error {
	aggregate.MergeEventMetadata(s.metadata)
	return s.Store.Save(ctx, aggregate, opts)
}

// TestSaveDecorator_InjectsMetadata covers the ambient-context path: a decorator
// amends the events queued on the aggregate before delegating the save, and the
// amended metadata is what reaches storage.
func TestSaveDecorator_InjectsMetadata(t *testing.T) {
	t.Parallel()

	eventStore, _ := memory.NewEventStore()

	store := metadataInjectingStore{
		Store:    newAccountStore(t, eventStore),
		metadata: map[string]string{"correlation_id": "corr-1"},
	}

	aggregate := store.New(uuid.Must(uuid.NewV4()))
	aggregate.Append(fundsDeposited{Amount: 10})

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	events := readStreamEvents(t, eventStore, aggregate.ID())
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}

	if want := map[string]string{"correlation_id": "corr-1"}; !maps.Equal(events[0].Metadata, want) {
		t.Errorf("want metadata %v, got %v", want, events[0].Metadata)
	}
}

// contentTypedEventCodec is the JSON event codec under a different declared name,
// so a test can tell "stamped with the codec's declaration" apart from "stamped
// with a hardcoded application/json".
type contentTypedEventCodec struct {
	estoria.JSONDomainEventCodec[account]
}

func (contentTypedEventCodec) ContentType() string { return "application/x-declared" }

func TestSave_DeclaresTheEventCodecContentType(t *testing.T) {
	t.Parallel()

	t.Run("default codec declares JSON", func(t *testing.T) {
		t.Parallel()

		eventStore, _ := memory.NewEventStore()
		store := newAccountStore(t, eventStore)

		aggregate := store.New(uuid.Must(uuid.NewV4()))
		aggregate.Append(fundsDeposited{Amount: 10}, fundsDeposited{Amount: 5})

		if err := store.Save(t.Context(), aggregate, nil); err != nil {
			t.Fatalf("saving aggregate: %v", err)
		}

		for i, event := range readStreamEvents(t, eventStore, aggregate.ID()) {
			if got := event.DataContentType; got != "application/json" {
				t.Errorf(`event %d: want data content type "application/json", got %q`, i, got)
			}
		}
	})

	t.Run("a configured codec's declaration is used", func(t *testing.T) {
		t.Parallel()

		eventStore, _ := memory.NewEventStore()

		store, err := aggregatestore.New(eventStore, "account", newAccount,
			aggregatestore.WithEventTypes[account](fundsDeposited{}),
			aggregatestore.WithDomainEventCodec[account](contentTypedEventCodec{}))
		if err != nil {
			t.Fatalf("creating aggregate store: %v", err)
		}

		aggregate := store.New(uuid.Must(uuid.NewV4()))
		aggregate.Append(fundsDeposited{Amount: 10})

		if err := store.Save(t.Context(), aggregate, nil); err != nil {
			t.Fatalf("saving aggregate: %v", err)
		}

		events := readStreamEvents(t, eventStore, aggregate.ID())
		if len(events) != 1 {
			t.Fatalf("want 1 event, got %d", len(events))
		}

		if got := events[0].DataContentType; got != "application/x-declared" {
			t.Errorf(`want data content type "application/x-declared", got %q`, got)
		}
	})
}

func TestSnapshottingStore_DeclaresTheStateCodecContentType(t *testing.T) {
	t.Parallel()

	eventStore, _ := memory.NewEventStore()
	snapshotStore := snapshotmemory.NewSnapshotStore()

	snapshotting, err := aggregatestore.NewSnapshottingStore(newAccountStore(t, eventStore), snapshotStore,
		snapshotstore.EventCountSnapshotPolicy{N: 1})
	if err != nil {
		t.Fatalf("creating snapshotting store: %v", err)
	}

	aggregate := snapshotting.New(uuid.Must(uuid.NewV4()))
	aggregate.Append(fundsDeposited{Amount: 10})

	if err := snapshotting.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	snap, err := snapshotStore.ReadSnapshot(t.Context(), aggregate.ID(), snapshotstore.ReadSnapshotOptions{})
	if err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}

	if got := snap.DataContentType; got != "application/json" {
		t.Errorf(`want snapshot data content type "application/json", got %q`, got)
	}
}

// TestSnapshottingStore_SkipsMismatchedSnapshotContentType pins the read-side
// dispatch on the declaration: a snapshot whose declared content type is not the
// codec's is never decoded — the payload could decode into state "successfully"
// with nothing matched — and hydration falls back to replaying events.
func TestSnapshottingStore_SkipsMismatchedSnapshotContentType(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name            string
		dataContentType string
		wantBalance     int
	}{
		{
			// The snapshot claims an encoding the JSON codec does not read, so the
			// balance must come from replaying events, not from the snapshot.
			name:            "a mismatched declaration skips the snapshot",
			dataContentType: "application/x-other",
			wantBalance:     15,
		},
		{
			name:            "a matching declaration uses the snapshot",
			dataContentType: "application/json",
			wantBalance:     999,
		},
		{
			// Written before content types were declared; decoded as before.
			name:            "an empty declaration uses the snapshot",
			dataContentType: "",
			wantBalance:     999,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eventStore, _ := memory.NewEventStore()
			snapshotStore := snapshotmemory.NewSnapshotStore()
			store := newAccountStore(t, eventStore)

			snapshotting, err := aggregatestore.NewSnapshottingStore(store, snapshotStore,
				snapshotstore.EventCountSnapshotPolicy{N: 0})
			if err != nil {
				t.Fatalf("creating snapshotting store: %v", err)
			}

			id := uuid.Must(uuid.NewV4())

			aggregate := store.New(id)
			aggregate.Append(fundsDeposited{Amount: 10}, fundsDeposited{Amount: 5})
			if err := store.Save(t.Context(), aggregate, nil); err != nil {
				t.Fatalf("saving aggregate: %v", err)
			}

			// A snapshot at the stream tip whose state disagrees with the events,
			// so the assertion can tell which source hydration trusted.
			data, err := json.Marshal(account{ID: aggregate.ID(), Balance: 999})
			if err != nil {
				t.Fatalf("marshaling snapshot state: %v", err)
			}

			if err := snapshotStore.WriteSnapshot(t.Context(), &snapshotstore.AggregateSnapshot{
				AggregateID:      aggregate.ID(),
				AggregateVersion: 2,
				Data:             data,
				DataContentType:  tt.dataContentType,
			}); err != nil {
				t.Fatalf("writing snapshot: %v", err)
			}

			loaded, err := snapshotting.Load(t.Context(), id, nil)
			if err != nil {
				t.Fatalf("loading aggregate: %v", err)
			}

			if got := loaded.State().Balance; got != tt.wantBalance {
				t.Errorf("want balance %d, got %d", tt.wantBalance, got)
			}

			if got := loaded.Version(); got != 2 {
				t.Errorf("want version 2, got %d", got)
			}
		})
	}
}
