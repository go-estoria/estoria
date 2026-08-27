package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// A RetirementPolicy is the durable witness policy governing a projection's
// retirements. Lifecycle history defines who must vouch before storage is
// destroyed — a restarted process configured with fewer witnesses cannot
// silently weaken the gate — while configuration only resolves
// implementations for the IDs the policy names. Generation counts audited
// policy transitions, 1-based from the first; zero means no policy has ever
// been recorded, and a retirement then requires a per-retirement audited
// override.
type RetirementPolicy struct {
	// Generation is the policy transition count: each RetirementPolicySet
	// increments it by exactly one. Each transition consumes a stream event
	// beside the lifecycle's initiation, so MaxInt64-1 is the last reachable
	// generation; MaxInt64 cannot arise from any fold.
	Generation int64

	// Witnesses are the stable IDs of the setters that must vouch for the
	// exact live cutover before the previous version's storage is destroyed,
	// in canonical order: unique, sorted, none empty. Empty in unwitnessed
	// mode.
	Witnesses []string

	// Unwitnessed records the explicit, audited choice to retire without
	// witness attestation.
	Unwitnessed bool
}

// zero reports whether no policy has ever been recorded.
func (p RetirementPolicy) zero() bool {
	return p.Generation == 0 && len(p.Witnesses) == 0 && !p.Unwitnessed
}

// validate reports whether the policy is a shape a legitimate fold could
// carry.
func (p RetirementPolicy) validate() error {
	switch {
	case p.Generation < 0:
		return errors.New("negative policy generation")
	case p.Generation == math.MaxInt64:
		return errors.New("policy generation exceeds the reachable transition count")
	case p.Generation == 0 && (len(p.Witnesses) != 0 || p.Unwitnessed):
		return errors.New("policy content recorded with no generation")
	case p.Generation > 0 && p.Unwitnessed && len(p.Witnesses) != 0:
		return errors.New("witnesses recorded alongside the unwitnessed mode")
	case p.Generation > 0 && !p.Unwitnessed && len(p.Witnesses) == 0:
		return errors.New("neither witnesses nor the unwitnessed mode recorded")
	}

	if err := invalidWitnessSet(p.Witnesses); err != nil {
		return err
	}

	return nil
}

// A RetirementPolicyChange describes one audited policy transition for
// SetRetirementPolicy: the witness IDs that must vouch for retirements from
// this point — or the explicit choice to retire unwitnessed — and the actor
// and reason authorizing the change.
type RetirementPolicyChange struct {
	Witnesses   []string
	Unwitnessed bool
	Reason      string
	Actor       string
}

// A RetirementOverride is a per-retirement audited authorization to retire
// without witness attestation, recorded durably in the retirement
// reservation. The zero value means no override.
type RetirementOverride struct {
	Actor  string
	Reason string
}

// A WitnessReceipt is one witness's attestation, captured by the retirement
// protocol: the named witness reported serving exactly this cutover.
type WitnessReceipt struct {
	Witness string
	Cutover Cutover
}

// A RetirementWitness vouches for the route it actually serves. Retirement
// destroys the previous version's storage only after every witness the
// durable policy requires attests to serving the exact live cutover, so a
// route still directing reads at the version about to be destroyed refuses
// the retirement. CutoverSetter implementations satisfy the interface
// through their AppliedCutover side. Attestations may be requested
// concurrently: preflights and rechecks from independent retirement retries
// overlap, and nothing serializes them across processes.
type RetirementWitness interface {
	// AppliedCutover reports the cutover the witness currently serves for
	// the named projection, per the CutoverSetter contract.
	AppliedCutover(ctx context.Context, name string) (Cutover, error)
}

// invalidWitnessSet reports why the witness IDs are not a canonical
// membership — unique, sorted, none empty — so a recorded set is
// self-evidently comparable against the policy that required it.
func invalidWitnessSet(ids []string) error {
	for i, id := range ids {
		switch {
		case id == "":
			return errors.New("witness IDs must not be empty")
		case i > 0 && ids[i-1] >= id:
			return fmt.Errorf("witness IDs must be unique and sorted: %q follows %q", id, ids[i-1])
		}
	}

	return nil
}

// invalidReceipts reports why the receipts do not attest the required
// witnesses serving exactly the given cutover: one receipt per required ID,
// in the captured order, each vouching for the exact pair.
func invalidReceipts(receipts []WitnessReceipt, witnesses []string, cutover Cutover) error {
	if len(receipts) != len(witnesses) {
		return fmt.Errorf("%d receipts recorded for %d required witnesses", len(receipts), len(witnesses))
	}

	for i, receipt := range receipts {
		switch {
		case receipt.Witness != witnesses[i]:
			return fmt.Errorf("receipt attests witness %q, want %q", receipt.Witness, witnesses[i])
		case receipt.Cutover != cutover:
			return fmt.Errorf("witness %q attests %s at revision %d, want %s at revision %d",
				receipt.Witness, receipt.Cutover.Live, receipt.Cutover.Revision, cutover.Live, cutover.Revision)
		}
	}

	return nil
}
