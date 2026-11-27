// Package policy implements configurable automerge criteria as a rule chain.
package policy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// Config holds the merge criteria that build a Policy. Required checks reach
// the policy only through RequiredCheckSource, so the source can change its
// names without a policy rebuild.
type Config struct {
	RequiredCheckSource core.RequiredCheckSource
	RequiredApprovals   int
	Labels              []string
	CommandGate         string
	ReviewMode          core.ReviewMode
}

// ReviewRule gates epic pull requests on review approval. A missing review
// mode means the LLM reviewer, matching the default on the task card.
type ReviewRule struct{ Mode core.ReviewMode }

// Name returns the review criterion name.
func (r ReviewRule) Name() string { return "review" }

// Evaluate reports whether o still needs review approval.
func (r ReviewRule) Evaluate(o core.Observation) (core.Verdict, string) {
	if r.Mode == core.ReviewModeNone {
		return core.VerdictNoop, ""
	}
	if o.Approvals > 0 {
		return core.VerdictNoop, ""
	}
	return core.VerdictWait, "epic review approval required"
}

// Policy evaluates an observation against a chain of criteria.
type Policy struct {
	criteria     []core.Criterion
	checksSource core.RequiredCheckSource
}

// RequiredChecks returns the names a merge must pass. It lets the failure
// invariant in core.Decide use the same source as the required-checks criterion,
// so a cancelled or failed optional check cannot fire the invariant.
func (p *Policy) RequiredChecks() ([]string, bool) {
	if p == nil || p.checksSource == nil {
		return nil, false
	}
	names, err := p.checksSource.Names(context.Background())
	if err != nil {
		return nil, false
	}
	return names, true
}

// New builds a Policy from the criteria enabled in cfg. Required checks are
// read through cfg.RequiredCheckSource when one is supplied.
func New(cfg Config) *Policy {
	var rules []core.Criterion
	if cfg.RequiredCheckSource != nil {
		rules = append(rules, SourceChecksRule{Source: cfg.RequiredCheckSource})
	}
	if cfg.RequiredApprovals > 0 {
		rules = append(rules, ApprovalsRule{Needed: cfg.RequiredApprovals})
	}
	if len(cfg.Labels) > 0 {
		rules = append(rules, LabelRule{Names: cfg.Labels})
	}
	if strings.TrimSpace(cfg.CommandGate) != "" {
		rules = append(rules, CommandRule{Command: cfg.CommandGate})
	}
	if cfg.ReviewMode == core.ReviewModeHuman || cfg.ReviewMode == core.ReviewModeLLM {
		rules = append(rules, ReviewRule{Mode: cfg.ReviewMode})
	}
	return &Policy{criteria: rules, checksSource: cfg.RequiredCheckSource}
}

// Evaluate decides whether the observation may merge.
func (p *Policy) Evaluate(o core.Observation) core.MergeDecision {
	return core.Decide(o, p.criteria)
}

// ReviewRequired reports whether mode still needs review approval for o. It
// uses the same rule as the review criterion in the policy chain, so the epic
// guard and the configured policy cannot drift apart.
func (p *Policy) ReviewRequired(mode core.ReviewMode, o core.Observation) bool {
	verdict, _ := ReviewRule{Mode: mode}.Evaluate(o)
	return verdict != core.VerdictNoop
}

// SideEffectCriterion marks criteria whose Evaluate does work beyond reading
// the observation. Explain records these rules without running them.
type SideEffectCriterion interface {
	HasSideEffects() bool
}

// Explain walks every invariant and criterion and returns the full trace. It
// does not call core.Decide, which stops at the first failed check. A
// side-effect rule is marked and skipped so a dry run never runs shell code.
func (p *Policy) Explain(o core.Observation) []app.ExplainStep {
	steps := make([]app.ExplainStep, 0, len(p.criteria)+6)
	if core.DecideFinalState(o.PRState).Closed {
		return append(steps, app.ExplainStep{
			Name: "invariant/closed", Verdict: core.VerdictNoop,
			Reason: "pull request is " + o.PRState,
		})
	}
	if o.Incomplete {
		reason := "observation is incomplete"
		if o.IncompleteReason != "" {
			reason += ": " + o.IncompleteReason
		}
		steps = append(steps, app.ExplainStep{Name: "invariant/incomplete", Verdict: core.VerdictWait, Reason: reason})
	}
	if o.Conflict {
		steps = append(steps, app.ExplainStep{Name: "invariant/conflict", Verdict: core.VerdictNeedsAgentFix, Reason: "merge conflict"})
	}
	if len(o.UnresolvedThreads) > 0 {
		steps = append(steps, app.ExplainStep{
			Name: "invariant/unresolved_threads", Verdict: core.VerdictNeedsAgentFix,
			Reason: "unresolved discussion: " + o.UnresolvedThreads[0],
		})
	}
	if c, ok := o.Failure(p.explainRequiredChecks()...); ok {
		reason := "required check failed: " + c.Name
		if o.FailureExcerpt != "" {
			reason += ": " + o.FailureExcerpt
		}
		steps = append(steps, app.ExplainStep{Name: "invariant/failed_check", Verdict: core.VerdictNeedsAgentFix, Reason: reason})
	}
	if o.Behind {
		steps = append(steps, app.ExplainStep{Name: "invariant/behind", Verdict: core.VerdictNeedsRebase, Reason: "branch is behind base"})
	}
	for _, rule := range p.criteria {
		if sideEffect, ok := rule.(SideEffectCriterion); ok && sideEffect.HasSideEffects() {
			steps = append(steps, app.ExplainStep{
				Name: rule.Name(), Verdict: core.VerdictNoop,
				Reason: "not run (side effects)", SideEffect: true,
			})
			continue
		}
		verdict, reason := rule.Evaluate(o)
		step := app.ExplainStep{Name: rule.Name(), Verdict: verdict, Reason: reason}
		if checks, ok := rule.(SourceChecksRule); ok {
			if described, ok := checks.Source.(interface{ NamesDescription() string }); ok {
				step.Detail = described.NamesDescription()
			}
		}
		steps = append(steps, step)
	}
	return steps
}

