package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoQueryRunner records the GraphQL document so a test can see which owner
// and name the checkall source resolved.
type repoQueryRunner struct {
	queries []string
}

func (r *repoQueryRunner) run(_, name string, args, _ []string) runResult {
	if name != "gh" {
		return runResult{code: 2, stderr: []byte("unexpected command " + name)}
	}
	if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
		return runResult{stdout: []byte(`[{"number":7,"headRefOid":"sha7"}]`)}
	}
	if len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
		for _, arg := range args {
			if strings.HasPrefix(arg, "query=") {
				r.queries = append(r.queries, arg)
			}
		}
		return runResult{stdout: []byte(`{"data":{}}`)}
	}
	return runResult{code: 2, stderr: []byte("unexpected gh call: " + strings.Join(args, " "))}
}

// hasRepositoryQuery accepts any number of GraphQL documents, since the CI
// source may add aliases. The owner and name must match exactly in one of them.
func hasRepositoryQuery(queries []string, owner, name string) bool {
	want := `repository(owner:"` + owner + `", name:"` + name + `")`
	for _, query := range queries {
		if strings.Contains(query, want) {
			return true
		}
	}
	return false
}

func TestCICheckUsesRepoFlag(t *testing.T) {
	t.Setenv("BEACON_STATE_DIR", t.TempDir())
	t.Setenv("BEACON_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	runner := &repoQueryRunner{}
	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: runner.run, root: t.TempDir()}
	if code := runCI(cli, []string{"check", "--all", "--repo", "cli/flag"}); code != 0 {
		t.Fatalf("runCI code = %d, stderr = %s", code, errOut.String())
	}
	if !hasRepositoryQuery(runner.queries, "cli", "flag") {
		t.Fatalf("queries = %v, want owner cli and name flag", runner.queries)
	}
}

func TestCICheckUsesRepoFromProjectConfig(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("repo: acme/widgets\nmaxRebases: 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEACON_PROJECT_ROOT", root)
	t.Setenv("BEACON_STATE_DIR", t.TempDir())
	t.Setenv("BEACON_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))

	runner := &repoQueryRunner{}
	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: runner.run, root: root}
	if code := runCI(cli, []string{"check", "--all"}); code != 0 {
		t.Fatalf("runCI code = %d, stderr = %s", code, errOut.String())
	}
	if !hasRepositoryQuery(runner.queries, "acme", "widgets") {
		t.Fatalf("queries = %v, want owner acme and name widgets", runner.queries)
	}
}
