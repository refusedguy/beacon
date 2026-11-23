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

func (g *GitLabIssues) ListAvailable(ctx context.Context) ([]core.Task, error) {
	path := "/projects/" + g.project() + "/issues"
	var issues []glIssue
	query := url.Values{"state": {"opened"}, "per_page": {"100"}}
	if err := g.getJSON(ctx, path, query, &issues); err != nil {
		return nil, err
	}
	var out []core.Task
	for _, issue := range issues {
		if glHasLabel(issue.Labels, "beacon/claimed") {
			continue
		}
		task := glParseIssue(issue)
		out = append(out, task)
	}
	core.SortTasks(out)
	return out, nil
}

func (g *GitLabIssues) Claim(ctx context.Context, id core.TaskID) error {
	iid, err := glIssueNumber(id)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/projects/%s/issues/%d/labels", g.project(), iid)
	_, err = g.request(ctx, http.MethodPost, path, nil, map[string]string{"labels": "beacon/claimed"})
	return err
}

func (g *GitLabIssues) Complete(ctx context.Context, id core.TaskID) error {
	iid, err := glIssueNumber(id)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/projects/%s/issues/%d", g.project(), iid)
	_, err = g.request(ctx, http.MethodPut, path, nil, map[string]string{"state_event": "close"})
	return err
}

func glIssueNumber(id core.TaskID) (int, error) {
	s := strings.TrimPrefix(string(id), "GL-")
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, core.Errorf(core.CodeInvalidInput, "issue id %q", id)
	}
	return n, nil
}

func glHasLabel(labels glLabels, want string) bool {
	for _, label := range labels {
		if strings.EqualFold(label, want) {
			return true
		}
	}
	return false
}

func glParseIssue(issue glIssue) core.Task {
	fields := ParseTaskFields(issue.Description, "", "GL")
	return core.Task{
		ID:        core.TaskID(firstNonEmpty("GL-"+strconv.Itoa(issue.IID), fields.ID)),
		Title:     issue.Title,
		Body:      issue.Description,
		Status:    core.TaskAvailable,
		Priority:  fields.Priority,
		Scope:     fields.Scopes(""),
		DependsOn: fields.Dependencies(),
		SourceRef: issue.WebURL,
	}
}
