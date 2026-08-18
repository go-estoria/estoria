package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/gofrs/uuid/v5"
)

// A Rebuild is a handle driving one projection's lifecycle. Run blocks while
// the in-flight attempt's processor runs; Promote, Rollback, Abandon, and
// Retire are command methods, safe to call from other goroutines. Every
// command appends to the projection's lifecycle stream under optimistic
// concurrency: after a version-mismatch error — a competing handle recorded
// a transition first — discard the handle and Resume the projection to
// observe what won.
type Rebuild struct {
	orchestrator *Orchestrator

	// name is the projection this handle was addressed by, immutably. Every
	// post-hydration check runs against it: the folded state's own name is
	// mutable data, so it can never vouch for the address.
	name string

	mu            sync.Mutex
	aggregate     *aggregatestore.Aggregate[State]
	stopProcessor context.CancelFunc

	// runner identifies this handle's Run for claim arbitration: generated
	// once per Run, recorded durably by its RunnerClaimed append, compared by
	// the reconcile loop against the runner the stream last recorded.
	runner uuid.UUID

	// certificate is the current catch-up certification, nil when none
	// exists. It is set when this run's drain records CaughtUp and cleared
	// when it can no longer vouch — processor exit, displacement by another
	// runner's claim, an uncertain lifecycle save, or consumption by a
	// successful promotion.
	certificate *certification

	// processorExited is closed when Run's processor goroutine has fully
	// exited; commands that clean up after stopping the processor wait on it
	// so cleanup cannot race a final checkpoint save.
	processorExited chan struct{}

	// stopped records that the processor was stopped deliberately — by a
	// command (Abandon, Rollback) or by the reconcile loop observing the
	// attempt's end or this runner's displacement — so Run reports nil
	// rather than the cancellation.
	stopped bool

	// failure records why the reconcile loop stopped the processor when the
	// stop carries a cause Run must surface rather than report as a benign
	// wind-down: the lifecycle could not be rehydrated, the hydrated state no
	// longer passed validation, or another runner claimed the attempt. Never
	// cleared once set — a displaced handle stays revoked.
	failure error

	// ran records that Run was called, successfully or not: a second call
	// would overwrite the processor ownership fields above, leaving commands
	// able to stop only the newest of two running processors.
	ran bool
}

// A certification is the in-process record that this handle's run drained
// the build to the head: the runner that certified, the position certified,
// and the lifecycle version the certificate was cut against. It is
// deliberately not durable — persisted PhaseCaughtUp is a historical fact,
// not a standing promotion license — and it is valid only while the
// certifying runner's processor is the attempt's current claimant and still
// running. The contract is point-in-time-head: domain events arriving after
// certification do not invalidate it; a certificate from a stopped run is
// never reused.
type certification struct {
	runner   uuid.UUID
	position int64
	version  int64
}

// ErrRunnerDisplaced reports that another runner claimed the in-flight
// attempt: the build continues under the new claimant, and the displaced
// handle is permanently revoked — its Run winds down and its certification,
// if any, is cleared.
var ErrRunnerDisplaced = errors.New("the rebuild attempt was claimed by another runner")

// ErrNotCertified reports a promotion refused for lack of a current catch-up
// certification: only the run that drained the rebuild to the head — and
// whose processor is still the attempt's current claimant — may promote it.
var ErrNotCertified = errors.New("the rebuild has no current catch-up certification")

// Name returns the projection whose lifecycle this handle drives.
func (r *Rebuild) Name() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.aggregate.State().Name
}

// State returns a snapshot of the projection's folded lifecycle state.
func (r *Rebuild) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.aggregate.State()
}

// Checkpoint returns the checkpoint of the version being built. Its recency
// is the build's liveness signal: the processor touches the checkpoint every
// poll cycle, so a checkpoint much older than the poll interval means the
// processor is not running.
func (r *Rebuild) Checkpoint(ctx context.Context) (checkpointstore.Checkpoint, error) {
	state := r.State()
	if state.Attempt.Phase == PhaseNone {
		return checkpointstore.Checkpoint{}, fmt.Errorf("projection %q has no rebuild in flight", state.Name)
	}

	return r.orchestrator.config.Checkpoints.Load(ctx, state.Attempt.Target)
}

