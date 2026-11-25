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

func (r ReviewRule) Name() string { return "review" }

func (r ReviewRule) Evaluate(o core.Observation) (core.Verdict, string) {
	if r.Mode == core.ReviewModeNone {
		return core.VerdictNoop, ""
	}
	if o.Approvals > 0 {
		return core.VerdictNoop, ""
	}
	return core.VerdictWait, "epic review approval required"
}

type Policy struct {
	criteria []core.Criterion
}

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

type ChecksRule struct{ Names []string }

func (r ChecksRule) Name() string { return "required_checks" }

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

type ApprovalsRule struct{ Needed int }

func (r ApprovalsRule) Name() string { return "approvals" }

func (r ApprovalsRule) Evaluate(o core.Observation) (core.Verdict, string) {
	if o.Approvals >= r.Needed {
		return core.VerdictNoop, ""
	}
	return core.VerdictWait, fmt.Sprintf("need %d approvals, have %d", r.Needed, o.Approvals)
}

type LabelRule struct{ Names []string }

func (r LabelRule) Name() string { return "labels" }

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

func (r CommandRule) Name() string { return "command_gate" }

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
