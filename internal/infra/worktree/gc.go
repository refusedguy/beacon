// Package worktree lists, inspects and removes git worktrees for beacon gc.
// It lives in infra because every operation shells out to git. The command in
// cmd/beacon owns flags, watch protection and the report.
package worktree

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Runner runs one git command. An empty dir selects the current working
// directory. Tests replace Runner so nothing here needs a real repository.
type Runner func(dir string, args ...string) (string, error)

// CommandRunner is the production Runner. It returns combined output so an
// error carries git's own explanation.
func CommandRunner(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Entry is one attached worktree from `git worktree list --porcelain`.
// Detached worktrees have an empty Branch and remain in the result.
type Entry struct {
	Dir    string `json:"dir"`
	Head   string `json:"head"`
	Branch string `json:"branch,omitempty"`
	Locked bool   `json:"locked,omitempty"`
}

// List returns every attached worktree except the main one. Git always prints
// the main worktree as the first porcelain block, so the first block is
// dropped here and never becomes a deletion candidate. Locked worktrees stay
// in the result with Locked set; the caller keeps them.
func List(run Runner) ([]Entry, error) {
	if run == nil {
		run = CommandRunner
	}
	out, err := run("", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("worktree: list: %w: %s", err, strings.TrimSpace(out))
	}
	all := parsePorcelain(out)
	if len(all) == 0 {
		return []Entry{}, nil
	}
	return all[1:], nil
}

// parsePorcelain maps porcelain blocks to entries. Unknown keys are ignored,
// which keeps the parser working when git adds a field.
func parsePorcelain(listing string) []Entry {
	var out []Entry
	var cur Entry
	flush := func() {
		if cur.Dir != "" {
			out = append(out, cur)
		}
		cur = Entry{}
	}
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Dir = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "HEAD ")))
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case strings.HasPrefix(line, "locked"):
			cur.Locked = true
		}
	}
	flush()
	return out
}

// Status runs `git status --porcelain` in dir. A non-empty result means the
// worktree has staged, modified or untracked files and must be kept.
func Status(run Runner, dir string) (string, error) {
	if run == nil {
		run = CommandRunner
	}
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("worktree: status: empty dir")
	}
	out, err := run(dir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("worktree: status %s: %w: %s", dir, err, strings.TrimSpace(out))
	}
	return out, nil
}

// Remove deletes one worktree with `git worktree remove`. Callers check Status
// first. There is no --force here, so git refuses to remove a dirty worktree
// even if a caller skips the check.
func Remove(run Runner, dir string) error {
	if run == nil {
		run = CommandRunner
	}
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("worktree: remove: empty dir")
	}
	out, err := run("", "worktree", "remove", "--", dir)
	if err != nil {
		return fmt.Errorf("worktree: remove %s: %w: %s", dir, err, strings.TrimSpace(out))
	}
	return nil
}

// Reasons recorded for a worktree that collection kept.
const (
	ReasonLocked       = "locked"
	ReasonActiveWatch  = "active_watch"
	ReasonStatusError  = "status_error"
	ReasonDirty        = "dirty"
	ReasonProtectedDir = "protected_dir"
	ReasonRemoveError  = "remove_error"
)

// Kept is one worktree that collection did not remove.
type Kept struct {
	Dir    string `json:"dir"`
	Branch string `json:"branch,omitempty"`
	Reason string `json:"reason"`
}

// Result is the outcome of one collection pass. Removed holds the paths git
// actually deleted; Kept explains every survivor.
type Result struct {
	Removed []string `json:"removed"`
	Kept    []Kept   `json:"kept"`
}

// WatchIDForHead maps a worktree head to a live Watch ID. A short head stays
// comparable through the prefix check. Sorted keys give a stable answer when
// two Watches record the same head. An empty head or no match returns "".
func WatchIDForHead(heads map[string]string, head string) string {
	head = strings.ToLower(strings.TrimSpace(head))
	if len(head) < 7 {
		return ""
	}
	keys := make([]string, 0, len(heads))
	for sha := range heads {
		keys = append(keys, sha)
	}
	sort.Strings(keys)
	for _, sha := range keys {
		if strings.HasPrefix(sha, head) {
			return heads[sha]
		}
	}
	return ""
}

func cleanDir(dir string) string {
	return filepath.Clean(strings.TrimSpace(dir))
}

// Collect removes clean, unlocked worktrees that have no live Watch at their
// head and are not in protectedDirs. It reuses List, Status and Remove, so List
// always drops the main worktree and Remove can never delete a dirty tree even
// if the status check is skipped. A status error or a failed remove keeps the
// worktree with a reason instead of aborting the whole pass.
func Collect(run Runner, heads map[string]string, protectedDirs ...string) (Result, error) {
	entries, err := List(run)
	if err != nil {
		return Result{}, err
	}
	protected := make(map[string]bool, len(protectedDirs))
	for _, dir := range protectedDirs {
		if dir = strings.TrimSpace(dir); dir != "" {
			protected[cleanDir(dir)] = true
		}
	}

	var out Result
	for _, e := range entries {
		keep := func(reason string) {
			out.Kept = append(out.Kept, Kept{Dir: e.Dir, Branch: e.Branch, Reason: reason})
		}
		switch {
		case e.Locked:
			keep(ReasonLocked)
			continue
		case protected[cleanDir(e.Dir)]:
			keep(ReasonProtectedDir)
			continue
		}
		if id := WatchIDForHead(heads, e.Head); id != "" {
			keep(ReasonActiveWatch + ":" + id)
			continue
		}
		status, statusErr := Status(run, e.Dir)
		switch {
		case statusErr != nil:
			keep(ReasonStatusError + ":" + statusErr.Error())
			continue
		case strings.TrimSpace(status) != "":
			keep(ReasonDirty)
			continue
		}
		if err := Remove(run, e.Dir); err != nil {
			keep(ReasonRemoveError + ":" + err.Error())
			continue
		}
		out.Removed = append(out.Removed, e.Dir)
	}
	return out, nil
}