// Run drives the in-flight rebuild. It first claims the attempt for this
// run — a RunnerClaimed append in every runnable phase, atomic with
// BuildStarted when the attempt has never started — so ownership is durably
// recorded before the processor exists. It then runs a processor for the
// target version; entering at a catch-up-eligible phase (created, building,
// or caught up), it waits for the drain to reach the head, records CaughtUp,
// and certifies the catch-up in-process. The certificate, not the persisted
// phase, is the promotion license: a rebuild entering at PhaseCaughtUp
// re-certifies by draining to the current head and recording a fresh
// CaughtUp — the phase is preserved, never regressed — and only then
// promotes, if the orchestrator auto-promotes. The processor keeps tailing
// until ctx is canceled. While it runs, a reconcile loop rehydrates the
// lifecycle on an interval and stops the processor once the attempt is no
// longer in flight or another runner has claimed it, so a superseded builder
// winds itself down instead of running until an operator notices.
//
// Run returns nil once the attempt reaches a terminal state — through this
// handle's own commands or through transitions recorded elsewhere — and the
// context's error on cancellation. It returns an error wrapping
// ErrRunnerDisplaced when another runner claims the attempt: the build
// continues under the claimant, and this handle is permanently revoked. A
// reconcile failure is terminal for the Run: if the lifecycle cannot be
// rehydrated or no longer validates, the processor is stopped and Run
// returns the cause; recovery is Resume and a fresh handle once the fault is
// resolved. A fully retired rebuild is complete: steady-state processing of
// the live version is a plain processor.Processor, not a lifecycle concern.
// Run may be called at most once per handle, successful or not; Resume the
// projection for a new handle to run it again.
func (r *Rebuild) Run(ctx context.Context) error {
	r.mu.Lock()

	if r.ran {
		r.mu.Unlock()
		return errors.New("rebuild handle has already run; resume the projection for a new handle")
	}

	r.ran = true

	// Refresh before deciding: a stale handle would otherwise claim and
	// start a processor for an attempt that was since rolled back, abandoned,
	// or retired — and a tailing processor appends nothing that would ever
	// surface the conflict.
	if err := r.orchestrator.config.Projections.Hydrate(ctx, r.aggregate, nil); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("refreshing lifecycle state: %w", err)
	}

	if err := checkLifecycleAggregate(r.aggregate, r.name); err != nil {
		r.mu.Unlock()
		return err
	}

	state := r.aggregate.State()
	attempt := state.Attempt
	runner := uuid.Must(uuid.NewV4())

	var transitions []estoria.DomainEvent[State]

	switch attempt.Phase {
	case PhaseNone:
		r.mu.Unlock()
		return fmt.Errorf("projection %q has no rebuild in flight; nothing to run", state.Name)
	case PhaseCreated:
		transitions = []estoria.DomainEvent[State]{
			RunnerClaimed{Runner: runner, At: time.Now()},
			BuildStarted{},
		}
	case PhaseBuilding, PhaseCaughtUp, PhasePromoted, PhaseRetiring:
		position := int64(0)

		checkpoint, err := r.orchestrator.config.Checkpoints.Load(ctx, attempt.Target)
		if err == nil {
			position = checkpoint.Position
		} else if !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
			r.mu.Unlock()
			return fmt.Errorf("loading checkpoint: %w", err)
		}

		transitions = []estoria.DomainEvent[State]{
			RunnerClaimed{Runner: runner, FromPosition: position, At: time.Now()},
		}
	default:
		r.mu.Unlock()
		return fmt.Errorf("cannot run a rebuild in unknown phase %s", attempt.Phase)
	}

	// Claim-then-act: ownership is recorded before the processor exists. A
	// pre-append failure — including losing the stream to a competing claim —
	// starts nothing; a crash after the append is exactly what resume
	// reconciliation handles.
	if err := r.appendLocked(ctx, transitions...); err != nil {
		if !errors.Is(err, aggregatestore.ErrEventsAppended) {
			r.mu.Unlock()
			return err
		}

		// The claim is durable but unobserved: reload, and start only if
		// this exact runner won it.
		if hydrateErr := r.orchestrator.config.Projections.Hydrate(ctx, r.aggregate, nil); hydrateErr != nil {
			r.mu.Unlock()
			return fmt.Errorf("refreshing lifecycle state after an unobserved claim: %w", hydrateErr)
		}

		if checkErr := checkLifecycleAggregate(r.aggregate, r.name); checkErr != nil {
			r.mu.Unlock()
			return checkErr
		}

		switch current := r.aggregate.State().Attempt; {
		case current.ID != attempt.ID:
			// The claim landed and the attempt then ended: terminal state
			// reached and observed, nothing to run.
			r.mu.Unlock()
			return nil
		case current.Runner != runner:
			r.mu.Unlock()
			return fmt.Errorf("the claim was recorded but superseded before this runner observed it: %w", ErrRunnerDisplaced)
		}
	}

	handler, err := r.orchestrator.config.Handler(attempt.Target)
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("creating handler for %s: %w", attempt.Target, err)
	}

	proc, err := processor.New(
		r.orchestrator.config.Events,
		r.orchestrator.config.Checkpoints,
		attempt.Target,
		handler,
		r.orchestrator.processorOptions...,
	)
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("creating processor: %w", err)
	}

	processorCtx, stop := context.WithCancel(ctx)
	defer stop()

	done := make(chan error, 1)
	exited := make(chan struct{})

	r.runner = runner
	r.stopProcessor = stop
	r.processorExited = exited
	catchingUp := attempt.Phase == PhaseCreated || attempt.Phase == PhaseBuilding || attempt.Phase == PhaseCaughtUp
	r.mu.Unlock()

	started := time.Now()

	go func() {
		done <- proc.Run(processorCtx)
		close(exited)
	}()

	reconcileExited := make(chan struct{})

	go func() {
		defer close(reconcileExited)
		r.reconcile(processorCtx, attempt.ID, runner, stop)
	}()

	// Runs before the deferred stop above: the reconcile loop exits on the
	// processor context, so it must be canceled before waiting.
	defer func() {
		stop()
		<-reconcileExited
	}()

	if catchingUp {
		if keepTailing, err := r.runToCaughtUp(ctx, proc, stop, done, reconcileExited, started); !keepTailing {
			return err
		}
	}

	exitErr := <-done
	stop()

	return r.classifyExit(reconcileExited, exitErr)
}

