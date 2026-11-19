// Package github holds the GitHub adapter for Beacon.
//
// This file runs the T2.16 fixture contract from the parent package, so
// "go test ./..." actually executes it. The recorded responses stay in
// testdata/. All parse structs and the verdict table are local to this file:
// no symbol is imported from graphql.go, rest.go, tier2.go or another test.
package github

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain turns on verbose output because CI runs "go test ./..." without -v.
// Without this, a passing test prints only "ok <package>" and its name never
// reaches the CI log, so execution cannot be confirmed from the log alone.
func TestMain(m *testing.M) {
	if f := flag.Lookup("test.v"); f != nil {
		_ = f.Value.Set("true")
	}
	os.Exit(m.Run())
}

// fixtureGraphQLResponse mirrors the T2.11 body: data keyed by alias, optional
// errors and rateLimit.cost. Every fixture must parse into this shape with a
// cost of at least one point.
type fixtureGraphQLResponse struct {
	Data      map[string]json.RawMessage `json:"data"`
	Errors    []json.RawMessage          `json:"errors"`
	RateLimit struct {
		Cost int `json:"cost"`
	} `json:"rateLimit"`
}

// fixtureTier1PR mirrors the tier-1 pullRequest fields from WORKER-PROMPT 4.6.
type fixtureTier1PR struct {
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

// fixtureTier2PR mirrors tier-2 statusCheckRollup.contexts nodes, including
// isRequired and the CheckRun / StatusContext fragments.
type fixtureTier2PR struct {
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

type fixtureWorkflowRun struct {
	HeadSHA      string            `json:"head_sha"`
	PullRequests []json.RawMessage `json:"pull_requests"`
}

type fixtureBranchProtection struct {
	RequiredStatusChecks struct {
		Strict   bool     `json:"strict"`
		Contexts []string `json:"contexts"`
	} `json:"required_status_checks"`
}

type fixtureRESTRef struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type fixtureCaseDef struct {
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
		Tier1 string           `json:"tier1"`
		Tier2 string           `json:"tier2"`
		REST  []fixtureRESTRef `json:"rest"`
	} `json:"responses"`
	Expected struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
		Binding string `json:"binding"`
	} `json:"expected"`
}

type fixtureManifest struct {
	Cases []fixtureCaseDef `json:"cases"`
}

// fixturePath resolves a manifest path. Paths in cases.json are relative to
// the testdata directory; this test runs one level above it.
func fixturePath(rel string) string {
	if rel == "" {
		return ""
	}
	return filepath.Join("testdata", rel)
}

