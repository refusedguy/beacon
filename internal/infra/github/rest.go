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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	gh "github.com/google/go-github/v93/github"
)

const (
	restTimeout     = 30 * time.Second
	restUserAgent   = "beacon"
	restRequestCost = 1

	// failureExcerptAnnotations bounds the annotations read per failed check.
	failureExcerptAnnotations = 8
	// failureExcerptLimit is the 2 KB ceiling applied by this adapter as well
	// as by the MCP boundary, so a caller cannot receive a huge blob.
	failureExcerptLimit = 2048
	// failureExcerptSourcePadding is the number of source lines kept on each
	// side of an annotated line. aider uses loi_pad=3 in its tree context
	// (aider/linter.py:234-256), so the agent sees the same window.
	failureExcerptSourcePadding = 3
	// failureExcerptSourceMaxFile caps the bytes read from one source file. A
	// failed annotation must not turn into a multi-megabyte read.
	failureExcerptSourceMaxFile = 256 << 10
	// excerptSignalMarkers are case-insensitive substrings that mark a line as
	// carrying diagnostic signal. When a long message is reduced, these lines
	// are kept before ordinary context so an error at the end of a log is not
	// lost to tail truncation (CA-22). The █ entry keeps a marked source line.
	excerptSignalMarkers = [...]string{
		"panic", "fatal", "error", "failure", "failed", "exception",
		"sigsegv", "stack overflow", "█",
	}
	// excerptContextLineLimit caps one non-signal line while compressing a
	// long message, so a single verbose line cannot consume the byte budget.
	excerptContextLineLimit = 160
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
		HeadSHA:        pr.GetHead().GetSHA(),
	}, nil
}

// MergeState is the mergeability snapshot read by a ping. HeadSHA is the head
// observed in the same response, so a caller can pin a merge to what it checked.
type MergeState struct {
	Mergeable      *bool
	MergeableState string
	HeadSHA        string
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
		HeadSHA:        pr.GetHead().GetSHA(),
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

// MergeHeldHeadMismatch is the held reason when GitHub rejects a merge because
// the head changed after the verdict was computed. The Watch needs a fresh CI
// cycle, not another merge attempt against the new head.
const MergeHeldHeadMismatch = "head_mismatch"

// MergePullRequest sends PUT /repos/{owner}/{repo}/pulls/{number}/merge with an
// explicit method. owner and repo always come from the Watch, never from the
// process working directory. expectedSHA is the head the verdict checked;
// go-github puts it in the request body so GitHub refuses the merge when the
// head moved in the meantime. Callers pass it whenever they hold a verified
// head. The variadic form keeps existing callers that omit it compiling.
// A 405 maps to merge_refused. A 409 or 422 maps to MergeHeldHeadMismatch when
// an expected SHA was supplied, and to merge_refused for a bare 409. Other
// statuses are returned as errors.
func (c *RestClient) MergePullRequest(ctx context.Context, owner, repo string, number int, method string, expectedSHA ...string) (MergeResult, error) {
	if owner == "" || repo == "" {
		return MergeResult{}, fmt.Errorf("merge: owner and repo are required, got %q/%q", owner, repo)
	}
	if method == "" {
		method = "squash"
	}
	opts := &gh.PullRequestOptions{MergeMethod: method}
	if len(expectedSHA) > 0 {
		opts.SHA = expectedSHA[0]
	}
	result, resp, err := c.gh.PullRequests.Merge(ctx, owner, repo, number, "", opts)
	if resp != nil && c.counter != nil {
		c.counter.Observe(resp.Response, restRequestCost)
	}
	if err != nil {
		var ghErr *gh.ErrorResponse
		if errors.As(err, &ghErr) && ghErr.Response != nil {
			status := ghErr.Response.StatusCode
			if status == http.StatusMethodNotAllowed || status == http.StatusConflict {
				if status == http.StatusConflict && opts.SHA != "" {
					return MergeResult{Reason: MergeHeldHeadMismatch}, nil
				}
				return MergeResult{Reason: "merge_refused"}, nil
			}
			if status == http.StatusUnprocessableEntity && opts.SHA != "" {
				return MergeResult{Reason: MergeHeldHeadMismatch}, nil
			}
		}
		return MergeResult{}, fmt.Errorf("REST merge %s/%s#%d: %w", owner, repo, number, err)
	}
	if result == nil || !result.GetMerged() {
		return MergeResult{Reason: "merge_not_confirmed"}, nil
	}
	return MergeResult{Merged: true, SHA: result.GetSHA()}, nil
}

// RebasePullRequest updates a PR branch onto its base and returns the new head
// SHA. It sends PUT /repos/{owner}/{repo}/pulls/{number}/update-branch through
// go-github, then re-reads the PR without a conditional header so a cached ETag
// cannot hide the new head. GitHub answers the PUT with 202 while it schedules
// the update in the background, so an AcceptedError counts as success.
func (c *RestClient) RebasePullRequest(ctx context.Context, owner, repo string, number int) (string, error) {
	if owner == "" || repo == "" {
		return "", fmt.Errorf("rebase: owner and repo are required, got %q/%q", owner, repo)
	}
	_, resp, err := c.gh.PullRequests.UpdateBranch(ctx, owner, repo, number, gh.UpdatePullRequestBranchRequest{})
	if resp != nil && c.counter != nil {
		c.counter.Observe(resp.Response, restRequestCost)
	}
	if err != nil {
		var accepted *gh.AcceptedError
		if !errors.As(err, &accepted) {
			return "", fmt.Errorf("REST rebase %s/%s#%d: %w", owner, repo, number, err)
		}
	}

	path := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number)
	var pr gh.PullRequest
	if err := c.GetUnconditional(ctx, path, &pr); err != nil {
		return "", fmt.Errorf("REST rebase reread %s/%s#%d: %w", owner, repo, number, err)
	}
	sha := pr.GetHead().GetSHA()
	if sha == "" {
		return "", fmt.Errorf("REST rebase %s/%s#%d: PR has no head SHA", owner, repo, number)
	}
	return sha, nil
}

