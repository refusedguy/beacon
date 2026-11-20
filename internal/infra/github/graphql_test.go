package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

type replyFunc func(w http.ResponseWriter, r *http.Request, query string)

// graphQLServer records the alias count of every query it receives.
type graphQLServer struct {
	*httptest.Server

	mu    sync.Mutex
	sizes []int
	calls int
	reply replyFunc
}

func newGraphQLServer(t *testing.T, reply replyFunc) *graphQLServer {
	t.Helper()
	s := &graphQLServer{reply: reply}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode query: %v", err)
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.sizes = append(s.sizes, strings.Count(req.Query, ": repository("))
		s.calls++
		s.mu.Unlock()
		s.reply(w, r, req.Query)
	}))
	t.Cleanup(s.Close)
	return s
}

// snapshot sorts the sizes because chunk requests run concurrently. The
// order of requests is not deterministic, only the multiset of sizes is.
func (s *graphQLServer) snapshot() (calls int, sizes []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]int(nil), s.sizes...)
	sort.Ints(out)
	return s.calls, out
}

var queryAlias = regexp.MustCompile(`(?m)^  ([A-Za-z0-9_]+): repository\(`)

// healthyReply returns one data entry per alias in the query, so a batch of
// chunks can be checked for merged results as well as request counts.
func healthyReply(w http.ResponseWriter, r *http.Request, query string) {
	data := make(map[string]string)
	for _, match := range queryAlias.FindAllStringSubmatch(query, -1) {
		data[match[1]] = `{"state":"OPEN"}`
	}
	body, err := json.Marshal(struct {
		Data      map[string]string `json:"data"`
		RateLimit rateLimitCost     `json:"rateLimit"`
	}{Data: data, RateLimit: rateLimitCost{Cost: 1}})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func watches(n int) []domain.Watch {
	out := make([]domain.Watch, n)
	for i := range out {
		out[i] = domain.Watch{
			ID:     fmt.Sprintf("w%d", i),
			Repo:   "refusedguy/beacon",
			PR:     i + 1,
			Status: domain.StatusWatching,
		}
	}
	return out
}

func TestCostFromRecordedResponse(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var (
		mu          sync.Mutex
		auth, ctype string
	)
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, query string) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		ctype = r.Header.Get("Content-Type")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"p0":{"state":"OPEN","headRefOid":"deadbeef"}},`+
			`"rateLimit":{"limit":5000,"cost":3,"remaining":4997,"resetAt":"2026-11-19T18:00:00Z"}}`)
	})

	client := NewClient(srv.URL, "read-only-token", srv.Client(), counter)
	if client.HTTPClient() == http.DefaultClient {
		t.Fatal("GraphQL client uses http.DefaultClient, want a dedicated client")
	}
	result, err := client.QueryTier1(context.Background(), watches(1))
	if err != nil {
		t.Fatalf("QueryTier1: %v", err)
	}
	if result.TotalCost != 3 {
		t.Fatalf("TotalCost = %d, want 3 from rateLimit.cost", result.TotalCost)
	}
	if got := counter.Used(); got != 3 {
		t.Fatalf("counter.Used = %d, want 3", got)
	}
	if raw, ok := result.Data["p0"]; !ok || !strings.Contains(string(raw), "deadbeef") {
		t.Fatalf("Data[p0] = %s, want the recorded PR", raw)
	}

	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer read-only-token" {
		t.Fatalf("Authorization = %q, want bearer token", auth)
	}
	if ctype != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ctype)
	}
}

func TestChunkCountsByTier(t *testing.T) {
	cases := []struct {
		name  string
		tier  Tier
		count int
		want  []int
	}{
		{name: "tier1 100 aliases is one chunk", tier: Tier1, count: 100, want: []int{100}},
		{name: "tier1 150 aliases is 100 and 50", tier: Tier1, count: 150, want: []int{50, 100}},
		{name: "tier2 100 aliases is two chunks of 50", tier: Tier2, count: 100, want: []int{50, 50}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter, _, _ := newTestCounter(t, 0)
			srv := newGraphQLServer(t, healthyReply)
			client := NewClient(srv.URL, "", srv.Client(), counter)

			result, err := client.Do(context.Background(), tc.tier, AliasesFromWatches(watches(tc.count)))
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			calls, sizes := srv.snapshot()
			if calls != len(tc.want) {
				t.Fatalf("requests = %d, want %d", calls, len(tc.want))
			}
			if fmt.Sprint(sizes) != fmt.Sprint(tc.want) {
				t.Fatalf("chunk sizes = %v, want %v", sizes, tc.want)
			}
			if len(result.Data) != tc.count {
				t.Fatalf("data entries = %d, want %d", len(result.Data), tc.count)
			}
			if result.TotalCost != len(tc.want) {
				t.Fatalf("TotalCost = %d, want %d", result.TotalCost, len(tc.want))
			}
		})
	}
}

func TestAliasesAreUniqueAndClosedWatchesAreSkipped(t *testing.T) {
	active := watches(3)
	dup := active[0]
	dup.ID = "another-watch"
	closed := domain.Watch{ID: "closed", Repo: "refusedguy/beacon", PR: 99, Status: domain.StatusClosed}

	aliases := AliasesFromWatches(append(append(active, dup), closed))
	if len(aliases) != 3 {
		t.Fatalf("aliases = %d, want 3 after dedupe and closed filter", len(aliases))
	}
	seen := map[string]bool{}
	for _, a := range aliases {
		if a.Name == "" {
			t.Fatal("empty alias name")
		}
		if seen[a.Name] {
			t.Fatalf("duplicate alias name %q", a.Name)
		}
		seen[a.Name] = true
	}
	if fmt.Sprint(aliasNames(aliases)) != fmt.Sprint([]string{"p0", "p1", "p2"}) {
		t.Fatalf("alias names = %v, want [p0 p1 p2]", aliasNames(aliases))
	}

	query := BuildQuery(Tier1, aliases)
	for name := range seen {
		if n := strings.Count(query, "  "+name+": repository("); n != 1 {
			t.Fatalf("alias %q appears %d times in query, want 1", name, n)
		}
	}

	counter, _, _ := newTestCounter(t, 0)
	srv := newGraphQLServer(t, healthyReply)
	client := NewClient(srv.URL, "", srv.Client(), counter)
	if _, err := client.Do(context.Background(), Tier1, nil); err != nil {
		t.Fatalf("Do with empty aliases: %v", err)
	}
	if calls, _ := srv.snapshot(); calls != 0 {
		t.Fatalf("empty aliases sent %d requests, want 0", calls)
	}
}

func aliasNames(aliases []Alias) []string {
	out := make([]string, len(aliases))
	for i, a := range aliases {
		out[i] = a.Name
	}
	return out
}

func TestParallelRequestsNeverExceedTwo(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var active, maxActive atomic.Int64
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, query string) {
		now := active.Add(1)
		for {
			seen := maxActive.Load()
			if now <= seen || maxActive.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		healthyReply(w, r, query)
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	if _, err := client.Do(context.Background(), Tier1, AliasesFromWatches(watches(500))); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got := maxActive.Load(); got > MaxParallel {
		t.Fatalf("max parallel requests = %d, want at most %d", got, MaxParallel)
	} else if got < 2 {
		t.Fatalf("max parallel requests = %d, want 2 to prove requests overlap", got)
	}
	if calls, sizes := srv.snapshot(); calls != 5 || fmt.Sprint(sizes) != fmt.Sprint([]int{100, 100, 100, 100, 100}) {
		t.Fatalf("requests = %d, sizes = %v, want five chunks of 100", calls, sizes)
	}
}

func TestQueryFieldsByTier(t *testing.T) {
	aliases := []Alias{{Name: "p0", Owner: "refusedguy", Repo: "beacon", PR: 42}}

	state := BuildQuery(Tier1, aliases)
	for _, want := range []string{"state", "isDraft", "headRefOid", "baseRefName", "mergeable", "mergeStateStatus", "reviewDecision", "commits(last:1)", "statusCheckRollup"} {
		if !strings.Contains(state, want) {
			t.Errorf("tier-1 query misses %q", want)
		}
	}
	if strings.Contains(state, "contexts(first:50)") {
		t.Error("tier-1 query must not fetch contexts")
	}

	contexts := BuildQuery(Tier2, aliases)
	for _, want := range []string{
		"contexts(first:50)", "... on CheckRun", "... on StatusContext",
		"isRequired(pullRequestNumber:42)", "checkSuite", "pageInfo", "endCursor",
	} {
		if !strings.Contains(contexts, want) {
			t.Errorf("tier-2 query misses %q", want)
		}
	}
	if strings.Contains(contexts, "reviewDecision") {
		t.Error("tier-2 query must not fetch tier-1 merge fields")
	}
}

func TestGraphQLPrimaryLimitBecomesRateLimited(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, query string) {
		w.Header().Set("x-ratelimit-remaining", "0")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":null,"errors":[{"message":"API rate limit exceeded","extensions":{"code":"RATE_LIMITED"}}],"rateLimit":{"cost":1}}`)
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	_, err := client.Do(context.Background(), Tier1, AliasesFromWatches(watches(1)))
	if err == nil {
		t.Fatal("Do succeeded on HTTP 200 with GraphQL rate-limit error")
	}
	var appErr *domain.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %T %v, want domain.AppError", err, err)
	}
	if appErr.Code != domain.CodeRateLimited {
		t.Fatalf("Code = %s, want %s", appErr.Code, domain.CodeRateLimited)
	}
}