// classifyExit joins reconciliation and classifies err through
// processorExit: the one exit discipline every return path shares. The
// caller must already have canceled the processor context — the reconcile
// loop hydrates while holding the handle's mutex, so joining (or
// classifying, which takes the mutex) before canceling would deadlock
// against a hydration that ends only on cancellation. Joining before
// classifying guarantees a fail-closed cause recorded during the wind-down
// wins over err.
func (r *Rebuild) classifyExit(reconcileExited <-chan struct{}, err error) error {
	<-reconcileExited

	return r.processorExit(err)
}

// reconcile periodically rehydrates the lifecycle aggregate while the
// processor runs, stopping the processor once the attempt it builds is no
// longer the one in flight, once another runner has claimed the attempt —
// displacement, recorded for Run to surface as ErrRunnerDisplaced — or,
// fail-closed, once the lifecycle can no longer vouch for the attempt, in
// which case the cause is recorded for Run to surface. Both failure shapes
// are terminal: a hydrated state that fails validation, and a rehydration
// that itself fails on anything but this run's own cancellation. Hydration
// applies events incrementally, so an error can strike after earlier events
// already mutated the aggregate; retrying would tail the processor over
// state that was never revalidated. A tailing processor appends nothing
// that would surface a terminal transition recorded elsewhere;
// self-reconciliation is what bounds a superseded builder's lifetime.
// Version numbers are never reused, so the reconcile interval bounds waste,
// not correctness — a not-yet-reconciled builder writes only to identities
// nothing else will ever read.
func (r *Rebuild) reconcile(ctx context.Context, attemptID, runnerID uuid.UUID, stop context.CancelFunc) {
	ticker := time.NewTicker(r.orchestrator.reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		r.mu.Lock()

		if r.stopped {
			r.mu.Unlock()
			return
		}

		if err := r.orchestrator.config.Projections.Hydrate(ctx, r.aggregate, nil); err != nil {
			// Benign only when the failure IS this run's own cancellation
			// and nothing else: deciding on the context's state alone would
			// discard a real failure that races the wind-down, and a joined
			// chain carrying an independent cause alongside the cancellation
			// must keep its fail-closed precedence.
			if ctx.Err() != nil && cancellationOnly(err, ctx.Err()) {
				r.mu.Unlock()
				return
			}

			r.stopped = true
			r.failure = fmt.Errorf("reconciling lifecycle state: %w", err)
			r.mu.Unlock()

			r.orchestrator.log.Error("reconciling lifecycle state failed; stopping the processor",
				"attempt_id", attemptID, "error", err)
			stop()

			return
		}

		// Validity precedes the attempt comparison: an inconsistent stream
		// can replace the attempt, and stopping over the replacement alone
		// would report a poisoned lifecycle as an ordinary nil wind-down.
		if err := checkLifecycleAggregate(r.aggregate, r.name); err != nil {
			r.stopped = true
			r.failure = err
			r.mu.Unlock()

			r.orchestrator.log.Error("hydrated lifecycle state is no longer valid; stopping the processor",
				"attempt_id", attemptID, "error", err)
			stop()

			return
		}

		current := r.aggregate.State().Attempt
		ended := current.ID != attemptID
		displaced := !ended && current.Runner != runnerID

		if ended {
			r.stopped = true
		}

		if displaced {
			// Sticky revocation: the recorded cause is never cleared, and the
			// certificate can no longer vouch for a superseded runner.
			r.stopped = true
			r.failure = fmt.Errorf("runner %s claimed rebuild attempt %s: %w", current.Runner, attemptID, ErrRunnerDisplaced)
			r.certificate = nil
		}
		r.mu.Unlock()

		switch {
		case ended:
			r.orchestrator.log.Info("rebuild attempt is no longer in flight; stopping its processor",
				"attempt_id", attemptID)
			stop()

			return
		case displaced:
			r.orchestrator.log.Info("another runner claimed the rebuild attempt; stopping this builder",
				"attempt_id", attemptID, "runner", runnerID, "claimed_by", current.Runner)
			stop()

			return
		}
	}
}

