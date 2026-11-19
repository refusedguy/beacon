package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// page1 is a recorded tier-2 response with both inline fragment types. The
// second page is signalled by hasNextPage and an endCursor.
const page1 = `{
  "data": {
    "p0": {
      "statusCheckRollup": {
        "contexts": {
          "nodes": [
            {
              "__typename": "CheckRun",
              "name": "ci/test",
              "status": "COMPLETED",
              "conclusion": "SUCCESS",
              "detailsUrl": "https://ci.test/runs/1",
              "isRequired": true,
              "checkSuite": {"id": "SUITE-1"}
            },
            {
              "__typename": "StatusContext",
              "context": "ci/legacy",
              "state": "SUCCESS",
              "targetUrl": "https://ci.test/contexts/2",
              "isRequired": false
            }
          ],
          "pageInfo": {"hasNextPage": true, "endCursor": "CUR-1"}
        }
      }
    }
  },
  "rateLimit": {"limit": 5000, "cost": 1, "remaining": 4999}
}`

const page2 = `{
  "data": {
    "p0": {
      "statusCheckRollup": {
        "contexts": {
          "nodes": [
            {
              "__typename": "CheckRun",
              "name": "ci/deploy",
              "status": "COMPLETED",
              "conclusion": "NEUTRAL",
              "detailsUrl": "https://ci.test/runs/3",
              "isRequired": true,
              "checkSuite": {"id": "SUITE-2"}
            }
          ],
          "pageInfo": {"hasNextPage": false, "endCursor": "CUR-2"}
        }
      }
    }
  },
  "rateLimit": {"limit": 5000, "cost": 1, "remaining": 4998}
}`

func TestTier2ParsesFragmentsAndRequired(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var (
		mu    sync.Mutex
		query string
	)
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, q string) {
		mu.Lock()
		query = q
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, strings.Replace(page1, `"hasNextPage": true, "endCursor": "CUR-1"`, `"hasNextPage": false, "endCursor": null`, 1))
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	batch, err := client.FetchTier2(context.Background(), watches(1))
	if err != nil {
		t.Fatalf("FetchTier2: %v", err)
	}
	if len(batch.PRs) != 1 {
		t.Fatalf("PRs = %d, want 1", len(batch.PRs))
	}
	pr := batch.PRs[0]
	if pr.Key() != "refusedguy/beacon#1" {
		t.Fatalf("Key = %q, want refusedguy/beacon#1", pr.Key())
	}

	want := map[string]domain.Check{
		"ci/test": {
			State:      "COMPLETED",
			Conclusion: "SUCCESS",
			Required:   true,
			DetailsURL: "https://ci.test/runs/1",
		},
		"ci/legacy": {
			State:      "SUCCESS",
			Required:   false,
			DetailsURL: "https://ci.test/contexts/2",
		},
	}
	if len(pr.Checks) != len(want) {
		t.Fatalf("checks = %v, want %v", pr.Checks, want)
	}
	for name, expected := range want {
		got, ok := pr.Checks[name]
		if !ok {
			t.Errorf("missing check %q", name)
			continue
		}
		if got != expected {
			t.Errorf("check %q = %+v, want %+v", name, got, expected)
		}
	}

	if len(pr.Suites) != 2 {
		t.Fatalf("suites = %v, want two entries", pr.Suites)
	}
	if pr.Suites[0] != (ContextSuite{Name: "ci/test", SuiteID: "SUITE-1"}) {
		t.Errorf("suites[0] = %+v, want suite SUITE-1 for ci/test", pr.Suites[0])
	}
	if pr.Suites[1] != (ContextSuite{Name: "ci/legacy", SuiteID: ""}) {
		t.Errorf("suites[1] = %+v, StatusContext has no checkSuite", pr.Suites[1])
	}
	if batch.Cost != 1 {
		t.Fatalf("Cost = %d, want 1 from rateLimit.cost", batch.Cost)
	}
	if got := counter.Used(); got != 1 {
		t.Fatalf("counter.Used = %d, want 1", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(query, "... on CheckRun") || !strings.Contains(query, "... on StatusContext") {
		t.Errorf("query misses inline fragments: %s", query)
	}
	if !strings.Contains(query, "isRequired(pullRequestNumber:1)") {
		t.Errorf("query misses isRequired with the PR number: %s", query)
	}
}

func TestTier2FollowsPagination(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var (
		mu          sync.Mutex
		secondQuery string
	)
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, q string) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(q, "after:") {
			secondQuery = q
			fmt.Fprint(w, page2)
			return
		}
		fmt.Fprint(w, page1)
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	batch, err := client.FetchTier2(context.Background(), watches(1))
	if err != nil {
		t.Fatalf("FetchTier2: %v", err)
	}
	calls, sizes := srv.snapshot()
	if calls != 2 || fmt.Sprint(sizes) != fmt.Sprint([]int{1, 1}) {
		t.Fatalf("requests = %d, sizes = %v, want two requests for one alias", calls, sizes)
	}

	mu.Lock()
	defer mu.Unlock()
	if secondQuery == "" {
		t.Fatal("no continuation request was sent for hasNextPage")
	}
	for _, want := range []string{`contexts(first:50, after:"CUR-1")`, "isRequired(pullRequestNumber:1)", "... on CheckRun"} {
		if !strings.Contains(secondQuery, want) {
			t.Errorf("continuation query misses %q: %s", want, secondQuery)
		}
	}

	pr := batch.PRs[0]
	for _, name := range []string{"ci/test", "ci/legacy", "ci/deploy"} {
		if _, ok := pr.Checks[name]; !ok {
			t.Errorf("missing check %q after following pageInfo", name)
		}
	}
	if len(pr.Suites) != 3 {
		t.Fatalf("suites = %v, want three entries across both pages", pr.Suites)
	}
	if got := pr.Checks["ci/deploy"]; got.Required != true || got.Conclusion != "NEUTRAL" {
		t.Errorf("ci/deploy = %+v, want required NEUTRAL from second page", got)
	}
	if batch.Cost != 2 {
		t.Fatalf("Cost = %d, want 2 (one per request)", batch.Cost)
	}
	if got := counter.Used(); got != 2 {
		t.Fatalf("counter.Used = %d, want 2", got)
	}
}

