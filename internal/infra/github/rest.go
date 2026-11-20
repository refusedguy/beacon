// Package github holds the GitHub adapter for Beacon.
//
// This file implements the REST half of the hybrid adapter (ADR 0004). The
// client is read-only by construction: it exposes conditional GETs and never
// builds a write request. ETags live only in memory, and every response is
// reported to the shared point counter (ADR 0013).
package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v93/github"
)

const (
	restTimeout     = 30 * time.Second
	restUserAgent   = "beacon"
	restRequestCost = 1
)

// tokenEnvChain is the token lookup order from WORKER-PROMPT §2.
var tokenEnvChain = [...]string{"BEACON_GITHUB_TOKEN", "GITHUB_TOKEN"}

// ResolveToken returns a read token from the chain BEACON_GITHUB_TOKEN,
// GITHUB_TOKEN, then `gh auth token`. The token is returned to the caller and
// never written to disk or included in an error message. The fallback command
// is run without stderr capture, so a failing gh cannot put a credential into
// the returned error.
func ResolveToken(ctx context.Context) (string, error) {
	for _, name := range tokenEnvChain {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token, nil
		}
	}

	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("GitHub token: BEACON_GITHUB_TOKEN and GITHUB_TOKEN are empty and gh auth token failed")
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("GitHub token: gh auth token returned an empty token")
	}
	return token, nil
}

// RestClient is the read-only GitHub REST client. It keeps one ETag per REST
// path so a repeated GET can be sent as a conditional request.
type RestClient struct {
	gh      *gh.Client
	counter *Counter

	mu    sync.Mutex
	etags map[string]string
}

// NewRestClient resolves the token and builds the REST client. The token is
// held by go-github and attached only to requests to the configured API
// origin. It is not copied onto RestClient and is never persisted.
func NewRestClient(ctx context.Context, counter *Counter) (*RestClient, error) {
	token, err := ResolveToken(ctx)
	if err != nil {
		return nil, err
	}
	client, err := gh.NewClient(
		gh.WithAuthToken(token),
		gh.WithTimeout(restTimeout),
		gh.WithUserAgent(restUserAgent),
	)
	if err != nil {
		return nil, fmt.Errorf("github REST client: %w", err)
	}
	return newRestClient(client, counter), nil
}

func newRestClient(client *gh.Client, counter *Counter) *RestClient {
	return &RestClient{
		gh:      client,
		counter: counter,
		etags:   make(map[string]string),
	}
}

// GetConditional performs a conditional GET on a REST path such as
// "repos/owner/repo/pulls/42". The 200 body is decoded into out. The boolean
// is true for a 304, in which case out is left untouched and no point is
// charged. A 200 stores the response ETag and charges one point.
func (c *RestClient) GetConditional(ctx context.Context, path string, out any) (bool, error) {
	c.mu.Lock()
	etag := c.etags[path]
	c.mu.Unlock()

	req, err := c.gh.NewRequest(ctx, http.MethodGet, path, nil, func(r *http.Request) {
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
	})
	if err != nil {
		return false, fmt.Errorf("REST GET %s: %w", path, err)
	}

	// go-github turns 304 into an *ErrorResponse. The response is still
	// returned, so check the status before the error.
	resp, err := c.gh.Do(req, out)
	if resp == nil {
		if err == nil {
			err = errors.New("no response")
		}
		return false, fmt.Errorf("REST GET %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusNotModified {
		if c.counter != nil {
			// Cost zero: a 304 does not spend a primary point. Headers, when
			// present, still refresh the counter's rate-limit view.
			c.counter.Observe(resp.Response, 0)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("REST GET %s: status %d: %w", path, resp.StatusCode, err)
	}
	if c.counter != nil {
		c.counter.Observe(resp.Response, restRequestCost)
	}
	c.setETag(path, resp.Header.Get("ETag"))
	return false, nil
}

func (c *RestClient) setETag(path, etag string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if etag == "" {
		delete(c.etags, path)
		return
	}
	c.etags[path] = etag
}

// GetUnconditional performs a plain GET on a REST path, ignoring any cached
// ETag. The server cannot answer 304 without If-None-Match, so a successful
// call always decodes a fresh body into out. It is used to resolve a lazy field
// such as mergeable when a conditional GET returned 304 but the caller still
// lacks a concrete value. The response ETag refreshes the cache so later
// conditional reads stay cheap.
func (c *RestClient) GetUnconditional(ctx context.Context, path string, out any) error {
	req, err := c.gh.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("REST GET %s: %w", path, err)
	}
	resp, err := c.gh.Do(req, out)
	if resp == nil {
		if err == nil {
			err = errors.New("no response")
		}
		return fmt.Errorf("REST GET %s: %w", path, err)
	}
	if c.counter != nil {
		c.counter.Observe(resp.Response, restRequestCost)
	}
	if err != nil {
		return fmt.Errorf("REST GET %s: status %d: %w", path, resp.StatusCode, err)
	}
	c.setETag(path, resp.Header.Get("ETag"))
	return nil
}

// PingMergeableForce reads /repos/{owner}/{repo}/pulls/{number} without a
// conditional header. Use it when PingMergeable returned notModified=true but
// the caller still needs a concrete mergeable value: GitHub computes that
// field lazily and a cached ETag can hide the UNKNOWN -> MERGEABLE transition.
func (c *RestClient) PingMergeableForce(ctx context.Context, owner, repo string, number int) (MergeState, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number)
	var pr gh.PullRequest
	if err := c.GetUnconditional(ctx, path, &pr); err != nil {
		return MergeState{}, err
	}
	return MergeState{
		Mergeable:      pr.Mergeable,
		MergeableState: pr.GetMergeableState(),
	}, nil
}

