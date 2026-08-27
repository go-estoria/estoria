package lifecycle

import (
	"math"
	"slices"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/projection"
	"github.com/gofrs/uuid/v5"
)

// RebuildInitiated records the decision that a rebuild is underway: admission
// to the projection's single in-flight attempt slot and allocation of the
// target version number, as one append. Optimistic concurrency on the
// lifecycle stream is the arbiter — Begin refuses an occupied slot, and two
// concurrent initiations conflict at the same stream version, so at most one
// is admitted. Previous carries the live version at initiation for ledger
// self-containment; the fold's own Live is the authority it is checked
// against. An admission with no attempt ID, with an invalid target, into an
// occupied slot, under a different projection name, from a non-live
// previous, or outside the allocation sequence poisons the fold; the
// projection name is immutable once set, and the allocation high-water mark
// never lowers.
type RebuildInitiated struct {
	Attempt  uuid.UUID
	Target   projection.ID
	Previous projection.ID
	Reason   string
	At       time.Time
}

// EventType returns the type of event.
func (RebuildInitiated) EventType() string { return "rebuildinitiated" }

// New returns a new instance of the event.
func (RebuildInitiated) New() estoria.DomainEvent[State] { return &RebuildInitiated{} }

// ApplyTo applies the event to state, returning the new state.
func (e RebuildInitiated) ApplyTo(s State) State {
	switch {
	case e.Attempt.IsNil():
		s = s.poison("rebuild initiated with no attempt ID",
			"projection", e.Target.Name, "target", e.Target)
	case e.Target.Validate() != nil:
		s = s.poison("rebuild initiated with an invalid target",
			"projection", e.Target.Name, "target", e.Target)
	case s.Attempt.Phase != PhaseNone:
		s = s.poison("rebuild initiated while the attempt slot is occupied",
			"projection", e.Target.Name, "attempt", e.Attempt, "displaced_attempt", s.Attempt.ID)
	case s.Name != "" && e.Target.Name != s.Name:
		s = s.poison("rebuild initiated for a different projection name",
			"projection", s.Name, "target", e.Target)
	case e.Previous != s.Live:
		s = s.poison("rebuild initiated with a previous version that was not live",
			"projection", e.Target.Name, "previous", e.Previous, "live", s.Live)
	case e.Target.Version != s.Allocated+1:
		s = s.poison("rebuild initiated with a target version outside the allocation sequence",
			"projection", e.Target.Name, "target", e.Target, "allocated", s.Allocated)
	}

	if s.Name == "" {
		s.Name = e.Target.Name
	}

	s.Allocated = max(s.Allocated, e.Target.Version)
	s.Attempt = AttemptState{
		ID:          e.Attempt,
		Target:      e.Target,
		Previous:    e.Previous,
		Phase:       PhaseCreated,
		Reason:      e.Reason,
		InitiatedAt: e.At,
	}

	return s
}

// RunnerClaimed records that a runner took ownership of the in-flight
// attempt: appended before the claimant constructs its processor, in every
// runnable phase and phase-preserving in all of them, so duplicate execution
// is durably observable in the stream. The claim names the attempt it
// covers, so a delayed or misdirected claim cannot silently reassign an
// attempt it never belonged to. Ownership transfer is gated on the
// incumbent's recorded exit: runner identity is not checked by handler
// writes or checkpoint saves, and two processors over one attempt would
// interleave writes into the same target storage and checkpoint — so a
// claim is admitted only over a vacant or released claim, or by carrying an
// explicitly attested Takeover of a standing one, durably auditing who
// vouched that the incumbent was quiesced. A superseded runner that is
// still alive observes its displacement and winds itself down.
// FromPosition is the checkpoint position observed at claim time — audit
// information; the processor loads its own resume position from the
// checkpoint store when it starts, and the two can differ if the checkpoint
// moves in between. A claim with no runner ID, with no attempt ID, for a
// different attempt than the one in flight, or with no attempt in flight
// poisons the fold — as does a takeover attestation missing its actor or
// reason, a claim over a standing claim with no attested takeover, and a
// takeover attested with no standing claim to take over.
type RunnerClaimed struct {
	Attempt      uuid.UUID
	Runner       uuid.UUID
	FromPosition int64

	// Takeover audits an explicitly attested takeover of a standing claim:
	// the actor who vouched that the incumbent runner was quiesced, and
	// why. Zero for a claim of a vacant or released attempt.
	Takeover RunnerTakeover

	At time.Time
}

