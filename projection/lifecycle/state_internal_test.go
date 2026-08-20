package lifecycle

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/gofrs/uuid/v5"
)

var (
	internalAttemptID = uuid.Must(uuid.FromString("aa5c0f2d-9c3e-4f2a-8b1d-2f6f6f0a1b2c"))
	internalRunnerID  = uuid.Must(uuid.FromString("5b1f7f6e-3a8c-4d0e-9f2b-7c4a1d8e6f0a"))
	ordersV6          = projection.ID{Name: "orders", Version: 6}
	ordersV7          = projection.ID{Name: "orders", Version: 7}
	internalAt        = time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
)

// stateInPhase returns a structurally valid orders state whose attempt is in
// the given phase, shaped per the phase's own invariants: pre-promotion
// phases keep the previous version live; promoted and retiring phases have
// cut over to the target; every phase past created records a claimed runner.
func stateInPhase(phase Phase) State {
	s := State{
		Name:            "orders",
		Live:            ordersV6,
		CutoverRevision: 1,
		Allocated:       7,
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
		s.CutoverRevision = 2
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
	}

	if phase != PhaseNone && phase != PhaseCreated {
		s.Attempt.Runner = internalRunnerID
	}

	return s
}

// withPolicy returns the state governed by a witnessed retirement policy at
// the given generation.
func withPolicy(s State, generation int64, witnesses ...string) State {
	s.RetirementPolicy = RetirementPolicy{Generation: generation, Witnesses: witnesses}
	return s
}

// withUnwitnessedPolicy returns the state governed by an explicitly
// unwitnessed retirement policy at the given generation.
func withUnwitnessedPolicy(s State, generation int64) State {
	s.RetirementPolicy = RetirementPolicy{Generation: generation, Unwitnessed: true}
	return s
}

