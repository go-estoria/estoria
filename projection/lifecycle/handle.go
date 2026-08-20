package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/aggregatestore"
	"github.com/go-estoria/estoria/eventstore"
	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/projection/checkpointstore"
	"github.com/go-estoria/estoria/projection/processor"
	"github.com/go-estoria/estoria/typeid"
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
	// runner's claim, any terminal reconcile stop, an uncertain lifecycle
	// save, or consumption by a successful promotion. Promote additionally
	// re-verifies every binding against the current state, so a stale
	// certificate that escaped clearing still cannot authorize anything.
	certificate *certification

	// processorExited is closed when Run's processor goroutine has fully
	// exited; commands that clean up after stopping the processor wait on it
	// so cleanup cannot race a final checkpoint save.
	processorExited chan struct{}

	// exitOrder arbitrates, atomically, whether the processor's return or a
	// wind-down's stop came first: the return claims it the instant the
	// processor's Run returns, a failure path claims it before initiating
	// its stop, and attribution reads the winner. A check-then-stop sample
	// would race the return and misattribute an independent processor
	// failure to the stop; publication cannot arbitrate either — it
	// serializes with promotion through the handle lock, so it observes
	// lock order, not return order.
	exitOrder atomic.Int32

	// processorReturned is closed immediately after the processor's return
	// claims its exit order, before publication takes the handle lock: an
	// unserialized observation point for the return itself, used by tests
	// that must order external actions after the claim.
	processorReturned chan struct{}

	// returnCtxErr is the run context's error as the processor's return
	// observed it, captured before the return claims its exit order: a
	// first-returned result is judged against the cancellation state it
	// actually returned under, so a cancellation arriving between the return
	// and its attribution cannot relabel an earlier independent result as
	// the run's own wind-down. Written once, by Run's processor goroutine;
	// both the exit-order claim and the result's delivery on done order the
	// write before any reader.
	returnCtxErr error

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

// Exit-order arbitration states: the zero value means neither the
// processor's return nor a wind-down's stop has claimed the order yet.
const (
	exitOrderRunning int32 = iota
	exitOrderReturned
	exitOrderStopped
)

