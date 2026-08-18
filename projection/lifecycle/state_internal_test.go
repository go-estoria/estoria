package lifecycle

import (
	"strings"
	"testing"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/gofrs/uuid/v5"
)

var (
	internalAttemptID = uuid.Must(uuid.FromString("aa5c0f2d-9c3e-4f2a-8b1d-2f6f6f0a1b2c"))
	ordersV6          = projection.ID{Name: "orders", Version: 6}
	ordersV7          = projection.ID{Name: "orders", Version: 7}
	internalAt        = time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
)

// stateInPhase returns a structurally valid orders state whose attempt is in
// the given phase, shaped per the phase's own invariants: pre-promotion
// phases keep the previous version live; promoted and retiring phases have
// cut over to the target.
func stateInPhase(phase Phase) State {
	s := State{
		Name:      "orders",
		Live:      ordersV6,
		Allocated: 7,
		Attempt: AttemptState{
			ID:          internalAttemptID,
			Target:      ordersV7,
			Previous:    ordersV6,
			Phase:       phase,
			InitiatedAt: internalAt,
		},
	}

	switch phase {
	case PhaseNone:
		s.Attempt = AttemptState{}
	case PhasePromoted, PhaseRetiring:
		s.Live = ordersV7
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
	}

	return s
}

// firstVersionInPhase returns a structurally valid state for a projection
// that has never been live: a first rebuild with no previous version.
func firstVersionInPhase(phase Phase) State {
	v1 := projection.ID{Name: "orders", Version: 1}

	s := State{
		Name:      "orders",
		Allocated: 1,
		Attempt: AttemptState{
			ID:          internalAttemptID,
			Target:      v1,
			Phase:       phase,
			InitiatedAt: internalAt,
		},
	}

	if phase == PhasePromoted || phase == PhaseRetiring {
		s.Live = v1
	}

	return s
}