// withCapturedWitnesses returns the state with the retirement reservation's
// captured membership installed.
func withCapturedWitnesses(s State, witnesses ...string) State {
	s.Attempt.RetiringWitnesses = witnesses
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
		s.CutoverRevision = 1
	}

	if phase != PhaseNone && phase != PhaseCreated {
		s.Attempt.Runner = internalRunnerID
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
			name:       "build started without a claimed runner",
			prior:      stateInPhase(PhaseCreated),
			event:      BuildStarted{},
			wantReason: "without a claimed runner",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseBuilding {
					t.Errorf("want the phase applied, got %s", got.Attempt.Phase)
				}
			},
		},
		{
			name:       "runner claim with no rebuild in flight",
			prior:      stateInPhase(PhaseNone),
			event:      RunnerClaimed{Attempt: internalAttemptID, Runner: internalRunnerID, At: internalAt},
			wantReason: "no rebuild in flight",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Runner != internalRunnerID {
					t.Errorf("want the claim applied as recorded, got %+v", got.Attempt)
				}
			},
		},
		{
			name: "runner claim in an unknown phase",
			prior: func() State {
				s := stateInPhase(PhaseBuilding)
				s.Attempt.Phase = Phase(99)
				return s
			}(),
			event:      RunnerClaimed{Attempt: internalAttemptID, Runner: internalRunnerID, At: internalAt},
			wantReason: "unknown phase",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "runner claim with no attempt ID",
			prior:      stateInPhase(PhaseBuilding),
			event:      RunnerClaimed{Runner: internalRunnerID, At: internalAt},
			wantReason: "no attempt ID",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Runner != internalRunnerID {
					t.Errorf("want the claim applied as recorded, got %+v", got.Attempt)
				}
			},
		},
		{
			name:       "runner claim for a different attempt",
			prior:      stateInPhase(PhaseBuilding),
			event:      RunnerClaimed{Attempt: uuid.Must(uuid.NewV4()), Runner: internalRunnerID, At: internalAt},
			wantReason: "different attempt",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Runner != internalRunnerID {
					t.Errorf("want the misdirected claim applied as recorded, got %+v", got.Attempt)
				}
			},
		},
		{
			name:       "runner claim with no runner ID",
			prior:      stateInPhase(PhaseBuilding),
			event:      RunnerClaimed{Attempt: internalAttemptID, At: internalAt},
			wantReason: "no runner ID",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if !got.Attempt.Runner.IsNil() {
					t.Errorf("want the nil claimant applied as recorded, got %+v", got.Attempt)
				}
			},
		},
		{
			name:       "catch-up outside the building and caught-up phases",
			prior:      stateInPhase(PhaseCreated),
			event:      CaughtUp{Position: 9, At: internalAt},
			wantReason: "outside the building and caught-up phases",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseCaughtUp || got.Attempt.CaughtUpPos != 9 {
					t.Errorf("want the catch-up applied, got %+v", got.Attempt)
				}
			},
		},
		{
			name:       "catch-up after promotion",
			prior:      stateInPhase(PhasePromoted),
			event:      CaughtUp{Position: 9, At: internalAt},
			wantReason: "outside the building and caught-up phases",
			applied:    func(*testing.T, State) {},
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

				if got.Live != ordersV6 || !got.Attempt.Vacant() {
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

				if got.Live != (projection.ID{}) || !got.Attempt.Vacant() {
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
			name:       "promotion outside the cutover revision sequence",
			prior:      stateInPhase(PhaseCaughtUp),
			event:      Promoted{Previous: ordersV6, Next: ordersV7, Revision: 3, At: internalAt},
			wantReason: "outside the cutover revision sequence",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Live != ordersV7 || got.CutoverRevision != 3 {
					t.Errorf("want the promotion applied with its recorded revision, got %+v", got)
				}
			},
		},
		{
			name:       "promotion with a revision that would lower the counter",
			prior:      stateInPhase(PhaseCaughtUp),
			event:      Promoted{Previous: ordersV6, Next: ordersV7, Revision: 0, At: internalAt},
			wantReason: "outside the cutover revision sequence",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.CutoverRevision != 1 {
					t.Errorf("want the revision never lowered, got %d", got.CutoverRevision)
				}
			},
		},
		{
			name:       "rollback outside the cutover revision sequence",
			prior:      stateInPhase(PhasePromoted),
			event:      RolledBack{From: ordersV7, RevertedTo: ordersV6, Revision: 9, At: internalAt},
			wantReason: "outside the cutover revision sequence",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Live != ordersV6 || got.CutoverRevision != 9 {
					t.Errorf("want the rollback applied with its recorded revision, got %+v", got)
				}
			},
		},
		{
			name:       "rollback with a revision that would lower the counter",
			prior:      stateInPhase(PhasePromoted),
			event:      RolledBack{From: ordersV7, RevertedTo: ordersV6, Revision: 1, At: internalAt},
			wantReason: "outside the cutover revision sequence",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.CutoverRevision != 2 {
					t.Errorf("want the revision never lowered, got %d", got.CutoverRevision)
				}
			},
		},
		{
			name: "promotion past an exhausted cutover revision",
			prior: func() State {
				s := stateInPhase(PhaseCaughtUp)
				s.CutoverRevision = math.MaxInt64
				return s
			}(),
			// The wrapped stamp an unguarded increment would record: the
			// sequence arm's own wrapped arithmetic accepts it as continuity,
			// so only a dedicated ceiling arm can mark it.
			event:      Promoted{Previous: ordersV6, Next: ordersV7, Revision: math.MinInt64, At: internalAt},
			wantReason: "exhausted cutover revision",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.CutoverRevision != math.MaxInt64 {
					t.Errorf("want the revision held at its ceiling, got %d", got.CutoverRevision)
				}
			},
		},
		{
			name: "rollback past an exhausted cutover revision",
			prior: func() State {
				s := stateInPhase(PhasePromoted)
				s.CutoverRevision = math.MaxInt64
				return s
			}(),
			event:      RolledBack{From: ordersV7, RevertedTo: ordersV6, Revision: math.MinInt64, At: internalAt},
			wantReason: "exhausted cutover revision",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.CutoverRevision != math.MaxInt64 {
					t.Errorf("want the revision held at its ceiling, got %d", got.CutoverRevision)
				}
			},
		},
		{
			name:       "abandonment outside the pre-promotion phases",
			prior:      stateInPhase(PhasePromoted),
			event:      Abandoned{Cause: "too late"},
			wantReason: "outside the pre-promotion phases",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if !got.Attempt.Vacant() {
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

				if !got.Attempt.Vacant() {
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
		{
			name:       "policy generation discontinuous",
			prior:      State{Name: "orders", Live: ordersV6, CutoverRevision: 1, Allocated: 6},
			event:      RetirementPolicySet{Generation: 2, Witnesses: []string{"router"}, Actor: "op", Reason: "gate", At: internalAt},
			wantReason: "generation is discontinuous",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.RetirementPolicy.Generation != 2 {
					t.Errorf("want the transition applied as recorded, got %+v", got.RetirementPolicy)
				}
			},
		},
		{
			name:       "policy transition without an actor",
			prior:      State{Name: "orders", Live: ordersV6, CutoverRevision: 1, Allocated: 6},
			event:      RetirementPolicySet{Generation: 1, Witnesses: []string{"router"}, Reason: "gate", At: internalAt},
			wantReason: "without an actor and reason",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "policy with witnesses alongside the unwitnessed mode",
			prior:      State{Name: "orders", Live: ordersV6, CutoverRevision: 1, Allocated: 6},
			event:      RetirementPolicySet{Generation: 1, Witnesses: []string{"router"}, Unwitnessed: true, Actor: "op", Reason: "gate", At: internalAt},
			wantReason: "alongside the unwitnessed mode",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "policy with neither witnesses nor the unwitnessed mode",
			prior:      State{Name: "orders", Live: ordersV6, CutoverRevision: 1, Allocated: 6},
			event:      RetirementPolicySet{Generation: 1, Actor: "op", Reason: "gate", At: internalAt},
			wantReason: "neither witnesses nor the unwitnessed mode",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "policy witness set unsorted",
			prior:      State{Name: "orders", Live: ordersV6, CutoverRevision: 1, Allocated: 6},
			event:      RetirementPolicySet{Generation: 1, Witnesses: []string{"router", "auditor"}, Actor: "op", Reason: "gate", At: internalAt},
			wantReason: "not canonical",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "policy witness set with duplicates",
			prior:      State{Name: "orders", Live: ordersV6, CutoverRevision: 1, Allocated: 6},
			event:      RetirementPolicySet{Generation: 1, Witnesses: []string{"router", "router"}, Actor: "op", Reason: "gate", At: internalAt},
			wantReason: "not canonical",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "promotion bound to an inactive policy generation",
			prior:      withPolicy(stateInPhase(PhaseCaughtUp), 1, "router"),
			event:      Promoted{Previous: ordersV6, Next: ordersV7, Revision: 2, PolicyGeneration: 0, At: internalAt},
			wantReason: "inactive retirement policy generation",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Live != ordersV7 {
					t.Errorf("want the promotion applied despite the poison, got live %s", got.Live)
				}
			},
		},
		{
			name:       "retirement reserved under an inactive policy generation",
			prior:      withPolicy(stateInPhase(PhasePromoted), 1, "router"),
			event:      RetireStarted{Retiring: ordersV6, PolicyGeneration: 0, Witnesses: []string{"router"}, Receipts: []WitnessReceipt{{Witness: "router", Cutover: Cutover{Live: ordersV7, Revision: 2}}}, At: internalAt},
			wantReason: "inactive policy generation",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "retirement reserved with neither a policy nor an override",
			prior:      stateInPhase(PhasePromoted),
			event:      RetireStarted{Retiring: ordersV6, At: internalAt},
			wantReason: "neither a policy nor an audited override",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.Attempt.Phase != PhaseRetiring {
					t.Errorf("want the reservation applied despite the poison, got %s", got.Attempt.Phase)
				}
			},
		},
		{
			name:       "retirement override without an actor",
			prior:      stateInPhase(PhasePromoted),
			event:      RetireStarted{Retiring: ordersV6, Override: RetirementOverride{Reason: "emergency"}, At: internalAt},
			wantReason: "override recorded without an actor and reason",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "overridden retirement records witnesses",
			prior:      stateInPhase(PhasePromoted),
			event:      RetireStarted{Retiring: ordersV6, Override: RetirementOverride{Actor: "op", Reason: "emergency"}, Witnesses: []string{"router"}, At: internalAt},
			wantReason: "overridden retirement records witnesses",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "unwitnessed retirement records witnesses",
			prior:      withUnwitnessedPolicy(stateInPhase(PhasePromoted), 1),
			event:      RetireStarted{Retiring: ordersV6, PolicyGeneration: 1, Witnesses: []string{"router"}, At: internalAt},
			wantReason: "unwitnessed retirement records witnesses",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "retirement captured a foreign witness set",
			prior:      withPolicy(stateInPhase(PhasePromoted), 1, "router"),
			event:      RetireStarted{Retiring: ordersV6, PolicyGeneration: 1, Witnesses: []string{"auditor"}, Receipts: []WitnessReceipt{{Witness: "auditor", Cutover: Cutover{Live: ordersV7, Revision: 2}}}, At: internalAt},
			wantReason: "not the active policy's",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if !slices.Equal(got.Attempt.RetiringWitnesses, []string{"auditor"}) {
					t.Errorf("want the captured set applied as recorded, got %v", got.Attempt.RetiringWitnesses)
				}
			},
		},
		{
			name:       "retirement receipts attest a stale cutover",
			prior:      withPolicy(stateInPhase(PhasePromoted), 1, "router"),
			event:      RetireStarted{Retiring: ordersV6, PolicyGeneration: 1, Witnesses: []string{"router"}, Receipts: []WitnessReceipt{{Witness: "router", Cutover: Cutover{Live: ordersV7, Revision: 99}}}, At: internalAt},
			wantReason: "do not attest the live cutover",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "retirement receipts missing a witness",
			prior:      withPolicy(stateInPhase(PhasePromoted), 1, "router"),
			event:      RetireStarted{Retiring: ordersV6, PolicyGeneration: 1, Witnesses: []string{"router"}, At: internalAt},
			wantReason: "do not attest the live cutover",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "first-version completion records receipts",
			prior:      firstVersionInPhase(PhasePromoted),
			event:      PreviousRetired{Receipts: []WitnessReceipt{{Witness: "router", Cutover: Cutover{Live: projection.ID{Name: "orders", Version: 1}, Revision: 1}}}},
			wantReason: "first-version retirement completion records receipts",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "completion receipts do not re-attest the captured witnesses",
			prior:      withCapturedWitnesses(stateInPhase(PhaseRetiring), "router"),
			event:      PreviousRetired{Retired: ordersV6},
			wantReason: "do not re-attest the captured witnesses",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "policy recorded before initialization",
			prior:      State{},
			event:      RetirementPolicySet{Generation: 1, Witnesses: []string{"router"}, Actor: "op", Reason: "gate", At: internalAt},
			wantReason: "before lifecycle initialization",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.RetirementPolicy.Generation != 1 {
					t.Errorf("want the transition applied as recorded, got %+v", got.RetirementPolicy)
				}
			},
		},
		{
			name:       "policy generations exhausted",
			prior:      withPolicy(State{Name: "orders", Live: ordersV6, CutoverRevision: 1, Allocated: 6}, math.MaxInt64, "router"),
			event:      RetirementPolicySet{Generation: math.MinInt64, Witnesses: []string{"router"}, Actor: "op", Reason: "gate", At: internalAt},
			wantReason: "generations are exhausted",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.RetirementPolicy.Generation != math.MinInt64 {
					t.Errorf("want the transition applied as recorded, got %+v", got.RetirementPolicy)
				}
			},
		},
		{
			name:       "policy transition past the reachable ceiling",
			prior:      withPolicy(State{Name: "orders", Live: ordersV6, CutoverRevision: 1, Allocated: 6}, math.MaxInt64-1, "router"),
			event:      RetirementPolicySet{Generation: math.MaxInt64, Witnesses: []string{"router"}, Actor: "op", Reason: "gate", At: internalAt},
			wantReason: "generations are exhausted",
			applied: func(t *testing.T, got State) {
				t.Helper()

				if got.RetirementPolicy.Generation != math.MaxInt64 {
					t.Errorf("want the transition applied as recorded, got %+v", got.RetirementPolicy)
				}
			},
		},
		{
			name:       "retirement receipt attests the wrong witness",
			prior:      withPolicy(stateInPhase(PhasePromoted), 1, "router"),
			event:      RetireStarted{Retiring: ordersV6, PolicyGeneration: 1, Witnesses: []string{"router"}, Receipts: []WitnessReceipt{{Witness: "auditor", Cutover: Cutover{Live: ordersV7, Revision: 2}}}, At: internalAt},
			wantReason: "do not attest the live cutover",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "overridden retirement records receipts",
			prior:      stateInPhase(PhasePromoted),
			event:      RetireStarted{Retiring: ordersV6, Override: RetirementOverride{Actor: "op", Reason: "emergency"}, Receipts: []WitnessReceipt{{Witness: "router", Cutover: Cutover{Live: ordersV7, Revision: 2}}}, At: internalAt},
			wantReason: "overridden retirement records witnesses",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "unwitnessed retirement records receipts",
			prior:      withUnwitnessedPolicy(stateInPhase(PhasePromoted), 1),
			event:      RetireStarted{Retiring: ordersV6, PolicyGeneration: 1, Receipts: []WitnessReceipt{{Witness: "router", Cutover: Cutover{Live: ordersV7, Revision: 2}}}, At: internalAt},
			wantReason: "unwitnessed retirement records witnesses",
			applied:    func(*testing.T, State) {},
		},
		{
			name:       "retirement override without a reason",
			prior:      stateInPhase(PhasePromoted),
			event:      RetireStarted{Retiring: ordersV6, Override: RetirementOverride{Actor: "op"}, At: internalAt},
			wantReason: "override recorded without an actor and reason",
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

// TestFold_PolicyCeilingBoundary pins the exhaustion arm's edge: the last
// reachable transition — to generation MaxInt64-1, each generation consuming
// a stream event beside the initiation — applies cleanly; only transitions
// past it poison. The prior is the one composition that reaches the edge
// within the stream's version space: a first attempt still in PhaseCreated,
// whose initiation plus MaxInt64-2 recorded transitions sit at aggregate
// version MaxInt64-1, leaving exactly one slot for this event.
func TestFold_PolicyCeilingBoundary(t *testing.T) {
	t.Parallel()

	prior := withPolicy(firstVersionInPhase(PhaseCreated), math.MaxInt64-2, "router")

	got := RetirementPolicySet{
		Generation: math.MaxInt64 - 1,
		Witnesses:  []string{"router"},
		Actor:      "op",
		Reason:     "gate",
		At:         internalAt,
	}.ApplyTo(prior)

	if got.InvalidReason != "" {
		t.Fatalf("want the final reachable transition applied cleanly, got poisoned: %s", got.InvalidReason)
	}

	if got.RetirementPolicy.Generation != math.MaxInt64-1 {
		t.Errorf("want the ceiling generation installed, got %+v", got.RetirementPolicy)
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

		if !got.Attempt.Vacant() || got.Live != ordersV7 {
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

		if !got.Attempt.Vacant() {
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

// TestValidate_ClaimedRunnerRequiredPastCreated pins the structural rule the
// claim protocol establishes: a runner claim precedes every processor start,
// so an attempt in any phase past created without a recorded runner cannot
// come from a clean fold — only from tampered or reset persistence — while
// an admitted-but-never-run attempt legitimately has no claimant yet.
func TestValidate_ClaimedRunnerRequiredPastCreated(t *testing.T) {
	t.Parallel()

	for _, phase := range []Phase{PhaseBuilding, PhaseCaughtUp, PhasePromoted, PhaseRetiring} {
		t.Run(phase.String(), func(t *testing.T) {
			t.Parallel()

			s := stateInPhase(phase)
			s.Attempt.Runner = uuid.UUID{}

			if err := s.validate(); err == nil {
				t.Errorf("want a runnerless %s attempt rejected, got nil", phase)
			}
		})
	}

	if err := stateInPhase(PhaseCreated).validate(); err != nil {
		t.Errorf("want an admitted-but-unclaimed attempt still valid, got %v", err)
	}
}

// TestValidate_CutoverRevisionPairsWithLive pins the pairing invariant — every
// cutover flips Live to a non-zero version and the first promotion records
// revision 1, so a live version and a positive revision exist together or
// not at all — and the cutover accounting bound: completed allocations record
// at most one promotion and one rollback each, the first promotion retains
// nothing to roll back to, and an in-flight attempt's own allocation has
// recorded nothing, or exactly its promotion once promoted or retiring.
func TestValidate_CutoverRevisionPairsWithLive(t *testing.T) {
	t.Parallel()

	t.Run("live version with no revision", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseNone)
		s.CutoverRevision = 0

		if err := s.validate(); err == nil {
			t.Error("want a live version without a cutover revision rejected, got nil")
		}
	})

	t.Run("revision with no live version", func(t *testing.T) {
		t.Parallel()

		s := State{Name: "orders", Allocated: 1, CutoverRevision: 1}

		if err := s.validate(); err == nil {
			t.Error("want a cutover revision without a live version rejected, got nil")
		}
	})

	t.Run("negative revision", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseNone)
		s.CutoverRevision = -1

		if err := s.validate(); err == nil {
			t.Error("want a negative cutover revision rejected, got nil")
		}
	})

	t.Run("revision at the allocation bound", func(t *testing.T) {
		t.Parallel()

		// A first promotion plus a promote-rollback pair per later
		// allocation: 2A-1 is the most cutovers A allocations can record.
		// Every pair reverts to what was live before it, so the bound-exact
		// history necessarily ends with the first version live.
		s := stateInPhase(PhaseNone)
		s.CutoverRevision = 2*int64(s.Allocated) - 1
		s.Live = projection.ID{Name: "orders", Version: 1}

		if err := s.validate(); err != nil {
			t.Errorf("want the bound-exact revision valid, got %v", err)
		}
	})

	t.Run("revision past the allocation bound", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseNone)
		s.CutoverRevision = 2 * int64(s.Allocated)

		if err := s.validate(); err == nil {
			t.Error("want a revision no clean history could record rejected, got nil")
		}
	})

	t.Run("revision far past the allocation bound", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseNone)
		s.CutoverRevision = 100 * int64(s.Allocated)

		if err := s.validate(); err == nil {
			t.Error("want a revision deep past the bound rejected, not only the boundary, got nil")
		}
	})

	t.Run("revision past the in-flight attempt's bound", func(t *testing.T) {
		t.Parallel()

		// The attempt's own allocation has recorded nothing yet, so only the
		// completed A-1 allocations bound the revision: 2(A-1)-1, not 2A-1.
		s := stateInPhase(PhaseCaughtUp)
		s.CutoverRevision = 2*int64(s.Allocated) - 2

		if err := s.validate(); err == nil {
			t.Error("want a revision the unpromoted attempt cannot have recorded rejected, got nil")
		}
	})

	t.Run("revision at the in-flight attempt's bound", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseCaughtUp)
		s.CutoverRevision = 2*int64(s.Allocated) - 3
		s.Live = projection.ID{Name: "orders", Version: 1}
		s.Attempt.Previous = s.Live

		if err := s.validate(); err != nil {
			t.Errorf("want the attempt-discounted bound-exact revision valid, got %v", err)
		}
	})

	t.Run("revision past the promoted attempt's bound", func(t *testing.T) {
		t.Parallel()

		// A promoted attempt has recorded exactly its promotion: its rollback
		// would vacate the slot, so 2(A-1)-1 completed cutovers plus one is
		// the ceiling while it remains in flight.
		s := stateInPhase(PhasePromoted)
		s.CutoverRevision = 2*int64(s.Allocated) - 1

		if err := s.validate(); err == nil {
			t.Error("want a revision the promoted attempt cannot have recorded rejected, got nil")
		}
	})

	t.Run("revision at the promoted attempt's bound", func(t *testing.T) {
		t.Parallel()

		// Bound-exact completed allocations left the first version live, so
		// the in-flight promotion necessarily retained v1 as its previous.
		s := stateInPhase(PhasePromoted)
		s.CutoverRevision = 2*int64(s.Allocated) - 2
		s.Attempt.Previous = projection.ID{Name: "orders", Version: 1}

		if err := s.validate(); err != nil {
			t.Errorf("want the promoted attempt's bound-exact revision valid, got %v", err)
		}
	})

	t.Run("revision at the retiring attempt's bound", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseRetiring)
		s.CutoverRevision = 2*int64(s.Allocated) - 2
		s.Attempt.Previous = projection.ID{Name: "orders", Version: 1}

		if err := s.validate(); err != nil {
			t.Errorf("want the retiring attempt's bound-exact revision valid, got %v", err)
		}
	})

	t.Run("a second cutover from a single allocation", func(t *testing.T) {
		t.Parallel()

		// One allocation, promoted and in flight: revision 1 is the only
		// cutover any history could have recorded.
		s := firstVersionInPhase(PhasePromoted)
		s.CutoverRevision = 2

		if err := s.validate(); err == nil {
			t.Error("want a second cutover from a single allocation rejected, got nil")
		}
	})

	t.Run("revision past the created attempt's bound", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseCreated)
		s.CutoverRevision = 2*int64(s.Allocated) - 2

		if err := s.validate(); err == nil {
			t.Error("want a revision the admitted attempt cannot have recorded rejected, got nil")
		}
	})

	t.Run("revision at the created attempt's bound", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseCreated)
		s.CutoverRevision = 2*int64(s.Allocated) - 3
		s.Live = projection.ID{Name: "orders", Version: 1}
		s.Attempt.Previous = s.Live

		if err := s.validate(); err != nil {
			t.Errorf("want the created attempt's bound-exact revision valid, got %v", err)
		}
	})

	t.Run("revision past the building attempt's bound", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseBuilding)
		s.CutoverRevision = 2*int64(s.Allocated) - 2

		if err := s.validate(); err == nil {
			t.Error("want a revision the building attempt cannot have recorded rejected, got nil")
		}
	})

	t.Run("revision at the building attempt's bound", func(t *testing.T) {
		t.Parallel()

		s := stateInPhase(PhaseBuilding)
		s.CutoverRevision = 2*int64(s.Allocated) - 3
		s.Live = projection.ID{Name: "orders", Version: 1}
		s.Attempt.Previous = s.Live

		if err := s.validate(); err != nil {
			t.Errorf("want the building attempt's bound-exact revision valid, got %v", err)
		}
	})

	t.Run("revision past the retiring attempt's bound", func(t *testing.T) {
		t.Parallel()

		// Previous v1 keeps the prefix on the parity rule alone, so an
		// over-discounted retiring arm cannot hide behind the later-version
		// ceiling.
		s := stateInPhase(PhaseRetiring)
		s.CutoverRevision = 2*int64(s.Allocated) - 1
		s.Attempt.Previous = projection.ID{Name: "orders", Version: 1}

		if err := s.validate(); err == nil {
			t.Error("want a revision the retiring attempt cannot have recorded rejected, got nil")
		}
	})

	t.Run("an even revision resting at the first version", func(t *testing.T) {
		t.Parallel()

		// v1 is promoted first, at revision 1, and history returns to it
		// only in promote-rollback pairs: v1 rests at odd revisions only.
		s := State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 1},
			CutoverRevision: 2,
			Allocated:       2,
		}

		if err := s.validate(); err == nil {
			t.Error("want an even revision resting at the first version rejected, got nil")
		}
	})

	t.Run("the vacant ceiling with a later version live", func(t *testing.T) {
		t.Parallel()

		// 2A-1 spends every pair returning to the first promotion, so only
		// version 1 can be live at the ceiling.
		s := State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 2},
			CutoverRevision: 3,
			Allocated:       2,
		}

		if err := s.validate(); err == nil {
			t.Error("want the vacant ceiling with a later version live rejected, got nil")
		}
	})

	t.Run("one below the vacant ceiling with a later version live", func(t *testing.T) {
		t.Parallel()

		s := State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 2},
			CutoverRevision: 2,
			Allocated:       2,
		}

		if err := s.validate(); err != nil {
			t.Errorf("want the later version accepted below the ceiling, got %v", err)
		}
	})

	t.Run("a second cutover behind a first-live promotion", func(t *testing.T) {
		t.Parallel()

		// A zero previous testifies nothing was live when the attempt began,
		// so its promotion is revision 1: the prefix recorded no cutovers.
		s := State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 2},
			CutoverRevision: 2,
			Allocated:       2,
			Attempt: AttemptState{
				ID:          internalAttemptID,
				Target:      projection.ID{Name: "orders", Version: 2},
				Phase:       PhasePromoted,
				Runner:      internalRunnerID,
				InitiatedAt: internalAt,
			},
		}

		if err := s.validate(); err == nil {
			t.Error("want recorded cutovers behind a first-live promotion rejected, got nil")
		}
	})

	t.Run("a first promotion in flight", func(t *testing.T) {
		t.Parallel()

		if err := firstVersionInPhase(PhasePromoted).validate(); err != nil {
			t.Errorf("want a first-live promotion in flight valid, got %v", err)
		}
	})

	t.Run("an even prefix resting at the first version behind an attempt", func(t *testing.T) {
		t.Parallel()

		// Counts alone accept revision 2 from two completed allocations; the
		// prefix parity does not — resting at v1 takes an odd revision.
		s := State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 1},
			CutoverRevision: 2,
			Allocated:       3,
			Attempt: AttemptState{
				ID:          internalAttemptID,
				Target:      projection.ID{Name: "orders", Version: 3},
				Previous:    projection.ID{Name: "orders", Version: 1},
				Phase:       PhaseCaughtUp,
				Runner:      internalRunnerID,
				InitiatedAt: internalAt,
			},
		}

		if err := s.validate(); err == nil {
			t.Error("want an even prefix resting at the first version rejected, got nil")
		}
	})

	t.Run("a promoted prefix at the ceiling with a later version live", func(t *testing.T) {
		t.Parallel()

		s := State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 3},
			CutoverRevision: 4,
			Allocated:       3,
			Attempt: AttemptState{
				ID:          internalAttemptID,
				Target:      projection.ID{Name: "orders", Version: 3},
				Previous:    projection.ID{Name: "orders", Version: 2},
				Phase:       PhasePromoted,
				Runner:      internalRunnerID,
				InitiatedAt: internalAt,
			},
		}

		if err := s.validate(); err == nil {
			t.Error("want a promoted prefix resting a later version at its ceiling rejected, got nil")
		}
	})

	t.Run("a promotion over a live version claiming the first revision", func(t *testing.T) {
		t.Parallel()

		// A non-zero previous testifies v2 was live when the attempt began,
		// so at least one cutover preceded this promotion — it cannot be
		// revision 1.
		s := State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 3},
			CutoverRevision: 1,
			Allocated:       3,
			Attempt: AttemptState{
				ID:          internalAttemptID,
				Target:      projection.ID{Name: "orders", Version: 3},
				Previous:    projection.ID{Name: "orders", Version: 2},
				Phase:       PhasePromoted,
				Runner:      internalRunnerID,
				InitiatedAt: internalAt,
			},
		}

		if err := s.validate(); err == nil {
			t.Error("want a first revision claimed over a live previous rejected, got nil")
		}
	})
}