// cancellationOnly reports whether err represents nothing but the given
// cancellation: every leaf of its error tree matches target. errors.Is
// alone proves the cancellation appears somewhere in the tree, which would
// let a joined independent failure ride along and be discarded as benign.
// The traversal mirrors the errors package's own: joined nodes fan out,
// wrapped nodes descend, and matching applies at the leaves — where a node
// whose unwrapping yields no children is itself a leaf, exactly as
// errors.Is treats it.
func cancellationOnly(err, target error) bool {
	if err == nil {
		return false
	}

	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		joined := multi.Unwrap()
		if len(joined) == 0 {
			return errors.Is(err, target)
		}

		for _, cause := range joined {
			if !cancellationOnly(cause, target) {
				return false
			}
		}

		return true
	}

	if single, ok := err.(interface{ Unwrap() error }); ok {
		if cause := single.Unwrap(); cause != nil {
			return cancellationOnly(cause, target)
		}

		return errors.Is(err, target)
	}

	return errors.Is(err, target)
}

// runToCaughtUp waits for this run's drain to reach the head, records the
// CaughtUp transition — the first, or a fresh one re-certifying an attempt
// entered at PhaseCaughtUp — certifies the catch-up in-process, and promotes
// if the orchestrator auto-promotes. It reports whether Run should keep
// tailing; when it reports false, its error is Run's result. Every exit
// cancels and joins reconciliation before classifying: the reconcile loop
// hydrates while holding the handle's mutex, so classifying first would
// deadlock against a hydration that ends only on cancellation, and a
// fail-closed cause it records must not be missed.
func (r *Rebuild) runToCaughtUp(ctx context.Context, proc *processor.Processor, stop context.CancelFunc, done <-chan error, reconcileExited <-chan struct{}, started time.Time) (bool, error) {
	select {
	case <-ctx.Done():
		stop()
		<-done

		// Classification still applies on cancellation: a recorded
		// fail-closed cause wins over the bare context error.
		return false, r.classifyExit(reconcileExited, ctx.Err())
	case err := <-done:
		stop()

		return false, r.classifyExit(reconcileExited, err)
	case <-proc.CaughtUp():
	}

	r.mu.Lock()

	// Recheck under the lock: a same-handle Abandon between the caught-up
	// signal and this append shares the aggregate, so it would not surface
	// as a version conflict — CaughtUp would land cleanly on top of the
	// Abandoned event.
	if r.stopped {
		failure := r.failure
		r.mu.Unlock()
		<-done

		return false, failure
	}

	position := proc.CaughtUpPosition()

	err := r.appendLocked(ctx, CaughtUp{
		Position: position,
		Duration: time.Since(started),
		At:       time.Now(),
	})
	if err == nil {
		r.certificate = &certification{
			runner:   r.runner,
			position: position,
			version:  r.aggregate.Version(),
		}
	}
	r.mu.Unlock()

	if err != nil {
		stop()
		<-done

		// An Abandon — or the reconcile loop observing the attempt's end —
		// can win the race against the catch-up transition; the outcome is
		// recorded, so the lost append is not an error. A fail-closed stop
		// surfaces its cause instead.
		return false, r.classifyExit(reconcileExited, err)
	}

	if r.orchestrator.autoPromote {
		return r.promoteAfterCatchUp(ctx, stop, done, reconcileExited)
	}

	return true, nil
}

