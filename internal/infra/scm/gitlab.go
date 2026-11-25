package scm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/refusedguy/beacon/pkg/core"
)

// GitLab implements core.ScmGateway against the GitLab REST v4 API.
type GitLab struct {
	baseURL string
	token   string
	owner   string
	repo    string
	client  *http.Client
	br      *gobreaker.CircuitBreaker[any]
}

// NewGitLab builds a GitLab gateway for owner and repo.
func NewGitLab(baseURL, token, owner, repo string) *GitLab {
	return &GitLab{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		owner:   owner,
		repo:    repo,
		client:  &http.Client{Timeout: 30 * time.Second},
		br:      newSCMBreaker("gitlab"),
	}
}

var _ core.ScmGateway = (*GitLab)(nil)

// project returns the URL-escaped GitLab project path.
func (g *GitLab) project() string {
	return url.PathEscape(g.owner + "/" + g.repo)
}

// request sends an authenticated GitLab API request and returns its body.
func (g *GitLab) request(ctx context.Context, method, path string, query url.Values, payload any) ([]byte, error) {
	u := g.baseURL + path
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
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return guarded(g.br, func() ([]byte, error) {
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
			return nil, core.Errorf(gitlabErrorCode(resp.StatusCode), "gitlab %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
		}
		return data, nil
	})
}

// getJSON decodes a GitLab GET response into out.
func (g *GitLab) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	data, err := g.request(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	if out == nil || len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.Unmarshal(data, out)
}

// gitlabErrorCode maps an HTTP status to a core error code.
func gitlabErrorCode(status int) core.ErrorCode {
	switch status {
	case http.StatusNotFound:
		return core.CodeNotFound
	case http.StatusConflict, http.StatusPreconditionFailed, http.StatusUnprocessableEntity:
		return core.CodeConflict
	default:
		return core.CodeUnavailable
	}
}

// mrPath returns the API path for merge request iid.
func (g *GitLab) mrPath(iid int) string {
	return fmt.Sprintf("/projects/%s/merge_requests/%d", g.project(), iid)
}
