package worktree

import (
	"os"
	"path/filepath"
	"strings"
)

// ListWithBacklink returns attached worktrees whose administration directory
// points back to the checkout that git listed. It wraps List, so the main
// worktree stays out of the result and the Entry shape does not change.
//
// A worktree listing can keep a stale path after a checkout moved. The
// administration directory still names the old .git file, so trusting the
// listed path can hand a caller a foreign checkout. For each entry this reads
// the gitdir file inside the administration directory, resolves both that
// backlink and the entry's .git file, and keeps the entry only when they are
// the same path. Entries that cannot be resolved are left out.
func ListWithBacklink(run Runner) ([]Entry, error) {
	if run == nil {
		run = CommandRunner
	}
	entries, err := List(run)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if hasBacklink(run, e.Dir) {
			out = append(out, e)
		}
	}
	return out, nil
}

// hasBacklink reports whether the administration directory for dir names dir's
// own .git file. Any failure on either side counts as no backlink: a path that
// cannot be resolved cannot prove that the registration is current.
func hasBacklink(run Runner, dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	gitDir, err := run(dir, "rev-parse", "--git-dir")
	if err != nil {
		return false
	}
	admin := strings.TrimSpace(gitDir)
	if admin == "" {
		return false
	}
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(dir, admin)
	}
	data, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return false
	}
	backlink := strings.TrimSpace(string(data))
	if backlink == "" {
		return false
	}
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(admin, backlink)
	}
	linked, err := canonicalPath(backlink)
	if err != nil {
		return false
	}
	gitFile, err := canonicalPath(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return linked == gitFile
}

// canonicalPath makes p absolute and resolves symlinks. Both paths must exist,
// because a missing path cannot establish an identity check.
func canonicalPath(p string) (string, error) {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.EvalSymlinks(p)
}
