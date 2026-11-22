// Package tasks runs one night-shift triage pass over a backlog. The worker
// owns no clock, store, process or network client. The caller supplies the
// backlog, the delegate, the Watch registry and the audit sink as ports, so
// selection and idempotency are testable with fakes (BS.63, ADR 0028).
//
// Idempotency is carried by State: every issue the worker delegates is marked
// handled in the returned state. Feeding that state into the next pass skips
// those issues, so a repeated pass never assigns the same task twice. The
// Delegator port is idempotent by issue ID as a second line of defence, because
// a crash after delegation but before the caller persists the state can replay
// the pass from the old snapshot.
package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// RoleExecutor is the Watch role registered for delegated work. It is an
// untyped constant so an adapter can pass it to the domain role without this
// package importing domain (ADR 0005).
const RoleExecutor = "executor"

// Audit actions recorded for one issue.
const (
	AuditDelegated       = "delegated"
	AuditWatchRegistered = "watch_registered"
	AuditWatchFound      = "watch_found"
)

// Issue is one candidate from the backlog. ID is the stable idempotency key;
// it stays the same across passes, retries and process restarts.
type Issue struct {
	ID    string
	Title string
}

// PR identifies the pull request a delegation opens. Number and HeadSHA are
// the coordinates a Watch is keyed by.
type PR struct {
	Repo    string
	Number  int
	HeadSHA string
}

// valid reports whether pr carries enough data to register a Watch.
func (p PR) valid() bool {
	return strings.TrimSpace(p.Repo) != "" && p.Number > 0 && strings.TrimSpace(p.HeadSHA) != ""
}

// Delegation is the task created for an issue and the PR it produced.
type Delegation struct {
	IssueID string
	TaskID  string
	PR      PR
}

// WatchRef is the small projection of a Beacon Watch this pass needs.
type WatchRef struct {
	ID       string
	Repo     string
	PR       int
	HeadSHA  string
	Role     string
	Revision uint64
}

// AuditEvent is one recorded action of a pass. Action is one of the Audit
// constants above.
type AuditEvent struct {
	IssueID string
	Action  string
	Detail  string
}

// Outcome kinds are the closed set of per-issue results a pass reports. The
// worker and the daemon report use the same strings, so one row reads the same
// at both ends.
const (
	OutcomeDelegated    = "delegated"
	OutcomeFailed       = "failed"
	OutcomeNotAttempted = "not_attempted"
	OutcomeSkipped      = "skipped"
)

// NotAttemptedAction is the action recorded for a unit the pass did not start
// because its context was cancelled at the unit boundary.
const NotAttemptedAction = "rerun in the next pass after cancellation"

// KernelPrefix marks a recovery unit that a caller schedules before the rest.
// PlanTriage keeps its input-order contract, so the marker only annotates the
// report and never reorders the plan.
const KernelPrefix = "KERNEL-"

// IsKernel reports whether issue carries the kernel priority marker.
func IsKernel(issue Issue) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(issue.ID)), KernelPrefix)
}

// Outcome is one ordered line of a triage pass report. Reason explains a
// failed unit; Action tells the caller what to do with a unit that was not
// started. Priority marks a kernel unit scheduled ahead of the rest.
type Outcome struct {
	IssueID  string
	Kind     string
	Reason   string
	Action   string
	Priority bool
}

// Failure is one unit whose delegate, watch or audit step failed.
type Failure struct {
	IssueID string
	Reason  string
}

// NotAttemptedUnit is one unit the pass did not start after cancellation.
type NotAttemptedUnit struct {
	IssueID string
	Action  string
}

// State is the durable record of issues already handled. The worker copies it
// on every pass and never mutates the snapshot it is given. A zero State
// handles every candidate.
type State struct {
	Handled map[string]bool
}

// Clone returns an independent copy of s, so writing to the result cannot
// change s. A nil Handled map stays nil and is allocated on first write.
func (s State) Clone() State {
	out := State{}
	if len(s.Handled) == 0 {
		return out
	}
	out.Handled = make(map[string]bool, len(s.Handled))
	for id, ok := range s.Handled {
		out.Handled[id] = ok
	}
	return out
}