// A certification is the in-process record that this handle's run drained
// the build to the head. It binds everything the promotion license depends
// on: the attempt certified, the runner that certified it, the certified
// position, the lifecycle version after the fresh CaughtUp, and the
// certifying processor's incarnation (its exit signal). It is deliberately
// not durable — persisted PhaseCaughtUp is a historical fact, not a
// standing promotion license — and it is valid only while every binding
// still holds: the attempt and runner are still the ones recorded, the
// folded catch-up position and lifecycle version are the ones certified,
// and the certifying processor has not published its exit. The contract is
// point-in-time-head: domain events arriving after certification do not
// invalidate it; a certificate from a stopped run is never reused.
type certification struct {
	attempt  uuid.UUID
	runner   uuid.UUID
	position int64
	version  int64
	exited   <-chan struct{}
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
			RunnerClaimed{Attempt: attempt.ID, Runner: runner, At: time.Now()},
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
			RunnerClaimed{Attempt: attempt.ID, Runner: runner, FromPosition: position, At: time.Now()},
		}
	default:
		r.mu.Unlock()
		return fmt.Errorf("cannot run a rebuild in unknown phase %s", attempt.Phase)
	}

	// Claim-then-act: ownership is recorded before the processor exists. A
	// pre-append failure starts nothing, and losing the stream to a version
	// conflict is classified from the exact event that won it — a competing
	// claim is displacement, an ended attempt is a terminal state observed;
	// a crash after the append is exactly what resume reconciliation handles.
	if err := r.appendLocked(ctx, transitions...); err != nil {
		if !errors.Is(err, aggregatestore.ErrEventsAppended) {
			result := r.claimDefeat(ctx, attempt, err)
			r.mu.Unlock()

			return result
		}

		// The claim is durable but unobserved. Per ErrEventsAppended's
		// recovery contract the uncertain aggregate is discarded for a fresh
		// reload — a save that failed mid-application can leave queued
		// unapplied events and partially advanced state behind, which no
		// incremental refresh repairs — and the run starts only if this
		// exact runner won the claim it recorded. A failed recovery keeps
		// carrying ErrEventsAppended: the claim's durability must stay
		// visible to the caller alongside the recovery failure.
		loaded, loadErr := r.hydrateFresh(ctx, 0)
		if loadErr != nil {
			r.mu.Unlock()
			return fmt.Errorf("reloading lifecycle state after an unobserved claim: %w", errors.Join(err, loadErr))
		}

		if checkErr := checkLifecycleAggregate(loaded, r.name); checkErr != nil {
			r.mu.Unlock()
			return fmt.Errorf("checking lifecycle state after an unobserved claim: %w", errors.Join(err, checkErr))
		}

		r.aggregate = loaded

		switch current := loaded.State().Attempt; {
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
	returned := make(chan struct{})

	r.runner = runner
	r.stopProcessor = stop
	r.processorExited = exited
	r.processorReturned = returned
	catchingUp := attempt.Phase == PhaseCreated || attempt.Phase == PhaseBuilding || attempt.Phase == PhaseCaughtUp
	r.mu.Unlock()

	started := time.Now()

	go func() {
		err := proc.Run(processorCtx)
		r.returnCtxErr = ctx.Err()
		r.exitOrder.CompareAndSwap(exitOrderRunning, exitOrderReturned)
		close(returned)
		r.publishProcessorExit()
		done <- err
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
		if keepTailing, err := r.runToCaughtUp(ctx, proc, attempt.ID, runner, stop, done, reconcileExited, started); !keepTailing {
			return err
		}
	}

	exitErr := <-done
	stop()

	return r.classifyExit(reconcileExited, exitErr)
}

// publishProcessorExit publishes the processor's exit under the handle's
// lock: the exit signal closes and the certificate dies in the same critical
// section a promotion holds through its check and append, so a promotion
// either observes the exit and refuses, or commits strictly before the exit
// is published — the exit can never interleave between a certificate check
// and the append it authorized. Called exactly once, by Run's processor
// goroutine, after the processor has fully returned.
func (r *Rebuild) publishProcessorExit() {
	r.mu.Lock()
	defer r.mu.Unlock()

	close(r.processorExited)
	r.certificate = nil
}

// classifyExit joins reconciliation and classifies err through
// processorExit: the one exit discipline every return path shares. The
// caller must already have canceled the processor context — the reconcile
// loop only exits on it, so joining first would otherwise wait forever —
// and joining before classifying guarantees a cause recorded during the
// wind-down wins over err.
func (r *Rebuild) classifyExit(reconcileExited <-chan struct{}, err error) error {
	<-reconcileExited

	return r.processorExit(err)
}

// reconcile periodically loads a fresh view of the lifecycle while the
// processor runs, stopping the processor once the attempt it builds is no
// longer the one in flight, once another runner has claimed the attempt —
// displacement, recorded for Run to surface as ErrRunnerDisplaced — or,
// fail-closed, once the lifecycle can no longer vouch for the attempt, in
// which case the cause is recorded for Run to surface. Both failure shapes
// are terminal: a loaded state that fails validation, and a load that
// itself fails on anything but this run's own cancellation — a fresh load
// either vouches for the whole lifecycle or for none of it, and the
// terminal contract is the settled decision. The load runs outside the
// handle's lock: a load that blocks until the processor is canceled must
// not hold the lock a command needs in order to reach that cancellation —
// command context cancellation cannot interrupt a mutex wait. The fresh
// view installs, and the verdict is decided, under the lock, and only when
// the view is at least as new as the one the handle already holds: a load
// begun at one version can return after a local append advanced past it,
// and installing its past over certified state would refuse a healthy
// promotion. A tailing processor appends nothing that would surface a
// terminal transition recorded elsewhere; self-reconciliation is what
// bounds a superseded builder's lifetime. Version numbers are never
// reused, so the reconcile interval bounds waste, not correctness — a
// not-yet-reconciled builder writes only to identities nothing else will
// ever read.
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
		stopped := r.stopped
		r.mu.Unlock()

		if stopped {
			return
		}

		loaded, err := r.hydrateFresh(ctx, 0)
		if err != nil {
			// Benign only when the failure IS this run's own cancellation
			// and nothing else: deciding on the context's state alone would
			// discard a real failure that races the wind-down, and a joined
			// chain carrying an independent cause alongside the cancellation
			// must keep its fail-closed precedence.
			if ctx.Err() != nil && cancellationOnly(err, ctx.Err()) {
				return
			}

			r.recordTerminalStop(fmt.Errorf("reconciling lifecycle state: %w", err))
			r.orchestrator.log.Error("reconciling lifecycle state failed; stopping the processor",
				"attempt_id", attemptID, "error", err)
			stop()

			return
		}

		r.mu.Lock()

		if r.stopped {
			r.mu.Unlock()
			return
		}

		// A view older than the one the handle holds is a read that raced a
		// newer local append: it proves nothing about the present, and
		// installing it would overwrite certified state with its own past.
		// It renders no verdict — not even a validity one; the next tick
		// reads a newer view, and anything terminal is still terminal there.
		if loaded.Version() < r.aggregate.Version() {
			r.mu.Unlock()
			continue
		}

		// Validity precedes the attempt comparison: an inconsistent stream
		// can replace the attempt, and stopping over the replacement alone
		// would report a poisoned lifecycle as an ordinary nil wind-down.
		if err := checkLifecycleAggregate(loaded, r.name); err != nil {
			r.recordTerminalStopLocked(err)
			r.mu.Unlock()
			r.orchestrator.log.Error("loaded lifecycle state is no longer valid; stopping the processor",
				"attempt_id", attemptID, "error", err)
			stop()

			return
		}

		r.aggregate = loaded

		current := loaded.State().Attempt
		ended := current.ID != attemptID
		displaced := !ended && current.Runner != runnerID

		if ended {
			r.stopped = true
			r.certificate = nil
		}

		if displaced {
			// Sticky revocation: the recorded cause is never cleared, and the
			// certificate can no longer vouch for a superseded runner.
			r.stopped = true
			r.failure = displacedError(current.Runner, attemptID)
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

// recordTerminalStop records a terminal reconcile stop: the cause Run must
// surface, the stop flag, and the death of any certification — a lifecycle
// that can no longer be vouched for licenses nothing. A stop that already
// happened owns the verdict; the cause is never overwritten.
func (r *Rebuild) recordTerminalStop(cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.recordTerminalStopLocked(cause)
}

// recordTerminalStopLocked is recordTerminalStop for callers already holding
// r.mu.
func (r *Rebuild) recordTerminalStopLocked(cause error) {
	if r.stopped {
		return
	}

	r.stopped = true
	r.certificate = nil
	r.failure = cause
}

// cancellationOnly reports whether err represents nothing but the given
// cancellation: every leaf of its error tree matches target.
func cancellationOnly(err, target error) bool {
	return leavesMatch(err, target)
}

// leavesMatch reports whether every leaf of err's error tree matches at
// least one of the targets. errors.Is alone proves a target appears
// somewhere in the tree, which would let a joined independent failure ride
// along and be discarded as benign. The traversal mirrors the errors
// package's own: joined nodes fan out, wrapped nodes descend, and matching
// applies at the leaves — where a node whose unwrapping yields no children
// is itself a leaf, exactly as errors.Is treats it. A nil err matches
// nothing.
func leavesMatch(err error, targets ...error) bool {
	if err == nil {
		return false
	}

	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		joined := multi.Unwrap()
		if len(joined) == 0 {
			return leafMatches(err, targets)
		}

		for _, cause := range joined {
			if !leavesMatch(cause, targets...) {
				return false
			}
		}

		return true
	}

	if single, ok := err.(interface{ Unwrap() error }); ok {
		if cause := single.Unwrap(); cause != nil {
			return leavesMatch(cause, targets...)
		}

		return leafMatches(err, targets)
	}

	return leafMatches(err, targets)
}

// leafMatches reports whether one leaf matches any target.
func leafMatches(err error, targets []error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}

	return false
}

