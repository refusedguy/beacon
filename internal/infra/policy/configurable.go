// Package policy implements configurable automerge criteria as a rule chain.
package policy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// Config holds the merge criteria that build a Policy. RequiredChecks is the
// resolved list of check names. The config layer fills it from an explicit
// list or from workflow job names on load and on every watcher reload.
type Config struct {
	RequiredChecks    []string
	RequiredApprovals int
	Labels            []string
	CommandGate       string
	ReviewMode        core.ReviewMode
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
	criteria []core.Criterion
}

// New builds a Policy from the criteria enabled in cfg.
func New(cfg Config) *Policy {
	var rules []core.Criterion
	if len(cfg.RequiredChecks) > 0 {
		rules = append(rules, ChecksRule{Names: cfg.RequiredChecks})
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
	return &Policy{criteria: rules}
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

// ChecksRule gates a merge on required checks passing.
type ChecksRule struct{ Names []string }

// Name returns the required checks criterion name.
func (r ChecksRule) Name() string { return "required_checks" }

// Evaluate reports which required checks have not passed.
func (r ChecksRule) Evaluate(o core.Observation) (core.Verdict, string) {
	if o.AllRequiredPass(r.Names) {
		return core.VerdictNoop, ""
	}
	if o.WorkflowSkipped {
		if _, failed := o.Failure(); !failed {
			return core.VerdictNoop, ""
		}
	}
	var notPassed []string
	for _, want := range r.Names {
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