// explainRequiredChecks mirrors the invariant scoping in core.Decide, so a
// failed optional check cannot appear as a blocker in the trace.
func (p *Policy) explainRequiredChecks() []string {
	for _, criterion := range p.criteria {
		if source, ok := criterion.(core.RequiredCheckPolicy); ok {
			names, _ := source.RequiredChecks()
			return names
		}
	}
	return nil
}

// SourceChecksRule gates a merge on the names reported by a source. Names are
// resolved on each evaluation, so a source that changes its answer is honoured
// without rebuilding the policy.
type SourceChecksRule struct {
	Source core.RequiredCheckSource
}

// Name returns the required checks criterion name.
func (r SourceChecksRule) Name() string { return "required_checks" }

// RequiredChecks exposes the rule's source to the failure invariant, so an
// optional failed check cannot block a merge before policy runs.
func (r SourceChecksRule) RequiredChecks() ([]string, bool) {
	if r.Source == nil {
		return nil, false
	}
	names, err := r.Source.Names(context.Background())
	if err != nil {
		return nil, false
	}
	return names, true
}

// Evaluate resolves the source's names and applies the same check gate. A
// source that cannot be read fails closed with a wait verdict.
func (r SourceChecksRule) Evaluate(o core.Observation) (core.Verdict, string) {
	if r.Source == nil {
		return evaluateChecks(nil, o)
	}
	names, err := r.Source.Names(context.Background())
	if err != nil {
		return core.VerdictWait, "required checks unavailable: " + err.Error()
	}
	return evaluateChecks(names, o)
}

// evaluateChecks is the shared gate for a resolved list of names. An empty
// list means no checks are required, so it never blocks a merge.
func evaluateChecks(names []string, o core.Observation) (core.Verdict, string) {
	if len(names) == 0 {
		return core.VerdictNoop, ""
	}
	if o.AllRequiredPass(names) {
		return core.VerdictNoop, ""
	}
	if o.WorkflowSkipped {
		if _, failed := o.Failure(); !failed {
			return core.VerdictNoop, ""
		}
	}
	var notPassed []string
	for _, want := range names {
		state := "missing"
		for _, c := range o.Checks {
			if strings.EqualFold(c.Name, want) {
				state = string(c.State)
				break
			}
		}
		if state != string(core.CheckSuccess) && state != string(core.CheckSkipped) {
			notPassed = append(notPassed, want+" ("+state+")")
		}
	}
	if len(notPassed) == 0 {
		return core.VerdictWait, "required checks are not green"
	}
	return core.VerdictWait, "required checks not passed: " + strings.Join(notPassed, ", ")
}

// ApprovalsRule gates a merge on a minimum approval count.
type ApprovalsRule struct{ Needed int }

// Name returns the approvals criterion name.
func (r ApprovalsRule) Name() string { return "approvals" }

// Evaluate reports whether o has enough approvals.
func (r ApprovalsRule) Evaluate(o core.Observation) (core.Verdict, string) {
	if o.Approvals >= r.Needed {
		return core.VerdictNoop, ""
	}
	return core.VerdictWait, fmt.Sprintf("need %d approvals, have %d", r.Needed, o.Approvals)
}

// LabelRule gates a merge on required labels.
type LabelRule struct{ Names []string }

// Name returns the labels criterion name.
func (r LabelRule) Name() string { return "labels" }

// Evaluate reports which required labels are missing.
func (r LabelRule) Evaluate(o core.Observation) (core.Verdict, string) {
	for _, want := range r.Names {
		found := false
		for _, have := range o.Labels {
			if strings.EqualFold(want, have) {
				found = true
				break
			}
		}
		if !found {
			return core.VerdictWait, "missing label " + want
		}
	}
	return core.VerdictNoop, ""
}

// CommandRule is a custom gate: a nonzero exit blocks the merge.
type CommandRule struct {
	Command string
	Timeout time.Duration
}

// Name returns the command gate criterion name.
func (r CommandRule) Name() string { return "command_gate" }

// HasSideEffects reports that Evaluate runs a shell command. Explain uses it
// to mark the rule and leave it unrun in a dry-run trace.
func (r CommandRule) HasSideEffects() bool { return true }

// Evaluate runs the gate command and blocks on a nonzero exit.
func (r CommandRule) Evaluate(o core.Observation) (core.Verdict, string) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", r.Command)
	cmd.Env = append(cmd.Environ(),
		"BEACON_PR="+fmt.Sprint(o.PRNumber),
		"BEACON_SHA="+o.HeadSHA,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return core.VerdictBlocked, "gate failed: " + detail
	}
	return core.VerdictNoop, ""
}

var _ core.MergePolicy = (*Policy)(nil)
