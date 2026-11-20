package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

const greenPRJSON = `{"state":"OPEN","isDraft":false,"headRefOid":"head-sha",` +
	`"baseRefName":"main","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN",` +
	`"reviewDecision":"APPROVED",` +
	`"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]},` +
	`"statusCheckRollup":{"contexts":{"nodes":[` +
	`{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"SKIPPED",` +
	`"detailsUrl":"https://ci.example/1","isRequired":true,"checkSuite":{"id":"suite-1"}},` +
	`{"__typename":"StatusContext","context":"lint","state":"NEUTRAL",` +
	`"targetUrl":"https://ci.example/2","isRequired":true}` +
	`],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`

const failingBaseJSON = `{"ref":{"target":{"oid":"main-sha","statusCheckRollup":{"contexts":{"nodes":[` +
	`{"__typename":"CheckRun","name":"main-ci","status":"COMPLETED","conclusion":"FAILURE",` +
	`"detailsUrl":"https://ci.example/main","checkSuite":{"id":"base-suite"}}` +
	`],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`

func prStateJSON(state, mergeable, status string) string {
	return fmt.Sprintf(`{"state":%q,"isDraft":false,"headRefOid":"head-sha",`+
		`"baseRefName":"main","mergeable":%q,"mergeStateStatus":%q,`+
		`"reviewDecision":"APPROVED","commits":{"nodes":[{"commit":`+
		`{"statusCheckRollup":{"state":"SUCCESS"}}}]},`+
		`"statusCheckRollup":{"contexts":{"nodes":[],`+
		`"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`, state, mergeable, status)
}

// batchReply answers one batched document with a payload for every alias it
// contains. PR aliases get prJSON; base aliases get baseJSON.
func batchReply(t *testing.T, w http.ResponseWriter, query, prJSON, baseJSON string, cost int) {
	t.Helper()
	data := make(map[string]json.RawMessage)
	for _, match := range queryAlias.FindAllStringSubmatch(query, -1) {
		name := match[1]
		if strings.HasPrefix(name, "b") {
			data[name] = json.RawMessage(baseJSON)
		} else {
			data[name] = json.RawMessage(prJSON)
		}
	}
	body, err := json.Marshal(struct {
		Data      map[string]json.RawMessage `json:"data"`
		RateLimit rateLimitCost              `json:"rateLimit"`
	}{Data: data, RateLimit: rateLimitCost{Cost: cost}})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func batchTestWatches(n int) []domain.Watch {
	out := make([]domain.Watch, 0, n+1)
	for i := 0; i < n; i++ {
		out = append(out, domain.Watch{
			ID:      fmt.Sprintf("w%d", i),
			Repo:    "refusedguy/beacon",
			PR:      i + 1,
			BaseRef: "main",
			Status:  domain.StatusWatching,
		})
	}
	out = append(out, domain.Watch{
		ID: "closed", Repo: "refusedguy/beacon", PR: 99,
		BaseRef: "main", Status: domain.StatusClosed,
	})
	return out
}

func TestFetchBatchTenPRsAndBaseInOneRequest(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var (
		mu       sync.Mutex
		calls    int
		aliases  int
		hasBase  bool
		requests []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode query: %v", err)
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		names := queryAlias.FindAllStringSubmatch(req.Query, -1)
		mu.Lock()
		calls++
		aliases = len(names)
		hasBase = strings.Contains(req.Query, `ref(qualifiedName:"refs/heads/main")`)
		requests = append(requests, req.Query)
		mu.Unlock()
		batchReply(t, w, req.Query, greenPRJSON, failingBaseJSON, 3)
	}))
	defer srv.Close()

	graph := NewClient(srv.URL, "test-token", srv.Client(), counter)
	rest := newRESTTestClient(t, srv, counter, "test-token")
	provider := NewBatchProvider(graph, rest, counter)

	result, err := provider.FetchBatch(context.Background(), batchTestWatches(10))
	if err != nil {
		t.Fatalf("FetchBatch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("GraphQL requests = %d, want one batch", calls)
	}
	if aliases != 11 {
		t.Fatalf("aliases = %d, want 10 PRs + 1 base", aliases)
	}
	if !hasBase {
		t.Fatal("batch query has no refs/heads/main alias")
	}
	if len(requests) != 1 || strings.Count(requests[0], "contexts(first:50)") != 11 {
		t.Fatalf("one document must carry all PR and base contexts, got %d documents", len(requests))
	}
	if len(result) != 10 {
		t.Fatalf("snapshots = %d, want 10 keys", len(result))
	}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("w%d", i)
		snap, ok := result[id]
		if !ok {
			t.Errorf("missing snapshot for %s", id)
			continue
		}
		if snap.PR.HeadRefOid != "head-sha" || snap.PR.State != "OPEN" {
			t.Errorf("%s PR = %+v, want observed OPEN/head-sha", id, snap.PR)
		}
		ci, ok := snap.PR.Checks["ci"]
		if !ok || ci.Conclusion != "SUCCESS" {
			t.Errorf("%s ci = %+v, want SKIPPED mapped to SUCCESS", id, ci)
		}
		lint, ok := snap.PR.Checks["lint"]
		if !ok || lint.State != "SUCCESS" {
			t.Errorf("%s lint = %+v, want NEUTRAL mapped to SUCCESS", id, lint)
		}
		if !snap.Base.ChecksKnown || snap.Base.BaseRef != "main" || snap.Base.HeadSHA != "main-sha" {
			t.Errorf("%s base = %+v, want known main at main-sha", id, snap.Base)
		}
		if !snap.Base.FailedChecks["main-ci"] {
			t.Errorf("%s base failures = %v, want main-ci", id, snap.Base.FailedChecks)
		}
	}
	used, budget := provider.PointStats()
	if used != 3 || budget != BudgetPerHour {
		t.Fatalf("PointStats = (%d, %d), want (%d, %d)", used, budget, 3, BudgetPerHour)
	}
	if counter.Used() != 3 {
		t.Fatalf("counter.Used = %d, want 3", counter.Used())
	}
}

