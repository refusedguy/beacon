package scm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

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

// observationErrors collects failed provider calls so an observation can be
// marked incomplete instead of silently dropping the failures.
type observationErrors []string

func (e *observationErrors) add(part string, err error) {
	if err != nil {
		*e = append(*e, part+": "+err.Error())
	}
}

func (e *observationErrors) apply(o *core.Observation) {
	if len(*e) == 0 {
		return
	}
	o.Incomplete = true
	o.IncompleteReason = gitlabTail(strings.Join(*e, "; "), 1200)
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

// observeChecks fills the check fields from a pipeline the caller fetched in
// the first round. Provider failures are reported through failed and leave the
// checks empty.
func (g *GitLab) observeChecks(ctx context.Context, o *core.Observation, pipeline glPipeline, failed *observationErrors) {
	checks, excerpt, err := g.pipelineChecks(ctx, pipeline.ID)
	failed.add("jobs", err)
	if err != nil {
		return
	}
	applyChecks(o, checks, excerpt, pipeline)
}

// applyChecks writes checks and the excerpt in the original field order.
func applyChecks(o *core.Observation, checks []core.CheckResult, excerpt string, pipeline glPipeline) {
	o.Checks = checks
	if len(checks) == 0 {
		o.Checks = []core.CheckResult{{Name: "pipeline", State: glCheckState(pipeline.Status, false)}}
	}
	o.FailureExcerpt = excerpt
	if excerpt == "" {
		if failedCheck, ok := o.Failure(); ok {
			o.FailureExcerpt = failedCheck.Output
		}
	}
}

func (g *GitLab) pipelineChecks(ctx context.Context, pipelineID int) ([]core.CheckResult, string, error) {
	jobs, err := g.pipelineJobs(ctx, pipelineID)
	if err != nil {
		return nil, "", err
	}
	checks, excerpt := g.checksFromJobs(ctx, jobs)
	return checks, excerpt, nil
}

func (g *GitLab) pipelineJobs(ctx context.Context, pipelineID int) ([]glJob, error) {
	path := fmt.Sprintf("/projects/%s/pipelines/%d/jobs", g.project(), pipelineID)
	var jobs []glJob
	if err := g.getJSON(ctx, path, url.Values{"per_page": {"100"}}, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

// checksFromJobs fetches failure traces concurrently but fills checks and the
// excerpt in job order, so callers see the same first failure as before.
func (g *GitLab) checksFromJobs(ctx context.Context, jobs []glJob) ([]core.CheckResult, string) {
	checks := make([]core.CheckResult, len(jobs))
	outputs := make([]string, len(jobs))
	var group errgroup.Group
	group.SetLimit(observeParallelLimit)
	for i, job := range jobs {
		i, job := i, job
		if glCheckState(job.Status, job.AllowFailure) != core.CheckFailure {
			continue
		}
		group.Go(func() error {
			outputs[i] = g.jobFailureOutput(ctx, job)
			return nil
		})
	}
	_ = group.Wait()
	excerpt := ""
	for i, job := range jobs {
		state := glCheckState(job.Status, job.AllowFailure)
		checks[i] = core.CheckResult{Name: job.Name, State: state, Output: outputs[i]}
		if state == core.CheckFailure && excerpt == "" {
			excerpt = outputs[i]
		}
	}
	return checks, excerpt
}

func (g *GitLab) jobFailureOutput(ctx context.Context, job glJob) string {
	path := fmt.Sprintf("/projects/%s/jobs/%d/trace", g.project(), job.ID)
	trace, err := g.request(ctx, http.MethodGet, path, nil, nil)
	if err == nil && len(trace) > 0 {
		return shared.Redact(gitlabTail(string(trace), 1200))
	}
	if job.FailureReason != "" {
		return shared.Redact(job.FailureReason)
	}
	return ""
}
