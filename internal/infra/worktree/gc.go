// Package worktree lists, inspects and removes git worktrees for beacon gc.
// It lives in infra because every operation shells out to git. The command in
// cmd/beacon owns flags, watch protection and the report.
package worktree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// HistogramGCDuration is the observe histogram that records one full
// collection pass. The daemon binds its WorktreeGC port to Collect, so one
// port call adds exactly one observation. The name is exported so callers and
// tests read the same series without repeating the literal.
const HistogramGCDuration = "worktree_gc_duration_seconds"

// collectNow is the clock used for the duration histogram. Tests replace it
// with a stepped clock so the recorded duration does not depend on wall time.
var collectNow = time.Now

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
// first. Two local-state guards run before git: the process must not sit inside
// the target tree, and the tree must hold no files git ignores. There is no
// --force here, so git refuses to remove a dirty worktree even if a caller
// skips the check.
func Remove(run Runner, dir string) error {
	if run == nil {
		run = CommandRunner
	}
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("worktree: remove: empty dir")
	}
	if insideWorktree(dir) {
		return &BlockedError{Reason: ReasonInsideWorktree, Dir: dir}
	}
	// A porcelain status hides ignored files. Codex-style cleanup keeps any
	// worktree that holds them, because `git worktree remove` deletes them with
	// no warning. A runner that cannot answer ls-files is treated as having
	// none; the real git runner answers it on every valid worktree.
	if ignored, err := IgnoredFiles(run, dir); err == nil && strings.TrimSpace(ignored) != "" {
		return &BlockedError{Reason: ReasonIgnoredFiles, Dir: dir}
	}
	out, err := run("", "worktree", "remove", "--", dir)
	if err != nil {
		return fmt.Errorf("worktree: remove %s: %w: %s", dir, err, strings.TrimSpace(out))
	}
	return nil
}

// IgnoredFiles runs `git ls-files --others --ignored --exclude-standard -z` in
// dir. The result is NUL-separated when git finds ignored local files and empty
// when it does not. Callers keep a worktree with any ignored file.
func IgnoredFiles(run Runner, dir string) (string, error) {
	if run == nil {
		run = CommandRunner
	}
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("worktree: ignored: empty dir")
	}
	out, err := run(dir, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return "", fmt.Errorf("worktree: ignored %s: %w: %s", dir, err, strings.TrimSpace(out))
	}
	return out, nil
}

// insideWorktree reports whether the process directory is dir or a descendant
// of dir. Git cannot remove a worktree that contains its own current directory,
// so callers keep the tree instead of handing git an impossible removal.
func insideWorktree(dir string) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	cwd, dir = cleanDir(cwd), cleanDir(dir)
	if cwd == dir {
		return true
	}
	rel, err := filepath.Rel(dir, cwd)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// BlockedError is the error Remove returns when a local-state guard refuses
// deletion. Reason is the keep reason a collector reports for the tree.
type BlockedError struct {
	Reason string
	Dir    string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("worktree: remove %s blocked: %s", e.Dir, e.Reason)
}

// Reasons recorded for a worktree that collection kept. Deferred and changed
// come from the grace window: a fresh candidate waits, and a change between
// the two status checks cancels it.
const (
	ReasonLocked         = "locked"
	ReasonActiveWatch    = "active_watch"
	ReasonStatusError    = "status_error"
	ReasonDirty          = "dirty"
	ReasonProtectedDir   = "protected_dir"
	ReasonRemoveError    = "remove_error"
	ReasonDeferred       = "deferred"
	ReasonChanged        = "changed"
	ReasonIgnoredFiles   = "ignored_files"
	ReasonInsideWorktree = "inside_worktree"
	ReasonOwnerLease     = "owner_lease"
	ReasonNoWatches      = "no_watches"
)

// gcLeaseOwner is the actor Collect uses when it takes an OwnerLease. A
// handoff holds the same slot under its own owner, so either actor can block
// the other while a target is being checked and removed.
const gcLeaseOwner = "worktree-gc"

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

// DecideGuard translates all five Guard outcomes into a report decision.
// Reason is empty only for a successful removal. A caller writes Removed into
// its own count and maps the reason into its report.
func DecideGuard(g Guard, c Candidate) (reason string, removed bool) {
	switch g.Check(c) {
	case GraceDeferred:
		return ReasonDeferred, false
	case GraceChanged:
		return ReasonChanged, false
	case GraceStatusErr:
		return ReasonStatusError, false
	case GraceRemoveErr:
		return ReasonRemoveError, false
	case GraceRemoved:
		return "", true
	default:
		return ReasonStatusError, false
	}
}