// TestValidate_RetirementPolicyAndCapture pins the structural invariants of
// the durable policy and the reservation's captured membership: no
// legitimate fold produces policy content without a generation, mixed
// modes, non-canonical witness sets, or captured witnesses outside the
// retiring phase.
func TestValidate_RetirementPolicyAndCapture(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		state   State
		wantErr string // empty accepts
	}{
		{
			name:  "a witnessed policy on a valid state",
			state: withPolicy(stateInPhase(PhasePromoted), 1, "router"),
		},
		{
			name:  "an unwitnessed policy on a valid state",
			state: withUnwitnessedPolicy(stateInPhase(PhasePromoted), 1),
		},
		{
			name:  "captured witnesses matching the sole policy generation",
			state: withPolicy(withCapturedWitnesses(stateInPhase(PhaseRetiring), "router"), 1, "router"),
		},
		{
			name:  "captured witnesses surviving a policy supersede",
			state: withUnwitnessedPolicy(withCapturedWitnesses(stateInPhase(PhaseRetiring), "router"), 2),
		},
		{
			name:    "policy content with no generation",
			state:   withPolicy(stateInPhase(PhaseNone), 0, "router"),
			wantErr: "policy content recorded with no generation",
		},
		{
			name:    "a negative policy generation",
			state:   withUnwitnessedPolicy(stateInPhase(PhaseNone), -1),
			wantErr: "negative policy generation",
		},
		{
			name:    "a generation at the unreachable maximum",
			state:   withPolicy(stateInPhase(PhaseNone), math.MaxInt64, "router"),
			wantErr: "reachable transition count",
		},
		{
			name:  "a generation at the reachable ceiling",
			state: withPolicy(stateInPhase(PhaseNone), math.MaxInt64-1, "router"),
		},
		{
			name: "witnesses alongside the unwitnessed mode",
			state: func() State {
				s := stateInPhase(PhaseNone)
				s.RetirementPolicy = RetirementPolicy{Generation: 1, Witnesses: []string{"router"}, Unwitnessed: true}
				return s
			}(),
			wantErr: "witnesses recorded alongside the unwitnessed mode",
		},
		{
			name: "neither witnesses nor the unwitnessed mode",
			state: func() State {
				s := stateInPhase(PhaseNone)
				s.RetirementPolicy = RetirementPolicy{Generation: 1}
				return s
			}(),
			wantErr: "neither witnesses nor the unwitnessed mode",
		},
		{
			name:    "an unsorted policy witness set",
			state:   withPolicy(stateInPhase(PhaseNone), 1, "router", "auditor"),
			wantErr: "unique and sorted",
		},
		{
			name:    "a duplicated policy witness",
			state:   withPolicy(stateInPhase(PhaseNone), 1, "router", "router"),
			wantErr: "unique and sorted",
		},
		{
			name:    "an empty policy witness ID",
			state:   withPolicy(stateInPhase(PhaseNone), 1, ""),
			wantErr: "must not be empty",
		},
		{
			name:    "captured witnesses outside the retiring phase",
			state:   withCapturedWitnesses(stateInPhase(PhasePromoted), "router"),
			wantErr: "captured retirement witnesses outside the retiring phase",
		},
		{
			name:    "a non-canonical captured set",
			state:   withCapturedWitnesses(stateInPhase(PhaseRetiring), "router", "auditor"),
			wantErr: "captured retirement witnesses",
		},
		{
			name:    "captured witnesses with no recorded policy",
			state:   withCapturedWitnesses(stateInPhase(PhaseRetiring), "router"),
			wantErr: "no recorded policy",
		},
		{
			name:    "captured witnesses under a sole unwitnessed generation",
			state:   withUnwitnessedPolicy(withCapturedWitnesses(stateInPhase(PhaseRetiring), "router"), 1),
			wantErr: "sole unwitnessed policy generation",
		},
		{
			name:    "captured witnesses diverging from the sole generation",
			state:   withPolicy(withCapturedWitnesses(stateInPhase(PhaseRetiring), "auditor"), 1, "router"),
			wantErr: "diverge from the sole policy generation",
		},
		{
			name: "policy on an empty state",
			state: State{
				RetirementPolicy: RetirementPolicy{Generation: 1, Unwitnessed: true},
			},
			wantErr: "no projection name but is not empty",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.state.validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want the state accepted, got %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want the state refused with %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestAttemptStateVacant sweeps AttemptState's fields reflectively: the zero
// value is vacant, and every field set non-zero defeats vacancy — so a new
// field cannot silently escape Vacant's hand-written comparison.
func TestAttemptStateVacant(t *testing.T) {
	t.Parallel()

	if !(AttemptState{}).Vacant() {
		t.Fatal("want the zero attempt vacant")
	}

	typ := reflect.TypeFor[AttemptState]()

	for i := range typ.NumField() {
		field := typ.Field(i)

		var attempt AttemptState

		value := reflect.ValueOf(&attempt).Elem().Field(i)

		switch field.Type {
		case reflect.TypeFor[uuid.UUID]():
			value.Set(reflect.ValueOf(internalAttemptID))
		case reflect.TypeFor[time.Time]():
			value.Set(reflect.ValueOf(internalAt))
		case reflect.TypeFor[projection.ID]():
			value.Set(reflect.ValueOf(ordersV6))
		default:
			//nolint:exhaustive // The default arm fails loudly on any kind the sweep does not handle yet.
			switch field.Type.Kind() {
			case reflect.String:
				value.SetString("x")
			case reflect.Int, reflect.Int64:
				value.SetInt(1)
			case reflect.Slice:
				value.Set(reflect.MakeSlice(field.Type, 1, 1))
			default:
				t.Fatalf("field %s has unhandled type %s: extend the sweep and Vacant together", field.Name, field.Type)
			}
		}

		if attempt.Vacant() {
			t.Errorf("want field %s to defeat vacancy", field.Name)
		}
	}
}

// TestStateClone pins the detachment clone guarantees: writing through a
// clone's slices leaves the receiver untouched, and a reflective sweep of
// State's type tree fails on any reference-typed field the hand-written
// clone does not sever — so a new slice, map, or pointer field cannot
// silently re-alias state handed out at the package boundary.
func TestStateClone(t *testing.T) {
	t.Parallel()

	t.Run("severs the witness slices", func(t *testing.T) {
		t.Parallel()

		original := withPolicy(withCapturedWitnesses(stateInPhase(PhaseRetiring), "router"), 1, "router")

		mutated := original.clone()
		mutated.RetirementPolicy.Witnesses[0] = "tampered"
		mutated.Attempt.RetiringWitnesses[0] = "tampered"

		if original.RetirementPolicy.Witnesses[0] != "router" {
			t.Error("want the policy membership severed from the clone, got the write visible")
		}

		if original.Attempt.RetiringWitnesses[0] != "router" {
			t.Error("want the captured membership severed from the clone, got the write visible")
		}
	})

	t.Run("covers every reference field", func(t *testing.T) {
		t.Parallel()

		cloned := map[string]bool{
			"Attempt.RetiringWitnesses":  true,
			"RetirementPolicy.Witnesses": true,
		}

		var walk func(typ reflect.Type, path string)
		walk = func(typ reflect.Type, path string) {
			if typ == reflect.TypeFor[time.Time]() {
				return // Opaque and immutable by convention; nothing to sever.
			}

			//nolint:exhaustive // The default arm covers every value kind; only reference kinds and containers matter here.
			switch typ.Kind() {
			case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Chan, reflect.Func, reflect.Interface, reflect.UnsafePointer:
				if !cloned[path] {
					t.Errorf("field %s holds a reference clone does not sever: extend clone and this sweep together", path)
				}
			case reflect.Struct:
				for field := range typ.Fields() {
					name := field.Name
					if path != "" {
						name = path + "." + field.Name
					}

					walk(field.Type, name)
				}
			case reflect.Array:
				walk(typ.Elem(), path)
			default:
			}
		}

		walk(reflect.TypeFor[State](), "")
	})
}

// TestValidateSnapshotState_Contract pins the decode-boundary contract
// directly, independent of what fallback replay would reconstruct: a
// poisoned payload is accepted — it is valid testimony of a poisoned fold,
// and commands refuse it through validation as usual — while a clean payload
// must be structurally valid and initialized. Behind the suite, fallback
// replay of a poisoned stream re-poisons, so only a direct assertion holds
// the acceptance arm itself in place.
func TestValidateSnapshotState_Contract(t *testing.T) {
	t.Parallel()

	poisonedLegitimate := stateInPhase(PhaseBuilding)
	poisonedLegitimate.InvalidReason = "two lifecycles claim the same slot"

	for _, tt := range []struct {
		name   string
		state  State
		accept bool
	}{
		{name: "poisoned uninitialized payload is accepted as testimony", state: State{InvalidReason: "two lifecycles claim the same slot"}, accept: true},
		{name: "poisoned structurally invalid payload is accepted as testimony", state: State{Name: "orders", InvalidReason: "two lifecycles claim the same slot"}, accept: true},
		{name: "poisoned initialized payload is accepted as testimony", state: poisonedLegitimate, accept: true},
		{name: "legitimate payload is accepted", state: stateInPhase(PhaseBuilding), accept: true},
		{name: "clean uninitialized payload is rejected", state: State{}, accept: false},
		{name: "clean named payload without allocations is rejected", state: State{Name: "orders"}, accept: false},
		{name: "clean building payload without a claimed runner is rejected", state: func() State {
			s := stateInPhase(PhaseBuilding)
			s.Attempt.Runner = uuid.UUID{}
			return s
		}(), accept: false},
		{name: "clean live payload without a cutover revision is rejected", state: func() State {
			s := stateInPhase(PhaseNone)
			s.CutoverRevision = 0
			return s
		}(), accept: false},
		{name: "clean payload past the allocation bound is rejected", state: func() State {
			s := stateInPhase(PhaseNone)
			s.CutoverRevision = 2 * int64(s.Allocated)
			return s
		}(), accept: false},
		{name: "clean payload whose revision the in-flight attempt cannot have recorded is rejected", state: func() State {
			s := stateInPhase(PhaseCaughtUp)
			s.CutoverRevision = 2*int64(s.Allocated) - 2
			return s
		}(), accept: false},
		{name: "clean payload resting the first version at an even revision is rejected", state: State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 1},
			CutoverRevision: 2,
			Allocated:       2,
		}, accept: false},
		{name: "clean payload recording cutovers behind a first-live promotion is rejected", state: State{
			Name:            "orders",
			Live:            projection.ID{Name: "orders", Version: 2},
			CutoverRevision: 2,
			Allocated:       2,
			Attempt: AttemptState{
				ID:          internalAttemptID,
				Target:      projection.ID{Name: "orders", Version: 2},
				Phase:       PhasePromoted,
				Runner:      internalRunnerID,
				InitiatedAt: internalAt,
			},
		}, accept: false},
		{name: "clean payload with policy content and no generation is rejected", state: withPolicy(stateInPhase(PhaseNone), 0, "router"), accept: false},
		{name: "clean payload with captured witnesses outside retiring is rejected", state: withCapturedWitnesses(stateInPhase(PhasePromoted), "router"), accept: false},
		{name: "witnessed retiring payload is accepted", state: withPolicy(withCapturedWitnesses(stateInPhase(PhaseRetiring), "router"), 1, "router"), accept: true},
		{name: "clean payload with captured witnesses and no policy is rejected", state: withCapturedWitnesses(stateInPhase(PhaseRetiring), "router"), accept: false},
		{name: "clean payload whose capture diverges from the sole generation is rejected", state: withPolicy(withCapturedWitnesses(stateInPhase(PhaseRetiring), "auditor"), 1, "router"), accept: false},
		{name: "clean payload at the unreachable maximum generation is rejected", state: withPolicy(stateInPhase(PhaseNone), math.MaxInt64, "router"), accept: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.state.ValidateSnapshotState()

			if tt.accept && err != nil {
				t.Errorf("want the payload accepted, got %v", err)
			}

			if !tt.accept && err == nil {
				t.Error("want the payload rejected, got nil")
			}
		})
	}
}
