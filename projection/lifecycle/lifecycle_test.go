package lifecycle_test

import (
	"testing"
	"time"

	"github.com/go-estoria/estoria"
	esmemory "github.com/go-estoria/estoria/eventstore/memory"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/lifecycle"
	"github.com/gofrs/uuid/v5"
)

var (
	attemptID  = uuid.Must(uuid.FromString("d3a95f2c-6c0e-4ac8-9a53-3d7202d0a1f6"))
	runnerID   = uuid.Must(uuid.FromString("0f6e2c8a-1b4d-4e7f-9a3c-5d8b2f1e4a6c"))
	runner2ID  = uuid.Must(uuid.FromString("9c3b1a7e-6f2d-4c8b-8e5a-2a7d4f9c1b3e"))
	targetID   = projection.ID{Name: "orders", Version: 7}
	previousID = projection.ID{Name: "orders", Version: 6}

	initiatedAt = time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	claimedAt   = time.Date(2026, 8, 13, 9, 1, 0, 0, time.UTC)
	caughtUpAt  = time.Date(2026, 8, 13, 9, 14, 0, 0, time.UTC)
	promotedAt  = time.Date(2026, 8, 13, 9, 15, 0, 0, time.UTC)
	retiringAt  = time.Date(2026, 8, 13, 9, 20, 0, 0, time.UTC)
)

func initiated() lifecycle.RebuildInitiated {
	return lifecycle.RebuildInitiated{
		Attempt:  attemptID,
		Target:   targetID,
		Previous: previousID,
		Reason:   "add customer_region column",
		At:       initiatedAt,
	}
}

func claimed() lifecycle.RunnerClaimed {
	return lifecycle.RunnerClaimed{Attempt: attemptID, Runner: runnerID, At: claimedAt}
}

func caughtUp() lifecycle.CaughtUp {
	return lifecycle.CaughtUp{Position: 4_182_331, Duration: 14 * time.Minute, At: caughtUpAt}
}

func promoted() lifecycle.Promoted {
	return lifecycle.Promoted{Previous: previousID, Next: targetID, Revision: 2, At: promotedAt}
}

// priorState is the state a v7-over-v6 rebuild is initiated against: v6 live
// at cutover revision 1 and six versions allocated, so the initiation's
// lineage fields are consistent with the fold.
func priorState() lifecycle.State {
	return lifecycle.State{Name: "orders", Live: previousID, CutoverRevision: 1, Allocated: 6}
}

// fold applies events in order to the prior state, as hydration does.
func fold(events ...estoria.DomainEvent[lifecycle.State]) lifecycle.State {
	state := priorState()
	for _, event := range events {
		state = event.ApplyTo(state)
	}

	return state
}

func TestPhase_String(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		phase lifecycle.Phase
		want  string
	}{
		{lifecycle.PhaseNone, "none"},
		{lifecycle.PhaseCreated, "created"},
		{lifecycle.PhaseBuilding, "building"},
		{lifecycle.PhaseCaughtUp, "caught_up"},
		{lifecycle.PhasePromoted, "promoted"},
		{lifecycle.PhaseRetiring, "retiring"},
		{lifecycle.Phase(99), "unknown(99)"},
	} {
		if got := tt.phase.String(); got != tt.want {
			t.Errorf("want %q, got %q", tt.want, got)
		}
	}
}

// TestRebuildInitiated_ApplyTo pins the admission fold: the attempt slot is
// occupied from the event payload, and the allocation high-water mark
// advances to the target version.
func TestRebuildInitiated_ApplyTo(t *testing.T) {
	t.Parallel()

	got := initiated().ApplyTo(priorState())

	want := lifecycle.State{
		Name:            "orders",
		Live:            previousID,
		CutoverRevision: 1,
		Allocated:       7,
		Attempt: lifecycle.AttemptState{
			ID:          attemptID,
			Target:      targetID,
			Previous:    previousID,
			Phase:       lifecycle.PhaseCreated,
			Reason:      "add customer_region column",
			InitiatedAt: initiatedAt,
		},
	}

	if got != want {
		t.Errorf("want state %+v, got %+v", want, got)
	}
}

