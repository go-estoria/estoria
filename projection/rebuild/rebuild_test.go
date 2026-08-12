package rebuild_test

import (
	"testing"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/rebuild"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

var (
	nextID     = projection.ID{Name: "orders", Version: 7}
	previousID = projection.ID{Name: "orders", Version: 6}

	createdAt  = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	caughtUpAt = time.Date(2026, 8, 12, 9, 14, 0, 0, time.UTC)
	promotedAt = time.Date(2026, 8, 12, 9, 15, 0, 0, time.UTC)
)

func created() rebuild.Created {
	return rebuild.Created{
		Name:     "orders",
		Next:     nextID,
		Previous: previousID,
		Reason:   "add customer_region column",
		At:       createdAt,
	}
}

func caughtUp() rebuild.CaughtUp {
	return rebuild.CaughtUp{Position: 4_182_331, Duration: 14 * time.Minute, At: caughtUpAt}
}

func promoted() rebuild.Promoted {
	return rebuild.Promoted{Previous: previousID, Next: nextID, At: promotedAt}
}

// fold applies events in order to a zero state, as hydration does.
func fold(events ...estoria.DomainEvent[rebuild.State]) rebuild.State {
	var state rebuild.State
	for _, event := range events {
		state = event.ApplyTo(state)
	}

	return state
}

func TestPhase_String(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		phase rebuild.Phase
		want  string
	}{
		{rebuild.PhaseCreated, "created"},
		{rebuild.PhaseBuilding, "building"},
		{rebuild.PhaseCaughtUp, "caught_up"},
		{rebuild.PhasePromoted, "promoted"},
		{rebuild.PhaseRolledBack, "rolled_back"},
		{rebuild.PhaseAbandoned, "abandoned"},
		{rebuild.PhaseRetired, "retired"},
		{rebuild.Phase(99), "unknown(99)"},
	} {
		if got := tt.phase.String(); got != tt.want {
			t.Errorf("want %q, got %q", tt.want, got)
		}
	}
}

// TestRebuildCreated_ApplyTo pins the creation fold: identity, targets,
// reason, and creation time all come from the event payload.
func TestRebuildCreated_ApplyTo(t *testing.T) {
	t.Parallel()

	got := created().ApplyTo(rebuild.State{})

	want := rebuild.State{
		Name:      "orders",
		Next:      nextID,
		Previous:  previousID,
		Reason:    "add customer_region column",
		CreatedAt: createdAt,
		Phase:     rebuild.PhaseCreated,
	}

	if got != want {
		t.Errorf("want state %+v, got %+v", want, got)
	}
}

// TestTransitions pins each subsequent event's fold: the phase it produces
// and the fields it carries into state, with everything else untouched.
func TestTransitions(t *testing.T) {
	t.Parallel()

	base := fold(created())

	for _, tt := range []struct {
		name  string
		prior rebuild.State
		event estoria.DomainEvent[rebuild.State]
		want  func(rebuild.State) rebuild.State
	}{
		{
			name:  "BuildStarted marks the rebuild building",
			prior: base,
			event: rebuild.BuildStarted{},
			want: func(s rebuild.State) rebuild.State {
				s.Phase = rebuild.PhaseBuilding
				return s
			},
		},
		{
			name:  "BuildResumed keeps the rebuild building",
			prior: fold(created(), rebuild.BuildStarted{}),
			event: rebuild.BuildResumed{FromPosition: 1_000},
			want: func(s rebuild.State) rebuild.State {
				s.Phase = rebuild.PhaseBuilding
				return s
			},
		},
		{
			name:  "CaughtUp records the position and time",
			prior: fold(created(), rebuild.BuildStarted{}),
			event: caughtUp(),
			want: func(s rebuild.State) rebuild.State {
				s.Phase = rebuild.PhaseCaughtUp
				s.CaughtUpAt = caughtUpAt
				s.CaughtUpPos = 4_182_331
				return s
			},
		},
		{
			name:  "Promoted records the cutover time",
			prior: fold(created(), rebuild.BuildStarted{}, caughtUp()),
			event: promoted(),
			want: func(s rebuild.State) rebuild.State {
				s.Phase = rebuild.PhasePromoted
				s.PromotedAt = promotedAt
				return s
			},
		},
		{
			name:  "RolledBack marks the reversion",
			prior: fold(created(), rebuild.BuildStarted{}, caughtUp(), promoted()),
			event: rebuild.RolledBack{RevertedTo: previousID},
			want: func(s rebuild.State) rebuild.State {
				s.Phase = rebuild.PhaseRolledBack
				return s
			},
		},
		{
			name:  "Abandoned records the cause",
			prior: fold(created(), rebuild.BuildStarted{}),
			event: rebuild.Abandoned{Cause: "handler bug discovered mid-replay"},
			want: func(s rebuild.State) rebuild.State {
				s.Phase = rebuild.PhaseAbandoned
				s.AbandonCause = "handler bug discovered mid-replay"
				return s
			},
		},
		{
			name:  "PreviousRetired marks the terminal success",
			prior: fold(created(), rebuild.BuildStarted{}, caughtUp(), promoted()),
			event: rebuild.PreviousRetired{Retired: previousID},
			want: func(s rebuild.State) rebuild.State {
				s.Phase = rebuild.PhaseRetired
				return s
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, want := tt.event.ApplyTo(tt.prior), tt.want(tt.prior); got != want {
				t.Errorf("want state %+v, got %+v", want, got)
			}
		})
	}
}