// watchInput normalizes the Watch argument Collect accepts. Legacy callers
// pass a head-to-Watch map, which carries only the recorded head, so their
// exact-head reason keeps the old active_watch format. New callers pass the
// full []WatchAnchor set and receive the GuardWatch reason. An explicitly
// empty set keeps every candidate, the same rule cibridge.ScopeWorktrees uses
// for an empty closedWatchID. A nil value means the caller supplied no Watch
// information and preserves the legacy collection pass.
func watchInput(watches any) (anchors []WatchAnchor, legacy, fullSet bool) {
	switch v := watches.(type) {
	case nil:
		return nil, true, false
	case map[string]string:
		return AnchorsFromHeads(v), true, false
	case []WatchAnchor:
		return v, false, true
	default:
		return nil, true, false
	}
}

// Collect removes clean, unlocked worktrees that no live Watch protects and
// that are not in protectedDirs. It reuses List, Status and Remove, so List
// always drops the main worktree and Remove can never delete a dirty tree even
// if the status check is skipped. A status error or a failed remove keeps the
// worktree with a reason instead of aborting the whole pass.
//
// Collect calls GuardWatch with the Watch anchors it was given. The guard
// checks an exact head, then the Watch branch and binding, then ancestry, so a
// worktree whose HEAD moved past a recorded headSHA is kept with reason
// head_ancestor. An OwnerLease spans the guard check, the status check and the
// removal; a parallel handoff holding the same directory wins the slot and the
// collector keeps the tree instead of racing on a swapped target.
func Collect(run Runner, watches any, protectedDirs ...string) (Result, error) {
	start := collectNow()
	defer func() { observe.Observe(HistogramGCDuration, collectNow().Sub(start).Seconds()) }()

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
	anchors, legacy, fullSet := watchInput(watches)

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
		case fullSet && len(anchors) == 0:
			keep(ReasonNoWatches)
			continue
		}

		release, ok := AcquireOwnerLease(e.Dir, gcLeaseOwner)
		if !ok {
			keep(ReasonOwnerLease)
			continue
		}
		func() {
			defer release()

			if answer, guarded := GuardWatch(run, e, anchors); guarded {
				reason := answer.Reason
				if legacy && answer.Reason == GuardReasonExactHead {
					reason = ReasonActiveWatch + ":" + answer.ID
				}
				keep(reason)
				return
			}
			status, statusErr := Status(run, e.Dir)
			switch {
			case statusErr != nil:
				keep(ReasonStatusError + ":" + statusErr.Error())
				return
			case strings.TrimSpace(status) != "":
				keep(ReasonDirty)
				return
			}
			if err := Remove(run, e.Dir); err != nil {
				var blocked *BlockedError
				if errors.As(err, &blocked) {
					keep(blocked.Reason)
				} else {
					keep(ReasonRemoveError + ":" + err.Error())
				}
				return
			}
			out.Removed = append(out.Removed, e.Dir)
		}()
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// IgnoreSpec: one compiled path spec, mtime-refreshed rules and a per-path
// cache. Aider keeps service paths and large blobs out of its repo map this
// way. Beacon's worktree scan and the index build both walk a directory tree,
// so the guard lives here in infra and cmd/beacon reuses it. See CA-12.
// ---------------------------------------------------------------------------

const (
	// IgnoreRefreshInterval is the minimum gap between two reads of a rules
	// file. A changed mtime is picked up on the first check after the window,
	// so a change is accepted within one second with the default interval.
	IgnoreRefreshInterval = time.Second

	// IgnoreMaxContentBytes is the largest file a traversal will open. Larger
	// files are rejected before ReadFile touches their contents.
	IgnoreMaxContentBytes int64 = 1 << 20
)

// defaultIgnorePatterns are the service paths and temporary suffixes aider
// drops before it reads file contents. A rules file replaces the list.
var defaultIgnorePatterns = []string{
	".git/**",
	"**/.git/**",
	"node_modules/**",
	"**/node_modules/**",
	"vendor/**",
	"**/vendor/**",
	".gocache/**",
	"**/.gocache/**",
	".gotmp/**",
	"**/.gotmp/**",
	"*.tmp",
	"*.log",
}

// IgnoreStats is the measurable outcome of the traversals through a spec.
// ContentReads counts real opens. A repeat walk over an unchanged tree leaves
// it untouched, which is the check in docs/tasks/CA-12.md.
type IgnoreStats struct {
	ContentReads int64
	CacheHits    int64
	RuleLoads    int64
	RuleVersion  uint64
}

type ignoreEntry struct {
	version uint64
	mtime   time.Time
	size    int64
	content []byte
}

// IgnoreSpec holds traversal rules, their mtime and a per-path cache of both
// the keep decision and the file contents. It is safe for concurrent use.
type IgnoreSpec struct {
	rulesPath string
	interval  time.Duration

	mu           sync.Mutex
	compiled     []*regexp.Regexp
	mtime        time.Time
	loadedAt     time.Time
	version      uint64
	entries      map[string]ignoreEntry
	contentReads int64
	cacheHits    int64
	ruleLoads    int64
}