func TestFetchBatchBrokenBaseIsSeparateFromPRChecks(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode query: %v", err)
			return
		}
		batchReply(t, w, req.Query, greenPRJSON, failingBaseJSON, 1)
	}))
	defer srv.Close()

	provider := NewBatchProvider(NewClient(srv.URL, "", srv.Client(), counter), nil, counter)
	result, err := provider.FetchBatch(context.Background(), batchTestWatches(1))
	if err != nil {
		t.Fatalf("FetchBatch: %v", err)
	}
	snap := result["w0"]
	if len(snap.Base.FailedChecks) != 1 || !snap.Base.FailedChecks["main-ci"] {
		t.Fatalf("BaseFailedChecks = %v, want main-ci from broken main", snap.Base.FailedChecks)
	}
	for name, check := range snap.PR.Checks {
		if check.Conclusion == "FAILURE" || check.State == "FAILURE" {
			t.Errorf("PR check %s = %+v, a skipped PR check must not fail", name, check)
		}
	}
}

func TestFetchBatchSingleflightSharesOneRequest(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		calls.Add(1)
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode query: %v", err)
			return
		}
		batchReply(t, w, req.Query, greenPRJSON, failingBaseJSON, 1)
	}))
	defer srv.Close()

	provider := NewBatchProvider(NewClient(srv.URL, "", srv.Client(), counter), nil, counter)
	watches := batchTestWatches(10)
	const n = 8
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := provider.FetchBatch(context.Background(), watches)
			done <- err
		}()
	}
	<-entered
	close(release)
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent FetchBatch: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("GraphQL requests = %d, want one shared batch", got)
	}
}