// runToCaughtUp waits for this run's drain to reach the head, records the
// CaughtUp transition — the first, or a fresh one re-certifying an attempt
// entered at PhaseCaughtUp — certifies the catch-up in-process, and promotes
// if the orchestrator auto-promotes. It reports whether Run should keep
// tailing; when it reports false, its error is Run's result. Every exit
// cancels and joins reconciliation before classifying, so a cause recorded
// during the wind-down is never missed.
func (r *Rebuild) runToCaughtUp(ctx context.Context, proc *processor.Processor, attemptID, runnerID uuid.UUID, stop context.CancelFunc, done <-chan error, reconcileExited <-chan struct{}, started time.Time) (bool, error) {
	select {
	case <-ctx.Done():
		// Classification still applies on cancellation: a recorded
		// fail-closed cause wins over the bare context error — and a result
		// the processor returned before this stop, carrying more than the
		// run's own cancellation, must not be discarded behind it.
		return false, r.classifyExit(reconcileExited, r.windDown(stop, done, ctx.Err()))
	case err := <-done:
		stop()

		return false, r.classifyExit(reconcileExited, err)
	case <-proc.CaughtUp():
	}

	stopped, exited, err := r.recordCatchUp(ctx, attemptID, proc.CaughtUpPosition(), time.Since(started))

	switch {
	case stopped:
		<-done
		return false, err
	case exited:
		stop()
		exitErr := <-done

		return false, r.classifyExit(reconcileExited, exitErr)
	case err != nil:
		wound := r.windDown(stop, done, err)

		// An Abandon — or the reconcile loop observing the attempt's end —
		// can win the race against the catch-up transition; the outcome is
		// recorded, so the lost append is not an error, and a loss to a
		// competing claim is typed as displacement. A fail-closed stop
		// surfaces its cause instead.
		r.recordLostAppend(ctx, attemptID, runnerID, err)

		return false, r.classifyExit(reconcileExited, wound)
	}

	if r.orchestrator.autoPromote {
		return r.promoteAfterCatchUp(ctx, attemptID, runnerID, stop, done, reconcileExited)
	}

	return true, nil
}

