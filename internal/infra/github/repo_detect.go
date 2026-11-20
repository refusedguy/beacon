// Package github's repo detection resolves the "owner/name" slug of the
// project a Beacon process serves. No caller passes a hardcoded slug: the
// project config wins over the git remote, and an explicit CLI override wins
// over both.
package github

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// gitRemoteURL is the process seam for reading the origin remote. Tests replace
// it so detection never needs a real repository or a network.
var gitRemoteURL = func(projectRoot string) (string, error) {
	out, err := exec.Command("git", "-C", projectRoot, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DetectRepoSlug resolves the repository slug for projectRoot. The project's
// .beacon/config.yaml key `repo` overrides the git remote. An empty result is
// not an error: a caller can report a clear "specify --repo" message, and a
// process that has no slug yet keeps starting.
func DetectRepoSlug(projectRoot string) string {
	return DetectRepoSlugWithOverride(projectRoot, "")
}

// DetectRepoSlugWithOverride resolves the slug with an explicit override from
// the CLI. Precedence is override, project config, git remote origin.
func DetectRepoSlugWithOverride(projectRoot, override string) string {
	if slug := parseRepoSlug(override); slug != "" {
		return slug
	}
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		root = "."
	}
	if slug := repoFromProjectConfig(root); slug != "" {
		return slug
	}
	raw, err := gitRemoteURL(root)
	if err != nil {
		return ""
	}
	return parseRepoSlug(raw)
}

// repoFromProjectConfig reads the `repo:` key from <root>/.beacon/config.yaml.
// A missing or malformed file leaves detection to the git remote.
func repoFromProjectConfig(root string) string {
	path := filepath.Join(root, ".beacon", "config.yaml")
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return ""
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	for key, raw := range doc {
		if strings.ToLower(strings.TrimSpace(key)) != "repo" {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return ""
		}
		return parseRepoSlug(value)
	}
	return ""
}

// parseRepoSlug accepts the two remote spellings Beacon meets in practice,
// git@github.com:owner/repo.git and https://github.com/owner/repo.git, plus the
// plain owner/repo form used by flags and config files. It returns "" for
// anything that does not carry exactly one owner and one repository name.
func parseRepoSlug(raw string) string {
	slug := strings.TrimSpace(raw)
	if slug == "" {
		return ""
	}
	if scheme := strings.Index(slug, "://"); scheme >= 0 {
		rest := slug[scheme+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			slug = rest[slash+1:]
		}
	} else if colon := strings.Index(slug, ":"); colon > 0 {
		// scp-like SSH: git@host:owner/repo.git
		slug = slug[colon+1:]
	}
	slug = strings.TrimSuffix(strings.TrimSpace(slug), ".git")
	slug = strings.Trim(slug, "/")
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