// promoteAfterCatchUp auto-promotes a rebuild directly after its catch-up
// was certified, mapping the outcome to the shared contract: it reports
// whether Run should keep tailing.
func (r *Rebuild) promoteAfterCatchUp(ctx context.Context, stop context.CancelFunc, done <-chan error, reconcileExited <-chan struct{}) (bool, error) {
	err := r.Promote(ctx)
	if err == nil {
		return true, nil
	}

	// The promotion can be durable even when the save could not observe it;
	// the version is live and must keep tailing. Only this handle is stale.
	if errors.Is(err, aggregatestore.ErrEventsAppended) {
		r.orchestrator.log.Error("promotion recorded, but the rebuild handle is stale",
			"projection", r.Name(), "error", err)

		return true, nil
	}

	stop()
	<-done

	// An Abandon can win the race against auto-promotion; the abandonment is
	// recorded, so the refused promotion is not an error. A fail-closed stop
	// surfaces its cause instead.
	return false, r.classifyExit(reconcileExited, err)
}

// Promote cuts reads over to the target version by recording Promoted — the
// event is the flip. The effect worker applies the flip to registered caches
// and storage objects in stream order; nothing runs inline, so there is no
// hook failure to special-case and no unrecorded cutover to repair.
//
// Promotion requires a current catch-up certification: persisted
// PhaseCaughtUp is a historical fact, not a standing license, so only the
// run that drained this rebuild to the head — and whose processor is still
// the attempt's current claimant — may promote it. A handle that merely
// resumed or read a caught-up rebuild must Run it, re-certifying against
// the current head, before it can promote; the refusal wraps
// ErrNotCertified.
func (r *Rebuild) Promote(ctx context.Context) error {
	r.mu.Lock()

	if err := checkLifecycleAggregate(r.aggregate, r.name); err != nil {
		r.mu.Unlock()
		return err
	}

	state := r.aggregate.State()

	switch state.Attempt.Phase {
	case PhaseNone:
		r.mu.Unlock()
		return fmt.Errorf("projection %q has no rebuild in flight", state.Name)
	case PhaseCaughtUp:
	case PhaseCreated, PhaseBuilding, PhasePromoted, PhaseRetiring:
		r.mu.Unlock()
		return fmt.Errorf("cannot promote a rebuild that is %s", state.Attempt.Phase)
	default:
		r.mu.Unlock()
		return fmt.Errorf("cannot promote a rebuild in unknown phase %s", state.Attempt.Phase)
	}

	switch certificate := r.certificate; {
	case certificate == nil:
		r.mu.Unlock()
		return fmt.Errorf("%w: run the rebuild to drain it to the head and certify", ErrNotCertified)
	case certificate.version != r.aggregate.Version():
		// The lifecycle advanced past the version the certificate was cut
		// against; the version only grows, so the certificate is dead.
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the lifecycle advanced after catch-up was certified", ErrNotCertified)
	}

	select {
	case <-r.processorExited:
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the certifying processor has exited", ErrNotCertified)
	default:
	}

	appendErr := r.appendLocked(ctx, Promoted{
		Previous: state.Live,
		Next:     state.Attempt.Target,
		At:       time.Now(),
	})
	if appendErr == nil {
		// Consumed: the flip is recorded and observed.
		r.certificate = nil
	}
	r.mu.Unlock()

	// An error carrying ErrEventsAppended means the event is durable and the
	// aggregate could not observe it: the flip happened; only this handle is
	// stale.
	if appendErr != nil && errors.Is(appendErr, aggregatestore.ErrEventsAppended) {
		return staleHandleError("promotion", appendErr)
	}

	return appendErr
}