// TestLifecycle_HappyPath pins the full successful fold end to end.
func TestLifecycle_HappyPath(t *testing.T) {
	t.Parallel()

	state := fold(created(), rebuild.BuildStarted{}, caughtUp(), promoted(), rebuild.PreviousRetired{Retired: previousID})

	if state.Phase != rebuild.PhaseRetired {
		t.Errorf("want phase %s, got %s", rebuild.PhaseRetired, state.Phase)
	}

	if state.CaughtUpPos != 4_182_331 {
		t.Errorf("want caught-up position 4182331, got %d", state.CaughtUpPos)
	}

	if !state.PromotedAt.Equal(promotedAt) {
		t.Errorf("want promoted at %s, got %s", promotedAt, state.PromotedAt)
	}
}

func TestEventTypes_AreUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, event := range allEvents() {
		eventType := event.EventType()
		if seen[eventType] {
			t.Errorf("duplicate event type %q", eventType)
		}

		seen[eventType] = true
	}
}

// TestEvents_RoundTripJSON pins that every event survives the default codec:
// a decoded copy folds identically to the original. This is the codec the
// aggregate store uses unless configured otherwise.
func TestEvents_RoundTripJSON(t *testing.T) {
	t.Parallel()

	codec := estoria.JSONDomainEventCodec[rebuild.State]{}
	prior := fold(created(), rebuild.BuildStarted{})

	for _, event := range allEvents() {
		t.Run(event.EventType(), func(t *testing.T) {
			t.Parallel()

			data, err := codec.MarshalDomainEvent(event)
			if err != nil {
				t.Fatalf("marshaling: %v", err)
			}

			decoded := event.New()
			if err := codec.UnmarshalDomainEvent(data, decoded); err != nil {
				t.Fatalf("unmarshaling: %v", err)
			}

			if got, want := decoded.ApplyTo(prior), event.ApplyTo(prior); got != want {
				t.Errorf("want the decoded event to fold identically:\nwant %+v\ngot  %+v", want, got)
			}
		})
	}
}

func TestNewState(t *testing.T) {
	t.Parallel()

	uid := uuid.Must(uuid.NewV4())

	state := rebuild.NewState(uid)

	if want := typeid.New(rebuild.StreamType, uid); state.ID != want {
		t.Errorf("want ID %s, got %s", want, state.ID)
	}
}

// TestRebuildAggregate_EndToEnd pins that the rebuild aggregate is an
// ordinary estoria aggregate: stored through a stock aggregate store, under
// the reserved estoria.rebuild stream type, with the default JSON codec, and
// hydrated back to the same state.
func TestRebuildAggregate_EndToEnd(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := aggregatestore.New(events, rebuild.StreamType, rebuild.NewState,
		aggregatestore.WithEventTypes(allEvents()...))
	if err != nil {
		t.Fatalf("creating aggregate store: %v", err)
	}

	uid := uuid.Must(uuid.NewV4())

	aggregate := store.New(uid)
	aggregate.Append(created(), rebuild.BuildStarted{}, caughtUp(), promoted())

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	loaded, err := store.Load(t.Context(), uid, nil)
	if err != nil {
		t.Fatalf("loading aggregate: %v", err)
	}

	if got, want := loaded.State(), aggregate.State(); got != want {
		t.Errorf("want hydrated state to match saved state:\nwant %+v\ngot  %+v", want, got)
	}

	if got := loaded.Version(); got != 4 {
		t.Errorf("want aggregate version 4, got %d", got)
	}
}

func allEvents() []estoria.DomainEvent[rebuild.State] {
	return []estoria.DomainEvent[rebuild.State]{
		created(),
		rebuild.BuildStarted{},
		rebuild.BuildResumed{FromPosition: 1_000},
		caughtUp(),
		promoted(),
		rebuild.RolledBack{RevertedTo: previousID},
		rebuild.Abandoned{Cause: "handler bug discovered mid-replay"},
		rebuild.PreviousRetired{Retired: previousID},
	}
}