// recordCatchUp records this run's CaughtUp transition and certifies it,
// unless the run was stopped while the caught-up signal was in flight — a
// same-handle Abandon shares the aggregate, so it would not surface as a
// version conflict — or the processor has already published its exit. The
// exit check, the append, and the certification share one critical section
// with exit publication, so a certificate is never created against a
// published exit: it would outlive its processor, and the refusals it then
// causes would shadow the processor's real result. On a stop it reports the
// recorded failure, nil for a deliberate one, as err.
func (r *Rebuild) recordCatchUp(ctx context.Context, attemptID uuid.UUID, position int64, elapsed time.Duration) (stopped, exited bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopped {
		return true, false, r.failure
	}

	select {
	case <-r.processorExited:
		return false, true, nil
	default:
	}

	if err := r.appendLocked(ctx, CaughtUp{
		Position: position,
		Duration: elapsed,
		At:       time.Now(),
	}); err != nil {
		return false, false, err
	}

	r.certificate = &certification{
		attempt:  attemptID,
		runner:   r.runner,
		position: position,
		version:  r.aggregate.Version(),
		exited:   r.processorExited,
	}

	return false, false, nil
}

// promoteAfterCatchUp auto-promotes a rebuild directly after its catch-up
// was certified, mapping the outcome to the shared contract: it reports
// whether Run should keep tailing.
func (r *Rebuild) promoteAfterCatchUp(ctx context.Context, attemptID, runnerID uuid.UUID, stop context.CancelFunc, done <-chan error, reconcileExited <-chan struct{}) (bool, error) {
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

	wound := r.windDown(stop, done, err)

	// An Abandon can win the race against auto-promotion; the abandonment is
	// recorded, so the refused promotion is not an error, and a loss to a
	// competing claim is typed as displacement. A fail-closed stop surfaces
	// its cause instead.
	r.recordLostAppend(ctx, attemptID, runnerID, err)

	return false, r.classifyExit(reconcileExited, wound)
}

// windDown ends a run's processor engagement on behalf of a failure or
// cancellation path: it claims the exit order for this stop, stops the
// processor, drains its result, and attributes the outcome against the
// path's local failure — from the recorded facts, so a result the processor
// returned first is never discarded behind the local story. The cancellation
// state it judges against is the one captured at the return itself, read
// only after the drain so the capture is always ordered before it; sampling
// the context here instead would let a cancellation arriving after the
// return relabel an earlier independent result. Callers pass the
// attribution through classifyExit, keeping recorded verdicts' precedence.
func (r *Rebuild) windDown(stop context.CancelFunc, done <-chan error, local error) error {
	returnedFirst := r.claimStopOrder()

	stop()

	exitErr := <-done

	return attributeExit(r.returnCtxErr, returnedFirst, exitErr, local)
}

