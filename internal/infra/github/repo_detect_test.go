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
