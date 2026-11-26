package scm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"

	"github.com/refusedguy/beacon/pkg/core"
	"github.com/refusedguy/beacon/pkg/core/redact"
)

// gitlabTail keeps the last max runes of s. The byte check avoids a rune slice
// for short strings, and valid UTF-8 is walked backwards from the end. Invalid
// UTF-8 keeps the eager conversion so the output stays byte identical.
func gitlabTail(s string, max int) string {
	if len(s) <= max {
		return strings.TrimSpace(s)
	}
	if max <= 0 || !utf8.ValidString(s) {
		r := []rune(s)
		if len(r) <= max {
			return strings.TrimSpace(s)
		}
		return strings.TrimSpace(string(r[len(r)-max:]))
	}
	i, n := len(s), 0
	for i > 0 && n < max {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
		n++
	}
	if i == 0 {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[i:])
}

type glPipeline struct {
	ID     int    `json:"id"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Status string `json:"status"`
}

// glTimestamp is a GitLab job timestamp. GitLab sends null or an empty string
// for an endpoint the job has not reached yet, and a malformed value must not
// turn a whole observation incomplete.
type glTimestamp struct{ time.Time }

func (t *glTimestamp) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" || string(data) == `""` {
		t.Time = time.Time{}
		return nil
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		t.Time = time.Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Time = time.Time{}
		return nil
	}
	t.Time = parsed
	return nil
}

type glJob struct {
	ID            int         `json:"id"`
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	AllowFailure  bool        `json:"allow_failure"`
	FailureReason string      `json:"failure_reason"`
	StartedAt     glTimestamp `json:"started_at"`
	FinishedAt    glTimestamp `json:"finished_at"`
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
	return getJSONPages[glJob](g, ctx, path, nil)
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
		checks[i] = core.CheckResult{
			Name: job.Name, State: state, Output: outputs[i],
			StartedAt: job.StartedAt.Time, CompletedAt: job.FinishedAt.Time,
		}
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
		return redact.Redact(gitlabTail(string(trace), 1200))
	}
	if job.FailureReason != "" {
		return redact.Redact(job.FailureReason)
	}
	return ""
}