// claimStopOrder claims the exit order for a wind-down's stop, reporting
// whether the processor's return had already claimed it. The compare-and-
// swap is the arbitration: exactly one of the return and the stop
// transitions the order out of running, so a return landing concurrently is
// either visibly first or defined to be a consequence of the stop — never
// sampled into a gap between checking and stopping.
func (r *Rebuild) claimStopOrder() (returnedFirst bool) {
	if r.exitOrder.CompareAndSwap(exitOrderRunning, exitOrderStopped) {
		return false
	}

	return r.exitOrder.Load() == exitOrderReturned
}

// attributeExit decides what a failure path reports when a local failure and
// the processor's result compete, from two explicitly recorded facts — exit
// order and the run context's state as the return observed it — never from
// the result's shape alone: a processor whose stop won the order reports
// nothing but the stop's echo, whatever shape its handler gave it, and the
// local failure governs. A result that returned first is the processor's
// own; when the run's context had ended at the return and the result carries
// nothing but that same cancellation, the documented context error is the
// story and the local failure is downstream of it; any other first-returned
// result — including a cancellation-shaped one returned while the context
// was still live — is a genuinely independent failure and joins the local
// one.
func attributeExit(ctxErr error, returnedFirst bool, exitErr, local error) error {
	if !returnedFirst || exitErr == nil {
		return local
	}

	if ctxErr != nil && cancellationOnly(exitErr, ctxErr) {
		return ctxErr
	}

	if local == nil {
		return exitErr
	}

	return errors.Join(exitErr, local)
}

// hydrateFresh reads the projection's lifecycle stream into a new aggregate,
// to the given version or to the head when toVersion is 0. Verdicts about
// what the stream records — reconciliation, claim recovery, defeat
// classification — must come from the durable events: the configured store's
// Load may serve a read-through cache entry (aggregatestore.CachedStore does,
// for unversioned loads), while New and Hydrate reach the stream itself.
func (r *Rebuild) hydrateFresh(ctx context.Context, toVersion int64) (*aggregatestore.Aggregate[State], error) {
	var opts *aggregatestore.HydrateOptions
	if toVersion > 0 {
		opts = &aggregatestore.HydrateOptions{ToVersion: toVersion}
	}

	loaded := r.orchestrator.config.Projections.New(StreamUUID(r.name))
	if err := r.orchestrator.config.Projections.Hydrate(ctx, loaded, opts); err != nil {
		return nil, err
	}

	return loaded, nil
}

// lifecycleStream is the typed stream identity this handle addresses.
func (r *Rebuild) lifecycleStream() typeid.ID {
	return typeid.New(StreamType, StreamUUID(r.name))
}

// displacedError reports another runner's claim over the attempt, wrapping
// ErrRunnerDisplaced.
func displacedError(claimant, attemptID uuid.UUID) error {
	return fmt.Errorf("runner %s claimed rebuild attempt %s: %w", claimant, attemptID, ErrRunnerDisplaced)
}

// defeatSlot reports the exact stream slot a lost lifecycle append contended
// for: the version the save expected, plus one. The slot is known only for a
// loss that is genuinely a foreign win over the given stream: a loss
// carrying ErrEventsAppended is never one — the events at the contended
// slots are this run's own, durably recorded, even if a retried save's error
// chain also carries a stale mismatch — and a mismatch naming a different
// stream, a negative expectation, or one at the version ceiling identifies
// no slot on this stream. An expectation AHEAD of the reported actual
// version identifies none either: no foreign event occupied the slot when
// the append failed, so a stream that merely grows to it later would hand
// classification an unrelated event. Equality stays admissible — a store
// that cannot see the concurrent tip (PostgreSQL) reports the expectation
// back as the actual.
func defeatSlot(lost error, stream typeid.ID) (int64, bool) {
	if errors.Is(lost, aggregatestore.ErrEventsAppended) {
		return 0, false
	}

	var mismatch eventstore.StreamVersionMismatchError
	if !errors.As(lost, &mismatch) {
		return 0, false
	}

	if mismatch.StreamID != stream {
		return 0, false
	}

	if mismatch.ExpectedVersion < 0 || mismatch.ExpectedVersion == math.MaxInt64 {
		return 0, false
	}

	if mismatch.ActualVersion < mismatch.ExpectedVersion {
		return 0, false
	}

	return mismatch.ExpectedVersion + 1, true
}

