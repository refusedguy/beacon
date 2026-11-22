// Package worktree lists, inspects and removes git worktrees for beacon gc.
// It lives in infra because every operation shells out to git. The command in
// cmd/beacon owns flags, watch protection and the report.
package worktree

import (
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
	start := time.Now()
	defer func() { observe.Observe(HistogramGCDuration, time.Since(start).Seconds()) }()

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
