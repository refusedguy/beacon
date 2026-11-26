package scm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

type glMR struct {
	IID                         int      `json:"iid"`
	State                       string   `json:"state"`
	SHA                         string   `json:"sha"`
	HasConflicts                bool     `json:"has_conflicts"`
	BlockingDiscussionsResolved *bool    `json:"blocking_discussions_resolved"`
	SourceBranch                string   `json:"source_branch"`
	TargetBranch                string   `json:"target_branch"`
	Labels                      []string `json:"labels"`
	ApprovedBy                  []glUser `json:"approved_by"`
	HeadPipeline                struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	} `json:"head_pipeline"`
	DiffRefs struct {
		BaseSHA string `json:"base_sha"`
		HeadSHA string `json:"head_sha"`
	} `json:"diff_refs"`
}

type glUser struct {
	ID   int `json:"id"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
}

type glNote struct {
	Body       string `json:"body"`
	Resolvable bool   `json:"resolvable"`
	Resolved   bool   `json:"resolved"`
	System     bool   `json:"system"`
}

type glThread struct {
	Resolved *bool    `json:"resolved"`
	Notes    []glNote `json:"notes"`
}

func (g *GitLab) Observe(ctx context.Context, mrIID int) (core.Observation, error) {
	var mr glMR
	if err := g.getJSON(ctx, g.mrPath(mrIID), nil, &mr); err != nil {
		return core.Observation{}, err
	}
	headSHA := mr.SHA
	if headSHA == "" {
		headSHA = mr.DiffRefs.HeadSHA
	}
	if mr.IID == 0 || headSHA == "" {
		return core.Observation{}, core.Errorf(core.CodeUnavailable, "MR %d response has no iid or head sha", mrIID)
	}
	o := core.Observation{
		PRNumber:  mrIID,
		HeadSHA:   headSHA,
		PRState:   mr.State,
		Conflict:  mr.HasConflicts,
		Labels:    append([]string(nil), mr.Labels...),
		Approvals: len(mr.ApprovedBy),
	}

	signals := g.fetchSignals(ctx, mrIID, mr, headSHA)
	var failed observationErrors
	failed.add("threads", signals.threadsErr)
	if signals.threadsErr == nil {
		o.UnresolvedThreads = signals.threads
	}
	if len(o.UnresolvedThreads) == 0 && mr.BlockingDiscussionsResolved != nil && !*mr.BlockingDiscussionsResolved {
		o.UnresolvedThreads = []string{"blocking discussions are unresolved"}
	}
	failed.add("approvals", signals.approvalsErr)
	if signals.approvalsErr == nil {
		o.Approvals = signals.approvals
	}
	g.applyObservedChecks(ctx, &o, signals, &failed)
	failed.add("compare", signals.compareErr)
	if signals.compareErr == nil {
		o.Behind = signals.behind
		o.BaseSHA = signals.baseSHA
	}
	failed.apply(&o)
	return o, nil
}

// gitlabSignals carries the first Observe round. Errors stay separate so
// Observe can record them in the original part order.
type gitlabSignals struct {
	threads          []string
	approvals        int
	pipeline         glPipeline
	jobs             []glJob
	behind           bool
	baseSHA          string
	threadsErr       error
	approvalsErr     error
	pipelineErr      error
	jobsErr          error
	compareErr       error
	usesHeadPipeline bool
}

// fetchSignals fetches every call that depends only on the MR read. A GitLab MR
// carries its head pipeline; when present, jobs join this round too and only
// the failure traces wait for the jobs result.
func (g *GitLab) fetchSignals(ctx context.Context, mrIID int, mr glMR, sha string) gitlabSignals {
	var out gitlabSignals
	out.usesHeadPipeline = mr.HeadPipeline.ID != 0
	var group errgroup.Group
	group.SetLimit(observeParallelLimit)
	group.Go(func() error {
		out.threads, out.threadsErr = g.unresolvedThreads(ctx, mrIID)
		return out.threadsErr
	})
	group.Go(func() error {
		out.approvals, out.approvalsErr = g.approvals(ctx, mrIID, mr)
		return out.approvalsErr
	})
	group.Go(func() error {
		if !out.usesHeadPipeline {
			out.pipeline, out.pipelineErr = g.latestPipeline(ctx, url.Values{"sha": {sha}})
			return out.pipelineErr
		}
		out.pipeline.ID = mr.HeadPipeline.ID
		out.pipeline.Status = mr.HeadPipeline.Status
		out.jobs, out.jobsErr = g.pipelineJobs(ctx, out.pipeline.ID)
		return out.jobsErr
	})
	group.Go(func() error {
		out.behind, out.baseSHA, out.compareErr = g.behind(ctx, mr)
		return out.compareErr
	})
	_ = group.Wait()
	return out
}

func (g *GitLab) applyObservedChecks(ctx context.Context, o *core.Observation, s gitlabSignals, failed *observationErrors) {
	if !s.usesHeadPipeline {
		failed.add("pipelines", s.pipelineErr)
		if s.pipelineErr == nil && s.pipeline.ID != 0 {
			g.observeChecks(ctx, o, s.pipeline, failed)
		}
		return
	}
	failed.add("jobs", s.jobsErr)
	if s.jobsErr == nil {
		checks, excerpt := g.checksFromJobs(ctx, s.jobs)
		applyChecks(o, checks, excerpt, s.pipeline)
	}
}

const (
	gitlabPageSize = 100
	maxGitLabPages = 100
)

// getJSONPages walks GitLab's page-number API until a short page or the
// explicit cap. Callers use it for every list that can exceed one page, so a
// check or comment page is never silently dropped.
func getJSONPages[T any](g *GitLab, ctx context.Context, path string, query url.Values) ([]T, error) {
	q := url.Values{}
	for key, values := range query {
		q[key] = append([]string(nil), values...)
	}
	q.Set("per_page", strconv.Itoa(gitlabPageSize))
	var all []T
	for page := 1; page <= maxGitLabPages; page++ {
		q.Set("page", strconv.Itoa(page))
		var items []T
		if err := g.getJSON(ctx, path, q, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < gitlabPageSize {
			break
		}
	}
	return all, nil
}

func (g *GitLab) unresolvedThreads(ctx context.Context, mrIID int) ([]string, error) {
	path := g.mrPath(mrIID) + "/threads"
	threads, err := getJSONPages[glThread](g, ctx, path, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, thread := range threads {
		if thread.Resolved != nil && *thread.Resolved {
			continue
		}
		unresolved, last := false, ""
		for _, note := range thread.Notes {
			if note.Body != "" && !note.System {
				last = note.Body
			}
			if note.Resolvable && !note.Resolved {
				unresolved = true
			}
		}
		if unresolved {
			if last == "" {
				last = "unresolved discussion"
			}
			out = append(out, shared.Redact(gitlabTail(last, 1200)))
		}
	}
	return out, nil
}

func (g *GitLab) approvals(ctx context.Context, mrIID int, mr glMR) (int, error) {
	// The MR detail already carries approved_by. Calling /approvals when it is
	// present adds a round trip without adding information.
	if len(mr.ApprovedBy) > 0 {
		return len(mr.ApprovedBy), nil
	}
	path := g.mrPath(mrIID) + "/approvals"
	var result struct {
		ApprovedBy []glUser `json:"approved_by"`
	}
	if err := g.getJSON(ctx, path, nil, &result); err != nil {
		return 0, err
	}
	if len(result.ApprovedBy) == 0 {
		return len(mr.ApprovedBy), nil
	}
	seen := make(map[string]bool, len(result.ApprovedBy))
	for _, approver := range result.ApprovedBy {
		key := approver.User.Username
		if key == "" {
			key = strconv.Itoa(approver.ID)
		}
		seen[key] = true
	}
	return len(seen), nil
}

func (g *GitLab) behind(ctx context.Context, mr glMR) (bool, string, error) {
	from, to := mr.TargetBranch, mr.SHA
	if from == "" {
		from = mr.DiffRefs.BaseSHA
	}
	if to == "" {
		to = mr.DiffRefs.HeadSHA
	}
	if from == "" || to == "" {
		return false, "", nil
	}
	path := "/projects/" + g.project() + "/repository/compare"
	var cmp struct {
		BehindBy   int `json:"behind_by"`
		BaseCommit struct {
			ID string `json:"id"`
		} `json:"base_commit"`
	}
	query := url.Values{"from": {from}, "to": {to}}
	if err := g.getJSON(ctx, path, query, &cmp); err != nil {
		return false, "", err
	}
	return cmp.BehindBy > 0, cmp.BaseCommit.ID, nil
}

func (g *GitLab) UpdateBranch(ctx context.Context, mrIID int, expectedSHA string) (string, error) {
	path := g.mrPath(mrIID) + "/rebase"
	data, err := g.request(ctx, http.MethodPut, path, nil, map[string]string{"expected_head_sha": expectedSHA})
	if err != nil {
		return "", err
	}
	var rebased glMR
	if err := json.Unmarshal(data, &rebased); err != nil {
		return "", core.Errorf(core.CodeUnavailable, "rebase of MR %d returned an invalid response: %v", mrIID, err)
	}
	if rebased.SHA != "" && rebased.SHA != expectedSHA {
		return rebased.SHA, nil
	}
	timer := time.NewTimer(time.Second)
	timer.Stop()
	defer timer.Stop()
	for i := 0; i < 12; i++ {
		timer.Reset(time.Second)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
		var current glMR
		if err := g.getJSON(ctx, g.mrPath(mrIID), nil, &current); err != nil {
			return "", err
		}
		if current.SHA != "" && current.SHA != expectedSHA {
			return current.SHA, nil
		}
	}
	return "", core.Errorf(core.CodeUnavailable, "MR %d did not advance after rebase", mrIID)
}

func (g *GitLab) Merge(ctx context.Context, mrIID int, expectedSHA, title string) error {
	payload := map[string]any{"squash": true, "sha": expectedSHA}
	if title != "" {
		payload["commit_title"] = title
	}
	path := g.mrPath(mrIID) + "/merge"
	data, err := g.request(ctx, http.MethodPut, path, nil, payload)
	if err != nil {
		return err
	}
	var result struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return core.Errorf(core.CodeUnavailable, "merge of MR %d returned an invalid response: %v", mrIID, err)
	}
	if result.State != "merged" {
		return core.Errorf(core.CodeConflict, "merge of MR %d was not confirmed: state %q", mrIID, result.State)
	}
	return nil
}

func (g *GitLab) BaseBranchStatus(ctx context.Context, base string) (core.BaseStatus, error) {
	pipeline, err := g.latestPipeline(ctx, url.Values{"ref": {base}})
	if err != nil {
		return core.BaseStatus{}, err
	}
	status := core.BaseStatus{BaseBranch: base, HeadSHA: pipeline.SHA}
	if pipeline.ID == 0 {
		return status, nil
	}
	checks, excerpt, err := g.pipelineChecks(ctx, pipeline.ID)
	if err != nil {
		return status, err
	}
	if len(checks) == 0 {
		state := glCheckState(pipeline.Status, false)
		checks = []core.CheckResult{{Name: "pipeline", State: state}}
		if state == core.CheckFailure {
			excerpt = "pipeline " + pipeline.Status
		}
	}
	for _, check := range checks {
		if check.State == core.CheckFailure {
			status.FailedChecks = append(status.FailedChecks, check)
		}
	}
	status.FailureExcerpt = excerpt
	return status, nil
}

func (g *GitLab) FindOpenPRByBranch(ctx context.Context, branch string) (int, error) {
	path := "/projects/" + g.project() + "/merge_requests"
	query := url.Values{"state": {"opened"}, "source_branch": {branch}, "per_page": {"20"}}
	var mrs []glMR
	if err := g.getJSON(ctx, path, query, &mrs); err != nil {
		return 0, err
	}
	if len(mrs) == 0 {
		return 0, core.Errorf(core.CodeNotFound, "no open MR for branch %s", branch)
	}
	return mrs[0].IID, nil
}
