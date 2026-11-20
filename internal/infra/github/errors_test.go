package github

import (
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// testNormalizer fixes the clock and jitter so retryAfterMs values are exact.
func testNormalizer(now time.Time, jitter time.Duration) *Normalizer {
	return &Normalizer{
		now:    func() time.Time { return now },
		jitter: func() time.Duration { return jitter },
	}
}

func header(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestNormalizeRateLimitSources(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	jitter := 250 * time.Millisecond
	n := testNormalizer(now, jitter)

	resetIn := func(d time.Duration) string {
		return strconv.FormatInt(now.Add(d).Unix(), 10)
	}

	cases := []struct {
		name           string
		status         int
		header         http.Header
		body           string
		wantRetryAfter int64
	}{
		{
			name:           "graphql 200 with errors and remaining 0",
			status:         http.StatusOK,
			header:         header("x-ratelimit-remaining", "0", "x-ratelimit-reset", resetIn(60*time.Second)),
			body:           `{"errors":[{"message":"API rate limit exceeded"}]}`,
			wantRetryAfter: (60 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "REST 429 with retry-after",
			status:         http.StatusTooManyRequests,
			header:         header("retry-after", "30"),
			wantRetryAfter: (30 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "REST 403 with retry-after",
			status:         http.StatusForbidden,
			header:         header("retry-after", "60"),
			wantRetryAfter: (60 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "secondary 200 with retry-after",
			status:         http.StatusOK,
			header:         header("retry-after", "45"),
			wantRetryAfter: (45 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "200 with remaining 0 and x-ratelimit-reset",
			status:         http.StatusOK,
			header:         header("x-ratelimit-remaining", "0", "x-ratelimit-reset", resetIn(20*time.Second)),
			wantRetryAfter: (20 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "REST 429 with body retry hint",
			status:         http.StatusTooManyRequests,
			body:           `{"retry_after":10}`,
			wantRetryAfter: (10 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "200 with body retryAfterMs",
			status:         http.StatusOK,
			body:           `{"data":{},"retryAfterMs":1500}`,
			wantRetryAfter: 1500 + jitter.Milliseconds(),
		},
		{
			name:           "403 with remaining 0 and x-ratelimit-reset",
			status:         http.StatusForbidden,
			header:         header("x-ratelimit-remaining", "0", "x-ratelimit-reset", resetIn(30*time.Second)),
			wantRetryAfter: (30 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := n.Normalize(tc.status, tc.header, []byte(tc.body))
			if got.Retry {
				t.Fatal("Retry = true, want RATE_LIMITED error")
			}
			if got.Err == nil {
				t.Fatal("Err = nil, want RATE_LIMITED")
			}
			if got.Err.Code != domain.CodeRateLimited {
				t.Fatalf("Code = %s, want %s", got.Err.Code, domain.CodeRateLimited)
			}
			if got.Err.Status != http.StatusTooManyRequests {
				t.Fatalf("Status = %d, want 429", got.Err.Status)
			}
			if got.Err.RetryAfterMs == nil {
				t.Fatal("RetryAfterMs = nil, want a value with jitter")
			}
			if *got.Err.RetryAfterMs != tc.wantRetryAfter {
				t.Fatalf("RetryAfterMs = %d, want %d", *got.Err.RetryAfterMs, tc.wantRetryAfter)
			}
		})
	}
}

// x-ratelimit-reset is the hourly window reset GitHub sends on every response,
// not a rate-limit signal. Treating it as one made every successful GraphQL
// call RATE_LIMITED while gh api rate_limit still showed a full quota.
func TestNormalizeIgnoresWindowResetWithoutExhaustion(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	n := testNormalizer(now, 0)
	reset := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)

	cases := []struct {
		name   string
		status int
		header http.Header
		body   string
	}{
		{
			name:   "200 with future reset and remaining 4999",
			status: http.StatusOK,
			header: header("x-ratelimit-reset", reset, "x-ratelimit-remaining", "4999"),
			body:   `{"data":{"viewer":{"login":"octocat"}}}`,
		},
		{
			name:   "200 with future reset and no remaining header",
			status: http.StatusOK,
			header: header("x-ratelimit-reset", reset),
			body:   `{"data":{}}`,
		},
		{
			name:   "200 with bare body reset",
			status: http.StatusOK,
			body:   `{"data":{},"reset":` + reset + `}`,
		},
		{
			name:   "200 with graphql errors but remaining 4999",
			status: http.StatusOK,
			header: header("x-ratelimit-reset", reset, "x-ratelimit-remaining", "4999"),
			body:   `{"errors":[{"message":"bad query"}]}`,
		},
		{
			name:   "403 with future reset and remaining 4999",
			status: http.StatusForbidden,
			header: header("x-ratelimit-reset", reset, "x-ratelimit-remaining", "4999"),
			body:   `{"message":"Resource not accessible by integration"}`,
		},
		{
			name:   "403 with bare body reset",
			status: http.StatusForbidden,
			body:   `{"reset":` + reset + `}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := n.Normalize(tc.status, tc.header, []byte(tc.body))
			if !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("Normalize = %+v, want empty Result", got)
			}
		})
	}
}

func TestNormalizeRetriesServerErrors(t *testing.T) {
	n := testNormalizer(time.Now(), 0)

	cases := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		wantRetry  bool
		wantAppErr bool
	}{
		{name: "502 bad gateway", status: http.StatusBadGateway, wantRetry: true},
		{name: "504 gateway timeout", status: http.StatusGatewayTimeout, wantRetry: true},
		{name: "200 healthy", status: http.StatusOK, body: `{"data":{}}`},
		{
			name:   "200 graphql error without remaining 0",
			status: http.StatusOK,
			header: header("x-ratelimit-remaining", "42"),
			body:   `{"errors":[{"message":"bad query"}]}`,
		},
		{name: "403 without rate signal", status: http.StatusForbidden},
		{name: "500 is not retried", status: http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := n.Normalize(tc.status, tc.header, []byte(tc.body))
			if got.Retry != tc.wantRetry {
				t.Fatalf("Retry = %v, want %v", got.Retry, tc.wantRetry)
			}
			if got.Retry && got.Backoff <= 0 {
				t.Fatalf("Backoff = %v, want positive backoff on retry", got.Backoff)
			}
			if (got.Err != nil) != tc.wantAppErr {
				t.Fatalf("Err = %v, wantAppErr = %v", got.Err, tc.wantAppErr)
			}
		})
	}
}

func TestNormalizeHeaderOverridesBody(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	jitter := 250 * time.Millisecond
	n := testNormalizer(now, jitter)

	cases := []struct {
		name           string
		status         int
		header         http.Header
		body           string
		wantRetryAfter int64
	}{
		{
			name:           "retry-after header beats body retry_after",
			status:         http.StatusTooManyRequests,
			header:         header("retry-after", "90"),
			body:           `{"retry_after":5}`,
			wantRetryAfter: (90 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
		{
			name:           "x-ratelimit-reset header beats body retry hint",
			status:         http.StatusOK,
			header:         header("x-ratelimit-remaining", "0", "x-ratelimit-reset", strconv.FormatInt(now.Add(90*time.Second).Unix(), 10)),
			body:           `{"retry_after":5}`,
			wantRetryAfter: (90 * time.Second).Milliseconds() + jitter.Milliseconds(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := n.Normalize(tc.status, tc.header, []byte(tc.body))
			if got.Err == nil || got.Err.RetryAfterMs == nil {
				t.Fatalf("want RATE_LIMITED with retryAfterMs, got %+v", got)
			}
			if *got.Err.RetryAfterMs != tc.wantRetryAfter {
				t.Fatalf("RetryAfterMs = %d, want %d (header value, not body)",
					*got.Err.RetryAfterMs, tc.wantRetryAfter)
			}
		})
	}
}

func TestNormalizeNoRetryAfterWithoutDelay(t *testing.T) {
	n := testNormalizer(time.Now(), 0)
	got := n.Normalize(http.StatusTooManyRequests, http.Header{}, nil)
	if got.Err == nil {
		t.Fatal("429 with no delay is still RATE_LIMITED")
	}
	if got.Err.RetryAfterMs != nil {
		t.Fatalf("RetryAfterMs = %v, want nil without a delay", *got.Err.RetryAfterMs)
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	n := testNormalizer(time.Now(), 300*time.Millisecond)

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 100*time.Millisecond + 300*time.Millisecond},
		{attempt: 2, want: 200*time.Millisecond + 300*time.Millisecond},
		{attempt: 3, want: 400*time.Millisecond + 300*time.Millisecond},
		{attempt: 0, want: 100*time.Millisecond + 300*time.Millisecond},
	}
	for _, tc := range cases {
		if got := n.Backoff(tc.attempt); got != tc.want {
			t.Errorf("Backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
	if got := n.Backoff(8); got < maxBackoff {
		t.Errorf("Backoff(8) = %v, want at least maxBackoff %v", got, maxBackoff)
	}
}

// ParseConflict turns a GitHub response into a structured conflict with a type
// and file paths. Plain errors stay unclassified.
func TestParseConflict(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantOK    bool
		wantType  domain.ConflictType
		wantPaths []string
	}{
		{
			name:      "409 merge conflict with files",
			status:    http.StatusConflict,
			body:      `{"message":"merge conflict","files":["src/a.go","docs/b.md"]}`,
			wantOK:    true,
			wantType:  domain.ConflictTypeMerge,
			wantPaths: []string{"src/a.go", "docs/b.md"},
		},
		{
			name:      "422 not mergeable with paths",
			status:    http.StatusUnprocessableEntity,
			body:      `{"message":"Pull Request is not mergeable","paths":["x/y.go"]}`,
			wantOK:    true,
			wantType:  domain.ConflictTypeMerge,
			wantPaths: []string{"x/y.go"},
		},
		{
			name:      "405 conflict error paths",
			status:    http.StatusMethodNotAllowed,
			body:      `{"message":"merge failed","errors":[{"code":"conflict","path":"a.go"},{"field":"internal/b.go"},{"field":"resource"}]}`,
			wantOK:    true,
			wantType:  domain.ConflictTypeMerge,
			wantPaths: []string{"a.go", "internal/b.go"},
		},
		{
			name:      "explicit file ownership type",
			status:    http.StatusOK,
			body:      `{"type":"file_ownership","message":"file ownership conflict","files":["shared.go"]}`,
			wantOK:    true,
			wantType:  domain.ConflictTypeFileOwnership,
			wantPaths: []string{"shared.go"},
		},
		{
			name:   "healthy 200 is not a conflict",
			status: http.StatusOK,
			body:   `{"data":{}}`,
		},
		{
			name:   "403 without conflict signal",
			status: http.StatusForbidden,
			body:   `{"message":"Resource not accessible by integration"}`,
		},
		{
			name:   "409 without conflict signal",
			status: http.StatusConflict,
			body:   `{"message":"Resource not accessible"}`,
		},
		{
			name:      "409 with conflict word and no paths",
			status:    http.StatusConflict,
			body:      `{"message":"merge conflict"}`,
			wantOK:    true,
			wantType:  domain.ConflictTypeMerge,
			wantPaths: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseConflict(tc.status, []byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				if got != nil {
					t.Fatalf("conflict = %#v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("conflict = nil, want a result")
			}
			if got.Type != tc.wantType {
				t.Fatalf("Type = %q, want %q", got.Type, tc.wantType)
			}
			if !reflect.DeepEqual(got.Paths, tc.wantPaths) {
				t.Fatalf("Paths = %#v, want %#v", got.Paths, tc.wantPaths)
			}
		})
	}
}

// Normalize exposes the parsed conflict on Result without setting Err/Retry.
func TestNormalizeReturnsConflict(t *testing.T) {
	n := testNormalizer(time.Now(), 0)
	got := n.Normalize(http.StatusConflict, http.Header{},
		[]byte(`{"message":"merge conflict","files":["src/a.go"]}`))
	if got.Err != nil || got.Retry || got.Backoff != 0 {
		t.Fatalf("unexpected retry/error side: %+v", got)
	}
	if got.Conflict == nil {
		t.Fatal("Conflict = nil, want a structured result")
	}
	if got.Conflict.Type != domain.ConflictTypeMerge {
		t.Fatalf("Type = %q, want %q", got.Conflict.Type, domain.ConflictTypeMerge)
	}
	if !reflect.DeepEqual(got.Conflict.Paths, []string{"src/a.go"}) {
		t.Fatalf("Paths = %#v, want [src/a.go]", got.Conflict.Paths)
	}

	healthy := n.Normalize(http.StatusOK, http.Header{}, []byte(`{"data":{}}`))
	if healthy.Conflict != nil {
		t.Fatalf("healthy response has Conflict = %#v", healthy.Conflict)
	}
	plain := n.Normalize(http.StatusConflict, http.Header{},
		[]byte(`{"message":"Resource not accessible"}`))
	if plain.Conflict != nil {
		t.Fatalf("plain 409 has Conflict = %#v", plain.Conflict)
	}
}

// --- Current-run CI verdict (ADR 0048 §1, T3.112) ---

// The adapter must reduce the raw check-runs list to the current attempt of
// each named check. A green re-run wins over an old red result, and the most
// recent attempt wins regardless of the order the provider listed the runs.
func TestGitHubCurrentRunVerdict(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantRollup string
		wantFail   []string
		wantStates []CheckState
		wantURLs   []string
	}{
		{
			name: "green re-run beats old red",
			body: `{"check_runs":[
				{"id":10,"name":"build","status":"completed","conclusion":"failure","html_url":"https://gh/old","started_at":"2026-11-20T10:00:00Z"},
				{"id":11,"name":"build","status":"completed","conclusion":"success","html_url":"https://gh/new","started_at":"2026-11-20T11:00:00Z"}]}`,
			wantRollup: "SUCCESS",
			wantStates: []CheckState{CheckSuccess},
			wantURLs:   []string{"https://gh/new"},
		},
		{
			name: "newest run wins regardless of payload order",
			body: `{"check_runs":[
				{"name":"build","status":"completed","conclusion":"success","html_url":"https://gh/new","started_at":"2026-11-20T11:00:00Z"},
				{"name":"build","status":"completed","conclusion":"failure","html_url":"https://gh/old","started_at":"2026-11-20T10:00:00Z"}]}`,
			wantRollup: "SUCCESS",
			wantStates: []CheckState{CheckSuccess},
			wantURLs:   []string{"https://gh/new"},
		},
		{
			name: "red re-run beats old green",
			body: `{"check_runs":[
				{"name":"build","status":"completed","conclusion":"success","started_at":"2026-11-20T10:00:00Z"},
				{"name":"build","status":"completed","conclusion":"failure","started_at":"2026-11-20T11:00:00Z"}]}`,
			wantRollup: "FAILURE",
			wantFail:   []string{"build"},
			wantStates: []CheckState{CheckFailure},
		},
		{
			name: "id breaks tie when timestamps absent",
			body: `{"check_runs":[
				{"id":20,"name":"build","status":"completed","conclusion":"success"},
				{"id":10,"name":"build","status":"completed","conclusion":"failure"}]}`,
			wantRollup: "SUCCESS",
			wantStates: []CheckState{CheckSuccess},
		},
		{
			name: "current pending run wins over old green",
			body: `{"check_runs":[
				{"name":"build","status":"completed","conclusion":"success","started_at":"2026-11-20T10:00:00Z"},
				{"name":"build","status":"in_progress","started_at":"2026-11-20T11:00:00Z"}]}`,
			wantRollup: "PENDING",
			wantStates: []CheckState{CheckPending},
		},
		{
			name: "distinct checks all survive the reduction",
			body: `{"check_runs":[
				{"name":"build","status":"completed","conclusion":"success"},
				{"name":"tests","status":"completed","conclusion":"failure"}]}`,
			wantRollup: "FAILURE",
			wantFail:   []string{"tests"},
			wantStates: []CheckState{CheckSuccess, CheckFailure},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, appErr := NewStatusParser().Parse(ProviderGitHub, []byte(tc.body))
			if appErr != nil {
				t.Fatalf("Parse error = %+v, want none", appErr)
			}
			if res.Rollup != tc.wantRollup {
				t.Fatalf("Rollup = %q, want %q", res.Rollup, tc.wantRollup)
			}
			if !reflect.DeepEqual(res.Failures, tc.wantFail) {
				t.Fatalf("Failures = %v, want %v", res.Failures, tc.wantFail)
			}
			if len(res.Checks) != len(tc.wantStates) {
				t.Fatalf("len(Checks) = %d, want %d (%+v)", len(res.Checks), len(tc.wantStates), res.Checks)
			}
			for i, c := range res.Checks {
				if c.State != tc.wantStates[i] {
					t.Errorf("Checks[%d].State = %q, want %q", i, c.State, tc.wantStates[i])
				}
				if i < len(tc.wantURLs) && c.DetailsURL != tc.wantURLs[i] {
					t.Errorf("Checks[%d].DetailsURL = %q, want %q", i, c.DetailsURL, tc.wantURLs[i])
				}
			}
		})
	}
}

// The reduced rollup must reach the verdict engine: a green re-run of a
// previously red check must make ready reachable through Apply.
func TestCurrentRunReachesVerdict(t *testing.T) {
	body := `{"check_runs":[
		{"name":"build","status":"completed","conclusion":"failure","started_at":"2026-11-20T10:00:00Z"},
		{"name":"build","status":"completed","conclusion":"success","started_at":"2026-11-20T11:00:00Z"},
		{"name":"ci/audit","status":"completed","conclusion":"success"},
		{"name":"tests","status":"completed","conclusion":"success"}]}`
	res, appErr := NewStatusParser().Parse(ProviderGitHub, []byte(body))
	if appErr != nil {
		t.Fatalf("Parse error = %+v, want none", appErr)
	}
	s := readySnapshotForShip()
	res.Apply(&s)
	if s.Rollup != "SUCCESS" {
		t.Fatalf("Snapshot.Rollup = %q, want SUCCESS", s.Rollup)
	}
	if want := (domain.ShipFacts{Build: true, Audit: true, TDD: true}); s.Ship != want {
		t.Fatalf("Snapshot.Ship = %+v, want %+v", s.Ship, want)
	}
	got, _ := domain.Evaluate(s, domain.Policy{RequireUpToDate: true})
	if got != domain.VerdictReady {
		t.Fatalf("Evaluate = %q, want %q", got, domain.VerdictReady)
	}
}

// --- Cross-platform CI status adapter tests (ADR 0039 §3, T3.62) ---

type statusCase struct {
	name     string
	provider Provider
	body     string
}

// One status from three providers produces one neutral representation.
func TestStatusParserSameStatusAcrossProviders(t *testing.T) {
	p := NewStatusParser()
	cases := []statusCase{
		{"github success", ProviderGitHub,
			`{"check_runs":[{"name":"build","status":"completed","conclusion":"success","html_url":"https://gh/build"}]}`},
		{"gitlab success", ProviderGitLab,
			`{"status":"success","jobs":[{"name":"build","status":"success","web_url":"https://gl/build"}]}`},
		{"bitbucket success", ProviderBitbucket,
			`{"values":[{"state":"SUCCESSFUL","name":"build","url":"https://bb/build"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, appErr := p.Parse(tc.provider, []byte(tc.body))
			if appErr != nil {
				t.Fatalf("Parse error = %+v, want none", appErr)
			}
			if got.Rollup != "SUCCESS" {
				t.Fatalf("Rollup = %q, want SUCCESS", got.Rollup)
			}
			if len(got.Failures) != 0 {
				t.Fatalf("Failures = %v, want empty", got.Failures)
			}
			if len(got.Checks) != 1 {
				t.Fatalf("len(Checks) = %d, want 1", len(got.Checks))
			}
			c := got.Checks[0]
			if c.Name != "build" || c.State != CheckSuccess || c.DetailsURL == "" {
				t.Fatalf("Check = %+v, want build/SUCCESS with a URL", c)
			}
		})
	}
}

func TestStatusParserFailureAcrossProviders(t *testing.T) {
	p := NewStatusParser()
	cases := []statusCase{
		{"github failure", ProviderGitHub,
			`{"check_runs":[{"name":"build","status":"completed","conclusion":"failure"}]}`},
		{"gitlab failure", ProviderGitLab,
			`{"status":"failed","jobs":[{"name":"build","status":"failed"}]}`},
		{"bitbucket failure", ProviderBitbucket,
			`{"values":[{"state":"FAILED","name":"build"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, appErr := p.Parse(tc.provider, []byte(tc.body))
			if appErr != nil {
				t.Fatalf("Parse error = %+v, want none", appErr)
			}
			if got.Rollup != "FAILURE" {
				t.Fatalf("Rollup = %q, want FAILURE", got.Rollup)
			}
			if !reflect.DeepEqual(got.Failures, []string{"build"}) {
				t.Fatalf("Failures = %v, want [build]", got.Failures)
			}
			if got.Checks[0].State != CheckFailure {
				t.Fatalf("Check.State = %q, want FAILURE", got.Checks[0].State)
			}
		})
	}
}

func TestStatusParserPendingAcrossProviders(t *testing.T) {
	p := NewStatusParser()
	cases := []statusCase{
		{"github in_progress", ProviderGitHub,
			`{"check_runs":[{"name":"build","status":"in_progress"}]}`},
		{"gitlab running", ProviderGitLab,
			`{"status":"running","jobs":[{"name":"build","status":"running"}]}`},
		{"bitbucket inprogress", ProviderBitbucket,
			`{"values":[{"state":"INPROGRESS","name":"build"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, appErr := p.Parse(tc.provider, []byte(tc.body))
			if appErr != nil {
				t.Fatalf("Parse error = %+v, want none", appErr)
			}
			if got.Rollup != "PENDING" {
				t.Fatalf("Rollup = %q, want PENDING", got.Rollup)
			}
			if len(got.Failures) != 0 {
				t.Fatalf("Failures = %v, want empty", got.Failures)
			}
			if got.Checks[0].State != CheckPending {
				t.Fatalf("Check.State = %q, want PENDING", got.Checks[0].State)
			}
		})
	}
}

// Skipped and neutral results, including an allowed-failure GitLab job, never
// enter Failures. Each provider spells that state differently.
func TestStatusParserNeutralAndSkipped(t *testing.T) {
	p := NewStatusParser()
	cases := []struct {
		statusCase
		want CheckState
	}{
		{statusCase{"github skipped", ProviderGitHub,
			`{"check_runs":[{"name":"doc","status":"completed","conclusion":"skipped"}]}`}, CheckSkipped},
		{statusCase{"github neutral", ProviderGitHub,
			`{"check_runs":[{"name":"lint","status":"completed","conclusion":"neutral"}]}`}, CheckNeutral},
		{statusCase{"gitlab allow_failure", ProviderGitLab,
			`{"status":"success","jobs":[{"name":"lint","status":"failed","allow_failure":true}]}`}, CheckNeutral},
		{statusCase{"bitbucket stopped", ProviderBitbucket,
			`{"values":[{"state":"STOPPED","name":"deploy"}]}`}, CheckNeutral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, appErr := p.Parse(tc.provider, []byte(tc.body))
			if appErr != nil {
				t.Fatalf("Parse error = %+v, want none", appErr)
			}
			if got.Rollup != "SUCCESS" {
				t.Fatalf("Rollup = %q, want SUCCESS", got.Rollup)
			}
			if len(got.Failures) != 0 {
				t.Fatalf("Failures = %v, want empty", got.Failures)
			}
			if got.Checks[0].State != tc.want {
				t.Fatalf("Check.State = %q, want %q", got.Checks[0].State, tc.want)
			}
		})
	}
}

func TestStatusParserStructuredErrors(t *testing.T) {
	p := NewStatusParser()
	cases := []struct {
		name     string
		provider Provider
		body     string
		wantCode domain.ErrorCode
	}{
		{"unknown provider", Provider("circle"), `{}`, CodeCIUnknownProvider},
		{"github malformed", ProviderGitHub, `{`, CodeCIMalformed},
		{"github missing", ProviderGitHub, `{}`, CodeCIMissingStatus},
		{"gitlab missing", ProviderGitLab, `{}`, CodeCIMissingStatus},
		{"bitbucket missing", ProviderBitbucket, `{}`, CodeCIMissingStatus},
		{"github invalid conclusion", ProviderGitHub,
			`{"check_runs":[{"name":"x","status":"completed","conclusion":"mystery"}]}`, CodeCIInvalidStatus},
		{"gitlab invalid status", ProviderGitLab,
			`{"status":"mystery","jobs":[]}`, CodeCIInvalidStatus},
		{"bitbucket invalid state", ProviderBitbucket,
			`{"values":[{"state":"MYSTERY","name":"x"}]}`, CodeCIInvalidStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, appErr := p.Parse(tc.provider, []byte(tc.body))
			if appErr == nil {
				t.Fatal("Parse error = nil, want a structured AppError")
			}
			if appErr.Code != tc.wantCode {
				t.Fatalf("Code = %q, want %q", appErr.Code, tc.wantCode)
			}
			if appErr.Status != http.StatusBadRequest {
				t.Fatalf("Status = %d, want %d", appErr.Status, http.StatusBadRequest)
			}
			if appErr.Error() == "" {
				t.Fatal("AppError.Error() is empty")
			}
			if !reflect.DeepEqual(got, StatusResult{}) {
				t.Fatalf("result on error = %+v, want zero", got)
			}
		})
	}
}

// All three adapters satisfy one interface, and Apply only prepares verdict
// input without calling the verdict engine.
func TestStatusAdaptersViaInterfaceAndApply(t *testing.T) {
	adapters := []StatusAdapter{
		NewGitHubActionsAdapter(),
		NewGitLabAdapter(),
		NewBitbucketAdapter(),
	}
	want := []Provider{ProviderGitHub, ProviderGitLab, ProviderBitbucket}
	for i, a := range adapters {
		if a.Provider() != want[i] {
			t.Errorf("adapter %d Provider = %q, want %q", i, a.Provider(), want[i])
		}
	}

	p := NewStatusParser()
	res, appErr := p.Parse(ProviderGitHub,
		[]byte(`{"check_runs":[{"name":"build","status":"completed","conclusion":"failure"}]}`))
	if appErr != nil {
		t.Fatalf("Parse error = %+v, want none", appErr)
	}
	var snap domain.Snapshot
	res.Apply(&snap)
	if snap.Rollup != "FAILURE" {
		t.Fatalf("Snapshot.Rollup = %q, want FAILURE", snap.Rollup)
	}
	if !reflect.DeepEqual(snap.Failures, []string{"build"}) {
		t.Fatalf("Snapshot.Failures = %v, want [build]", snap.Failures)
	}
}

// readySnapshotForShip satisfies every ready row in the domain decision table
// except Ship. Only the adapter under test fills Ship, so this reproduces the
// production shape of the regression where Evaluate never saw any facts.
func readySnapshotForShip() domain.Snapshot {
	return domain.Snapshot{
		State:            "OPEN",
		HeadSHA:          "sha-ready",
		WatchHeadSHA:     "sha-ready",
		Rollup:           "SUCCESS",
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		SuitesValid:      true,
	}
}

// TestApplyFillsShipForReady proves ready is reachable through the real github
// status path (T3.78). Green required checks must yield VerdictReady after
// Apply; a failed required check must keep the verdict away from ready. If
// Apply stops filling Ship, the first case fails because Evaluate sees zero
// facts and falls through to watching.
func TestApplyFillsShipForReady(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantFacts domain.ShipFacts
		want      domain.Verdict
	}{
		{
			name: "green build audit and test make ready reachable",
			body: `{"check_runs":[
				{"name":"build","status":"completed","conclusion":"success"},
				{"name":"ci/audit","status":"completed","conclusion":"success"},
				{"name":"tests","status":"completed","conclusion":"success"}]}`,
			wantFacts: domain.ShipFacts{Build: true, Audit: true, TDD: true},
			want:      domain.VerdictReady,
		},
		{
			name: "a failed required build is not ready",
			body: `{"check_runs":[
				{"name":"build","status":"completed","conclusion":"failure"},
				{"name":"ci/audit","status":"completed","conclusion":"success"},
				{"name":"tests","status":"completed","conclusion":"success"}]}`,
			wantFacts: domain.ShipFacts{Audit: true, TDD: true},
			want:      domain.VerdictKeepWorking,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, appErr := NewStatusParser().Parse(ProviderGitHub, []byte(tc.body))
			if appErr != nil {
				t.Fatalf("Parse error = %+v, want none", appErr)
			}
			s := readySnapshotForShip()
			res.Apply(&s)
			if s.Ship != tc.wantFacts {
				t.Fatalf("Snapshot.Ship = %+v, want %+v", s.Ship, tc.wantFacts)
			}
			got, wake := domain.Evaluate(s, domain.Policy{RequireUpToDate: true})
			if got != tc.want {
				t.Fatalf("Evaluate = %q, want %q (Ship %+v)", got, tc.want, s.Ship)
			}
			if tc.want == domain.VerdictReady && len(wake) == 0 {
				t.Fatal("a ready verdict must wake the orchestrator")
			}
		})
	}
}