// Rollback reverts reads to the previous version by recording RolledBack.
// Terminal for the attempt: its processor is stopped, and a subsequent
// rebuild is a new attempt targeting a new version number. The rolled-back
// version's storage and checkpoint are deliberately left in place for
// inspection; its version number is never reused, so the residue is inert
// until explicitly collected. Rolling back is illegal once retirement of the
// previous version has started — the reservation forfeits the rollback
// target.
func (r *Rebuild) Rollback(ctx context.Context) error {
	r.mu.Lock()

	if err := checkLifecycleAggregate(r.aggregate, r.name); err != nil {
		r.mu.Unlock()
		return err
	}

	state := r.aggregate.State()

	switch state.Attempt.Phase {
	case PhaseNone:
		r.mu.Unlock()
		return fmt.Errorf("projection %q has no rebuild in flight", state.Name)
	case PhaseRetiring:
		r.mu.Unlock()
		return errors.New("cannot roll back: retirement of the previous version has started, forfeiting the rollback target")
	case PhasePromoted:
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
		r.mu.Unlock()
		return fmt.Errorf("cannot roll back a rebuild that is %s", state.Attempt.Phase)
	default:
		r.mu.Unlock()
		return fmt.Errorf("cannot roll back a rebuild in unknown phase %s", state.Attempt.Phase)
	}

	if state.Attempt.Previous.Version == 0 {
		r.mu.Unlock()
		return errors.New("rebuild has no previous version to roll back to")
	}

	appendErr := r.appendLocked(ctx, RolledBack{
		From:       state.Live,
		RevertedTo: state.Attempt.Previous,
		At:         time.Now(),
	})
	if appendErr != nil && !errors.Is(appendErr, aggregatestore.ErrEventsAppended) {
		r.mu.Unlock()
		return appendErr
	}

	r.stopped = true
	r.mu.Unlock()

	stopErr := r.awaitProcessorStop(ctx)

	if appendErr != nil {
		return errors.Join(staleHandleError("rollback", appendErr), stopErr)
	}

	return stopErr
}

