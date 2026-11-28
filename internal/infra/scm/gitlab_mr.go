package scm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/refusedguy/beacon/pkg/core"
	"github.com/refusedguy/beacon/pkg/core/redact"
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
	ID       int      `json:"id"`
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
	if signals.threadsCapped {
		failed.add("threads", errCappedRead)
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
		// A provider conflict with no commits ahead of the base is stale.
		// Keep the cross-checked value in the observation so downstream
		// decisions see the same conflict state that was verified.
		if o.Conflict && signals.ahead == 0 {
			o.Conflict = false
		}
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
	ahead            int
	baseSHA          string
	threadsCapped    bool
	jobsCapped       bool
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
		out.threads, out.threadsCapped, out.threadsErr = g.unresolvedThreads(ctx, mrIID)
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
		out.jobs, out.jobsCapped, out.jobsErr = g.pipelineJobsCapped(ctx, out.pipeline.ID)
		return out.jobsErr
	})
	group.Go(func() error {
		out.behind, out.ahead, out.baseSHA, out.compareErr = g.behind(ctx, mr)
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
	if s.jobsCapped {
		failed.add("jobs", errCappedRead)
	}
}

const (
	gitlabPageSize = 100
	maxGitLabPages = 100
)

// glPage carries one GitLab page through the breaker, which stores any. The
// raw X-Next-Page values travel with the body so the walk can tell an explicit
// end of pagination from a header the provider did not send.
type glPage struct {
	body     []byte
	nextPage []string
}

// getJSONPage fetches one GitLab page into out and returns the X-Next-Page
// header values. It mirrors getJSON's request rules and keeps the breaker in
// the path, so a paginated read cannot bypass it.
func (g *GitLab) getJSONPage(ctx context.Context, path string, query url.Values, out any) ([]string, error) {
	u := g.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	page, err := guarded(g.br, func() (glPage, error) {
		resp, err := g.client.Do(req)
		if err != nil {
			return glPage{}, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return glPage{}, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return glPage{}, core.Errorf(gitlabErrorCode(resp.StatusCode), "gitlab GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
		}
		return glPage{body: data, nextPage: append([]string(nil), resp.Header.Values("X-Next-Page")...)}, nil
	})
	if err != nil {
		return nil, err
	}
	if out != nil && len(page.body) > 0 && string(page.body) != "null" {
		if err := json.Unmarshal(page.body, out); err != nil {
			return nil, err
		}
	}
	return page.nextPage, nil
}

// noNextPage reports an explicit end of pagination. GitLab sends X-Next-Page
// as an empty value on the last page, including one that is exactly full. An
// absent header leaves the short-page rule in charge for providers that do not
// send pagination headers.
func noNextPage(values []string) bool {
	return len(values) > 0 && strings.TrimSpace(values[0]) == ""
}

// getJSONPages walks GitLab's page-number API until a short page, an explicit
// no-next-page header, or the cap. It reports capped reads so callers can mark
// an observation incomplete instead of deciding from a partial list. When id
// is non-nil, repeated provider IDs from pages that shifted under concurrent
// writes are collapsed.
func getJSONPages[T any](g *GitLab, ctx context.Context, path string, query url.Values, id func(T) int64) ([]T, bool, error) {
	q := url.Values{}
	for key, values := range query {
		q[key] = append([]string(nil), values...)
	}
	q.Set("per_page", strconv.Itoa(gitlabPageSize))
	var all []T
	seen := make(map[int64]bool)
	for page := 1; page <= maxGitLabPages; page++ {
		q.Set("page", strconv.Itoa(page))
		var items []T
		nextPage, err := g.getJSONPage(ctx, path, q, &items)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if id != nil {
				key := id(item)
				if key != 0 {
					if seen[key] {
						continue
					}
					seen[key] = true
				}
			}
			all = append(all, item)
		}
		if noNextPage(nextPage) || len(items) < gitlabPageSize {
			return all, false, nil
		}
	}
	return all, true, nil
}

func (g *GitLab) unresolvedThreads(ctx context.Context, mrIID int) ([]string, bool, error) {
	path := g.mrPath(mrIID) + "/threads"
	threads, capped, err := getJSONPages[glThread](g, ctx, path, nil, func(t glThread) int64 { return int64(t.ID) })
	if err != nil {
		return nil, capped, err
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
			out = append(out, redact.Redact(gitlabTail(last, 1200)))
		}
	}
	return out, capped, nil
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

func (g *GitLab) behind(ctx context.Context, mr glMR) (bool, int, string, error) {
	// Compare from the base commit carried by the MR. A branch name cannot be
	// resolved once the base branch is deleted, while the commit remains.
	from, to := mr.DiffRefs.BaseSHA, mr.SHA
	if from == "" {
		from = mr.TargetBranch
	}
	if to == "" {
		to = mr.DiffRefs.HeadSHA
	}
	if from == "" || to == "" {
		return false, 0, "", nil
	}
	path := "/projects/" + g.project() + "/repository/compare"
	var cmp struct {
		AheadBy    int `json:"ahead_by"`
		BehindBy   int `json:"behind_by"`
		BaseCommit struct {
			ID string `json:"id"`
		} `json:"base_commit"`
	}
	query := url.Values{"from": {from}, "to": {to}}
	if err := g.getJSON(ctx, path, query, &cmp); err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			return false, 0, "", core.Errorf(core.CodeNotFound, "base commit %s is unavailable", from)
		}
		return false, 0, "", err
	}
	return cmp.BehindBy > 0, cmp.AheadBy, cmp.BaseCommit.ID, nil
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
