package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// SkipCode is a stable reason a task was left out of a dispatch.
type SkipCode string

const (
	// SkipActiveRun means the task already has an active run.
	SkipActiveRun SkipCode = "active_run"
	// SkipActiveScopeOverlap means the task scope overlaps a live run's scope.
	SkipActiveScopeOverlap SkipCode = "active_scope_overlap"
	// SkipSelectedScopeOverlap means the task scope overlaps another task selected in this cycle.
	SkipSelectedScopeOverlap SkipCode = "selected_scope_overlap"
	// SkipNoScope means the task has no scope while strict scope checking is enabled.
	SkipNoScope SkipCode = "no_scope"
	// SkipOversizedScope means the card breaks the 3-2-1200 scope cap. It is a
	// dispatch-time backstop; creation is the authority that measures lines.
	SkipOversizedScope SkipCode = "oversized_scope"
	// SkipBadStatus means the task status is not available.
	SkipBadStatus SkipCode = "bad_status"
)

// SkippedTask explains why a candidate was not dispatched and what the caller
// can do next. Overlap holds the intersecting paths from the skipped task's own
// scope.
type SkippedTask struct {
	TaskID       core.TaskID      `json:"task_id"`
	Code         SkipCode         `json:"code"`
	Reason       string           `json:"reason"`
	Next         string           `json:"next"`
	BlockedBy    core.TaskID      `json:"blocked_by,omitempty"`
	BlockedByRun core.RunID       `json:"blocked_by_run,omitempty"`
	Overlap      []core.ScopePath `json:"overlap,omitempty"`
}

// MarshalJSON fills in the next action for a skip built without one. Skips
// reach callers through this encoding, so no wire response leaves a reason
// without an instruction.
func (s SkippedTask) MarshalJSON() ([]byte, error) {
	type plain SkippedTask
	out := plain(s)
	if out.Next == "" {
		out.Next = nextActionFor(out.Code)
	}
	return json.Marshal(out)
}

// nextActionFor maps a skip code to the step that clears it. An unknown code
// still gets an instruction so callers never have to invent one.
func nextActionFor(code SkipCode) string {
	switch code {
	case SkipActiveRun:
		return "wait for the active run to finish, then dispatch again"
	case SkipActiveScopeOverlap:
		return "wait for the overlapping run to finish, then dispatch again"
	case SkipSelectedScopeOverlap:
		return "wait for this dispatch cycle to finish, then dispatch again"
	case SkipNoScope:
		return "add a scope to the card, then dispatch again"
	case SkipOversizedScope:
		return "shrink the scope to fit the 3-2-1200 cap, then dispatch again"
	case SkipBadStatus:
		return "set the card status to available, then dispatch again"
	default:
		return "review the skip reason, then dispatch again"
	}
}

// ConflictPlanner selects tasks that can run alongside active runs and each other.
type ConflictPlanner interface {
	Select(candidates []core.Task, active []*core.Run, limit int, strict bool) ([]core.Task, []SkippedTask, error)
}

// GreedyPlanner selects tasks in candidate order using an injected overlap checker.
type GreedyPlanner struct {
	Overlap core.OverlapChecker
}

var _ func(core.OverlapChecker) ConflictPlanner = NewPlanner

// NewPlanner returns a ConflictPlanner that uses checker to detect scope overlap.
// A nil checker uses core.PathsAndDependencies.
func NewPlanner(checker core.OverlapChecker) ConflictPlanner {
	if checker == nil {
		checker = core.PathsAndDependencies
	}
	return GreedyPlanner{Overlap: checker}
}

// Probe IDs keep an active-scope comparison path-only. They cannot collide with
// a real card ID or appear in a real DependsOn list, so identity and dependency
// rules in an injected checker cannot fire.
const (
	probeCandidateID core.TaskID = "\x00planner-candidate"
	probeRunID       core.TaskID = "\x00planner-run"
)