func TestTier2FragmentsStayInsideNodes(t *testing.T) {
	aliases := []Alias{{Name: "p0", Owner: "refusedguy", Repo: "beacon", PR: 42}}
	queries := map[string]string{
		"first page": BuildQuery(Tier2, aliases),
		"next page":  BuildTier2PageQuery(aliases[0], "CUR-1"),
	}

	for label, query := range queries {
		t.Run(label, func(t *testing.T) {
			rollup := strings.Index(query, "statusCheckRollup {")
			if rollup < 0 {
				t.Fatalf("query misses statusCheckRollup: %s", query)
			}
			insideRollup := strings.TrimSpace(query[rollup+len("statusCheckRollup {"):])
			if strings.HasPrefix(insideRollup, "... on") {
				t.Errorf("fragment spread directly on StatusCheckRollup: %s", insideRollup)
			}
			nodes := strings.Index(query, "nodes {")
			if nodes < 0 {
				t.Fatalf("query misses contexts.nodes: %s", query)
			}
			for _, fragment := range []string{"... on CheckRun", "... on StatusContext"} {
				at := strings.Index(query, fragment)
				if at < nodes {
					t.Errorf("fragment %q appears before nodes", fragment)
				}
			}
			if !strings.Contains(query, "contexts(first:50)") {
				t.Errorf("query misses contexts(first:50): %s", query)
			}
			if !strings.Contains(query, "isRequired(pullRequestNumber:42)") {
				t.Errorf("query misses isRequired(pullRequestNumber:42): %s", query)
			}
			if !strings.Contains(query, "checkSuite") {
				t.Errorf("query misses checkSuite: %s", query)
			}
		})
	}

	if !strings.Contains(queries["next page"], `after:"CUR-1"`) {
		t.Errorf("continuation query misses the cursor: %s", queries["next page"])
	}
}

func TestTier2CannotSpreadFragment(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, q string) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":null,"errors":[{"message":"Fragment on CheckRun can't be spread inside StatusCheckRollup",`+
			`"extensions":{"code":"cannotSpreadFragment","typeName":"CheckRun","parentName":"StatusCheckRollup"}}],`+
			`"rateLimit":{"cost":1}}`)
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	_, err := client.FetchTier2(context.Background(), watches(1))
	if err == nil {
		t.Fatal("FetchTier2 succeeded on a cannotSpreadFragment response")
	}
	var spread *FragmentSpreadError
	if !errors.As(err, &spread) {
		t.Fatalf("error = %T %v, want *FragmentSpreadError", err, err)
	}
	if spread.Code != codeCannotSpreadFragment {
		t.Fatalf("Code = %q, want %q", spread.Code, codeCannotSpreadFragment)
	}
}

func TestTier2ChunkCostIsOne(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, q string) {
		data := make(map[string]string)
		for _, match := range queryAlias.FindAllStringSubmatch(q, -1) {
			data[match[1]] = `{"statusCheckRollup":null}`
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
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	batch, err := client.FetchTier2(context.Background(), watches(50))
	if err != nil {
		t.Fatalf("FetchTier2: %v", err)
	}
	calls, sizes := srv.snapshot()
	if calls != 1 || fmt.Sprint(sizes) != fmt.Sprint([]int{50}) {
		t.Fatalf("requests = %d, sizes = %v, want one chunk of 50 aliases", calls, sizes)
	}
	if batch.Cost != 1 {
		t.Fatalf("Cost = %d, want 1 for the chunk", batch.Cost)
	}
	if got := counter.Used(); got != 1 {
		t.Fatalf("counter.Used = %d, want 1", got)
	}
	if len(batch.PRs) != 50 {
		t.Fatalf("PRs = %d, want 50", len(batch.PRs))
	}
	for _, pr := range batch.PRs {
		if len(pr.Checks) != 0 || len(pr.Suites) != 0 {
			t.Fatalf("null rollup produced checks for %s", pr.Key())
		}
	}
}