func TestNormalizeGitHubCheckStates(t *testing.T) {
	cases := []struct {
		name, conclusion, state, wantState, wantConclusion string
	}{
		{"checkrun skipped", "SKIPPED", "COMPLETED", "COMPLETED", "SUCCESS"},
		{"checkrun neutral", "NEUTRAL", "COMPLETED", "COMPLETED", "SUCCESS"},
		{"checkrun success", "SUCCESS", "COMPLETED", "COMPLETED", "SUCCESS"},
		{"checkrun failure", "FAILURE", "COMPLETED", "COMPLETED", "FAILURE"},
		{"checkrun error", "ERROR", "COMPLETED", "COMPLETED", "FAILURE"},
		{"checkrun timeout", "TIMED_OUT", "COMPLETED", "COMPLETED", "FAILURE"},
		{"checkrun startup failure", "STARTUP_FAILURE", "COMPLETED", "COMPLETED", "FAILURE"},
		{"checkrun cancelled", "CANCELLED", "COMPLETED", "COMPLETED", "FAILURE"},
		{"checkrun queued", "", "QUEUED", "PENDING", ""},
		{"checkrun in progress", "", "IN_PROGRESS", "PENDING", ""},
		{"checkrun waiting", "", "WAITING", "PENDING", ""},
		{"checkrun requested", "", "REQUESTED", "PENDING", ""},
		{"checkrun action required", "ACTION_REQUIRED", "", "PENDING", ""},
		{"context skipped", "", "SKIPPED", "SUCCESS", "SUCCESS"},
		{"context neutral", "", "NEUTRAL", "SUCCESS", "SUCCESS"},
		{"context failure", "", "FAILURE", "FAILURE", "FAILURE"},
		{"context error", "", "ERROR", "FAILURE", "FAILURE"},
		{"context cancelled", "", "CANCELLED", "FAILURE", "FAILURE"},
		{"context pending", "", "PENDING", "PENDING", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeGitHubCheck(tc.conclusion, tc.state)
			if got.State != tc.wantState || got.Conclusion != tc.wantConclusion {
				t.Fatalf("normalize(%q,%q) = %+v, want state=%q conclusion=%q",
					tc.conclusion, tc.state, got, tc.wantState, tc.wantConclusion)
			}
		})
	}
}

func TestMergePRHoldsDirtyWithoutWrite(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var (
		mu   sync.Mutex
		puts int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			var req struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode query: %v", err)
				return
			}
			batchReply(t, w, req.Query, prStateJSON("OPEN", "MERGEABLE", "DIRTY"), `{}`, 1)
			return
		}
		mu.Lock()
		if r.Method == http.MethodPut {
			puts++
		}
		mu.Unlock()
		http.Error(w, "unexpected write", http.StatusInternalServerError)
	}))
	defer srv.Close()

	provider := NewBatchProvider(
		NewClient(srv.URL+"/graphql", "", srv.Client(), counter),
		newRESTTestClient(t, srv, counter, "test-token"), counter)
	merged, held, reason, err := provider.MergePR(context.Background(), "refusedguy/beacon", 42)
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if merged || !held || reason != MergeHeldDirty {
		t.Fatalf("MergePR = (%v,%v,%q), want dirty hold", merged, held, reason)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 0 {
		t.Fatalf("merge writes = %d, want none for a dirty PR", puts)
	}
}

type recordingRunner struct {
	mu   sync.Mutex
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.name = name
	r.args = append([]string(nil), args...)
	return []byte("merged"), nil
}

func TestMergePRRunnerUsesExplicitRepo(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode query: %v", err)
			return
		}
		state := "OPEN"
		if reads.Add(1) > 1 {
			state = "MERGED"
		}
		batchReply(t, w, req.Query, prStateJSON(state, "MERGEABLE", "CLEAN"), `{}`, 1)
	}))
	defer srv.Close()

	runner := &recordingRunner{}
	provider := NewBatchProvider(
		NewClient(srv.URL, "", srv.Client(), counter),
		newRESTTestClient(t, srv, counter, "test-token"),
		counter, WithRunner(runner))

	merged, held, reason, err := provider.MergePR(context.Background(), "refusedguy/beacon", 42)
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if !merged || held || reason != "" {
		t.Fatalf("MergePR = (%v,%v,%q), want confirmed merge", merged, held, reason)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.name != "gh" {
		t.Fatalf("runner command = %q, want gh", runner.name)
	}
	want := []string{"pr", "merge", "42", "--repo", "refusedguy/beacon", "--squash", "--delete-branch"}
	if fmt.Sprint(runner.args) != fmt.Sprint(want) {
		t.Fatalf("runner args = %v, want %v", runner.args, want)
	}
}