// TestFold_PoisonBranches sweeps every poison arm in the fold: each
// inconsistent event marks the state, the mark fails validation, and the
// event still applies — a persisted event won its arbitration, so poisoning
// records the observation without rewriting history.
func TestFold_PoisonBranches(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		prior      State
		event      interface{ ApplyTo(State) State }
		wantReason string
		applied    func(t *testing.T, got State)
	}{
		{
			name:  "admission with no attempt ID",
			prior: State{Name: "orders", Live: ordersV6, Allocated: 6},
			event: RebuildInitiated{
				Attempt:  uuid.UUID{},
				Target:   ordersV7,
				Previous: ordersV6,
				At:       internalAt,
			},
			wantReason: "no attempt ID",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseCreated || got.Allocated != 7 {
					t.Errorf("want the admission applied despite the poison, got %+v", got)
				}
			},
		},
		{
			name:  "admission with an invalid target",
			prior: State{},
			event: RebuildInitiated{
				Attempt: uuid.Must(uuid.NewV4()),
				Target:  projection.ID{Version: 1},
				At:      internalAt,
			},
			wantReason: "invalid target",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseCreated || got.Allocated != 1 || got.Name != "" {
					t.Errorf("want the malformed admission applied as recorded, got %+v", got)
				}
			},
		},
		{
			name:  "admission into an occupied slot",
			prior: stateInPhase(PhaseBuilding),
			event: RebuildInitiated{
				Attempt:  uuid.Must(uuid.NewV4()),
				Target:   projection.ID{Name: "orders", Version: 8},
				Previous: ordersV6,
				At:       internalAt,
			},
			wantReason: "slot is occupied",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Target.Version != 8 {
					t.Errorf("want the displacing admission applied, got %+v", got)
				}
			},
		},
		{
			name:  "admission under a different projection name",
			prior: State{Name: "orders", Allocated: 6, Live: ordersV6},
			event: RebuildInitiated{
				Attempt:  uuid.Must(uuid.NewV4()),
				Target:   projection.ID{Name: "customers", Version: 7},
				Previous: ordersV6,
				At:       internalAt,
			},
			wantReason: "different projection name",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Name != "orders" {
					t.Errorf("want the projection name immutable, got %q", got.Name)
				}
			},
		},
		{
			name:  "admission from a non-live previous",
			prior: State{Name: "orders", Allocated: 6, Live: ordersV6},
			event: RebuildInitiated{
				Attempt:  uuid.Must(uuid.NewV4()),
				Target:   ordersV7,
				Previous: projection.ID{Name: "orders", Version: 3},
				At:       internalAt,
			},
			wantReason: "was not live",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Previous.Version != 3 {
					t.Errorf("want the recorded previous applied, got %+v", got.Attempt)
				}
			},
		},
		{
			name:  "admission outside the allocation sequence",
			prior: State{Name: "orders", Allocated: 6, Live: ordersV6},
			event: RebuildInitiated{
				Attempt:  uuid.Must(uuid.NewV4()),
				Target:   projection.ID{Name: "orders", Version: 6},
				Previous: ordersV6,
				At:       internalAt,
			},
			wantReason: "outside the allocation sequence",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Allocated != 6 {
					t.Errorf("want the allocation high-water mark never lowered, got %d", got.Allocated)
				}
			},
		},
		{
			name:  "admission below the allocation sequence keeps the high-water mark",
			prior: State{Name: "orders", Allocated: 6, Live: ordersV6},
			event: RebuildInitiated{
				Attempt:  uuid.Must(uuid.NewV4()),
				Target:   projection.ID{Name: "orders", Version: 3},
				Previous: ordersV6,
				At:       internalAt,
			},
			wantReason: "outside the allocation sequence",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Allocated != 6 {
					t.Errorf("want the allocation high-water mark never lowered, got %d", got.Allocated)
				}
			},
		},
		{
			name:       "build started outside the created phase",
			prior:      stateInPhase(PhaseBuilding),
			event:      BuildStarted{},
			wantReason: "outside the created phase",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseBuilding {
					t.Errorf("want the phase applied, got %s", got.Attempt.Phase)
				}
			},
		},
		{
			name:       "build resumed outside the building phase",
			prior:      stateInPhase(PhaseCreated),
			event:      BuildResumed{FromPosition: 10},
			wantReason: "outside the building phase",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseBuilding {
					t.Errorf("want the phase applied, got %s", got.Attempt.Phase)
				}
			},
		},
		{
			name:       "catch-up outside the building phase",
			prior:      stateInPhase(PhaseCreated),
			event:      CaughtUp{Position: 9, At: internalAt},
			wantReason: "outside the building phase",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseCaughtUp || got.Attempt.CaughtUpPos != 9 {
					t.Errorf("want the catch-up applied, got %+v", got.Attempt)
				}
			},
		},
		{
			name:       "promotion outside the caught-up phase",
			prior:      stateInPhase(PhaseBuilding),
			event:      Promoted{Previous: ordersV6, Next: ordersV7, At: internalAt},
			wantReason: "outside the caught-up phase",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Live != ordersV7 || got.Attempt.Phase != PhasePromoted {
					t.Errorf("want the promotion applied, got %+v", got)
				}
			},
		},
		{
			name:       "promotion from a version that was not live",
			prior:      stateInPhase(PhaseCaughtUp),
			event:      Promoted{Previous: projection.ID{Name: "orders", Version: 2}, Next: ordersV7, At: internalAt},
			wantReason: "was not live",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "promotion of a version that was not the target",
			prior:      stateInPhase(PhaseCaughtUp),
			event:      Promoted{Previous: ordersV6, Next: projection.ID{Name: "orders", Version: 9}, At: internalAt},
			wantReason: "was not the attempt's target",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "rollback outside the promoted phase",
			prior:      stateInPhase(PhaseCaughtUp),
			event:      RolledBack{From: ordersV7, RevertedTo: ordersV6, At: internalAt},
			wantReason: "outside the promoted phase",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Live != ordersV6 || got.Attempt != (AttemptState{}) {
					t.Errorf("want the rollback applied, got %+v", got)
				}
			},
		},
		{
			name:       "rollback from a version that was not live",
			prior:      stateInPhase(PhasePromoted),
			event:      RolledBack{From: projection.ID{Name: "orders", Version: 2}, RevertedTo: ordersV6, At: internalAt},
			wantReason: "was not live",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "rollback with no previous version",
			prior:      firstVersionInPhase(PhasePromoted),
			event:      RolledBack{From: projection.ID{Name: "orders", Version: 1}, At: internalAt},
			wantReason: "no previous version",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Live != (projection.ID{}) || got.Attempt != (AttemptState{}) {
					t.Errorf("want the rollback applied, got %+v", got)
				}
			},
		},
		{
			name:       "rollback to a version that was not the previous",
			prior:      stateInPhase(PhasePromoted),
			event:      RolledBack{From: ordersV7, RevertedTo: projection.ID{Name: "orders", Version: 2}, At: internalAt},
			wantReason: "was not the attempt's previous",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "abandonment outside the pre-promotion phases",
			prior:      stateInPhase(PhasePromoted),
			event:      Abandoned{Cause: "too late"},
			wantReason: "outside the pre-promotion phases",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt != (AttemptState{}) {
					t.Errorf("want the slot vacated, got %+v", got.Attempt)
				}
			},
		},
		{
			name: "abandonment in an unknown phase",
			prior: func() State {
				s := stateInPhase(PhaseBuilding)
				s.Attempt.Phase = Phase(99)
				return s
			}(),
			event:      Abandoned{Cause: "unknown"},
			wantReason: "unknown phase",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "retirement reserved outside the promoted phase",
			prior:      stateInPhase(PhaseCaughtUp),
			event:      RetireStarted{Retiring: ordersV6, At: internalAt},
			wantReason: "outside the promoted phase",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseRetiring {
					t.Errorf("want the reservation applied, got %s", got.Attempt.Phase)
				}
			},
		},
		{
			name:       "retirement reserved with no previous version",
			prior:      firstVersionInPhase(PhasePromoted),
			event:      RetireStarted{At: internalAt},
			wantReason: "no previous version",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseRetiring {
					t.Errorf("want the reservation applied, got %s", got.Attempt.Phase)
				}
			},
		},
		{
			name:       "retirement reserved for a version that was not the previous",
			prior:      stateInPhase(PhasePromoted),
			event:      RetireStarted{Retiring: projection.ID{Name: "orders", Version: 2}, At: internalAt},
			wantReason: "was not the attempt's previous",
			applied:    func(*testing.T, State) {},
		},
		{
			name: "retirement completed for a reservation with no previous version",
			prior: func() State {
				s := firstVersionInPhase(PhaseRetiring)
				s.Attempt.RetiringAt = internalAt
				return s
			}(),
			event:      PreviousRetired{},
			wantReason: "no previous version",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt != (AttemptState{}) {
					t.Errorf("want the slot vacated, got %+v", got.Attempt)
				}
			},
		},
		{
			name:       "retirement completed outside the retiring phase",
			prior:      stateInPhase(PhaseBuilding),
			event:      PreviousRetired{Retired: ordersV6},
			wantReason: "outside the retiring phase",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "retirement completed directly despite a previous version",
			prior:      stateInPhase(PhasePromoted),
			event:      PreviousRetired{Retired: ordersV6},
			wantReason: "outside the retiring phase",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "retirement completed for a version that was not the previous",
			prior:      stateInPhase(PhaseRetiring),
			event:      PreviousRetired{Retired: projection.ID{Name: "orders", Version: 2}},
			wantReason: "was not the attempt's previous",
			applied:    func(*testing.T, State) {},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.event.ApplyTo(tt.prior)

			if got.InvalidReason == "" {
				t.Fatal("want the fold poisoned, got no mark")
			}

			if !strings.Contains(got.InvalidReason, tt.wantReason) {
				t.Errorf("want the mark to record %q, got %q", tt.wantReason, got.InvalidReason)
			}

			if err := got.validate(); err == nil {
				t.Error("want the poisoned state to fail validation, got nil")
			}

			tt.applied(t, got)
		})
	}
}