// A RunnerTakeover attests that a standing runner claim was taken over
// deliberately: the incumbent recorded no release — it crashed, or its
// process is otherwise provably gone — and the named actor vouched for its
// quiescence. Recorded in the claim that performed the takeover, so the
// audit trail lives in the same event the arbitration admitted.
type RunnerTakeover struct {
	Actor  string
	Reason string
}

// EventType returns the type of event.
func (RunnerClaimed) EventType() string { return "runnerclaimed" }

// New returns a new instance of the event.
func (RunnerClaimed) New() estoria.DomainEvent[State] { return &RunnerClaimed{} }

// ApplyTo applies the event to state, returning the new state.
func (e RunnerClaimed) ApplyTo(s State) State {
	switch {
	case e.Attempt.IsNil():
		s = s.poison("runner claim recorded with no attempt ID",
			"projection", s.Name)
	case e.Runner.IsNil():
		s = s.poison("runner claim recorded with no runner ID",
			"projection", s.Name)
	}

	switch s.Attempt.Phase {
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp, PhasePromoted, PhaseRetiring:
		if e.Attempt != s.Attempt.ID {
			s = s.poison("runner claim recorded for a different attempt",
				"projection", s.Name, "claimed_attempt", e.Attempt, "attempt", s.Attempt.ID)
		}
	case PhaseNone:
		s = s.poison("runner claim recorded with no rebuild in flight",
			"projection", s.Name, "runner", e.Runner)
	default:
		s = s.poison("runner claim recorded in an unknown phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	}

	switch standing := !s.Attempt.Runner.IsNil() && !s.Attempt.Released; {
	case e.Takeover != (RunnerTakeover{}) && (e.Takeover.Actor == "" || e.Takeover.Reason == ""):
		s = s.poison("runner takeover recorded without an actor and reason",
			"projection", s.Name, "runner", e.Runner)
	case standing && e.Takeover == (RunnerTakeover{}):
		s = s.poison("runner claim recorded over a standing claim without an attested takeover",
			"projection", s.Name, "claimant", s.Attempt.Runner, "runner", e.Runner)
	case !standing && e.Takeover != (RunnerTakeover{}):
		s = s.poison("runner takeover attested with no standing claim to take over",
			"projection", s.Name, "runner", e.Runner)
	}

	s.Attempt.Runner = e.Runner
	s.Attempt.ClaimedAt = e.At
	s.Attempt.Released = false
	s.Attempt.ReleasedAt = time.Time{}

	return s
}

// RunnerReleased records that the attempt's claimed runner wound its run
// down with its processor fully exited: the claim is released, and the
// runner's data-plane writes have provably ceased. A released attempt
// admits the next claim transparently; a standing claim — claimed, never
// released — admits only an explicitly attested takeover, because nothing
// fences data-plane writes within one attempt (see RunnerClaimed). Appended
// best-effort by every wind-down that leaves the attempt in flight under
// this runner's claim; a crashed runner appends nothing, leaving its claim
// standing for an operator-attested takeover. A release with no attempt ID,
// with no runner ID, with no rebuild in flight, for a different attempt, by
// a runner that is not the recorded claimant, or over a claim already
// released poisons the fold.
type RunnerReleased struct {
	Attempt uuid.UUID
	Runner  uuid.UUID
	At      time.Time
}

// EventType returns the type of event.
func (RunnerReleased) EventType() string { return "runnerreleased" }

// New returns a new instance of the event.
func (RunnerReleased) New() estoria.DomainEvent[State] { return &RunnerReleased{} }

// ApplyTo applies the event to state, returning the new state.
func (e RunnerReleased) ApplyTo(s State) State {
	switch {
	case e.Attempt.IsNil():
		s = s.poison("runner release recorded with no attempt ID",
			"projection", s.Name)
	case e.Runner.IsNil():
		s = s.poison("runner release recorded with no runner ID",
			"projection", s.Name)
	}

	switch s.Attempt.Phase {
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp, PhasePromoted, PhaseRetiring:
		switch {
		case e.Attempt != s.Attempt.ID:
			s = s.poison("runner release recorded for a different attempt",
				"projection", s.Name, "released_attempt", e.Attempt, "attempt", s.Attempt.ID)
		case e.Runner != s.Attempt.Runner:
			s = s.poison("runner release recorded by a runner that is not the recorded claimant",
				"projection", s.Name, "releasing_runner", e.Runner, "claimant", s.Attempt.Runner)
		case s.Attempt.Released:
			s = s.poison("runner release recorded over a claim already released",
				"projection", s.Name, "runner", e.Runner)
		}
	case PhaseNone:
		s = s.poison("runner release recorded with no rebuild in flight",
			"projection", s.Name, "runner", e.Runner)
	default:
		s = s.poison("runner release recorded in an unknown phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	}

	s.Attempt.Released = true
	s.Attempt.ReleasedAt = e.At

	return s
}

// BuildStarted records the first start of the target version's processor: the
// one Created-to-Building transition, appended atomically after the starting
// runner's claim. A start with no claimed runner poisons the fold.
type BuildStarted struct{}

// EventType returns the type of event.
func (BuildStarted) EventType() string { return "buildstarted" }

// New returns a new instance of the event.
func (BuildStarted) New() estoria.DomainEvent[State] { return &BuildStarted{} }

// ApplyTo applies the event to state, returning the new state.
func (BuildStarted) ApplyTo(s State) State {
	switch {
	case s.Attempt.Phase != PhaseCreated:
		s = s.poison("build started outside the created phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case s.Attempt.Runner.IsNil():
		s = s.poison("build started without a claimed runner",
			"projection", s.Name)
	}

	s.Attempt.Phase = PhaseBuilding

	return s
}

// CaughtUp records that the target version drained to the head of the event
// sequence — the first time, making the attempt eligible for promotion, or
// again when a later run re-certifies a caught-up attempt by draining to the
// then-current head, preserving the phase. One event with the position and
// elapsed time as payload, not per-batch telemetry.
type CaughtUp struct {
	Position int64
	Duration time.Duration
	At       time.Time
}

// EventType returns the type of event.
func (CaughtUp) EventType() string { return "caughtup" }

// New returns a new instance of the event.
func (CaughtUp) New() estoria.DomainEvent[State] { return &CaughtUp{} }

// ApplyTo applies the event to state, returning the new state.
func (e CaughtUp) ApplyTo(s State) State {
	if s.Attempt.Phase != PhaseBuilding && s.Attempt.Phase != PhaseCaughtUp {
		s = s.poison("catch-up recorded outside the building and caught-up phases",
			"projection", s.Name, "phase", s.Attempt.Phase)
	}

	s.Attempt.Phase = PhaseCaughtUp
	s.Attempt.CaughtUpAt = e.At
	s.Attempt.CaughtUpPos = e.Position

	return s
}

// Promoted records the cutover of reads from Previous to Next. This event is
// the flip: routers and the cutover worker derive or cache what it records.
// The payload carries both versions so the promotion history is
// self-contained; Previous is defense in depth, checked against the fold's
// own Live, not the arbiter — same-stream optimistic concurrency is.
// Revision is the projection's cutover revision this flip records: the
// fold's current revision plus one, stamped by the command under the same
// append that wins the arbitration, so setters order deliveries by it. A
// promotion recorded outside the revision sequence poisons the fold; the
// revision never lowers.
type Promoted struct {
	Previous projection.ID
	Next     projection.ID
	Revision int64

	// PolicyGeneration binds the promotion to the retirement policy
	// generation active when it was recorded, so the policy era that
	// governed every flip is durable and auditable.
	PolicyGeneration int64

	At time.Time
}

// EventType returns the type of event.
func (Promoted) EventType() string { return "promoted" }

// New returns a new instance of the event.
func (Promoted) New() estoria.DomainEvent[State] { return &Promoted{} }

// ApplyTo applies the event to state, returning the new state.
func (e Promoted) ApplyTo(s State) State {
	switch {
	case s.Attempt.Phase != PhaseCaughtUp:
		s = s.poison("promotion recorded outside the caught-up phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case e.Previous != s.Live:
		s = s.poison("promotion recorded from a version that was not live",
			"projection", s.Name, "recorded_previous", e.Previous, "live", s.Live)
	case e.Next != s.Attempt.Target:
		s = s.poison("promotion recorded for a version that was not the attempt's target",
			"projection", s.Name, "recorded_next", e.Next, "target", s.Attempt.Target)
	case s.CutoverRevision == math.MaxInt64:
		// The ceiling arm must precede the sequence arm: the increment below
		// would wrap, and the wrapped stamp would satisfy the wrapped
		// comparison — overflow reading as continuity.
		s = s.poison("promotion recorded past an exhausted cutover revision",
			"projection", s.Name, "recorded_revision", e.Revision)
	case e.Revision != s.CutoverRevision+1:
		s = s.poison("promotion recorded outside the cutover revision sequence",
			"projection", s.Name, "recorded_revision", e.Revision, "revision", s.CutoverRevision)
	case e.PolicyGeneration != s.RetirementPolicy.Generation:
		s = s.poison("promotion bound to an inactive retirement policy generation",
			"projection", s.Name, "recorded_generation", e.PolicyGeneration, "active_generation", s.RetirementPolicy.Generation)
	}

	s.Live = e.Next
	s.CutoverRevision = max(s.CutoverRevision, e.Revision)
	s.Attempt.Phase = PhasePromoted
	s.Attempt.PromotedAt = e.At

	return s
}

// RolledBack records the reversion of reads to the previous version.
// Terminal for the attempt: the slot is vacated, and a subsequent rebuild is
// a new attempt targeting a new version number. From is defense in depth,
// checked against the fold's own Live. A first rebuild has no previous
// version and so no rollback target: a rollback recorded for an attempt with
// no previous poisons the fold rather than passing the lineage check on two
// zero values. Revision is the projection's cutover revision this reversion
// records — a rollback is a cutover, ordered by the same counter as
// promotions, so a setter never mistakes a redelivered older flip for the
// current route. A rollback recorded outside the revision sequence poisons
// the fold; the revision never lowers.
type RolledBack struct {
	From       projection.ID
	RevertedTo projection.ID
	Revision   int64
	At         time.Time
}

// EventType returns the type of event.
func (RolledBack) EventType() string { return "rolledback" }

// New returns a new instance of the event.
func (RolledBack) New() estoria.DomainEvent[State] { return &RolledBack{} }

// ApplyTo applies the event to state, returning the new state.
func (e RolledBack) ApplyTo(s State) State {
	switch {
	case s.Attempt.Phase != PhasePromoted:
		s = s.poison("rollback recorded outside the promoted phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case e.From != s.Live:
		s = s.poison("rollback recorded from a version that was not live",
			"projection", s.Name, "recorded_from", e.From, "live", s.Live)
	case s.Attempt.Previous == (projection.ID{}):
		s = s.poison("rollback recorded for an attempt with no previous version",
			"projection", s.Name, "attempt", s.Attempt.ID)
	case e.RevertedTo != s.Attempt.Previous:
		s = s.poison("rollback recorded to a version that was not the attempt's previous",
			"projection", s.Name, "recorded_reverted_to", e.RevertedTo, "previous", s.Attempt.Previous)
	case s.CutoverRevision == math.MaxInt64:
		s = s.poison("rollback recorded past an exhausted cutover revision",
			"projection", s.Name, "recorded_revision", e.Revision)
	case e.Revision != s.CutoverRevision+1:
		s = s.poison("rollback recorded outside the cutover revision sequence",
			"projection", s.Name, "recorded_revision", e.Revision, "revision", s.CutoverRevision)
	}

	s.Live = e.RevertedTo
	s.CutoverRevision = max(s.CutoverRevision, e.Revision)
	s.Attempt = AttemptState{}

	return s
}

// Abandoned records that the rebuild was given up before promotion. Terminal
// for the attempt: the slot is vacated. The abandoned build's checkpoint and
// storage belong to a version number that is never reused, so any residue is
// inert until explicitly collected.
type Abandoned struct {
	Cause string
}

// EventType returns the type of event.
func (Abandoned) EventType() string { return "abandoned" }

// New returns a new instance of the event.
func (Abandoned) New() estoria.DomainEvent[State] { return &Abandoned{} }

// ApplyTo applies the event to state, returning the new state.
func (Abandoned) ApplyTo(s State) State {
	switch s.Attempt.Phase {
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
	case PhaseNone, PhasePromoted, PhaseRetiring:
		s = s.poison("abandonment recorded outside the pre-promotion phases",
			"projection", s.Name, "phase", s.Attempt.Phase)
	default:
		s = s.poison("abandonment recorded in an unknown phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	}

	s.Attempt = AttemptState{}

	return s
}

// RetireStarted reserves the retirement of the previous version. It contends
// directly with rollback on the lifecycle stream — one wins, the other is
// refused — and nothing is destroyed before that arbitration: teardown runs
// only after this event is durable, and rolling back is illegal from
// PhaseRetiring, so retirement can never destroy a version that is about to
// serve reads again. There is deliberately no un-retire. A first rebuild has
// no previous version and nothing to reserve: its completion is recorded
// directly by PreviousRetired, and a reservation for an attempt with no
// previous poisons the fold rather than passing the lineage check on two
// zero values.
//
// The reservation captures the retirement's witness gate durably: the
// active policy generation, the required witness IDs, and one preflight
// receipt per witness attesting to the exact live cutover — or the audited
// override that authorized proceeding without them. A retry from
// PhaseRetiring uses the captured membership, never current process
// configuration, so a restarted process cannot weaken a reservation it did
// not make.
type RetireStarted struct {
	Retiring         projection.ID
	PolicyGeneration int64
	Witnesses        []string
	Receipts         []WitnessReceipt
	Override         RetirementOverride
	At               time.Time
}

// EventType returns the type of event.
func (RetireStarted) EventType() string { return "retirestarted" }

// New returns a new instance of the event.
func (RetireStarted) New() estoria.DomainEvent[State] { return &RetireStarted{} }

// ApplyTo applies the event to state, returning the new state.
func (e RetireStarted) ApplyTo(s State) State {
	switch {
	case s.Attempt.Phase != PhasePromoted:
		s = s.poison("retirement reserved outside the promoted phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case s.Attempt.Previous == (projection.ID{}):
		s = s.poison("retirement reserved for an attempt with no previous version",
			"projection", s.Name, "attempt", s.Attempt.ID)
	case e.Retiring != s.Attempt.Previous:
		s = s.poison("retirement reserved for a version that was not the attempt's previous",
			"projection", s.Name, "recorded_retiring", e.Retiring, "previous", s.Attempt.Previous)
	case e.PolicyGeneration != s.RetirementPolicy.Generation:
		s = s.poison("retirement reserved under an inactive policy generation",
			"projection", s.Name, "recorded_generation", e.PolicyGeneration, "active_generation", s.RetirementPolicy.Generation)
	}

	switch {
	case e.Override != (RetirementOverride{}):
		switch {
		case e.Override.Actor == "" || e.Override.Reason == "":
			s = s.poison("retirement override recorded without an actor and reason",
				"projection", s.Name, "attempt", s.Attempt.ID)
		case len(e.Witnesses) != 0 || len(e.Receipts) != 0:
			s = s.poison("overridden retirement records witnesses",
				"projection", s.Name, "attempt", s.Attempt.ID)
		}
	case s.RetirementPolicy.zero():
		s = s.poison("retirement reserved with neither a policy nor an audited override",
			"projection", s.Name, "attempt", s.Attempt.ID)
	case s.RetirementPolicy.Unwitnessed:
		if len(e.Witnesses) != 0 || len(e.Receipts) != 0 {
			s = s.poison("unwitnessed retirement records witnesses",
				"projection", s.Name, "attempt", s.Attempt.ID)
		}
	case !slices.Equal(e.Witnesses, s.RetirementPolicy.Witnesses):
		s = s.poison("retirement captured a witness set that is not the active policy's",
			"projection", s.Name, "captured", e.Witnesses, "required", s.RetirementPolicy.Witnesses)
	default:
		if err := invalidReceipts(e.Receipts, e.Witnesses, Cutover{Live: s.Live, Revision: s.CutoverRevision}); err != nil {
			s = s.poison("retirement receipts do not attest the live cutover",
				"projection", s.Name, "cause", err.Error())
		}
	}

	s.Attempt.Phase = PhaseRetiring
	s.Attempt.RetiringAt = e.At
	s.Attempt.RetiringWitnesses = slices.Clone(e.Witnesses)

	return s
}

// PreviousRetired records that the previous version was retired: its storage
// was torn down (when the handler implements projection.Teardowner) and its
// checkpoint deleted. Terminal for the attempt: the slot is vacated and the
// rebuild is complete; Live is unchanged. A first rebuild has no previous
// version; its completion carries a zero Retired ID, recording that there
// was nothing to retire, and is recorded directly from PhasePromoted. The
// two completion forms are exclusive: a reserved retirement completes only
// from PhaseRetiring with a previous version recorded, so a completion
// cannot vacate the evidence of a reservation that had nothing to reserve.
//
// Receipts are the final attestations: one per witness the reservation
// captured, taken after the reservation and before the teardown, each
// vouching for the exact live cutover.
type PreviousRetired struct {
	Retired  projection.ID
	Receipts []WitnessReceipt
}

// EventType returns the type of event.
func (PreviousRetired) EventType() string { return "previousretired" }

// New returns a new instance of the event.
func (PreviousRetired) New() estoria.DomainEvent[State] { return &PreviousRetired{} }

// ApplyTo applies the event to state, returning the new state.
func (e PreviousRetired) ApplyTo(s State) State {
	reserved := s.Attempt.Phase == PhaseRetiring && s.Attempt.Previous != (projection.ID{})
	firstVersion := s.Attempt.Phase == PhasePromoted && s.Attempt.Previous == (projection.ID{})

	switch {
	case s.Attempt.Phase == PhaseRetiring && s.Attempt.Previous == (projection.ID{}):
		s = s.poison("retirement completed for a reservation with no previous version",
			"projection", s.Name, "attempt", s.Attempt.ID)
	case !reserved && !firstVersion:
		s = s.poison("retirement completed outside the retiring phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case e.Retired != s.Attempt.Previous:
		s = s.poison("retirement completed for a version that was not the attempt's previous",
			"projection", s.Name, "recorded_retired", e.Retired, "previous", s.Attempt.Previous)
	case firstVersion && len(e.Receipts) != 0:
		s = s.poison("first-version retirement completion records receipts",
			"projection", s.Name, "attempt", s.Attempt.ID)
	case reserved:
		if err := invalidReceipts(e.Receipts, s.Attempt.RetiringWitnesses, Cutover{Live: s.Live, Revision: s.CutoverRevision}); err != nil {
			s = s.poison("retirement completion receipts do not re-attest the captured witnesses",
				"projection", s.Name, "cause", err.Error())
		}
	}

	s.Attempt = AttemptState{}

	return s
}

// RetirementPolicySet records an audited transition of the projection's
// retirement policy: the durable witness membership — or the explicit
// choice to retire unwitnessed — that governs retirements from this point.
// Lifecycle history defines the required policy so a restarted process
// configured with fewer witnesses cannot silently weaken the gate;
// configuration only resolves implementations for the IDs the policy names.
// Generations count transitions by exactly one, and every transition
// carries the actor and reason that authorized it. A policy recorded before
// the lifecycle's first admission, or past the reachable generation ceiling
// (each transition consumes a stream event beside the initiation, so
// MaxInt64-1 is the last recordable generation), poisons the fold.
type RetirementPolicySet struct {
	Generation  int64
	Witnesses   []string
	Unwitnessed bool
	Reason      string
	Actor       string
	At          time.Time
}

// EventType returns the type of event.
func (RetirementPolicySet) EventType() string { return "retirementpolicyset" }

// New returns a new instance of the event.
func (RetirementPolicySet) New() estoria.DomainEvent[State] { return &RetirementPolicySet{} }

// ApplyTo applies the event to state, returning the new state.
func (e RetirementPolicySet) ApplyTo(s State) State {
	switch {
	case s.Name == "":
		s = s.poison("retirement policy recorded before lifecycle initialization",
			"generation", e.Generation)
	case s.RetirementPolicy.Generation >= math.MaxInt64-1:
		s = s.poison("retirement policy generations are exhausted",
			"projection", s.Name, "recorded_generation", e.Generation)
	case e.Generation != s.RetirementPolicy.Generation+1:
		s = s.poison("retirement policy generation is discontinuous",
			"projection", s.Name, "recorded_generation", e.Generation, "active_generation", s.RetirementPolicy.Generation)
	case e.Actor == "" || e.Reason == "":
		s = s.poison("retirement policy transition recorded without an actor and reason",
			"projection", s.Name, "generation", e.Generation)
	case e.Unwitnessed && len(e.Witnesses) != 0:
		s = s.poison("retirement policy records witnesses alongside the unwitnessed mode",
			"projection", s.Name, "generation", e.Generation)
	case !e.Unwitnessed && len(e.Witnesses) == 0:
		s = s.poison("retirement policy records neither witnesses nor the unwitnessed mode",
			"projection", s.Name, "generation", e.Generation)
	default:
		if err := invalidWitnessSet(e.Witnesses); err != nil {
			s = s.poison("retirement policy witness set is not canonical",
				"projection", s.Name, "generation", e.Generation, "cause", err.Error())
		}
	}

	s.RetirementPolicy = RetirementPolicy{
		Generation:  e.Generation,
		Witnesses:   slices.Clone(e.Witnesses),
		Unwitnessed: e.Unwitnessed,
	}

	return s
}