// claimDefeat maps a claim append lost to a version conflict onto Run's
// contract by classifying the exact event that won the contended slot: the
// attempt ending there is a terminal state observed (nil), a claim recording
// another runner is displacement, and any other winner — or a slot that
// cannot be read back — leaves the raw loss to surface. The baseline is the
// fold the claim was appended against: base names the attempt and the
// claimant it recorded before the defeat, so an incumbent's own transition
// winning the slot is not mistaken for a new claimant. Classified verdicts
// install the verified slot fold, so the handle's state reflects the defeat
// it just observed; the caller holds r.mu.
func (r *Rebuild) claimDefeat(ctx context.Context, base AttemptState, lost error) error {
	slot, ok := defeatSlot(lost, r.lifecycleStream())
	if !ok {
		return lost
	}

	loaded, err := r.hydrateFresh(ctx, slot)
	if err != nil {
		return lost
	}

	// ToVersion is an upper bound, so a short stream hydrates below it: a
	// fold that never reaches the slot proves the expectation was not this
	// stream's — classifying from it would read an unrelated fold as the
	// defeating event.
	if loaded.Version() != slot {
		return lost
	}

	if err := checkLifecycleAggregate(loaded, r.name); err != nil {
		// The winning history does not even validate; fail closed with both.
		return errors.Join(lost, err)
	}

	current := loaded.State().Attempt

	switch {
	case current.ID != base.ID:
		// The defeating event ended or replaced the attempt: terminal state
		// reached and observed, nothing to run.
		r.aggregate = loaded
		return nil
	case current.Runner != base.Runner:
		r.aggregate = loaded
		return fmt.Errorf("the claim lost the stream to a competing runner: %w", ErrRunnerDisplaced)
	default:
		return lost
	}
}

// recordLostAppend classifies a lifecycle append this run lost to a version
// conflict, from the exact event that defeated it — the fold at the slot the
// append contended for — so the verdict is deterministic no matter what
// landed on the stream afterward: an ended attempt is a deliberate
// wind-down, a competing claim is displacement, and any other winner leaves
// the original error to surface. A loss carrying ErrEventsAppended is never
// classified: the events at the contended slots are this run's own durable
// append, and the raw error already reports exactly that. The verdict
// records through the same fields the reconcile loop uses, so exit
// classification surfaces it uniformly — with explicit precedence between
// the two observers: a displacement read at the defeated slot upgrades a
// clean terminal stop the reconcile loop recorded from the head, because the
// head can already show the end that FOLLOWED the defeating claim, and the
// verdict belongs to the exact defeat; a stop that carries a cause is never
// overwritten. The slot fold installs only when it is at least as new as the
// handle's current view. A load failure — or a fold that never reaches the
// slot — leaves the verdict unclassified rather than masking the original
// loss; a slot fold that fails validation is terminal.
func (r *Rebuild) recordLostAppend(ctx context.Context, attemptID, runnerID uuid.UUID, lost error) {
	slot, ok := defeatSlot(lost, r.lifecycleStream())
	if !ok {
		return
	}

	loaded, err := r.hydrateFresh(ctx, slot)
	if err != nil {
		return
	}

	if loaded.Version() != slot {
		return
	}

	if err := checkLifecycleAggregate(loaded, r.name); err != nil {
		r.recordTerminalStop(err)
		return
	}

	current := loaded.State().Attempt
	ended := current.ID != attemptID
	displaced := !ended && current.Runner != runnerID

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopped {
		if displaced && r.failure == nil {
			r.failure = displacedError(current.Runner, attemptID)
		}

		return
	}

	if loaded.Version() >= r.aggregate.Version() {
		r.aggregate = loaded
	}

	switch {
	case ended:
		r.stopped = true
		r.certificate = nil
	case displaced:
		r.stopped = true
		r.failure = displacedError(current.Runner, attemptID)
		r.certificate = nil
	}
}