// FailureExcerpt is the W11-B read path. It finds the failed CheckRun for the
// Watch's head SHA by name, then reads its annotations and formats them into a
// short text block. The MCP boundary applies Redact and escapeUntrusted and
// enforces the 2 KB ceiling, so this method returns raw CI output.
//
// A repo without an owner/name pair, or a head SHA with no matching failed
// run, yields an empty excerpt and a nil error: no annotations is a normal
// outcome, not a transport failure.
func (c *RestClient) FailureExcerpt(ctx context.Context, repo, headSHA, checkName string) (string, error) {
	if c == nil || c.gh == nil {
		return "", nil
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || headSHA == "" || checkName == "" {
		return "", nil
	}
	status := "completed"
	opts := &gh.ListCheckRunsOptions{
		CheckName:   &checkName,
		Status:      &status,
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	runs, resp, err := c.gh.Checks.ListCheckRunsForRef(ctx, owner, name, headSHA, opts)
	if resp != nil && c.counter != nil {
		c.counter.Observe(resp.Response, restRequestCost)
	}
	if err != nil {
		return "", fmt.Errorf("check-runs %s@%s: %w", repo, headSHA, err)
	}
	var id int64
	if runs != nil {
		for _, run := range runs.CheckRuns {
			if run == nil || !failedCheckRunConclusion(run.GetConclusion()) {
				continue
			}
			id = run.GetID()
			break
		}
	}
	if id == 0 {
		return "", nil
	}
	annotations, resp, err := c.gh.Checks.ListCheckRunAnnotations(ctx, owner, name, id,
		&gh.ListOptions{PerPage: failureExcerptAnnotations})
	if resp != nil && c.counter != nil {
		c.counter.Observe(resp.Response, restRequestCost)
	}
	if err != nil {
		return "", fmt.Errorf("check-run %d annotations: %w", id, err)
	}
	return formatCheckRunAnnotations(annotations), nil
}

func failedCheckRunConclusion(conclusion string) bool {
	switch strings.ToUpper(conclusion) {
	case "FAILURE", "ERROR", "TIMED_OUT", "STARTUP_FAILURE", "CANCELLED", "CANCELED":
		return true
	}
	return false
}

// annotationSourceRoot returns the directory annotation paths are resolved
// against. Beacon runs inside a repository checkout, so the working directory
// is the default. BEACON_EXCERPT_SOURCE_ROOT can point at another checkout.
func annotationSourceRoot() string {
	if root := strings.TrimSpace(os.Getenv("BEACON_EXCERPT_SOURCE_ROOT")); root != "" {
		return filepath.Clean(root)
	}
	return "."
}

// loadAnnotationSource reads the checked-out file named by a CI annotation.
// Annotation paths are external input: an absolute path and any path that
// escapes the source root are rejected. A missing or unreadable file yields an
// empty string, so a source miss never turns into a failed excerpt.
func loadAnnotationSource(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return ""
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ""
	}
	f, err := os.Open(filepath.Join(annotationSourceRoot(), clean))
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, failureExcerptSourceMaxFile))
	if err != nil {
		return ""
	}
	return string(b)
}

