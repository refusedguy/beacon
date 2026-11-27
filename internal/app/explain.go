package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// ExplainStep is one check in a dry-run verdict trace. SideEffect marks a rule
// whose evaluation would do work outside the observation, so the trace records
// it without running it.
type ExplainStep struct {
	Name       string       `json:"name"`
	Verdict    core.Verdict `json:"verdict"`
	Reason     string       `json:"reason,omitempty"`
	SideEffect bool         `json:"side_effect,omitempty"`
	Detail     string       `json:"detail,omitempty"`
}

// ExplainResult is the read-only verdict trace for one pull request.
type ExplainResult struct {
	PR      int           `json:"pr"`
	RunID   core.RunID    `json:"run_id,omitempty"`
	Verdict core.Verdict  `json:"verdict"`
	Reason  string        `json:"reason"`
	Steps   []ExplainStep `json:"steps"`
}

// ExplainPolicy is the policy surface the explain service traces. A policy
// implements Explain by walking every invariant and criterion, not by calling
// core.Decide, which stops at the first failure.
type ExplainPolicy interface {
	Explain(core.Observation) []ExplainStep
}

// ExplainStore is the read-only store surface used by ExplainService. It has
// no write method, so an explain call cannot change a run.
type ExplainStore interface {
	FindByPR(ctx context.Context, pr int) (*core.Run, error)
	Approved(ctx context.Context, id core.RunID, sha string) (bool, error)
}

// ExplainService produces a verdict trace from a fresh observation. It never
// writes to runs, cards or the source-control provider.
type ExplainService struct {
	Store  ExplainStore
	SCM    core.ScmGateway
	Policy ExplainPolicy
}

// Explain observes pr once, folds in the stored approval for that head and
// traces every rule. A pull request without a run still gets a trace. Only its
// stored approval is absent.
func (s ExplainService) Explain(ctx context.Context, pr int) (ExplainResult, error) {
	if s.Store == nil || s.SCM == nil || s.Policy == nil {
		return ExplainResult{}, errors.New("explain needs a store, an SCM gateway and a policy")
	}
	obs, err := s.SCM.Observe(ctx, pr)
	if err != nil {
		return ExplainResult{}, err
	}
	obs.PRNumber = pr
	result := ExplainResult{PR: pr}
	run, err := s.Store.FindByPR(ctx, pr)
	if err != nil {
		return ExplainResult{}, err
	}
	if run != nil {
		result.RunID = run.ID
		approved, err := s.Store.Approved(ctx, run.ID, obs.HeadSHA)
		if err != nil {
			return ExplainResult{}, err
		}
		if approved {
			obs.Approvals++
		}
	}
	result.Steps = s.Policy.Explain(obs)
	result.Verdict, result.Reason = explainSummary(result.Steps)
	return result, nil
}

// explainSummary picks the first step that would change a run. A noop step
// with a reason describes a terminal pull request, so it is the summary when
// no criterion blocks. Skipped side-effect rules never supply the reason.
func explainSummary(steps []ExplainStep) (core.Verdict, string) {
	for _, step := range steps {
		if step.Verdict == core.VerdictNoop || step.SideEffect {
			continue
		}
		return step.Verdict, step.Reason
	}
	for _, step := range steps {
		if step.SideEffect || step.Reason == "" {
			continue
		}
		return step.Verdict, step.Reason
	}
	return core.VerdictMerge, "all criteria passed"
}

// FormatExplain renders a trace as operator text. It lists every step in the
// order the policy recorded them and names the actionable verdict first.
func FormatExplain(r ExplainResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PR %d dry-run verdict: %s\n", r.PR, r.Verdict)
	fmt.Fprintf(&b, "reason: %s\n", r.Reason)
	for _, step := range r.Steps {
		status := string(step.Verdict)
		if step.SideEffect {
			status = "skipped"
		}
		fmt.Fprintf(&b, "  %s: %s", step.Name, status)
		if step.Reason != "" {
			fmt.Fprintf(&b, " %s", step.Reason)
		}
		if step.Detail != "" {
			fmt.Fprintf(&b, " [%s]", step.Detail)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