// MergeState is the mergeability snapshot read by a ping.
type MergeState struct {
	Mergeable      *bool
	MergeableState string
}

// Unknown reports whether GitHub has not computed mergeability yet.
func (s MergeState) Unknown() bool {
	return s.Mergeable == nil || strings.EqualFold(s.MergeableState, "unknown")
}

// PingMergeable forces GitHub to compute the lazy mergeable field by issuing
// GET /repos/{owner}/{repo}/pulls/{number}. Call it when GraphQL reports
// mergeable=UNKNOWN. A 304 returns notModified=true with an empty snapshot;
// the caller keeps its previous state in that case.
func (c *RestClient) PingMergeable(ctx context.Context, owner, repo string, number int) (MergeState, bool, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number)
	var pr gh.PullRequest
	notModified, err := c.GetConditional(ctx, path, &pr)
	if err != nil || notModified {
		return MergeState{}, notModified, err
	}
	return MergeState{
		Mergeable:      pr.Mergeable,
		MergeableState: pr.GetMergeableState(),
	}, false, nil
}

// MergeResult is the outcome of a merge attempt through the REST API. A refused
// merge is not a transport failure: Reason is set and err is nil, so the caller
// can hold the Watch instead of retrying blindly.
type MergeResult struct {
	Merged bool
	Reason string
	SHA    string
}

// MergePullRequest sends PUT /repos/{owner}/{repo}/pulls/{number}/merge with an
// explicit method. owner and repo always come from the Watch, never from the
// process working directory. A 405 (not mergeable) or 409 (conflict) maps to a
// held result; other statuses are returned as errors.
func (c *RestClient) MergePullRequest(ctx context.Context, owner, repo string, number int, method string) (MergeResult, error) {
	if owner == "" || repo == "" {
		return MergeResult{}, fmt.Errorf("merge: owner and repo are required, got %q/%q", owner, repo)
	}
	if method == "" {
		method = "squash"
	}
	result, resp, err := c.gh.PullRequests.Merge(ctx, owner, repo, number, "", &gh.PullRequestOptions{MergeMethod: method})
	if resp != nil && c.counter != nil {
		c.counter.Observe(resp.Response, restRequestCost)
	}
	if err != nil {
		var ghErr *gh.ErrorResponse
		if errors.As(err, &ghErr) && ghErr.Response != nil &&
			(ghErr.Response.StatusCode == http.StatusMethodNotAllowed ||
				ghErr.Response.StatusCode == http.StatusConflict) {
			return MergeResult{Reason: "merge_refused"}, nil
		}
		return MergeResult{}, fmt.Errorf("REST merge %s/%s#%d: %w", owner, repo, number, err)
	}
	if result == nil || !result.GetMerged() {
		return MergeResult{Reason: "merge_not_confirmed"}, nil
	}
	return MergeResult{Merged: true, SHA: result.GetSHA()}, nil
}