// Abandon gives up on the rebuild before promotion: it records Abandoned and
// stops this handle's processor. The target version's storage and checkpoint
// are deliberately left in place — no handle can prove it owns the only
// processor writing to the target, so no automatic cleanup runs beneath a
// possible concurrent builder. The residue is inert: the version number is
// never reused, and the lifecycle stream and checkpoint store enumerate it
// until it is explicitly collected. A concurrent builder observes the
// abandonment through its reconcile loop and stops itself.
func (r *Rebuild) Abandon(ctx context.Context, cause string) error {
	r.mu.Lock()

	if err := checkLifecycleAggregate(r.aggregate, r.name); err != nil {
		r.mu.Unlock()
		return err
	}

	state := r.aggregate.State()

	switch state.Attempt.Phase {
	case PhaseNone:
		r.mu.Unlock()
		return fmt.Errorf("projection %q has no rebuild in flight", state.Name)
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
	case PhasePromoted, PhaseRetiring:
		r.mu.Unlock()
		return fmt.Errorf("cannot abandon a rebuild that is %s", state.Attempt.Phase)
	default:
		r.mu.Unlock()
		return fmt.Errorf("cannot abandon a rebuild in unknown phase %s", state.Attempt.Phase)
	}

	appendErr := r.appendLocked(ctx, Abandoned{Cause: cause})
	if appendErr != nil && !errors.Is(appendErr, aggregatestore.ErrEventsAppended) {
		r.mu.Unlock()
		return appendErr
	}

	r.stopped = true
	r.mu.Unlock()

	if appendErr != nil {
		appendErr = staleHandleError("abandonment", appendErr)
	}

	return errors.Join(appendErr, r.awaitProcessorStop(ctx))
}

// Retire completes a successful rebuild by removing the previous version.
// Reserve-then-act-then-record: RetireStarted is appended first, contending
// directly with Rollback on the lifecycle stream so exactly one wins and
// nothing is destroyed before the arbitration; the teardown and checkpoint
// delete run only after the reservation is durable; PreviousRetired records
// completion, vacating the attempt slot. A Retire interrupted between
// reservation and completion is repaired by calling Retire again — from
// PhaseRetiring it skips the reservation and re-runs the contractually
// idempotent teardown.
//
// Retiring a nonzero previous version requires its handler to implement
// projection.Teardowner. The capability is resolved before anything is
// reserved — a refused retirement leaves rollback available — and the same
// resolved handler performs the teardown. The previous version's steady-state
// processor must be stopped and joined before Retire: teardown does not fence
// a running processor, and its writes would race the removal.
//
// A first rebuild has no previous version, so there is nothing to tear down
// and — rollback being impossible without a rollback target — nothing to
// reserve against: Retire completes it by recording PreviousRetired with a
// zero ID.
func (r *Rebuild) Retire(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Refresh before reserving, so the common stale-handle case fails fast
	// with a phase error instead of a version conflict. The reservation
	// append remains the arbiter.
	if err := r.orchestrator.config.Projections.Hydrate(ctx, r.aggregate, nil); err != nil {
		return fmt.Errorf("refreshing lifecycle state: %w", err)
	}

	if err := checkLifecycleAggregate(r.aggregate, r.name); err != nil {
		return err
	}

	state := r.aggregate.State()

	switch state.Attempt.Phase {
	case PhaseNone:
		return fmt.Errorf("projection %q has no rebuild in flight", state.Name)
	case PhasePromoted, PhaseRetiring:
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
		return fmt.Errorf("cannot retire the previous version of a rebuild that is %s", state.Attempt.Phase)
	default:
		return fmt.Errorf("cannot retire the previous version of a rebuild in unknown phase %s", state.Attempt.Phase)
	}

	previous := state.Attempt.Previous

	if previous.Version == 0 {
		// A first rebuild: nothing to tear down, and nothing to reserve
		// against. Record completion directly.
		return r.recordRetirement(ctx, previous)
	}

	// Resolve the teardown capability before reserving: a retirement that
	// will be refused must be refused while rollback is still possible, not
	// after the reservation has forfeited the rollback target.
	handler, err := r.orchestrator.config.Handler(previous)
	if err != nil {
		return fmt.Errorf("creating handler for %s: %w", previous, err)
	}

	teardowner, ok := handler.(projection.Teardowner)
	if !ok {
		return fmt.Errorf("cannot retire %s: its handler does not implement projection.Teardowner", previous)
	}

	if state.Attempt.Phase == PhasePromoted {
		err := r.appendLocked(ctx, RetireStarted{Retiring: previous, At: time.Now()})
		if err != nil {
			if errors.Is(err, aggregatestore.ErrEventsAppended) {
				return staleHandleError("retirement start", err)
			}

			return err
		}
	}

	if err := teardowner.Teardown(ctx, previous); err != nil {
		return fmt.Errorf("tearing down %s: %w", previous, err)
	}

	// The checkpoint goes last, and only after the teardown succeeded: it is
	// the durable marker that a build of this identity existed, so it must
	// outlive any failure to remove the storage it marks.
	if err := r.orchestrator.config.Checkpoints.Delete(ctx, previous); err != nil && !errors.Is(err, checkpointstore.ErrCheckpointNotFound) {
		return fmt.Errorf("deleting checkpoint for %s: %w", previous, err)
	}

	return r.recordRetirement(ctx, previous)
}