// Promote cuts reads over to the target version by recording Promoted — the
// event is the flip. Cutover workers converge registered setters on it: a
// running worker tails and delivers the flip, and a restarted worker refolds
// and applies each projection's final cutover. Nothing runs inline, so there
// is no hook failure to special-case and no unrecorded cutover to repair.
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

	certificate := r.certificate

	switch {
	case r.failure != nil:
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the handle is revoked: %w", ErrNotCertified, r.failure)
	case r.stopped:
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the run was stopped", ErrNotCertified)
	case certificate == nil:
		r.mu.Unlock()
		return fmt.Errorf("%w: run the rebuild to drain it to the head and certify", ErrNotCertified)
	case certificate.attempt != state.Attempt.ID:
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the certificate covers a different attempt", ErrNotCertified)
	case certificate.runner != state.Attempt.Runner:
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the certifying runner is no longer the attempt's recorded claimant", ErrNotCertified)
	case certificate.position != state.Attempt.CaughtUpPos:
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the certified position is not the recorded catch-up position", ErrNotCertified)
	case certificate.version != r.aggregate.Version():
		// The lifecycle advanced past the version the certificate was cut
		// against; the version only grows, so the certificate is dead.
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the lifecycle advanced after catch-up was certified", ErrNotCertified)
	case certificate.exited != (<-chan struct{})(r.processorExited):
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the certificate is bound to a different processor incarnation", ErrNotCertified)
	}

	select {
	case <-certificate.exited:
		// Exit publication and this check are serialized under the handle's
		// lock, so a promotion past this point commits strictly before the
		// certifying processor's exit is published.
		r.certificate = nil
		r.mu.Unlock()

		return fmt.Errorf("%w: the certifying processor has exited", ErrNotCertified)
	default:
	}

	// With the append: at the ceiling the increment would wrap negative —
	// and the fold's own wrapped comparison would accept it as continuity,
	// splitting routing (which refuses negative revisions) from the
	// lifecycle. Checked after certification so the license protocol keeps
	// its refusals' precedence.
	if state.CutoverRevision == math.MaxInt64 {
		r.mu.Unlock()
		return fmt.Errorf("cannot promote %s: the projection's cutover revision is exhausted", state.Attempt.Target)
	}

	appendErr := r.appendLocked(ctx, Promoted{
		Previous:         state.Live,
		Next:             state.Attempt.Target,
		Revision:         state.CutoverRevision + 1,
		PolicyGeneration: state.RetirementPolicy.Generation,
		At:               time.Now(),
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

	if state.CutoverRevision == math.MaxInt64 {
		r.mu.Unlock()
		return errors.New("cannot roll back: the projection's cutover revision is exhausted")
	}

	appendErr := r.appendLocked(ctx, RolledBack{
		From:       state.Live,
		RevertedTo: state.Attempt.Previous,
		Revision:   state.CutoverRevision + 1,
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

// A RetireOption configures one retirement.
type RetireOption func(*retireConfig)

// retireConfig collects a retirement's options.
type retireConfig struct {
	override  RetirementOverride
	optionErr error
}

// WithRetirementOverride authorizes this one retirement without witness
// attestation, durably audited: the actor and reason are recorded in the
// reservation. It substitutes for a witness policy, not for the teardown
// preconditions, and a retry of an already-reserved retirement ignores it —
// the reservation's captured gate governs.
func WithRetirementOverride(actor, reason string) RetireOption {
	return func(c *retireConfig) {
		if actor == "" || reason == "" {
			c.optionErr = errors.Join(c.optionErr, errors.New("a retirement override requires an actor and a reason"))
			return
		}

		c.override = RetirementOverride{Actor: actor, Reason: reason}
	}
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
// Destruction is gated on the durable retirement policy. Every witness the
// policy requires is resolved from the orchestrator's registrations and
// must attest to serving the exact live (version, revision) pair — first
// while rollback remains available, and again after the reservation, so
// the receipts recorded with the reservation and the completion bound the
// destruction. A projection with no recorded policy refuses to retire
// unless the call carries an audited WithRetirementOverride; an explicitly
// unwitnessed policy retires without attestation. A retry from
// PhaseRetiring re-attests the membership the reservation captured, never
// current process configuration.
//
// Retiring a nonzero previous version requires its handler to implement
// projection.Teardowner. The capability and the witness gate are resolved
// before anything is reserved — a refused retirement leaves rollback
// available — and the same resolved handler performs the teardown. The
// previous version's steady-state processor must be stopped and joined
// before Retire: teardown does not fence a running processor, and its
// writes would race the removal. Read quiescence is what the witnesses
// attest: a route still serving the version about to be destroyed cannot
// vouch for the live cutover.
//
// A first rebuild has no previous version, so there is nothing to tear down
// and — rollback being impossible without a rollback target — nothing to
// reserve against: Retire completes it by recording PreviousRetired with a
// zero ID and no receipts.
func (r *Rebuild) Retire(ctx context.Context, opts ...RetireOption) error {
	var config retireConfig

	for _, opt := range opts {
		if opt == nil {
			return errors.New("retire option must not be nil")
		}

		opt(&config)
	}

	if config.optionErr != nil {
		return config.optionErr
	}

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
		return r.recordRetirement(ctx, previous, nil)
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

	// The witness gate: a fresh retirement captures the active policy's
	// membership (or the audited override); a retry re-attests exactly what
	// its reservation captured.
	var required []string

	if state.Attempt.Phase == PhaseRetiring {
		required = state.Attempt.RetiringWitnesses
	} else {
		switch {
		case config.override != (RetirementOverride{}):
		case state.RetirementPolicy.zero():
			return fmt.Errorf("cannot retire %s: projection %q has no retirement policy; record one with SetRetirementPolicy or authorize this retirement with WithRetirementOverride",
				previous, state.Name)
		case state.RetirementPolicy.Unwitnessed:
		default:
			required = state.RetirementPolicy.Witnesses
		}
	}

	witnesses, err := r.resolveWitnesses(required)
	if err != nil {
		return fmt.Errorf("cannot retire %s: %w", previous, err)
	}

	// Preflight while rollback remains available (or, on a retry, before
	// the teardown re-runs): every required witness vouches for the exact
	// live cutover.
	cutover := Cutover{Live: state.Live, Revision: state.CutoverRevision}

	receipts, err := attest(ctx, witnesses, required, state.Name, cutover)
	if err != nil {
		return fmt.Errorf("retirement preflight for %s: %w", previous, err)
	}

	if state.Attempt.Phase == PhasePromoted {
		err := r.appendLocked(ctx, RetireStarted{
			Retiring:         previous,
			PolicyGeneration: state.RetirementPolicy.Generation,
			Witnesses:        required,
			Receipts:         receipts,
			Override:         config.override,
			At:               time.Now(),
		})
		if err != nil {
			if errors.Is(err, aggregatestore.ErrEventsAppended) {
				return staleHandleError("retirement start", err)
			}

			return err
		}

		// Recheck the captured set after the reservation: the attestation
		// that counts is the one no concurrent rollback can undercut. A
		// refusal here leaves the reservation standing; retry Retire.
		if receipts, err = attest(ctx, witnesses, required, state.Name, cutover); err != nil {
			return fmt.Errorf("retirement recheck for %s (the reservation stands; retry Retire): %w", previous, err)
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

	return r.recordRetirement(ctx, previous, receipts)
}

// resolveWitnesses maps required witness IDs to their registered
// implementations, refusing when any is missing: an unregistered required
// witness refuses the retirement rather than weakening it.
func (r *Rebuild) resolveWitnesses(ids []string) ([]RetirementWitness, error) {
	witnesses := make([]RetirementWitness, len(ids))

	var missing []string

	for i, id := range ids {
		witness, ok := r.orchestrator.witnesses[id]
		if !ok {
			missing = append(missing, strconv.Quote(id))
			continue
		}

		witnesses[i] = witness
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("required retirement witnesses are not registered: %s", strings.Join(missing, ", "))
	}

	return witnesses, nil
}

// attest collects one receipt per required witness, each attesting to
// serving exactly the given cutover; any witness that cannot vouch refuses
// the retirement.
func attest(ctx context.Context, witnesses []RetirementWitness, ids []string, name string, cutover Cutover) ([]WitnessReceipt, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	receipts := make([]WitnessReceipt, len(ids))

	for i, witness := range witnesses {
		applied, err := witness.AppliedCutover(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("witness %q cannot vouch for %s: %w", ids[i], name, err)
		}

		if applied != cutover {
			return nil, fmt.Errorf("witness %q serves %s at revision %d, not the live %s at revision %d",
				ids[i], applied.Live, applied.Revision, cutover.Live, cutover.Revision)
		}

		receipts[i] = WitnessReceipt{Witness: ids[i], Cutover: applied}
	}

	return receipts, nil
}

// recordRetirement appends the PreviousRetired completion with its final
// receipts, mapping a durable-but-unobserved append to the stale-handle
// contract.
func (r *Rebuild) recordRetirement(ctx context.Context, retired projection.ID, receipts []WitnessReceipt) error {
	err := r.appendLocked(ctx, PreviousRetired{Retired: retired, Receipts: receipts})
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