// TestFold_LegalCompletionForms pins the two exclusive completion forms the
// poison arms must not catch: a reserved retirement completing from
// PhaseRetiring with its previous version, and a first rebuild completing
// directly from PhasePromoted with a zero Retired ID.
func TestFold_LegalCompletionForms(t *testing.T) {
	t.Parallel()

	t.Run("reserved completion", func(t *testing.T) {
		t.Parallel()

		prior := stateInPhase(PhaseRetiring)

		got := PreviousRetired{Retired: ordersV6}.ApplyTo(prior)
		if got.InvalidReason != "" {
			t.Fatalf("want a clean fold, got mark %q", got.InvalidReason)
		}

		if err := got.validate(); err != nil {
			t.Errorf("want the completed state valid, got %v", err)
		}

		if got.Attempt != (AttemptState{}) || got.Live != ordersV7 {
			t.Errorf("want the slot vacated with the target live, got %+v", got)
		}
	})

	t.Run("first-version direct completion", func(t *testing.T) {
		t.Parallel()

		prior := firstVersionInPhase(PhasePromoted)

		got := PreviousRetired{}.ApplyTo(prior)
		if got.InvalidReason != "" {
			t.Fatalf("want a clean fold, got mark %q", got.InvalidReason)
		}

		if err := got.validate(); err != nil {
			t.Errorf("want the completed state valid, got %v", err)
		}

		if got.Attempt != (AttemptState{}) {
			t.Errorf("want the slot vacated, got %+v", got.Attempt)
		}
	})
}

