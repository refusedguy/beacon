package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// remote identifies the project's SCM host, owner and repository.
type remote struct {
	provider string
	owner    string
	repo     string
	host     string
}

// parseRemote extracts provider, owner and repo from a git remote URL.
func parseRemote(raw string) (remote, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		rest := s[i+1:]
		if j := strings.Index(rest, "/"); j >= 0 && allDigits(rest[:j]) {
			rest = rest[j+1:]
		}
		s = s[:i] + "/" + rest
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 3 {
		return remote{}, fmt.Errorf("cannot parse git remote %q", raw)
	}
	host := parts[0]
	owner := parts[len(parts)-2]
	repo := parts[len(parts)-1]
	provider := "github"
	if strings.Contains(host, "gitlab") {
		provider = "gitlab"
	}
	return remote{provider: provider, owner: owner, repo: repo, host: host}, nil
}

// allDigits reports whether s is a non-empty string of decimal digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// git runs a git command and returns its output and error. An empty root runs
// git in the current directory; otherwise the command runs at root.
func git(root string, args ...string) (string, error) {
	if root != "" {
		args = append([]string{"-C", root}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	return string(out), err
}

// gitTrim runs a git command and returns trimmed output, or an empty string on error.
func gitTrim(root string, args ...string) string {
	out, err := git(root, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// detectRemoteAndBase reads the origin URL and the default base branch.
func detectRemoteAndBase() (remote, string, error) {
	r, err := parseRemote(gitTrim("", "remote", "get-url", "origin"))
	if err != nil {
		return remote{}, "", err
	}
	base := strings.TrimPrefix(gitTrim("", "rev-parse", "--abbrev-ref", "origin/HEAD"), "origin/")
	if base == "" || base == "origin/HEAD" {
		base = "main"
	}
	return r, base, nil
}

// stdinIsTTY reports whether stdin is a terminal.
func stdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// confirmInit asks the operator to accept the detected configuration.
func confirmInit(out io.Writer, in io.Reader, r remote, base string) (bool, error) {
	_, _ = fmt.Fprintf(out, "Configure Beacon for %s/%s (base %s)? [Y/n] ", r.owner, r.repo, base)
	var answer string
	if _, err := fmt.Fscanln(in, &answer); err != nil {
		if err == io.EOF {
			return true, nil
		}
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "" || answer == "y" || answer == "yes", nil
}

// initDeclinedError reports that the operator rejected the detected config.
type initDeclinedError struct{}

func (initDeclinedError) Error() string { return "init declined" }

var errInitDeclined = initDeclinedError{}
