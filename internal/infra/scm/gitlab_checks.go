package scm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

func gitlabRedact(s string) string {
	return shared.Redact(s)
}

func gitlabTail(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(r[len(r)-max:]))
}

type glPipeline struct {
	ID     int    `json:"id"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Status string `json:"status"`
}

type glJob struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	AllowFailure  bool   `json:"allow_failure"`
	FailureReason string `json:"failure_reason"`
}

func glCheckState(status string, allowFailure bool) core.CheckState {
	switch strings.ToLower(status) {
	case "success":
		return core.CheckSuccess
	case "failed":
		if allowFailure {
			return core.CheckSkipped
		}
		return core.CheckFailure
	case "canceled", "skipped", "manual":
		return core.CheckSkipped
	default:
		return core.CheckPending
	}
}

func (g *GitLab) latestPipeline(ctx context.Context, query url.Values) (glPipeline, error) {
	q := url.Values{}
	for key, values := range query {
		q[key] = append([]string(nil), values...)
	}
	q.Set("order_by", "id")
	q.Set("sort", "desc")
	q.Set("per_page", "1")
	var pipelines []glPipeline
	path := fmt.Sprintf("/projects/%s/pipelines", g.project())
	if err := g.getJSON(ctx, path, q, &pipelines); err != nil {
		return glPipeline{}, err
	}
	if len(pipelines) == 0 {
		return glPipeline{}, nil
	}
	return pipelines[0], nil
}

func (g *GitLab) pipelineChecks(ctx context.Context, pipelineID int) ([]core.CheckResult, string, error) {
	path := fmt.Sprintf("/projects/%s/pipelines/%d/jobs", g.project(), pipelineID)
	var jobs []glJob
	if err := g.getJSON(ctx, path, url.Values{"per_page": {"100"}}, &jobs); err != nil {
		return nil, "", err
	}
	checks := make([]core.CheckResult, 0, len(jobs))
	excerpt := ""
	for _, job := range jobs {
		state := glCheckState(job.Status, job.AllowFailure)
		out := ""
		if state == core.CheckFailure {
			out = g.jobFailureOutput(ctx, job)
			if excerpt == "" {
				excerpt = out
			}
		}
		checks = append(checks, core.CheckResult{Name: job.Name, State: state, Output: out})
	}
	return checks, excerpt, nil
}

func (g *GitLab) jobFailureOutput(ctx context.Context, job glJob) string {
	path := fmt.Sprintf("/projects/%s/jobs/%d/trace", g.project(), job.ID)
	trace, err := g.request(ctx, http.MethodGet, path, nil, nil)
	if err == nil && len(trace) > 0 {
		return gitlabRedact(gitlabTail(string(trace), 1200))
	}
	if job.FailureReason != "" {
		return gitlabRedact(job.FailureReason)
	}
	return ""
}