// Select returns up to limit candidates that are available and free of scope
// conflicts with active runs or earlier selections. It reports a reason for
// each candidate it skips.
func (p GreedyPlanner) Select(candidates []core.Task, active []*core.Run, limit int, strict bool) ([]core.Task, []SkippedTask, error) {
	var selected []core.Task
	var skipped []SkippedTask
	if limit <= 0 {
		return selected, skipped, nil
	}
	busyIDs, index := newActiveRunIndex(active)

	for _, t := range candidates {
		if len(selected) >= limit {
			break
		}
		if t.Status != core.TaskAvailable && t.Status != "" {
			skipped = append(skipped, SkippedTask{
				TaskID: t.ID,
				Code:   SkipBadStatus,
				Reason: "task status is " + string(t.Status),
				Next:   nextActionFor(SkipBadStatus),
			})
			continue
		}
		if activeRun, ok := busyIDs[t.ID]; ok {
			skipped = append(skipped, SkippedTask{
				TaskID:       t.ID,
				Code:         SkipActiveRun,
				BlockedBy:    t.ID,
				BlockedByRun: activeRun.ID,
				Reason:       "task already has an active run",
				Next:         nextActionFor(SkipActiveRun),
			})
			continue
		}
		if len(t.Scope) == 0 {
			if strict {
				skipped = append(skipped, SkippedTask{
					TaskID: t.ID,
					Code:   SkipNoScope,
					Reason: "task has no scope and strict_scope is enabled",
					Next:   nextActionFor(SkipNoScope),
				})
				continue
			}
			t.Scope = []core.ScopePath{core.ScopeAll()}
		}
		if skip, ok := oversizedSkip(t); ok {
			skipped = append(skipped, skip)
			continue
		}
		if blocker, paths := p.activeConflict(t, index); blocker != nil {
			skipped = append(skipped, SkippedTask{
				TaskID:       t.ID,
				Code:         SkipActiveScopeOverlap,
				BlockedBy:    blocker.TaskID,
				BlockedByRun: blocker.ID,
				Overlap:      paths,
				Reason:       "scope overlaps " + string(blocker.TaskID) + overlapSuffix(paths),
				Next:         nextActionFor(SkipActiveScopeOverlap),
			})
			continue
		}
		conflict := false
		for _, s := range selected {
			if p.Overlap(t, s) {
				paths := intersectScopes(t.Scope, s.Scope)
				skipped = append(skipped, SkippedTask{
					TaskID:    t.ID,
					Code:      SkipSelectedScopeOverlap,
					BlockedBy: s.ID,
					Overlap:   paths,
					Reason:    "scope overlaps " + string(s.ID) + overlapSuffix(paths),
					Next:      nextActionFor(SkipSelectedScopeOverlap),
				})
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		selected = append(selected, t)
	}
	return selected, skipped, nil
}

// oversizedSkip reports the dispatch-time backstop for a card that breaks the
// 3-2-1200 cap. Creation measures lines against the checkout; the planner only
// sees the parsed card, so it checks paths and production file paths.
func oversizedSkip(t core.Task) (SkippedTask, bool) {
	budget := declaredScopeBudget(t.Scope)
	if scopeWithinCap(budget) {
		return SkippedTask{}, false
	}
	return SkippedTask{
		TaskID: t.ID,
		Code:   SkipOversizedScope,
		Reason: fmt.Sprintf("scope breaks the 3-2-1200 cap: %d paths, %d production files",
			budget.paths, budget.files),
		Next: nextActionFor(SkipOversizedScope),
	}, true
}

// newActiveRunIndex builds the per-cycle view of active runs: busy task IDs and
// an index of live scope claims. Terminal and nil runs are ignored.
func newActiveRunIndex(active []*core.Run) (map[core.TaskID]*core.Run, *activeScopeIndex) {
	busyIDs := make(map[core.TaskID]*core.Run, len(active))
	live := make([]*core.Run, 0, len(active))
	for _, r := range active {
		if r == nil || r.State.Terminal() {
			continue
		}
		busyIDs[r.TaskID] = r
		live = append(live, r)
	}
	return busyIDs, newActiveScopeIndex(live)
}

// activeScopeIndex maps scope claims of live runs to the runs that carry them.
// It is built once per selection cycle and shared by every candidate, so a
// candidate only checks the runs its own claims could overlap.
type activeScopeIndex struct {
	runs   []*core.Run
	exact  map[core.ScopePath][]int
	cover  map[core.ScopePath][]int
	under  map[core.ScopePath][]int
	globs  []int
	unpath []int
	broad  []int
	epoch  int
	seen   []int
	out    []int
	keys   []core.ScopePath
}

func newActiveScopeIndex(runs []*core.Run) *activeScopeIndex {
	idx := &activeScopeIndex{
		runs:  runs,
		exact: make(map[core.ScopePath][]int, len(runs)),
		cover: make(map[core.ScopePath][]int, len(runs)),
		under: make(map[core.ScopePath][]int, len(runs)),
		seen:  make([]int, len(runs)),
		out:   make([]int, 0, len(runs)),
	}
	var keysBuf []core.ScopePath
	for i, r := range runs {
		for _, claim := range r.Scope {
			exactKey := core.ScopePath(claim.Value())
			idx.exact[exactKey] = appendUnique(idx.exact[exactKey], i)
			keys := claimDirKeys(keysBuf, claim)
			keysBuf = keys
			for _, key := range keys {
				idx.under[key] = appendUnique(idx.under[key], i)
			}
			if claim.Kind() == 'd' || claim.Kind() == 'g' {
				for _, key := range keys {
					idx.cover[key] = appendUnique(idx.cover[key], i)
				}
			}
			if claim.Kind() == 'g' {
				idx.globs = appendUnique(idx.globs, i)
			}
			if claim.Kind() == 'a' || (claim.Kind() == 'g' && leadingWildcard(claim.Value())) {
				idx.broad = appendUnique(idx.broad, i)
			}
			if claim.Kind() != 'a' && !strings.Contains(claim.Value(), "/") {
				idx.unpath = appendUnique(idx.unpath, i)
			}
		}
	}
	return idx
}

// matching returns the live runs whose claims could overlap t, in live order.
// The result is a superset: every overlapping run is included, and the caller
// confirms each one with the injected checker. The index owns the returned
// slice and reuses it on the next call, so callers must consume it before
// calling matching again. Deduplication uses an epoch buffer instead of a map
// so a candidate costs no per-candidate heap allocations. The seen slice is
// sized to the live run count, which bounds every index below.
func (idx *activeScopeIndex) matching(t core.Task) []int {
	idx.epoch++
	out := idx.out[:0]
	add := func(i int) {
		if idx.seen[i] == idx.epoch {
			return
		}
		idx.seen[i] = idx.epoch
		out = append(out, i)
	}
	allRuns := func() {
		for i := range idx.runs {
			add(i)
		}
	}
	for _, claim := range t.Scope {
		kind, value := claim.Kind(), claim.Value()
		if kind == 'a' || (kind == 'g' && leadingWildcard(value)) {
			// A broad claim can cover any active path, so no prefix key is
			// selective enough. Fall back to a full scan for this candidate.
			allRuns()
			continue
		}
		for _, i := range idx.exact[core.ScopePath(value)] {
			add(i)
		}
		keys := claimDirKeys(idx.keys, claim)
		idx.keys = keys
		switch kind {
		case 'f':
			for _, key := range keys {
				for _, i := range idx.cover[key] {
					add(i)
				}
			}
		default:
			// cover is only filled for d and g claims, and it uses the same
			// claimDirKeys prefixes that fill under for every claim. So
			// cover[key] is always a subset of under[key], and walking cover
			// here adds nothing the under walk has not already added.
			for _, key := range keys {
				for _, i := range idx.under[key] {
					add(i)
				}
			}
			if kind == 'g' {
				for _, i := range idx.globs {
					add(i)
				}
				for _, i := range idx.unpath {
					add(i)
				}
			}
		}
	}
	for _, i := range idx.broad {
		add(i)
	}
	if len(out) > 1 {
		slices.Sort(out)
	}
	idx.out = out
	return out
}

// claimDirKeys returns the directory keys under which a claim is indexed: the
// claim's own value as a directory, then each literal ancestor directory. A
// wildcard segment stops the walk because later segments are not fixed paths.
// Every key is a bare prefix of the claim value with no kind tag, so slicing
// the value avoids a strings.Split slice, a builder, its intermediate prefix
// strings and the per-key "d:" concatenation. The caller passes a reusable
// dst, so a claim costs no key-slice and no key-string allocation.
func claimDirKeys(dst []core.ScopePath, claim core.ScopePath) []core.ScopePath {
	out := dst[:0]
	value := claim.Value()
	for start := 0; ; {
		rel := strings.IndexByte(value[start:], '/')
		end := len(value)
		if rel >= 0 {
			end = start + rel
		}
		if strings.ContainsAny(value[start:end], "*?") {
			return out
		}
		out = append(out, core.ScopePath(value[:end]))
		if rel < 0 {
			return out
		}
		start = end + 1
	}
}

func leadingWildcard(value string) bool {
	end := strings.IndexByte(value, '/')
	if end < 0 {
		end = len(value)
	}
	return strings.ContainsAny(value[:end], "*?")
}

// appendUnique adds i to indices when it is not already present. The caller
// walks runs in ascending order, so a repeated i is always the last element.
// Checking only that element deduplicates in constant time without a scan.
func appendUnique(indices []int, i int) []int {
	if n := len(indices); n > 0 && indices[n-1] == i {
		return indices
	}
	return append(indices, i)
}

// activeConflict returns the first live run whose scope overlaps t and the
// paths from t that intersect that run.
func (p GreedyPlanner) activeConflict(t core.Task, index *activeScopeIndex) (*core.Run, []core.ScopePath) {
	for _, i := range index.matching(t) {
		r := index.runs[i]
		if p.overlapsScope(t, r.Scope) {
			return r, intersectScopes(t.Scope, r.Scope)
		}
	}
	return nil, nil
}

// overlapsScope checks path overlap only. The probe IDs keep identity and
// dependency rules out of the comparison while still counting it through the
// injected checker. An active run carries scope but no card identity.
func (p GreedyPlanner) overlapsScope(t core.Task, runScope []core.ScopePath) bool {
	checker := p.Overlap
	if checker == nil {
		checker = core.PathsAndDependencies
	}
	return checker(
		core.Task{ID: probeCandidateID, Scope: t.Scope},
		core.Task{ID: probeRunID, Scope: runScope},
	)
}

// intersectScopes returns the entries of a that cover or are covered by an
// entry of b, in the order they appear in a.
func intersectScopes(a, b []core.ScopePath) []core.ScopePath {
	var out []core.ScopePath
	for _, x := range a {
		for _, y := range b {
			if x.Covers(y) || y.Covers(x) {
				if !containsPath(out, x) {
					out = append(out, x)
				}
				break
			}
		}
	}
	return out
}

func containsPath(paths []core.ScopePath, path core.ScopePath) bool {
	for _, p := range paths {
		if p == path {
			return true
		}
	}
	return false
}

func overlapSuffix(paths []core.ScopePath) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" on ")
	for i, p := range paths {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(string(p))
	}
	return b.String()
}