func TestMergePRRestConflictIsHeld(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var puts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			var req struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode query: %v", err)
				return
			}
			batchReply(t, w, req.Query, prStateJSON("OPEN", "MERGEABLE", "CLEAN"), `{}`, 1)
			return
		}
		if r.Method == http.MethodPut {
			puts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprint(w, `{"message":"Pull Request is not mergeable"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	provider := NewBatchProvider(
		NewClient(srv.URL+"/graphql", "", srv.Client(), counter),
		newRESTTestClient(t, srv, counter, "test-token"), counter)

	merged, held, reason, err := provider.MergePR(context.Background(), "refusedguy/beacon", 7)
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if merged || !held || reason != MergeHeldRefused {
		t.Fatalf("MergePR = (%v,%v,%q), want refused merge hold", merged, held, reason)
	}
	if got := puts.Load(); got != 1 {
		t.Fatalf("PUT calls = %d, want 1", got)
	}
}

// TestMergePRResolvesUnknownAfterPing304 reproduces the auto-merge stall: the
// batch resolved MERGEABLE/CLEAN, but a fresh GraphQL read reports UNKNOWN and
// the cached ETag makes the conditional ping answer 304. The merge must still
// go through after an unconditional re-read.
func TestMergePRResolvesUnknownAfterPing304(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	const etag = `"pr-42-unknown"`
	var graphCalls, gets, puts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/graphql":
			var req struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode query: %v", err)
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			state, mergeable := "OPEN", "UNKNOWN"
			if graphCalls.Add(1) > 1 {
				state, mergeable = "MERGED", "MERGEABLE"
			}
			batchReply(t, w, req.Query, prStateJSON(state, mergeable, "CLEAN"), `{}`, 1)
		case r.URL.Path == "/repos/refusedguy/beacon/pulls/42" && r.Method == http.MethodGet:
			n := gets.Add(1)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				fmt.Fprint(w, `{"number":42,"mergeable":null,"mergeable_state":"unknown"}`)
			} else {
				fmt.Fprint(w, `{"number":42,"mergeable":true,"mergeable_state":"clean"}`)
			}
		case r.URL.Path == "/repos/refusedguy/beacon/pulls/42/merge" && r.Method == http.MethodPut:
			puts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"merged":true,"sha":"abc123"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	graph := NewClient(srv.URL+"/graphql", "", srv.Client(), counter)
	rest := newRESTTestClient(t, srv, counter, "test-token")
	provider := NewBatchProvider(graph, rest, counter)

	// The batch ping cached an ETag while mergeable was still UNKNOWN.
	seed, notModified, err := rest.PingMergeable(context.Background(), "refusedguy", "beacon", 42)
	if err != nil || notModified || !seed.Unknown() {
		t.Fatalf("seed ping = (%+v,%v,%v), want UNKNOWN/200", seed, notModified, err)
	}

	merged, held, reason, err := provider.MergePR(context.Background(), "refusedguy/beacon", 42)
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if !merged || held || reason != "" {
		t.Fatalf("MergePR = (%v,%v,%q), want confirmed merge", merged, held, reason)
	}
	if puts.Load() != 1 {
		t.Fatalf("merge writes = %d, want 1", puts.Load())
	}
	if graphCalls.Load() != 2 {
		t.Fatalf("GraphQL reads = %d, want 2 (read + confirm)", graphCalls.Load())
	}
	if gets.Load() != 3 {
		t.Fatalf("REST ping GETs = %d, want 3 (seed + 304 + forced)", gets.Load())
	}
}

// TestMergePRWithStateUsesProvidedState proves a fresh batch snapshot skips the
// second GraphQL read and the mergeability ping entirely.
func TestMergePRWithStateUsesProvidedState(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var graphCalls, getCalls, puts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/graphql":
			var req struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode query: %v", err)
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			graphCalls.Add(1)
			batchReply(t, w, req.Query, prStateJSON("MERGED", "MERGEABLE", "CLEAN"), `{}`, 1)
		case r.URL.Path == "/repos/refusedguy/beacon/pulls/42/merge" && r.Method == http.MethodPut:
			puts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"merged":true,"sha":"abc"}`)
		case r.URL.Path == "/repos/refusedguy/beacon/pulls/42" && r.Method == http.MethodGet:
			getCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"number":42,"mergeable":true,"mergeable_state":"clean"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	provider := NewBatchProvider(
		NewClient(srv.URL+"/graphql", "", srv.Client(), counter),
		newRESTTestClient(t, srv, counter, "test-token"), counter)

	fresh := domain.PRState{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}
	merged, held, reason, err := provider.MergePRWithState(context.Background(), "refusedguy/beacon", 42, fresh)
	if err != nil {
		t.Fatalf("MergePRWithState: %v", err)
	}
	if !merged || held || reason != "" {
		t.Fatalf("MergePRWithState = (%v,%v,%q), want confirmed merge", merged, held, reason)
	}
	if puts.Load() != 1 {
		t.Fatalf("merge writes = %d, want 1", puts.Load())
	}
	if getCalls.Load() != 0 {
		t.Fatalf("REST ping GETs = %d, want 0 when state is already fresh", getCalls.Load())
	}
	if graphCalls.Load() != 1 {
		t.Fatalf("GraphQL reads = %d, want 1 (confirm only)", graphCalls.Load())
	}
}