// TestTransitions pins each subsequent event's fold: the phase it produces
// and the fields it carries into state, with everything else untouched.
func TestTransitions(t *testing.T) {
	t.Parallel()

	base := fold(initiated())

	for _, tt := range []struct {
		name  string
		prior lifecycle.State
		event estoria.DomainEvent[lifecycle.State]
		want  func(lifecycle.State) lifecycle.State
	}{
		{
			name:  "RunnerClaimed records the claimant and preserves the phase",
			prior: base,
			event: claimed(),
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Runner = runnerID
				s.Attempt.ClaimedAt = claimedAt
				return s
			},
		},
		{
			name:  "RunnerClaimed supersedes the previous claimant",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}),
			event: lifecycle.RunnerClaimed{Attempt: attemptID, Runner: runner2ID, FromPosition: 1_000, At: caughtUpAt},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Runner = runner2ID
				s.Attempt.ClaimedAt = caughtUpAt
				return s
			},
		},
		{
			name:  "RunnerClaimed preserves the caught-up phase",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp()),
			event: lifecycle.RunnerClaimed{Attempt: attemptID, Runner: runner2ID, FromPosition: 2_000, At: promotedAt},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Runner = runner2ID
				s.Attempt.ClaimedAt = promotedAt
				return s
			},
		},
		{
			name:  "RunnerClaimed preserves the promoted phase",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp(), promoted()),
			event: lifecycle.RunnerClaimed{Attempt: attemptID, Runner: runner2ID, FromPosition: 3_000, At: retiringAt},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Runner = runner2ID
				s.Attempt.ClaimedAt = retiringAt
				return s
			},
		},
		{
			name:  "RunnerClaimed preserves the retiring phase",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp(), promoted(), lifecycle.RetireStarted{Retiring: previousID, At: retiringAt}),
			event: lifecycle.RunnerClaimed{Attempt: attemptID, Runner: runner2ID, FromPosition: 4_000, At: retiringAt},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Runner = runner2ID
				s.Attempt.ClaimedAt = retiringAt
				return s
			},
		},
		{
			name:  "BuildStarted marks the attempt building",
			prior: fold(initiated(), claimed()),
			event: lifecycle.BuildStarted{},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Phase = lifecycle.PhaseBuilding
				return s
			},
		},
		{
			name:  "CaughtUp records the position and time",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}),
			event: caughtUp(),
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Phase = lifecycle.PhaseCaughtUp
				s.Attempt.CaughtUpAt = caughtUpAt
				s.Attempt.CaughtUpPos = 4_182_331
				return s
			},
		},
		{
			name:  "CaughtUp re-certifies from the caught-up phase",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp()),
			event: lifecycle.CaughtUp{Position: 4_182_400, Duration: time.Minute, At: promotedAt},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Phase = lifecycle.PhaseCaughtUp
				s.Attempt.CaughtUpAt = promotedAt
				s.Attempt.CaughtUpPos = 4_182_400
				return s
			},
		},
		{
			name:  "Promoted flips the live version",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp()),
			event: promoted(),
			want: func(s lifecycle.State) lifecycle.State {
				s.Live = targetID
				s.CutoverRevision = 2
				s.Attempt.Phase = lifecycle.PhasePromoted
				s.Attempt.PromotedAt = promotedAt
				return s
			},
		},
		{
			name:  "RolledBack reverts the live version and vacates the slot",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp(), promoted()),
			event: lifecycle.RolledBack{From: targetID, RevertedTo: previousID, Revision: 3, At: promotedAt},
			want: func(s lifecycle.State) lifecycle.State {
				s.Live = previousID
				s.CutoverRevision = 3
				s.Attempt = lifecycle.AttemptState{}
				return s
			},
		},
		{
			name:  "Abandoned vacates the slot",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}),
			event: lifecycle.Abandoned{Cause: "handler bug discovered mid-replay"},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt = lifecycle.AttemptState{}
				return s
			},
		},
		{
			name:  "RetireStarted reserves the retirement",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp(), promoted()),
			event: lifecycle.RetireStarted{Retiring: previousID, At: retiringAt},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt.Phase = lifecycle.PhaseRetiring
				s.Attempt.RetiringAt = retiringAt
				return s
			},
		},
		{
			name:  "PreviousRetired completes the rebuild, leaving the live version",
			prior: fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp(), promoted(), lifecycle.RetireStarted{Retiring: previousID, At: retiringAt}),
			event: lifecycle.PreviousRetired{Retired: previousID},
			want: func(s lifecycle.State) lifecycle.State {
				s.Attempt = lifecycle.AttemptState{}
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

// TestFold_HappyPath pins the full successful fold end to end: the slot is
// vacated, the new version stays live, and the allocation mark is permanent.
func TestFold_HappyPath(t *testing.T) {
	t.Parallel()

	state := fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp(), promoted(),
		lifecycle.RetireStarted{Retiring: previousID, At: retiringAt},
		lifecycle.PreviousRetired{Retired: previousID})

	want := lifecycle.State{Name: "orders", Live: targetID, CutoverRevision: 2, Allocated: 7}
	if state != want {
		t.Errorf("want state %+v, got %+v", want, state)
	}
}

