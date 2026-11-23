// Package worktree lists, inspects and removes git worktrees for beacon gc.
// This file keeps the rule for opening a branch again. It reads facts that the
// caller collected from git, so it never starts a git command itself.
package worktree

import (
	"strconv"
	"strings"
)

// Decisions returned by CheckResume.
const (
	ResumeOpen   = "open"
	ResumeReuse  = "reuse"
	ResumeRefuse = "refuse"
)

// ResumeInput holds the facts collected before a branch is opened again.
// Ahead and behind are the two counts from git rev-list --left-right --count.
type ResumeInput struct {
	Branch              string
	Worktree            string
	CheckedOutElsewhere bool
	Ahead               int
	Behind              int
	ExistingWorktree    string
}

// ResumeAnswer tells the caller whether to open the branch, reuse the worktree
// that already holds it or refuse the operation. Reason explains the decision
// to the user.
type ResumeAnswer struct {
	Decision string
	Reason   string
}

// CheckResume decides how a branch can be opened again. A branch already
// checked out in another worktree is reused, so no second worktree takes it
// over. A branch that is both ahead and behind is refused, because a reset
// would drop its local commits. A branch that is only behind can be opened.
func CheckResume(in ResumeInput) ResumeAnswer {
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "unknown branch"
	}
	worktree := strings.TrimSpace(in.Worktree)
	if worktree == "" {
		worktree = "unknown path"
	}

	if in.CheckedOutElsewhere {
		existing := strings.TrimSpace(in.ExistingWorktree)
		if existing == "" {
			existing = "an existing worktree"
		}
		return ResumeAnswer{
			Decision: ResumeReuse,
			Reason: "branch " + branch + " is already checked out in " + existing +
				"; open that worktree instead of creating a copy at " + worktree,
		}
	}

	if in.Ahead > 0 && in.Behind > 0 {
		return ResumeAnswer{
			Decision: ResumeRefuse,
			Reason: "branch " + branch + " diverged from upstream: " + strconv.Itoa(in.Ahead) +
				" local commits (локальные коммиты) and " + strconv.Itoa(in.Behind) +
				" behind; refusing to overwrite local commits",
		}
	}

	return ResumeAnswer{
		Decision: ResumeOpen,
		Reason:   "branch " + branch + " is safe to open at " + worktree,
	}
}
