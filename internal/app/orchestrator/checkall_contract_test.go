package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// checkRunFragment returns the fields of the CheckRun inline fragment. It
// fails the test if the fragment is missing, so the field assertions cannot
// pass vacuously after a query rewrite.
func checkRunFragment(t *testing.T, query string) string {
	t.Helper()
	const marker = "... on CheckRun {"
	start := strings.Index(query, marker)
	if start < 0 {
		t.Fatalf("query has no CheckRun fragment:\n%s", query)
	}
	start += len(marker)
	end := strings.Index(query[start:], "}")
	if end < 0 {
		t.Fatalf("CheckRun fragment is not closed:\n%s", query)
	}
	return query[start : start+end]
}

// TestBuildChecksQueryCheckRunFields is the offline guard for the GraphQL
// schema bug: CheckRun has status and conclusion, but no state. Asking for
// state makes every `ci check --all` pass fail before it reads any check.
func TestBuildChecksQueryCheckRunFields(t *testing.T) {
	query := BuildChecksQuery([]PR{{Repo: "refusedguy/beacon", Number: 340}})
	fields := strings.Fields(checkRunFragment(t, query))

	present := make(map[string]bool, len(fields))
	for _, field := range fields {
		present[field] = true
	}
	if present["state"] {
		t.Fatalf("CheckRun fragment asks for nonexistent field state:\n%s", query)
	}
	for _, field := range []string{"conclusion", "status"} {
		if !present[field] {
			t.Errorf("CheckRun fragment is missing %s:\n%s", field, query)
		}
	}
}

// TestChecksQueryAgainstGitHub sends the exact document checkall builds to
// the live GitHub API. GraphQL validation errors come back in a 200 body, so
// the test reads errors instead of relying on the gh exit code alone.
func TestChecksQueryAgainstGitHub(t *testing.T) {
	if os.Getenv("BEACON_CONTRACT_TEST") == "" {
		t.Skip("set BEACON_CONTRACT_TEST=1 to run the live GraphQL contract test")
	}
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		t.Skipf("gh is unavailable: %v", err)
	}
	if os.Getenv("GH_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") == "" {
		auth := exec.Command(ghPath, "auth", "status")
		if out, err := auth.CombinedOutput(); err != nil {
			t.Skipf("gh is not authenticated: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}

	query := BuildChecksQuery([]PR{{Repo: "refusedguy/beacon", Number: 340}})
	out, err := exec.Command(ghPath, "api", "graphql", "-f", "query="+query).CombinedOutput()
	if err != nil {
		t.Fatalf("gh api graphql: %v\n%s", err, out)
	}
	var body graphQLChecksResponse
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("decode GraphQL response: %v\n%s", err, out)
	}
	if len(body.Errors) > 0 {
		t.Fatalf("GraphQL rejected the checkall query: %s", body.Errors[0].Message)
	}
	if _, ok := body.Data["p0"]; !ok {
		t.Fatalf("GraphQL response has no p0 alias:\n%s", out)
	}
}

// TestBuildChecksQueryRequestsRequired guards the source of Check.Required.
// Without isRequired every payload check would look informational.
func TestBuildChecksQueryRequestsRequired(t *testing.T) {
	query := BuildChecksQuery([]PR{{Repo: "refusedguy/beacon", Number: 340}})
	want := "isRequired(pullRequestNumber:340)"
	if !strings.Contains(query, want) {
		t.Fatalf("query misses %q:\n%s", want, query)
	}
}

// TestCheckFromNodeKeepsRequired checks the provider to Check step. A missing
// isRequired field stays nil so the writer can treat it as unknown.
func TestCheckFromNodeKeepsRequired(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		raw  string
		want *bool
	}{
		{"required", `{"__typename":"CheckRun","name":"build","isRequired":true}`, &yes},
		{"informational", `{"__typename":"CheckRun","name":"info","isRequired":false}`, &no},
		{"unknown", `{"__typename":"StatusContext","context":"lint","state":"FAILURE"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := checkFromNode([]byte(tc.raw))
			if !ok {
				t.Fatalf("checkFromNode rejected %s", tc.raw)
			}
			if (got.Required == nil) != (tc.want == nil) {
				t.Fatalf("Required = %v, want %v", got.Required, tc.want)
			}
			if tc.want != nil && *got.Required != *tc.want {
				t.Fatalf("Required = %v, want %v", *got.Required, *tc.want)
			}
		})
	}
}

// TestChecksPayloadKeepsRequired checks the writer step. The payload must
// carry both values from the provider, and an unknown source must not become
// mandatory.
func TestChecksPayloadKeepsRequired(t *testing.T) {
	yes, no := true, false
	raw, err := checksPayload([]Check{
		{Name: "build", Conclusion: "SUCCESS", Required: &yes},
		{Name: "info", Conclusion: "FAILURE", Required: &no},
		{Name: "unknown", Conclusion: "SUCCESS"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows map[string]struct {
		Required bool `json:"required"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if rows["build"].Required != true {
		t.Errorf("required check lost its flag: %s", raw)
	}
	if rows["info"].Required != false {
		t.Errorf("informational check became required: %s", raw)
	}
	if rows["unknown"].Required != false {
		t.Errorf("unknown source became required: %s", raw)
	}
}

// TestSQLVerdictWriterRejectsOutOfContractVerdict pins the write boundary. A
// verdict outside the domain contract must be rejected before the Watch is
// touched: its status stays watching and no obligation is staged.
func TestSQLVerdictWriterRejectsOutOfContractVerdict(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	const head = "sha-ooc"
	w := domain.Watch{
		ID:      "watch:refusedguy/beacon:9:" + head,
		Repo:    "refusedguy/beacon",
		PR:      9,
		HeadSHA: head,
		Status:  domain.StatusWatching,
		Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{
			{ID: "orch-1", Role: domain.RoleOrchestrator},
		},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatal(err)
	}

	ref := WatchRef{ID: w.ID, Repo: w.Repo, PR: w.PR, HeadSHA: w.HeadSHA}
	err = NewSQLVerdictWriter(db).Publish(context.Background(), ref, Verdict("out_of_contract"), nil)
	if err == nil || !strings.Contains(err.Error(), "вне доменного контракта") {
		t.Fatalf("Publish error = %v, want out-of-contract rejection", err)
	}
	if got := rawWatchStatus(t, db, w.ID); got != string(domain.StatusWatching) {
		t.Fatalf("status = %s, want watching: an out-of-contract verdict must not write", got)
	}
	if _, err := db.Obligation(obligationKey(w.ID, 1, "orch-1")); err == nil {
		t.Fatal("out-of-contract verdict staged an obligation")
	}
}
