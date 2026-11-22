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
	"regexp"
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

// nativeSchemes are returned untouched by Normalize. Every entry carries
// "://", so isSCPStyleGitURL rejects them and the closing return in Normalize
// keeps the bytes unchanged. The list records intent; the "://" guard does the
// work.
var nativeSchemes = []string{
	"dolthub://",
	"file://",
	"aws://",
	"gs://",
	"s3://",
	"git+https://",
	"git+ssh://",
	"git+http://",
	"git+file://",
}

// scpStyleGitURLPattern matches user@host:path and dotted host:path remotes.
// The "://" guard in isSCPStyleGitURL keeps a scheme URL with "@" in its path
// from being read as an SCP remote.
var scpStyleGitURLPattern = regexp.MustCompile(`^(?:[a-zA-Z0-9._-]+@[a-zA-Z0-9][a-zA-Z0-9._-]*|[a-zA-Z0-9][a-zA-Z0-9._-]*\.[a-zA-Z0-9._-]*):[^\x00-\x1f\x7f]+$`)

// Normalize converts a remote URL to a comparable git form. Native schemes
// and unknown schemes are returned as-is. HTTPS, HTTP and SSH URLs get a
// "git+" prefix, and SCP-style remotes become git+ssh:// URLs. The conversion
// is deliberately scheme-preserving: http and https never collapse into one
// another.
func Normalize(raw string) string {
	for _, scheme := range nativeSchemes {
		if strings.HasPrefix(raw, scheme) {
			return raw
		}
	}
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") ||
		strings.HasPrefix(raw, "ssh://") {
		return fromGitURL(raw)
	}
	if isWindowsDrivePath(raw) {
		return fromGitURL(raw)
	}
	if isSCPStyleGitURL(raw) {
		return fromGitURL(raw)
	}
	return raw
}

// fromGitURL adds the git+ prefix used for comparison. A URL that already
// carries "://" is never split on the colon, so s3://bucket/team@prod cannot
// be rebuilt into a git+ssh remote.
func fromGitURL(raw string) string {
	if strings.HasPrefix(raw, "git+") {
		return raw
	}
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") ||
		strings.HasPrefix(raw, "ssh://") || isWindowsDrivePath(raw) {
		return "git+" + raw
	}
	if !strings.Contains(raw, "://") {
		if idx := strings.Index(raw, ":"); idx > 0 && !strings.Contains(raw[:idx], "/") {
			return "git+ssh://" + raw[:idx] + "/" + raw[idx+1:]
		}
	}
	return "git+" + raw
}

// isSCPStyleGitURL reports whether raw looks like an SCP remote. A scheme URL
// is never SCP-style because it contains "://". Known limits: dotless SSH
// aliases, non-ASCII userinfo and IDN hosts pass through unconverted.
func isSCPStyleGitURL(raw string) bool {
	return !strings.Contains(raw, "://") && scpStyleGitURLPattern.MatchString(raw)
}

func isWindowsDrivePath(path string) bool {
	if len(path) < 3 || path[1] != ':' {
		return false
	}
	drive := path[0]
	return ((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) &&
		(path[2] == '/' || path[2] == '\\')
}

// Redact removes embedded user[:pass]@ credentials from the authority of a
// scheme URL and lowercases the host. HTTP credentials are transport data and
// are always dropped. SSH userinfo selects the remote account, so it is kept
// except for the default "git" user. This helper is for address comparison;
// log redaction in internal/infra/observe stays a separate task. URLs without
// "://" are returned unchanged.
func Redact(raw string) string {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return raw
	}
	scheme := raw[:schemeEnd]
	authorityStart := schemeEnd + len("://")
	rest := raw[authorityStart:]

	authority := rest
	tail := ""
	if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
		authority = rest[:slashIdx]
		tail = rest[slashIdx:]
	}

	if atIdx := strings.LastIndex(authority, "@"); atIdx >= 0 {
		if !isSSHScheme(scheme) || authority[:atIdx] == "git" {
			authority = authority[atIdx+1:]
		}
	}
	authority = strings.ToLower(authority)

	return raw[:authorityStart] + authority + tail
}

func isSSHScheme(scheme string) bool {
	return scheme == "ssh" || scheme == "git+ssh"
}

// CanonicalForComparison returns a form of raw suitable for equality checks
// between URLs that name the same repository with different schemes or
// spellings. It normalizes to the git+ form, drops trailing slashes and .git,
// strips credentials and folds host case. http and https remain distinct.
func CanonicalForComparison(raw string) string {
	raw = Normalize(raw)
	raw = strings.TrimRight(raw, "/")
	raw = strings.TrimSuffix(raw, ".git")
	return Redact(raw)
}

// DetectRepoSlug resolves the repository slug for projectRoot. The project's
// .beacon/config.yaml key `repo` overrides the git remote. An empty result is
// not an error: a caller can report a clear "specify --repo" message, and a
// process that has no slug yet keeps starting.
func DetectRepoSlug(projectRoot string) string {
	return DetectRepoSlugWithOverride(projectRoot, "")
}

// DetectRepoSlugWithOverride resolves the slug with an explicit override from
// the CLI. Precedence is override, project config, git remote origin. Each
// source is reduced to its canonical owner/name form before it is returned.
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

// parseRepoSlug accepts the remote spellings Beacon meets in practice: HTTPS
// and HTTP URLs with or without credentials, SSH and SCP forms, native
// schemes and the plain owner/name form used by flags and config files. It
// canonicalizes the input first, so host case and HTTP credentials do not
// change the result. It returns "" when the canonical form has no exactly one
// owner and one repository name.
func parseRepoSlug(raw string) string {
	canonical := CanonicalForComparison(strings.TrimSpace(raw))
	path := canonical
	if i := strings.Index(path, "://"); i >= 0 {
		path = path[i+3:]
		if slash := strings.Index(path, "/"); slash >= 0 {
			path = path[slash+1:]
		}
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