// TestFold_MarkIsSticky pins first-observation permanence: the first
// inconsistency's message survives a later, differently-worded poisoning
// and a later well-formed terminal event. The second poisoning must carry a
// distinct message, or an overwriting mark would be indistinguishable from
// a retained one.
func TestFold_MarkIsSticky(t *testing.T) {
	t.Parallel()

	poisoned := BuildStarted{}.ApplyTo(stateInPhase(PhaseBuilding))

	first := poisoned.InvalidReason
	if first == "" {
		t.Fatal("want the first event to poison the fold")
	}

	// A second poisoning with a different message: reserving retirement from
	// the building phase.
	again := RetireStarted{Retiring: ordersV6, At: internalAt}.ApplyTo(poisoned)
	if again.InvalidReason == "" || again.InvalidReason != first {
		t.Errorf("want the first mark retained across a later distinct poisoning, got %q", again.InvalidReason)
	}

	// The reservation applied (the attempt is retiring with its previous
	// recorded), so a well-formed completion vacates the slot cleanly.
	covered := PreviousRetired{Retired: ordersV6}.ApplyTo(again)
	if covered.InvalidReason != first {
		t.Errorf("want the mark to survive a well-formed terminal event, got %q", covered.InvalidReason)
	}

	if err := covered.validate(); err == nil {
		t.Error("want the covered-up state to fail validation, got nil")
	}
}

// TestValidate_NamedStateRequiresAllocation pins the representability rule
// behind the snapshot-reset refusal: the admission that records a name
// allocates at least version 1, so a named state with no allocations cannot
// come from any clean fold — only from persistence reset underneath the
// aggregate — and accepting it would let Begin reissue version 1.
func TestValidate_NamedStateRequiresAllocation(t *testing.T) {
	t.Parallel()

	if err := (State{Name: "orders"}).validate(); err == nil {
		t.Error("want a named state with no allocations rejected, got nil")
	}

	if err := (State{}).validate(); err != nil {
		t.Errorf("want the zero state still valid (a fresh aggregate), got %v", err)
	}
}
