package orchestrator

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
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