// TriagePlan is the pure decision of which candidates to delegate. Skipped
// holds the issues already handled or with no usable ID, in input order.
type TriagePlan struct {
	ToDelegate []Issue
	Skipped    []Issue
}

// PlanTriage selects the candidates a pass should delegate. It walks the
// candidates in input order and takes an issue only when its trimmed ID is
// non-empty and absent from state.Handled. Duplicate IDs in the same batch are
// delegated once. The function reads no clock and touches no I/O, and it does
// not mutate state or candidates.
func PlanTriage(state State, candidates []Issue) TriagePlan {
	var plan TriagePlan
	seen := make(map[string]bool, len(state.Handled)+len(candidates))
	for id := range state.Handled {
		seen[id] = true
	}
	for _, issue := range candidates {
		id := strings.TrimSpace(issue.ID)
		if id == "" {
			plan.Skipped = append(plan.Skipped, issue)
			continue
		}
		if seen[id] {
			plan.Skipped = append(plan.Skipped, issue)
			continue
		}
		seen[id] = true
		issue.ID = id
		plan.ToDelegate = append(plan.ToDelegate, issue)
	}
	return plan
}

// Backlog lists the candidate issues for one triage pass.
type Backlog interface {
	Candidates(ctx context.Context) ([]Issue, error)
}

// Delegator assigns an issue to a worker and returns the task it created.
// Implementations must be idempotent by issue.ID: a second call with the same
// ID returns the original delegation and starts no new work.
type Delegator interface {
	Delegate(ctx context.Context, issue Issue) (Delegation, error)
}

// WatchRegistry finds an existing Watch for a PR by (repo, pr, headSHA) or
// registers a new one with the given role.
type WatchRegistry interface {
	Find(ctx context.Context, pr PR) (WatchRef, bool, error)
	Register(ctx context.Context, pr PR, role string) (WatchRef, error)
}

// AuditWriter records one action of a pass through the audit sink.
type AuditWriter interface {
	Record(ctx context.Context, event AuditEvent) error
}

// Result is the observable outcome of one triage pass. State is the snapshot
// to persist; it marks every delegated issue as handled. On a port error after
// a successful delegation the Result still carries that mark, so the caller
// can persist partial progress and avoid a second delegation.
type Result struct {
	Delegated    []Delegation
	Watches      []WatchRef
	Skipped      int
	State        State
	Outcomes     []Outcome          // ordered, one row per candidate
	Failed       []Failure          // failed units with their reason
	NotAttempted []NotAttemptedUnit // units not started after cancellation
}

// ErrNilPorts is reported when a pass needs a port that was not connected.
var ErrNilPorts = errors.New("tasks: night shift: ports not connected")

// Worker applies triage passes through its ports. Reuse one Worker across
// passes; it keeps no mutable state of its own between calls.
type Worker struct {
	backlog   Backlog
	delegator Delegator
	watches   WatchRegistry
	audit     AuditWriter
}

// New builds a Worker over the four ports.
func New(backlog Backlog, delegator Delegator, watches WatchRegistry, audit AuditWriter) *Worker {
	return &Worker{backlog: backlog, delegator: delegator, watches: watches, audit: audit}
}