// Real GitHub replies carry x-ratelimit-reset on every response. With quota
// left, the client must accept the answer instead of surfacing RATE_LIMITED on
// every cycle. This guards that production regression.
func TestGraphQLWindowResetIsNotRateLimited(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, query string) {
		w.Header().Set("x-ratelimit-reset", reset)
		w.Header().Set("x-ratelimit-remaining", "4999")
		healthyReply(w, r, query)
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	data, err := client.Do(context.Background(), Tier1, AliasesFromWatches(watches(2)))
	if err != nil {
		t.Fatalf("Do failed on a healthy response with a window reset: %v", err)
	}
	if data == nil {
		t.Fatal("Do returned nil data")
	}
}

func TestServerErrorsAreRetried(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var calls atomic.Int64
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, query string) {
		if calls.Add(1) == 1 {
			http.Error(w, "gateway timeout", http.StatusBadGateway)
			return
		}
		healthyReply(w, r, query)
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	client.attempts = 2
	client.normalizer = testNormalizer(time.Now(), 0)
	client.sleep = func(time.Duration) {}

	result, err := client.Do(context.Background(), Tier1, AliasesFromWatches(watches(1)))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (502 then success)", got)
	}
	if result.TotalCost != 1 {
		t.Fatalf("TotalCost = %d, want 1", result.TotalCost)
	}
}

func TestClientOwnsSeparateHTTPClient(t *testing.T) {
	rest := &http.Client{}
	fresh := NewClient("", "", nil, nil)
	if fresh.HTTPClient() == nil || fresh.HTTPClient() == http.DefaultClient {
		t.Fatal("default GraphQL client is nil or shared with http.DefaultClient")
	}
	if fresh.HTTPClient() == rest {
		t.Fatal("default GraphQL client shares the REST client")
	}
	if fresh.endpoint != DefaultEndpoint {
		t.Fatalf("endpoint = %q, want %q", fresh.endpoint, DefaultEndpoint)
	}

	injected := NewClient("https://example.test/graphql", "", rest, nil)
	if injected.HTTPClient() != rest {
		t.Fatal("explicit http.Client was not used")
	}
}
