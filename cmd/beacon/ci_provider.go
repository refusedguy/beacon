package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

// ghCIProvider adapts the authenticated `gh` CLI to domain.CIProvider. The
// project already has a richer GitHub adapter under internal/infra/github;
// this one is the small composition-root bridge used by the local MCP server
// to turn observed checks into verdicts without a second credential store.
type ghCIProvider struct{}

var _ domain.CIProvider = (*ghCIProvider)(nil)

func (ghCIProvider) PRState(ctx context.Context, repo string, pr int) (domain.PRState, error) {
	prArg := strconv.Itoa(pr)
	view, err := exec.CommandContext(ctx, "gh", "pr", "view", prArg, "--repo", repo, "--json",
		"headRefOid,baseRefName,state,isDraft,mergeable,mergeStateStatus,reviewDecision").Output()
	if err != nil {
		return domain.PRState{}, fmt.Errorf("gh pr view: %w", err)
	}
	var raw struct {
		HeadRefOid       string `json:"headRefOid"`
		BaseRefName      string `json:"baseRefName"`
		State            string `json:"state"`
		IsDraft          bool   `json:"isDraft"`
		Mergeable        string `json:"mergeable"`
		MergeStateStatus string `json:"mergeStateStatus"`
		ReviewDecision   string `json:"reviewDecision"`
	}
	if err := json.Unmarshal(view, &raw); err != nil {
		return domain.PRState{}, fmt.Errorf("gh pr view decode: %w", err)
	}

	checksOut, err := exec.CommandContext(ctx, "gh", "pr", "checks", prArg, "--repo", repo, "--json",
		"name,state,link").Output()
	if err != nil {
		// No checks yet is an honest pending state, not an error.
		if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "no checks reported") {
			return domain.PRState{
				State: raw.State, IsDraft: raw.IsDraft, HeadRefOid: raw.HeadRefOid,
				BaseRefName: raw.BaseRefName, Mergeable: raw.Mergeable,
				MergeStateStatus: raw.MergeStateStatus, ReviewDecision: raw.ReviewDecision,
			}, nil
		}
		return domain.PRState{}, fmt.Errorf("gh pr checks: %w", err)
	}
	var rows []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Link  string `json:"link"`
	}
	if err := json.Unmarshal(checksOut, &rows); err != nil {
		return domain.PRState{}, fmt.Errorf("gh pr checks decode: %w", err)
	}

	checks := make(map[string]domain.Check, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		if name == "" {
			continue
		}
		conclusion := ghCheckConclusion(row.State)
		checks[name] = domain.Check{
			State:      conclusion,
			Conclusion: conclusion,
			Required:   true,
			DetailsURL: row.Link,
		}
	}
	return domain.PRState{
		State: raw.State, IsDraft: raw.IsDraft, HeadRefOid: raw.HeadRefOid,
		BaseRefName: raw.BaseRefName, Mergeable: raw.Mergeable,
		MergeStateStatus: raw.MergeStateStatus, ReviewDecision: raw.ReviewDecision,
		Checks: checks,
	}, nil
}

// ghCheckConclusion maps gh's check state to Beacon's conclusion strings.
func ghCheckConclusion(state string) string {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "PASS", "SUCCESS":
		return "SUCCESS"
	case "FAIL", "FAILURE", "ERROR", "TIMED_OUT", "ACTION_REQUIRED":
		return "FAILURE"
	case "CANCELLED", "SKIPPED":
		return "CANCELLED"
	default:
		return "PENDING"
	}
}