// recordRetirement appends the PreviousRetired completion, mapping a
// durable-but-unobserved append to the stale-handle contract.
func (r *Rebuild) recordRetirement(ctx context.Context, retired projection.ID) error {
	err := r.appendLocked(ctx, PreviousRetired{Retired: retired})
	if err != nil && errors.Is(err, aggregatestore.ErrEventsAppended) {
		return staleHandleError("retirement", err)
	}

	return err
}

// awaitProcessorStop cancels the processor, if one is running, and waits for
// its goroutine to fully exit.
func (r *Rebuild) awaitProcessorStop(ctx context.Context) error {
	r.mu.Lock()
	stop := r.stopProcessor
	exited := r.processorExited
	r.mu.Unlock()

	if stop == nil {
		return nil
	}

	stop()

	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// appendLocked appends the transitions to the aggregate and saves them as
// one atomic append. The save carries the aggregate's expected version, so
// competing handles are arbitrated here: the loser's save reports a version
// mismatch, and the handle should be discarded and the projection resumed to
// observe what won. The caller must hold r.mu.
func (r *Rebuild) appendLocked(ctx context.Context, events ...estoria.DomainEvent[State]) error {
	r.aggregate.Append(events...)

	if err := r.orchestrator.config.Projections.Save(ctx, r.aggregate, nil); err != nil {
		// Discard the failed append: left queued, it would ride along with a
		// later command's save and durably record both transitions. When the
		// error carries ErrEventsAppended the events are durable regardless,
		// and the next hydration observes them — but the aggregate can no
		// longer vouch for the version a certificate was cut against.
		r.aggregate.DiscardUnsavedEvents()

		if errors.Is(err, aggregatestore.ErrEventsAppended) {
			r.certificate = nil
		}

		types := make([]string, 0, len(events))
		for _, event := range events {
			types = append(types, event.EventType())
		}

		return fmt.Errorf("recording %s: %w", strings.Join(types, "+"), err)
	}

	return nil
}

// staleHandleError describes a transition that is durably recorded even
// though the aggregate could not observe it (aggregatestore.ErrEventsAppended):
// the action's effects have been applied, but the handle must be discarded
// and the projection resumed before further commands.
func staleHandleError(action string, err error) error {
	return fmt.Errorf("%s recorded, but the rebuild handle is stale; resume the projection before issuing further commands: %w", action, err)
}

// processorExit maps an exited processor to Run's result: a recorded cause —
// fail-closed or displacement — is surfaced, a deliberate stop by a command
// or by the reconcile loop observing the attempt's end is not an error, and
// anything else reports err. Both fields are read in one critical section:
// reading them separately would let the reconcile loop record a cause
// between the reads, and the exit would classify as deliberate and clean. A
// certificate never survives its processor's exit.
func (r *Rebuild) processorExit(err error) error {
	r.mu.Lock()
	stopped, failure := r.stopped, r.failure
	r.certificate = nil
	r.mu.Unlock()

	switch {
	case failure != nil:
		return failure
	case stopped:
		return nil
	default:
		return err
	}
}