// TestFold_NeverReuseAfterRollback pins the allocation semantics: rolling v7
// back to v6 reverts Live but not Allocated, so the next rebuild targets v8.
func TestFold_NeverReuseAfterRollback(t *testing.T) {
	t.Parallel()

	state := fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp(), promoted(),
		lifecycle.RolledBack{From: targetID, RevertedTo: previousID, Revision: 3, At: promotedAt})

	want := lifecycle.State{Name: "orders", Live: previousID, CutoverRevision: 3, Allocated: 7}
	if state != want {
		t.Errorf("want state %+v, got %+v", want, state)
	}

	next := lifecycle.RebuildInitiated{
		Attempt:  attemptID,
		Target:   projection.ID{Name: "orders", Version: 8},
		Previous: previousID,
		Reason:   "second attempt",
		At:       initiatedAt,
	}.ApplyTo(state)

	if next.Allocated != 8 || next.Attempt.Target.Version != 8 {
		t.Errorf("want the next attempt allocated v8, got %+v", next)
	}
}

// TestFold_AppliesDespiteInconsistentLineage pins the lineage stance: the
// payload's defense-in-depth fields are checked against the fold's own
// state, but a persisted event won its arbitration, so it applies either
// way — logged, never silently skipped.
func TestFold_AppliesDespiteInconsistentLineage(t *testing.T) {
	t.Parallel()

	prior := fold(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp())

	inconsistent := lifecycle.Promoted{
		Previous: projection.ID{Name: "orders", Version: 3},
		Next:     targetID,
		At:       promotedAt,
	}

	got := inconsistent.ApplyTo(prior)

	if got.Live != targetID || got.Attempt.Phase != lifecycle.PhasePromoted {
		t.Errorf("want the inconsistent promotion applied, got %+v", got)
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

	codec := estoria.JSONDomainEventCodec[lifecycle.State]{}
	prior := fold(initiated(), claimed(), lifecycle.BuildStarted{})

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

	if got := lifecycle.NewState(uuid.Must(uuid.NewV4())); got != (lifecycle.State{}) {
		t.Errorf("want a zero state regardless of stream UUID, got %+v", got)
	}
}

// TestStreamUUID pins the addressing scheme to its exact derived values:
// the namespace constant is the address of every existing deployment's
// lifecycle streams, so a drift in the derivation is a breaking change this
// test must catch.
func TestStreamUUID(t *testing.T) {
	t.Parallel()

	want := uuid.Must(uuid.FromString("58bd8765-4b5a-55eb-b48f-b6e5449c307b"))
	if got := lifecycle.StreamUUID("orders"); got != want {
		t.Errorf("want the pinned stream UUID %s for \"orders\", got %s", want, got)
	}

	if lifecycle.StreamUUID("orders") == lifecycle.StreamUUID("carts") {
		t.Error("want distinct stream UUIDs for distinct names")
	}
}

// TestLifecycleAggregate_EndToEnd pins that the lifecycle aggregate is an
// ordinary estoria aggregate: stored through a stock aggregate store, under
// the reserved estoria.projection stream type at the name-derived UUID, with
// the default JSON codec, and hydrated back to the same state.
func TestLifecycleAggregate_EndToEnd(t *testing.T) {
	t.Parallel()

	events, err := esmemory.NewEventStore()
	if err != nil {
		t.Fatalf("creating event store: %v", err)
	}

	store, err := lifecycle.NewStore(events)
	if err != nil {
		t.Fatalf("creating lifecycle store: %v", err)
	}

	if got, want := store.AggregateType(), "estoria.projection"; got != want {
		t.Errorf("want aggregate type %q, got %q", want, got)
	}

	aggregate := store.New(lifecycle.StreamUUID("orders"))
	aggregate.Append(initiated(), claimed(), lifecycle.BuildStarted{}, caughtUp(), promoted())

	if err := store.Save(t.Context(), aggregate, nil); err != nil {
		t.Fatalf("saving aggregate: %v", err)
	}

	loaded, err := store.Load(t.Context(), lifecycle.StreamUUID("orders"), nil)
	if err != nil {
		t.Fatalf("loading aggregate: %v", err)
	}

	if got, want := loaded.State(), aggregate.State(); got != want {
		t.Errorf("want hydrated state to match saved state:\nwant %+v\ngot  %+v", want, got)
	}

	if got := loaded.Version(); got != 5 {
		t.Errorf("want aggregate version 5, got %d", got)
	}
}

func allEvents() []estoria.DomainEvent[lifecycle.State] {
	return []estoria.DomainEvent[lifecycle.State]{
		initiated(),
		lifecycle.RunnerClaimed{Attempt: attemptID, Runner: runnerID, FromPosition: 1_000, At: claimedAt},
		lifecycle.BuildStarted{},
		caughtUp(),
		promoted(),
		lifecycle.RolledBack{From: targetID, RevertedTo: previousID, At: promotedAt},
		lifecycle.Abandoned{Cause: "handler bug discovered mid-replay"},
		lifecycle.RetireStarted{Retiring: previousID, At: retiringAt},
		lifecycle.PreviousRetired{Retired: previousID},
	}
}