// Run performs one triage pass. It selects the unhandled candidates, delegates
// each one, follows the resulting PR with a Watch and writes an audit event for
// every action. It returns a new State that marks every delegated issue as
// handled, so repeating the pass with that state delegates nothing.
//
// A backlog read failure is fatal and is returned as an error. A failure on one
// issue is recorded as a failed outcome and the pass moves to the next issue.
// Cancellation is checked only at the boundary of a unit; work already started
// is never interrupted. When cancellation is observed, the current and all
// remaining units are recorded as not attempted with the action to take.
func (w *Worker) Run(ctx context.Context, in State) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := Result{State: in.Clone()}
	if w == nil || w.backlog == nil || w.delegator == nil || w.watches == nil || w.audit == nil {
		return out, ErrNilPorts
	}

	candidates, err := w.backlog.Candidates(ctx)
	if err != nil {
		return out, fmt.Errorf("tasks: night shift: backlog: %w", err)
	}

	plan := PlanTriage(in, candidates)
	out.Skipped = len(plan.Skipped)
	for _, issue := range plan.Skipped {
		out.Outcomes = append(out.Outcomes, Outcome{
			IssueID: issue.ID, Kind: OutcomeSkipped,
			Reason: "already handled or missing id", Action: "none",
			Priority: IsKernel(issue),
		})
	}
	for i, issue := range plan.ToDelegate {
		// The only cancellation check in the pass sits at the unit boundary.
		// A unit that has begun runs to completion even if ctx is cancelled.
		if err := ctx.Err(); err != nil {
			for _, rest := range plan.ToDelegate[i:] {
				out.NotAttempted = append(out.NotAttempted, NotAttemptedUnit{
					IssueID: rest.ID, Action: NotAttemptedAction,
				})
				out.Outcomes = append(out.Outcomes, Outcome{
					IssueID: rest.ID, Kind: OutcomeNotAttempted,
					Reason: err.Error(), Action: NotAttemptedAction,
					Priority: IsKernel(rest),
				})
			}
			return out, nil
		}
		if err := w.triageIssue(ctx, issue, &out); err != nil {
			out.Failed = append(out.Failed, Failure{IssueID: issue.ID, Reason: err.Error()})
			out.Outcomes = append(out.Outcomes, Outcome{
				IssueID: issue.ID, Kind: OutcomeFailed,
				Reason: err.Error(), Action: "inspect reason and retry in the next pass",
				Priority: IsKernel(issue),
			})
			continue
		}
		out.Outcomes = append(out.Outcomes, Outcome{
			IssueID: issue.ID, Kind: OutcomeDelegated,
			Priority: IsKernel(issue),
		})
	}
	return out, nil
}

// triageIssue delegates one issue, follows its PR and records the actions. It
// marks the issue handled as soon as delegation succeeds so a later failure in
// the same pass cannot cause a second delegation on retry.
func (w *Worker) triageIssue(ctx context.Context, issue Issue, out *Result) error {
	delegation, err := w.delegator.Delegate(ctx, issue)
	if err != nil {
		return fmt.Errorf("tasks: night shift: delegate %s: %w", issue.ID, err)
	}
	if out.State.Handled == nil {
		out.State.Handled = make(map[string]bool)
	}
	out.State.Handled[issue.ID] = true
	out.Delegated = append(out.Delegated, delegation)

	if err := w.audit.Record(ctx, AuditEvent{
		IssueID: issue.ID,
		Action:  AuditDelegated,
		Detail:  delegation.TaskID,
	}); err != nil {
		return fmt.Errorf("tasks: night shift: audit delegate %s: %w", issue.ID, err)
	}

	if !delegation.PR.valid() {
		return fmt.Errorf("tasks: night shift: delegate %s: invalid PR %+v", issue.ID, delegation.PR)
	}
	ref, found, err := w.watches.Find(ctx, delegation.PR)
	if err != nil {
		return fmt.Errorf("tasks: night shift: find watch %s: %w", issue.ID, err)
	}
	action := AuditWatchFound
	if !found {
		ref, err = w.watches.Register(ctx, delegation.PR, RoleExecutor)
		if err != nil {
			return fmt.Errorf("tasks: night shift: register watch %s: %w", issue.ID, err)
		}
		action = AuditWatchRegistered
	}
	if ref.ID == "" {
		return fmt.Errorf("tasks: night shift: watch %s: empty watchID", issue.ID)
	}
	if err := w.audit.Record(ctx, AuditEvent{
		IssueID: issue.ID,
		Action:  action,
		Detail:  ref.ID,
	}); err != nil {
		return fmt.Errorf("tasks: night shift: audit watch %s: %w", issue.ID, err)
	}
	out.Watches = append(out.Watches, ref)
	return nil
}