// TestPingMergeableConcurrentCallersShareResolvedState checks that a second
// caller joining an in-flight ping still gets its own state updated. The old
// closure mutated only the leader's pointer and left the follower UNKNOWN.
func TestPingMergeableConcurrentCallersShareResolvedState(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	entered := make(chan struct{})
	secondEntered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var gets atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		once.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"number":7,"mergeable":true,"mergeable_state":"clean"}`)
	}))
	defer srv.Close()

	provider := NewBatchProvider(nil, newRESTTestClient(t, srv, counter, "test-token"), counter)
	ctx := context.Background()
	stateA := domain.PRState{Mergeable: "UNKNOWN"}
	stateB := domain.PRState{Mergeable: "UNKNOWN"}
	done := make(chan struct{}, 2)

	go func() {
		provider.pingMergeable(ctx, "owner", "repo", 7, &stateA)
		done <- struct{}{}
	}()
	<-entered
	go func() {
		close(secondEntered)
		provider.pingMergeable(ctx, "owner", "repo", 7, &stateB)
		done <- struct{}{}
	}()
	<-secondEntered
	// Give the second caller time to join the in-flight singleflight before the
	// REST reply is released.
	for i := 0; i < 8; i++ {
		runtime.Gosched()
	}
	close(release)
	<-done
	<-done

	if stateA.Mergeable != "MERGEABLE" || stateA.MergeStateStatus != "CLEAN" {
		t.Fatalf("leader state = %+v, want MERGEABLE/CLEAN", stateA)
	}
	if stateB.Mergeable != "MERGEABLE" || stateB.MergeStateStatus != "CLEAN" {
		t.Fatalf("follower state = %+v, want MERGEABLE/CLEAN", stateB)
	}
	if gets.Load() != 1 {
		t.Fatalf("REST ping GETs = %d, want one shared request", gets.Load())
	}
}

func TestMergePRWithStateHoldsOnlyForRealConflicts(t *testing.T) {
	cases := []struct {
		name       string
		state      domain.PRState
		wantReason string
	}{
		{"conflict", domain.PRState{State: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "CLEAN"}, MergeHeldConflict},
		{"dirty", domain.PRState{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "DIRTY"}, MergeHeldDirty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter, _, _ := newTestCounter(t, 0)
			var writes atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				http.Error(w, "unexpected merge write", http.StatusInternalServerError)
			}))
			defer srv.Close()

			provider := NewBatchProvider(nil, newRESTTestClient(t, srv, counter, "test-token"), counter)
			merged, held, reason, err := provider.MergePRWithState(context.Background(), "refusedguy/beacon", 42, tc.state)
			if err != nil {
				t.Fatalf("MergePRWithState: %v", err)
			}
			if merged || !held || reason != tc.wantReason {
				t.Fatalf("MergePRWithState = (%v,%v,%q), want held %q", merged, held, reason, tc.wantReason)
			}
			if writes.Load() != 0 {
				t.Fatalf("unexpected writes = %d, want 0", writes.Load())
			}
		})
	}
}

// TestMergePRWithStateHoldsWhenPingCannotResolve keeps mergeable_unknown as a
// real hold when the REST endpoint still has no computed value.
func TestMergePRWithStateHoldsWhenPingCannotResolve(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var writes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			writes.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"number":42,"mergeable":null,"mergeable_state":"unknown"}`)
	}))
	defer srv.Close()

	provider := NewBatchProvider(nil, newRESTTestClient(t, srv, counter, "test-token"), counter)
	stale := domain.PRState{State: "OPEN", Mergeable: "UNKNOWN", MergeStateStatus: "CLEAN"}
	merged, held, reason, err := provider.MergePRWithState(context.Background(), "refusedguy/beacon", 42, stale)
	if err != nil {
		t.Fatalf("MergePRWithState: %v", err)
	}
	if merged || !held || reason != MergeHeldMergeable {
		t.Fatalf("MergePRWithState = (%v,%v,%q), want mergeable_unknown hold", merged, held, reason)
	}
	if writes.Load() != 0 {
		t.Fatalf("merge writes = %d, want 0 for an unresolved PR", writes.Load())
	}
}