// FormatAnnotationSource renders the source window for one annotation. The
// target line carries a █ marker and the three lines on each side are numbered.
// Passing source avoids a disk read for callers that already have the file text.
// An empty source or a line number outside the file yields no window.
func FormatAnnotationSource(path string, startLine int, source string) string {
	path = strings.TrimSpace(path)
	if path == "" || startLine <= 0 {
		return ""
	}
	if source == "" {
		source = loadAnnotationSource(path)
	}
	if source == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	if startLine > len(lines) {
		return ""
	}
	first := startLine - failureExcerptSourcePadding
	if first < 1 {
		first = 1
	}
	last := startLine + failureExcerptSourcePadding
	if last > len(lines) {
		last = len(lines)
	}
	width := len(strconv.Itoa(last))
	var b strings.Builder
	b.WriteString("# Fix any errors below, the annotated line is marked with █.\n")
	b.WriteString(path)
	b.WriteString(":\n")
	for n := first; n <= last; n++ {
		marker := " "
		if n == startLine {
			marker = "█"
		}
		digits := strconv.Itoa(n)
		b.WriteString(marker)
		b.WriteByte(' ')
		b.WriteString(strings.Repeat(" ", width-len(digits)))
		b.WriteString(digits)
		b.WriteString(" │ ")
		b.WriteString(lines[n-1])
		if n < last {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// clampExcerptBytes cuts a string at the 2 KB ceiling without splitting a
// UTF-8 rune. The █ marker is multibyte, so a plain byte slice can corrupt it.
func clampExcerptBytes(s string) string {
	if len(s) <= failureExcerptLimit {
		return s
	}
	s = s[:failureExcerptLimit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// isExcerptSignal reports whether a line carries an error or panic marker.
// Matching is case-insensitive so a CI log in any case keeps its error lines.
func isExcerptSignal(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range excerptSignalMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// compressExcerptText reduces a long annotation message to the 2 KB ceiling.
// Signal lines are kept first, most recent first, then context lines follow in
// their original order with each one capped in length. The result is clamped on
// a rune boundary so the █ marker is never split. Short messages pass through
// unchanged, which preserves the existing wire form for normal CI output.
func compressExcerptText(s string) string {
	if len(s) <= failureExcerptLimit {
		return s
	}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var signal, context []string
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue
		}
		if isExcerptSignal(line) {
			signal = append(signal, line)
			continue
		}
		if len(line) > excerptContextLineLimit {
			line = line[:excerptContextLineLimit]
		}
		context = append(context, line)
	}
	var b strings.Builder
	write := func(line string) {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	// Walk signal lines from the end: the failure case is an error near the
	// tail of a long log, and it must survive before earlier error mentions.
	for i := len(signal) - 1; i >= 0; i-- {
		if b.Len() >= failureExcerptLimit {
			break
		}
		write(signal[i])
	}
	for _, line := range context {
		if b.Len() >= failureExcerptLimit {
			break
		}
		write(line)
	}
	return clampExcerptBytes(b.String())
}

// formatCheckRunAnnotations renders "path:line: title: message" lines followed
// by the marked source window when the annotated file is available. Empty
// fields are skipped. When the output exceeds the byte budget, blocks carrying
// an error or panic marker are emitted first so an error in a late annotation
// survives. The byte budget is enforced here as a second line of defence behind
// the MCP boundary.
func formatCheckRunAnnotations(annotations []*gh.CheckRunAnnotation) string {
	type excerptBlock struct {
		text   string
		signal bool
	}
	var blocks []excerptBlock
	for i, a := range annotations {
		if i >= failureExcerptAnnotations || a == nil {
			continue
		}
		message := strings.TrimSpace(a.GetMessage())
		title := strings.TrimSpace(a.GetTitle())
		if message == "" && title == "" {
			continue
		}
		path := strings.TrimSpace(a.GetPath())
		line := int(a.GetStartLine())
		var head strings.Builder
		if path != "" {
			head.WriteString(path)
			if line > 0 {
				head.WriteByte(':')
				head.WriteString(strconv.Itoa(line))
			}
			head.WriteString(": ")
		}
		if title != "" {
			head.WriteString(title)
			if message != "" {
				head.WriteString(": ")
			}
		}
		head.WriteString(compressExcerptText(message))
		header := head.String()
		signal := isExcerptSignal(header)
		var block strings.Builder
		block.WriteString(header)
		if source := FormatAnnotationSource(path, line, ""); source != "" {
			block.WriteByte('\n')
			block.WriteString(source)
			signal = signal || isExcerptSignal(source)
		}
		blocks = append(blocks, excerptBlock{text: block.String(), signal: signal})
	}

	// Keep the original order for a short excerpt. Only a reduced excerpt is
	// reordered, so the common case is byte-for-byte the old output.
	total := 0
	for i, bl := range blocks {
		if i > 0 {
			total++
		}
		total += len(bl.text)
	}
	if total <= failureExcerptLimit {
		var out strings.Builder
		for i, bl := range blocks {
			if i > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(bl.text)
		}
		return clampExcerptBytes(out.String())
	}

	ordered := make([]excerptBlock, 0, len(blocks))
	for _, bl := range blocks {
		if bl.signal {
			ordered = append(ordered, bl)
		}
	}
	for _, bl := range blocks {
		if !bl.signal {
			ordered = append(ordered, bl)
		}
	}
	var out strings.Builder
	for _, bl := range ordered {
		if out.Len() >= failureExcerptLimit {
			break
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(bl.text)
	}
	return clampExcerptBytes(out.String())
}
