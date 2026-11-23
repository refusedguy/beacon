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
// The optional tree conditions let one table cover both sides of a file
// split. only_if_exists applies the row when that path exists, and
// only_if_missing applies it when that path is absent.
type PermanentEntry struct {
	Rule          string `json:"rule"`
	Path          string `json:"path"`
	Reason        string `json:"reason"`
	OnlyIfExists  string `json:"only_if_exists,omitempty"`
	OnlyIfMissing string `json:"only_if_missing,omitempty"`
}

func existsUnder(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
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
	for _, b := range ex.Baseline {
		if b.Task == "" {
			return ex, fmt.Errorf("baseline entry %s %s has no task", b.Rule, b.Path)
		}
	}
	for _, p := range ex.Permanent {
		if p.Reason == "" {
			return ex, fmt.Errorf("permanent entry %s %s has no reason", p.Rule, p.Path)
		}
	}
	return ex, nil
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
		if !activeUnder(root, p.Path) {
			continue
		}
		if p.OnlyIfExists != "" && !existsUnder(root, p.OnlyIfExists) {
			continue
		}
		if p.OnlyIfMissing != "" && existsUnder(root, p.OnlyIfMissing) {
			continue
		}
		permanent = append(permanent, p)
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
