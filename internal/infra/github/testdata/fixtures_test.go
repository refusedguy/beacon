// Package testdata holds recorded GitHub GraphQL and REST responses for the
// verdict edge cases required by T2.16. This test is self-contained: its
// structs mirror the T2.11 GraphQL response contract, and the verdict table is
// reimplemented locally so the test never imports symbols from other files.
//
// The directory is named testdata, so "go test ./..." skips it. Run this test
// explicitly with: go test ./internal/infra/github/testdata
package testdata

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// graphQLResponse mirrors the T2.11 body: data keyed by alias, optional errors
// and rateLimit.cost. Every fixture must parse into this shape with cost >= 1.
type graphQLResponse struct {
	Data      map[string]json.RawMessage `json:"data"`
	Errors    []json.RawMessage          `json:"errors"`
	RateLimit struct {
		Cost int `json:"cost"`
	} `json:"rateLimit"`
}

// tier1PR mirrors the tier-1 pullRequest fields from WORKER-PROMPT 4.6.
type tier1PR struct {
	State            string `json:"state"`
	IsDraft          bool   `json:"isDraft"`
	HeadRefOID       string `json:"headRefOid"`
	BaseRefName      string `json:"baseRefName"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
	Commits          struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// tier2PR mirrors the tier-2 statusCheckRollup.contexts nodes, including
// isRequired and the CheckRun / StatusContext fragments.
type tier2PR struct {
	StatusCheckRollup struct {
		Contexts struct {
			Nodes []struct {
				Typename   string `json:"__typename"`
				Name       string `json:"name"`
				Context    string `json:"context"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				State      string `json:"state"`
				IsRequired bool   `json:"isRequired"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `json:"contexts"`
	} `json:"statusCheckRollup"`
}

type restWorkflowRun struct {
	HeadSHA      string            `json:"head_sha"`
	PullRequests []json.RawMessage `json:"pull_requests"`
}

type restBranchProtection struct {
	RequiredStatusChecks struct {
		Strict   bool     `json:"strict"`
		Contexts []string `json:"contexts"`
	} `json:"required_status_checks"`
}

type restRef struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type caseDef struct {
	ID          string `json:"id"`
	Case        string `json:"case"`
	Description string `json:"description"`
	Watch       struct {
		Repo    string `json:"repo"`
		PR      int    `json:"pr"`
		HeadSHA string `json:"headSHA"`
	} `json:"watch"`
	Policy struct {
		RequireUpToDate bool `json:"requireUpToDate"`
		IgnoreReviews   bool `json:"ignoreReviews"`
	} `json:"policy"`
	Responses struct {
		Tier1 string    `json:"tier1"`
		Tier2 string    `json:"tier2"`
		REST  []restRef `json:"rest"`
	} `json:"responses"`
	Expected struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
		Binding string `json:"binding"`
	} `json:"expected"`
}

type manifest struct {
	Cases []caseDef `json:"cases"`
}

func loadManifest(t *testing.T) manifest {
	t.Helper()
	b, err := os.ReadFile("cases.json")
	if err != nil {
		t.Fatalf("read cases.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode cases.json: %v", err)
	}
	return m
}

// loadPR reads a recorded GraphQL response, checks it against the T2.11 body
// contract, and decodes data.p0 into out.
func loadPR(t *testing.T, path string, out any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var resp graphQLResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("%s has GraphQL errors: %s", path, resp.Errors)
	}
	if resp.RateLimit.Cost < 1 {
		t.Fatalf("%s rateLimit.cost = %d, want >= 1", path, resp.RateLimit.Cost)
	}
	raw, ok := resp.Data["p0"]
	if !ok {
		t.Fatalf("%s data has no p0 alias", path)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode data.p0 of %s: %v", path, err)
	}
}

func loadREST(t *testing.T, path, kind string) (*restWorkflowRun, *restBranchProtection) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	switch kind {
	case "workflow_run":
		var r restWorkflowRun
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return &r, nil
	case "branch_protection":
		var r restBranchProtection
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return nil, &r
	default:
		t.Fatalf("unknown REST kind %q for %s", kind, path)
		return nil, nil
	}
}

type decision struct {
	verdict string
	reason  string
}

// decide is a local reference implementation of the WORKER-PROMPT 4.6 verdict
// table, limited to the branches these fixtures exercise.
func decide(c caseDef, t1 tier1PR, t2 *tier2PR, bp *restBranchProtection) decision {
	if t1.State == "MERGED" {
		return decision{"merged", ""}
	}
	if t1.State == "CLOSED" {
		return decision{"closed", ""}
	}
	if t1.HeadRefOID != c.Watch.HeadSHA {
		return decision{"head_changed", "head sha changed"}
	}
	if t1.MergeStateStatus == "DIRTY" {
		return decision{"rebase", "merge conflict"}
	}
	if t1.MergeStateStatus == "BEHIND" && c.Policy.RequireUpToDate {
		return decision{"rebase", "behind and requireUpToDate"}
	}
	if t2 != nil {
		if t2.StatusCheckRollup.Contexts.PageInfo.HasNextPage {
			return decision{"watching", "contexts paginated"}
		}
		if pendingReasons(t2) {
			return decision{"watching", "checks pending"}
		}
	}
	if bp != nil && t2 != nil {
		if missing := missingRequired(t2, bp); len(missing) > 0 {
			return decision{"timeout", "required checks not created: " + strings.Join(missing, ",")}
		}
	}
	rollup := ""
	if len(t1.Commits.Nodes) > 0 {
		rollup = t1.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	if rollup == "FAILURE" || rollup == "ERROR" {
		return decision{"keep_working", "checks failed"}
	}
	if rollup == "SUCCESS" && t1.Mergeable == "MERGEABLE" &&
		(t1.MergeStateStatus == "CLEAN" || t1.MergeStateStatus == "HAS_HOOKS" ||
			(t1.MergeStateStatus == "BEHIND" && !c.Policy.RequireUpToDate)) &&
		!t1.IsDraft && (c.Policy.IgnoreReviews || t1.ReviewDecision != "CHANGES_REQUESTED") {
		return decision{"ready", ""}
	}
	if t1.IsDraft {
		return decision{"watching", "draft"}
	}
	if t1.Mergeable == "UNKNOWN" {
		return decision{"watching", "mergeable unknown"}
	}
	return decision{"watching", "watching"}
}

// pendingReasons reports ACTION_REQUIRED, WAITING, QUEUED, IN_PROGRESS and
// pending StatusContext states. SKIPPED and NEUTRAL are not pending.
func pendingReasons(t2 *tier2PR) bool {
	for _, n := range t2.StatusCheckRollup.Contexts.Nodes {
		switch {
		case n.Typename == "CheckRun" && n.Status == "QUEUED",
			n.Typename == "CheckRun" && n.Status == "WAITING",
			n.Typename == "CheckRun" && n.Status == "IN_PROGRESS",
			n.Typename == "CheckRun" && n.Status == "COMPLETED" && n.Conclusion == "ACTION_REQUIRED",
			n.Typename == "StatusContext" && n.State == "PENDING":
			return true
		}
	}
	return false
}

// missingRequired returns required contexts from branch protection that are
// not present in the recorded rollup contexts.
func missingRequired(t2 *tier2PR, bp *restBranchProtection) []string {
	present := make(map[string]bool)
	for _, n := range t2.StatusCheckRollup.Contexts.Nodes {
		if !n.IsRequired {
			continue
		}
		switch n.Typename {
		case "CheckRun":
			present[n.Name] = true
		case "StatusContext":
			present[n.Context] = true
		}
	}
	var missing []string
	for _, ctx := range bp.RequiredStatusChecks.Contexts {
		if !present[ctx] {
			missing = append(missing, ctx)
		}
	}
	sort.Strings(missing)
	return missing
}

func findCase(t *testing.T, id string) caseDef {
	t.Helper()
	for _, c := range loadManifest(t).Cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no fixture with id %q", id)
	return caseDef{}
}

func casesOf(t *testing.T, name string) []caseDef {
	t.Helper()
	var out []caseDef
	for _, c := range loadManifest(t).Cases {
		if c.Case == name {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no fixtures for case %q", name)
	}
	return out
}

// TestAllCasesPresent enforces the eight cases from the T2.16 card. BEHIND is
// one case recorded twice, once for each requireUpToDate policy.
func TestAllCasesPresent(t *testing.T) {
	m := loadManifest(t)
	want := []string{"fork", "missing-required", "skipped-neutral", "pending-states", "draft", "behind", "merged", "closed"}
	have := make(map[string]bool, len(m.Cases))
	for _, c := range m.Cases {
		have[c.Case] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("missing case %q", name)
		}
	}
	if len(have) != len(want) {
		t.Errorf("distinct cases = %d, want %d: %v", len(have), len(want), have)
	}
}

// TestFixtures loads every fixture, validates the recorded responses against
// the T2.11 parse contract, and compares the expected verdict with the local
// reference verdict table.
func TestFixtures(t *testing.T) {
	m := loadManifest(t)
	if len(m.Cases) != 9 {
		t.Fatalf("fixture count = %d, want 9 (8 cases, BEHIND twice)", len(m.Cases))
	}
	for _, c := range m.Cases {
		t.Run(c.ID, func(t *testing.T) {
			var t1 tier1PR
			loadPR(t, c.Responses.Tier1, &t1)
			var t2 *tier2PR
			if c.Responses.Tier2 != "" {
				t2 = &tier2PR{}
				loadPR(t, c.Responses.Tier2, t2)
			}
			var bp *restBranchProtection
			for _, r := range c.Responses.REST {
				_, parsed := loadREST(t, r.Path, r.Kind)
				if parsed != nil {
					bp = parsed
				}
			}
			got := decide(c, t1, t2, bp)
			if got.verdict != c.Expected.Verdict {
				t.Errorf("verdict = %q, want %q", got.verdict, c.Expected.Verdict)
			}
			if got.reason != c.Expected.Reason {
				t.Errorf("reason = %q, want %q", got.reason, c.Expected.Reason)
			}
		})
	}
}

// TestForkBindsByHeadSHA proves the fork fixture needs no PR number: its REST
// pull_requests is empty, and the GraphQL headRefOid matches the watch SHA.
func TestForkBindsByHeadSHA(t *testing.T) {
	c := findCase(t, "fork")
	var t1 tier1PR
	loadPR(t, c.Responses.Tier1, &t1)
	if t1.HeadRefOID != c.Watch.HeadSHA {
		t.Fatalf("fork headRefOid = %s, watch headSHA = %s", t1.HeadRefOID, c.Watch.HeadSHA)
	}
	if len(c.Responses.REST) != 1 {
		t.Fatalf("fork REST responses = %d, want 1", len(c.Responses.REST))
	}
	wr, _ := loadREST(t, c.Responses.REST[0].Path, c.Responses.REST[0].Kind)
	if wr == nil {
		t.Fatal("fork REST response is not a workflow run")
	}
	if len(wr.PullRequests) != 0 {
		t.Fatalf("fork pull_requests = %d, want 0", len(wr.PullRequests))
	}
	if wr.HeadSHA != c.Watch.HeadSHA {
		t.Fatalf("workflow run head_sha = %s, watch headSHA = %s", wr.HeadSHA, c.Watch.HeadSHA)
	}
	if c.Expected.Binding != "headSHA" {
		t.Fatalf("fork binding = %q, want headSHA", c.Expected.Binding)
	}
	if c.Expected.Verdict != "ready" {
		t.Fatalf("fork verdict = %q, want ready", c.Expected.Verdict)
	}
}

// TestBehindBothPolicies proves BEHIND is checked with both policy values.
func TestBehindBothPolicies(t *testing.T) {
	got := make(map[bool]string)
	for _, c := range casesOf(t, "behind") {
		got[c.Policy.RequireUpToDate] = c.Expected.Verdict
	}
	if got[true] != "rebase" {
		t.Errorf("behind requireUpToDate=true verdict = %q, want rebase", got[true])
	}
	if got[false] != "ready" {
		t.Errorf("behind requireUpToDate=false verdict = %q, want ready", got[false])
	}
}