// NewIgnoreSpec builds a spec with the default patterns. When rulesPath
// exists, its non-empty and non-comment lines replace the defaults. A missing
// rules file is not an error.
func NewIgnoreSpec(rulesPath string) *IgnoreSpec {
	s := &IgnoreSpec{
		rulesPath: rulesPath,
		interval:  IgnoreRefreshInterval,
		entries:   make(map[string]ignoreEntry),
	}
	s.setPatterns(defaultIgnorePatterns)
	s.refreshRulesLocked(time.Now())
	return s
}

// WithInterval overrides the refresh throttle. Tests shorten it to observe a
// rule change without waiting. Production uses IgnoreRefreshInterval.
func (s *IgnoreSpec) WithInterval(d time.Duration) *IgnoreSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d > 0 {
		s.interval = d
	}
	return s
}

// setPatterns compiles raw lines into anchored regexps and bumps version so
// every cached entry is invalidated on the next access. Caller holds mu.
func (s *IgnoreSpec) setPatterns(lines []string) {
	s.compiled = nil
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s.compiled = append(s.compiled, compileIgnorePattern(line))
	}
	s.version++
}

// compileIgnorePattern turns a slash-separated glob into a regexp. A double
// star spans separators; a single star and a question mark do not.
func compileIgnorePattern(pattern string) *regexp.Regexp {
	p := strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/")
	p = strings.TrimPrefix(p, "/")
	runes := []rune(p)
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(runes); {
		if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '*' {
			b.WriteString(".*")
			i += 2
			continue
		}
		switch runes[i] {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
		i++
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile("^$")
	}
	return re
}

// refreshRulesLocked reloads the rules file when its mtime moved and at least
// one interval passed since the last load. Caller holds mu. A missing or
// unreadable file leaves the current rules in force.
func (s *IgnoreSpec) refreshRulesLocked(now time.Time) {
	info, err := os.Stat(s.rulesPath)
	if err != nil || !info.ModTime().After(s.mtime) {
		return
	}
	if !s.loadedAt.IsZero() && now.Sub(s.loadedAt) < s.interval {
		return
	}
	data, err := os.ReadFile(s.rulesPath)
	if err != nil {
		return
	}
	s.mtime = info.ModTime()
	s.loadedAt = now
	s.ruleLoads++
	s.setPatterns(strings.Split(string(data), "\n"))
	s.entries = make(map[string]ignoreEntry)
}

// relativeIgnorePath maps path to the slash-separated path relative to root.
func relativeIgnorePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	return strings.TrimPrefix(rel, "./")
}

func (s *IgnoreSpec) keepLocked(rel string) bool {
	for _, re := range s.compiled {
		if re.MatchString(rel) {
			return false
		}
	}
	return true
}

// Keep reports whether path survives the ignore rules. Matching uses the path
// relative to root with forward slashes.
func (s *IgnoreSpec) Keep(root, path string) bool {
	rel := relativeIgnorePath(root, path)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshRulesLocked(time.Now())
	return s.keepLocked(rel)
}

// ReadFile returns the contents of path when it passes the rules and is at
// most IgnoreMaxContentBytes. The second result is false for ignored and
// oversized files. Repeat calls for an unchanged file come from the cache and
// do not increment ContentReads.
func (s *IgnoreSpec) ReadFile(root, path string) ([]byte, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, err
	}
	rel := relativeIgnorePath(root, path)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshRulesLocked(time.Now())

	if entry, ok := s.entries[path]; ok &&
		entry.version == s.version &&
		entry.mtime.Equal(info.ModTime()) &&
		entry.size == info.Size() {
		s.cacheHits++
		return entry.content, entry.content != nil, nil
	}

	if !s.keepLocked(rel) || info.Size() > IgnoreMaxContentBytes {
		s.entries[path] = ignoreEntry{version: s.version, mtime: info.ModTime(), size: info.Size()}
		return nil, false, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	s.contentReads++
	s.entries[path] = ignoreEntry{
		version: s.version,
		mtime:   info.ModTime(),
		size:    info.Size(),
		content: data,
	}
	return data, true, nil
}

// Walk visits every file below root and returns the paths that passed the
// rules and the size limit. A second walk over an unchanged tree performs no
// additional content reads.
func (s *IgnoreSpec) Walk(root string) ([]string, error) {
	var kept []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		_, ok, err := s.ReadFile(root, p)
		if err != nil {
			return err
		}
		if ok {
			kept = append(kept, p)
		}
		return nil
	})
	return kept, err
}

// CountIgnored walks root and counts files rejected by the rules without
// opening their contents. The index build uses it as a cheap pre-pass.
func (s *IgnoreSpec) CountIgnored(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !s.Keep(root, p) {
			count++
		}
		return nil
	})
	return count
}

// Stats snapshots the counters used by the CA-12 check.
func (s *IgnoreSpec) Stats() IgnoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return IgnoreStats{
		ContentReads: s.contentReads,
		CacheHits:    s.cacheHits,
		RuleLoads:    s.ruleLoads,
		RuleVersion:  s.version,
	}
}
