package tasksource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// GitLabIssues implements core.TaskSource on top of GitLab issue tracking.
type GitLabIssues struct {
	BaseURL string
	Token   string
	Owner   string
	Repo    string
	client  *http.Client
}

type glIssue struct {
	IID         int      `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      glLabels `json:"labels"`
	WebURL      string   `json:"web_url"`
}

type glLabels []string

func (l *glLabels) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, item := range raw {
		var name string
		if json.Unmarshal(item, &name) == nil {
			*l = append(*l, name)
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(item, &obj) == nil && obj.Name != "" {
			*l = append(*l, obj.Name)
		}
	}
	return nil
}

func NewGitLabIssues(baseURL, token, owner, repo string) *GitLabIssues {
	return &GitLabIssues{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		Owner:   owner,
		Repo:    repo,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

var _ core.TaskSource = (*GitLabIssues)(nil)

func (g *GitLabIssues) project() string {
	return url.PathEscape(g.Owner + "/" + g.Repo)
}

func (g *GitLabIssues) request(ctx context.Context, method, path string, query url.Values, payload any) ([]byte, error) {
	u := g.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := core.CodeUnavailable
		if resp.StatusCode == http.StatusNotFound {
			code = core.CodeNotFound
		}
		return nil, core.Errorf(code, "gitlab %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (g *GitLabIssues) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	data, err := g.request(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	if out == nil || len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.Unmarshal(data, out)
}

const gitlabIssuesPerPage = 100

// paginateIssuePages walks issue pages until a page comes back empty or the
// shared page cap is reached. The callback only receives a 1-based page
// number, so the loop does not depend on one tracker's response types.
func paginateIssuePages[T any](fetch func(page int) ([]T, error)) ([]T, error) {
	var all []T
	for page := 1; page <= maxIssuePages; page++ {
		items, err := fetch(page)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		all = append(all, items...)
	}
	return all, nil
}

func (g *GitLabIssues) ListAvailable(ctx context.Context) ([]core.Task, error) {
	path := "/projects/" + g.project() + "/issues"
	issues, err := paginateIssuePages(func(page int) ([]glIssue, error) {
		query := url.Values{
			"state":    {"opened"},
			"per_page": {strconv.Itoa(gitlabIssuesPerPage)},
			"page":     {strconv.Itoa(page)},
		}
		var pageIssues []glIssue
		if err := g.getJSON(ctx, path, query, &pageIssues); err != nil {
			return nil, err
		}
		return pageIssues, nil
	})
	if err != nil {
		return nil, err
	}
	var out []core.Task
	for _, issue := range issues {
		rec := glIssueRecord(issue)
		if !issueAvailable(rec) {
			continue
		}
		out = append(out, parseIssueRecord(rec, "GL"))
	}
	core.SortTasks(out)
	return out, nil
}

func (g *GitLabIssues) Claim(ctx context.Context, id core.TaskID) error {
	iid, err := issueNumber(id, "GL-")
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/projects/%s/issues/%d/labels", g.project(), iid)
	_, err = g.request(ctx, http.MethodPost, path, nil, map[string]string{"labels": "beacon/claimed"})
	return err
}

func (g *GitLabIssues) Complete(ctx context.Context, id core.TaskID) error {
	iid, err := issueNumber(id, "GL-")
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/projects/%s/issues/%d", g.project(), iid)
	_, err = g.request(ctx, http.MethodPut, path, nil, map[string]string{"state_event": "close"})
	return err
}

// MarkInWorkspace is a no-op: GitLab issues are updated through the API.
func (g *GitLabIssues) MarkInWorkspace(context.Context, core.TaskID, string, string) error {
	return nil
}

// Create opens a GitLab issue. The issue IID becomes the task ID so the Claim
// and Complete calls can address it.
func (g *GitLabIssues) Create(ctx context.Context, task core.Task) (core.Task, error) {
	body, err := issueBody(task)
	if err != nil {
		return core.Task{}, err
	}
	data, err := g.request(ctx, http.MethodPost, "/projects/"+g.project()+"/issues", nil, map[string]string{
		"title":       task.Title,
		"description": body,
	})
	if err != nil {
		return core.Task{}, err
	}
	var issue glIssue
	if err := json.Unmarshal(data, &issue); err != nil {
		return core.Task{}, err
	}
	return parseIssueRecord(glIssueRecord(issue), "GL"), nil
}

// glIssueRecord adapts a GitLab issue to the shared parser input.
func glIssueRecord(issue glIssue) issueRecord {
	return issueRecord{
		Number:    issue.IID,
		Title:     issue.Title,
		Body:      issue.Description,
		Labels:    []string(issue.Labels),
		SourceRef: issue.WebURL,
	}
}