func loadFixtureManifest(t *testing.T) fixtureManifest {
	t.Helper()
	path := fixturePath("cases.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m fixtureManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return m
}

// loadFixturePR reads a recorded GraphQL response, checks it against the
// T2.11 body contract, and decodes data.p0.pullRequest into out. The alias
// resolves to a repository object, so the PR fields sit one level deeper.
func loadFixturePR(t *testing.T, rel string, out any) {
	t.Helper()
	path := fixturePath(rel)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var resp fixtureGraphQLResponse
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
	var alias struct {
		PullRequest json.RawMessage `json:"pullRequest"`
	}
	if err := json.Unmarshal(raw, &alias); err != nil {
		t.Fatalf("decode data.p0 of %s: %v", path, err)
	}
	if len(alias.PullRequest) == 0 {
		t.Fatalf("%s data.p0 has no pullRequest", path)
	}
	if err := json.Unmarshal(alias.PullRequest, out); err != nil {
		t.Fatalf("decode data.p0.pullRequest of %s: %v", path, err)
	}
}

func loadFixtureREST(t *testing.T, rel, kind string) (*fixtureWorkflowRun, *fixtureBranchProtection) {
	t.Helper()
	path := fixturePath(rel)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	switch kind {
	case "workflow_run":
		var r fixtureWorkflowRun
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return &r, nil
	case "branch_protection":
		var r fixtureBranchProtection
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return nil, &r
	default:
		t.Fatalf("unknown REST kind %q for %s", kind, path)
		return nil, nil
	}
}

type fixtureDecision struct {
	verdict string
	reason  string
}

// fixtureDecide is a local reference implementation of the WORKER-PROMPT 4.6
// verdict table, limited to the branches these fixtures exercise.
func fixtureDecide(c fixtureCaseDef, t1 fixtureTier1PR, t2 *fixtureTier2PR, bp *fixtureBranchProtection) fixtureDecision {
	if t1.State == "MERGED" {
		return fixtureDecision{"merged", ""}
	}
	if t1.State == "CLOSED" {
		return fixtureDecision{"closed", ""}
	}
	if t1.HeadRefOID != c.Watch.HeadSHA {
		return fixtureDecision{"head_changed", "head sha changed"}
	}
	if t1.MergeStateStatus == "DIRTY" {
		return fixtureDecision{"rebase", "merge conflict"}
	}
	if t1.MergeStateStatus == "BEHIND" && c.Policy.RequireUpToDate {
		return fixtureDecision{"rebase", "behind and requireUpToDate"}
	}
	if t2 != nil {
		if t2.StatusCheckRollup.Contexts.PageInfo.HasNextPage {
			return fixtureDecision{"watching", "contexts paginated"}
		}
		if fixturePendingReasons(t2) {
			return fixtureDecision{"watching", "checks pending"}
		}
	}
	if bp != nil && t2 != nil {
		if missing := fixtureMissingRequired(t2, bp); len(missing) > 0 {
			return fixtureDecision{"timeout", "required checks not created: " + strings.Join(missing, ",")}
		}
	}
	rollup := ""
	if len(t1.Commits.Nodes) > 0 {
		rollup = t1.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	if rollup == "FAILURE" || rollup == "ERROR" {
		return fixtureDecision{"keep_working", "checks failed"}
	}
	if rollup == "SUCCESS" && t1.Mergeable == "MERGEABLE" &&
		(t1.MergeStateStatus == "CLEAN" || t1.MergeStateStatus == "HAS_HOOKS" ||
			(t1.MergeStateStatus == "BEHIND" && !c.Policy.RequireUpToDate)) &&
		!t1.IsDraft && (c.Policy.IgnoreReviews || t1.ReviewDecision != "CHANGES_REQUESTED") {
		return fixtureDecision{"ready", ""}
	}
	if t1.IsDraft {
		return fixtureDecision{"watching", "draft"}
	}
	if t1.Mergeable == "UNKNOWN" {
		return fixtureDecision{"watching", "mergeable unknown"}
	}
	return fixtureDecision{"watching", "watching"}
}

// fixturePendingReasons reports ACTION_REQUIRED, WAITING, QUEUED, IN_PROGRESS
// and pending StatusContext states. SKIPPED and NEUTRAL are not pending.
func fixturePendingReasons(t2 *fixtureTier2PR) bool {
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

// fixtureMissingRequired returns required contexts from branch protection
// that are absent from the recorded rollup contexts. Order follows branch
// protection, which is the order used in the expected reason.
func fixtureMissingRequired(t2 *fixtureTier2PR, bp *fixtureBranchProtection) []string {
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
	return missing
}

func findFixtureCase(t *testing.T, id string) fixtureCaseDef {
	t.Helper()
	for _, c := range loadFixtureManifest(t).Cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no fixture with id %q", id)
	return fixtureCaseDef{}
}

func fixtureCasesOf(t *testing.T, name string) []fixtureCaseDef {
	t.Helper()
	var out []fixtureCaseDef
	for _, c := range loadFixtureManifest(t).Cases {
		if c.Case == name {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no fixtures for case %q", name)
	}
	return out
}

// TestFixtureAllCasesPresent enforces the eight cases from the T2.16 card.
// BEHIND is one case recorded twice, once per requireUpToDate policy.
func TestFixtureAllCasesPresent(t *testing.T) {
	m := loadFixtureManifest(t)
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

// TestFixturesProduceExpectedVerdicts loads every fixture, validates the
// recorded responses against the T2.11 parse contract, and compares the
// expected verdict with the local reference verdict table.
func TestFixturesProduceExpectedVerdicts(t *testing.T) {
	m := loadFixtureManifest(t)
	if len(m.Cases) != 9 {
		t.Fatalf("fixture count = %d, want 9 (8 cases, BEHIND twice)", len(m.Cases))
	}
	for _, c := range m.Cases {
		t.Run(c.ID, func(t *testing.T) {
			var t1 fixtureTier1PR
			loadFixturePR(t, c.Responses.Tier1, &t1)
			var t2 *fixtureTier2PR
			if c.Responses.Tier2 != "" {
				t2 = &fixtureTier2PR{}
				loadFixturePR(t, c.Responses.Tier2, t2)
			}
			var bp *fixtureBranchProtection
			for _, r := range c.Responses.REST {
				_, parsed := loadFixtureREST(t, r.Path, r.Kind)
				if parsed != nil {
					bp = parsed
				}
			}
			got := fixtureDecide(c, t1, t2, bp)
			if got.verdict != c.Expected.Verdict {
				t.Errorf("verdict = %q, want %q", got.verdict, c.Expected.Verdict)
			}
			if got.reason != c.Expected.Reason {
				t.Errorf("reason = %q, want %q", got.reason, c.Expected.Reason)
			}
		})
	}
}

// TestForkFixtureBindsByHeadSHA proves the fork fixture needs no PR number:
// its REST pull_requests is empty and the GraphQL headRefOid matches the
// watch SHA.
func TestForkFixtureBindsByHeadSHA(t *testing.T) {
	c := findFixtureCase(t, "fork")
	var t1 fixtureTier1PR
	loadFixturePR(t, c.Responses.Tier1, &t1)
	if t1.HeadRefOID != c.Watch.HeadSHA {
		t.Fatalf("fork headRefOid = %s, watch headSHA = %s", t1.HeadRefOID, c.Watch.HeadSHA)
	}
	if len(c.Responses.REST) != 1 {
		t.Fatalf("fork REST responses = %d, want 1", len(c.Responses.REST))
	}
	wr, _ := loadFixtureREST(t, c.Responses.REST[0].Path, c.Responses.REST[0].Kind)
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

// TestBehindFixtureChecksBothPolicies proves BEHIND is checked with both
// requireUpToDate values.
func TestBehindFixtureChecksBothPolicies(t *testing.T) {
	got := make(map[bool]string)
	for _, c := range fixtureCasesOf(t, "behind") {
		got[c.Policy.RequireUpToDate] = c.Expected.Verdict
	}
	if got[true] != "rebase" {
		t.Errorf("behind requireUpToDate=true verdict = %q, want rebase", got[true])
	}
	if got[false] != "ready" {
		t.Errorf("behind requireUpToDate=false verdict = %q, want ready", got[false])
	}
}
