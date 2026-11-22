package github

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseRepoSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"git@github.com:owner/repo.git", "owner/repo"},
		{"https://github.com/owner/repo.git", "owner/repo"},
		{"https://github.com/owner/repo", "owner/repo"},
		{"ssh://git@github.com/owner/repo.git", "owner/repo"},
		{"git://github.com/owner/repo.git", "owner/repo"},
		{"owner/repo", "owner/repo"},
		{"/owner/repo.git", "owner/repo"},
		{"", ""},
		{"owner", ""},
		{"owner/repo/extra", ""},
		{"not a url", ""},
	}
	for _, tc := range cases {
		if got := parseRepoSlug(tc.in); got != tc.want {
			t.Errorf("parseRepoSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDetectRepoSlugFromRemote(t *testing.T) {
	restore := gitRemoteURL
	t.Cleanup(func() { gitRemoteURL = restore })
	gitRemoteURL = func(string) (string, error) {
		return "git@github.com:harbor/harness.git", nil
	}
	if got := DetectRepoSlug(t.TempDir()); got != "harbor/harness" {
		t.Fatalf("DetectRepoSlug = %q, want harbor/harness", got)
	}
}

func TestDetectRepoSlugConfigOverridesRemote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("maxRebases: 4\nrepo: acme/widgets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := gitRemoteURL
	t.Cleanup(func() { gitRemoteURL = restore })
	gitRemoteURL = func(string) (string, error) {
		return "https://github.com/ignored/repo.git", nil
	}
	if got := DetectRepoSlug(root); got != "acme/widgets" {
		t.Fatalf("DetectRepoSlug = %q, want acme/widgets", got)
	}
}

func TestDetectRepoSlugCLIOverrideWins(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".beacon")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("repo: acme/widgets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := gitRemoteURL
	t.Cleanup(func() { gitRemoteURL = restore })
	gitRemoteURL = func(string) (string, error) {
		return "", errors.New("no remote")
	}
	if got := DetectRepoSlugWithOverride(root, "cli/flag"); got != "cli/flag" {
		t.Fatalf("DetectRepoSlugWithOverride = %q, want cli/flag", got)
	}
	if got := DetectRepoSlugWithOverride(root, "not-a-slug"); got != "acme/widgets" {
		t.Fatalf("invalid override must fall through: %q", got)
	}
}

func TestCanonicalForComparisonCorpus(t *testing.T) {
	// Six spellings from the beads remote.go corpus name one repository.
	forms := []string{
		"https://github.com/org/repo.git",
		"git+https://github.com/org/repo.git",
		"https://GitHub.com/org/repo",
		"https://user:pass@github.com/org/repo",
		"https://github.com/org/repo",
		"git+https://github.com/org/repo",
	}
	want := "git+https://github.com/org/repo"
	for _, raw := range forms {
		if got := CanonicalForComparison(raw); got != want {
			t.Errorf("CanonicalForComparison(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCanonicalForComparisonKeepsSchemesDistinct(t *testing.T) {
	http := CanonicalForComparison("http://github.com/org/repo")
	https := CanonicalForComparison("https://github.com/org/repo")
	if http == https {
		t.Fatalf("http and https folded into one value: %q", http)
	}
}

func TestCanonicalForComparisonStripsCredentials(t *testing.T) {
	withCreds := CanonicalForComparison("https://user:pass@github.com/org/repo.git")
	plain := CanonicalForComparison("https://github.com/org/repo")
	if withCreds != plain {
		t.Fatalf("credentials changed canonical form: %q vs %q", withCreds, plain)
	}
	if got := parseRepoSlug("https://user:pass@github.com/org/repo.git"); got != "org/repo" {
		t.Fatalf("parseRepoSlug with credentials = %q, want org/repo", got)
	}
}

func TestNormalizeLeavesNativeSchemesAlone(t *testing.T) {
	raw := "s3://bucket/team@prod"
	if got := Normalize(raw); got != raw {
		t.Fatalf("Normalize(%q) = %q, want unchanged", raw, got)
	}
}

func TestSCPGrammarLimitations(t *testing.T) {
	// The grammar intentionally declines these inputs and keeps their bytes.
	cases := []struct {
		raw  string
		want string
	}{
		{"github:org/repo.git", "github:org/repo.git"},                   // dotless alias
		{"usér@host.com:path", "usér@host.com:path"},                     // non-ASCII user
		{"git@bücher.example:repo", "git@bücher.example:repo"},           // IDN host
		{"git@host.com:", "git@host.com:"},                               // empty path
		{"host:pa@th", "host:pa@th"},                                     // @ after the colon
		{"s3://bucket/team@prod/db", "s3://bucket/team@prod/db"},         // scheme URL with @
		{`C:\Users\alice\beads`, `git+C:\Users\alice\beads`},             // Windows drive path
		{"github.com:org/repo.git", "git+ssh://github.com/org/repo.git"}, // user-less dotted host
	}
	for _, tc := range cases {
		if got := Normalize(tc.raw); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestDetectRepoSlugMissingRemoteIsEmpty(t *testing.T) {
	restore := gitRemoteURL
	t.Cleanup(func() { gitRemoteURL = restore })
	gitRemoteURL = func(string) (string, error) {
		return "", errors.New("no remote")
	}
	if got := DetectRepoSlug(t.TempDir()); got != "" {
		t.Fatalf("DetectRepoSlug = %q, want empty", got)
	}
}
