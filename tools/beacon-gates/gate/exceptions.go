package gate

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed config/baseline.json
var baselineJSON []byte

//go:embed config/permanent.json
var permanentJSON []byte

// BaselineEntry is a shrinking exception. Every row cites the task that owes
// its removal.
type BaselineEntry struct {
	Rule string `json:"rule"`
	Path string `json:"path"`
	Task string `json:"task"`
}

// PermanentEntry is a standing exception. Every row needs a reason.
type PermanentEntry struct {
	Rule   string `json:"rule"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Exceptions struct {
	BaselineLimit int             `json:"baseline_limit"`
	Baseline      []BaselineEntry `json:"baseline"`
	Permanent     []PermanentEntry
}

// LoadExceptions reads and validates both exception tables.
func LoadExceptions() (Exceptions, error) {
	var ex Exceptions
	if err := json.Unmarshal(baselineJSON, &ex); err != nil {
		return ex, fmt.Errorf("parse baseline: %w", err)
	}
	var perm struct {
		Permanent []PermanentEntry `json:"permanent"`
	}
	if err := json.Unmarshal(permanentJSON, &perm); err != nil {
		return ex, fmt.Errorf("parse permanent: %w", err)
	}
	ex.Permanent = perm.Permanent
	if err := ex.validate(); err != nil {
		return ex, err
	}
	return ex, nil
}

// validate enforces the exception-table policy. Baseline is a shrinking
// ratchet: its limit must cover every row and each row names the task that owes
// the removal. Permanent is a standing exception: each row must give a reason.
// A rule and path pair may live in only one table, so every covered finding has
// one owner and neither table can mask the other going stale.
func (ex Exceptions) validate() error {
	for _, b := range ex.Baseline {
		if b.Rule == "" || b.Path == "" {
			return fmt.Errorf("baseline entry has no rule or path: %+v", b)
		}
		if b.Task == "" {
			return fmt.Errorf("baseline entry %s %s has no task", b.Rule, b.Path)
		}
	}
	for _, p := range ex.Permanent {
		if p.Rule == "" || p.Path == "" {
			return fmt.Errorf("permanent entry has no rule or path: %+v", p)
		}
		if p.Reason == "" {
			return fmt.Errorf("permanent entry %s %s has no reason", p.Rule, p.Path)
		}
	}
	if len(ex.Baseline) > ex.BaselineLimit {
		return fmt.Errorf("baseline has %d rows, limit is %d", len(ex.Baseline), ex.BaselineLimit)
	}
	permanent := make(map[string]bool, len(ex.Permanent))
	for _, p := range ex.Permanent {
		permanent[entryKey(p.Rule, p.Path)] = true
	}
	for _, b := range ex.Baseline {
		if permanent[entryKey(b.Rule, b.Path)] {
			return fmt.Errorf("baseline entry %s %s also appears in permanent", b.Rule, b.Path)
		}
	}
	return nil
}

func entryKey(rule, path string) string { return rule + "\x00" + path }

// activeUnder reports whether an exception row belongs to the tree being
// scanned. The file itself or its parent directory must be present. A row for
// a deleted file in an existing directory stays active and can still be
// reported stale; a row for a repository path never seen in a fixture tree is
// simply out of scope for that scan.
func activeUnder(root, rel string) bool {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Stat(p); err == nil {
		return true
	}
	_, err := os.Stat(filepath.Dir(p))
	return err == nil
}

// Apply suppresses covered findings, then checks both tables for liveness
// and checks that the baseline has not grown past its ratchet limit. Rows are
// scoped to root so that self-test fixtures are not judged by repository
// exceptions.
func (ex Exceptions) Apply(fs []Finding, root string) []Finding {
	var baseline []BaselineEntry
	var permanent []PermanentEntry
	for _, b := range ex.Baseline {
		if activeUnder(root, b.Path) {
			baseline = append(baseline, b)
		}
	}
	for _, p := range ex.Permanent {
		if activeUnder(root, p.Path) {
			permanent = append(permanent, p)
		}
	}
	covered := func(rule, path string) bool {
		key := entryKey(rule, path)
		for _, b := range baseline {
			if entryKey(b.Rule, b.Path) == key {
				return true
			}
		}
		for _, p := range permanent {
			if entryKey(p.Rule, p.Path) == key {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	var out []Finding
	for _, f := range fs {
		key := entryKey(f.Rule, f.Path)
		if covered(f.Rule, f.Path) {
			seen[key] = true
			continue
		}
		out = append(out, f)
	}
	if len(baseline) > ex.BaselineLimit {
		out = append(out, Finding{
			Rule:    "CAP-BASELINE-EXCEEDED",
			Path:    ".",
			Line:    0,
			Message: fmt.Sprintf("baseline has %d rows, limit is %d", len(baseline), ex.BaselineLimit),
		})
	}
	for _, b := range baseline {
		if !seen[entryKey(b.Rule, b.Path)] {
			out = append(out, Finding{
				Rule:    "CAP-BASELINE-STALE",
				Path:    b.Path,
				Line:    0,
				Message: fmt.Sprintf("baseline row %s no longer matches a finding (task %s)", b.Rule, b.Task),
			})
		}
	}
	for _, p := range permanent {
		if !seen[entryKey(p.Rule, p.Path)] {
			out = append(out, Finding{
				Rule:    "CAP-PERMANENT-STALE",
				Path:    p.Path,
				Line:    0,
				Message: fmt.Sprintf("permanent row %s no longer matches a finding", p.Rule),
			})
		}
	}
	SortFindings(out)
	return out
}
