// Package gate holds the shared contract for the beacon-gates binary:
// exit codes, findings, fixture helpers and flag parsing.
package gate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Exit codes from ADR 0016.
const (
	Green  = 0
	Red    = 1
	Broken = 2
)

// Finding names the rule, the file, the line and the reason.
type Finding struct {
	Rule    string
	Path    string
	Line    int
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", f.Rule, f.Path, f.Line, f.Message)
}

// SortFindings puts findings in a deterministic order.
func SortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Path != fs[j].Path {
			return fs[i].Path < fs[j].Path
		}
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		return fs[i].Rule < fs[j].Rule
	})
}

// HasRule reports whether rule is present at all.
func HasRule(fs []Finding, rule string) bool {
	for _, f := range fs {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// HasRuleAt reports whether rule is present at the exact path and line.
func HasRuleAt(fs []Finding, rule, path string, line int) bool {
	for _, f := range fs {
		if f.Rule == rule && f.Path == path && f.Line == line {
			return true
		}
	}
	return false
}

// Fixture creates a temp tree from relative-path-to-content pairs.
// The caller owns the tree and must remove it.
func Fixture(files map[string]string) (string, error) {
	root, err := os.MkdirTemp("", "beacon-gates-*")
	if err != nil {
		return "", err
	}
	for rel, content := range files {
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			os.RemoveAll(root)
			return "", err
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			os.RemoveAll(root)
			return "", err
		}
	}
	return root, nil
}

// Options is the flag set shared by all three gates.
type Options struct {
	Root        string
	MinFindings int
}

// ParseOptions parses -root and -min-findings in both "-flag value" and
// "-flag=value" forms. Unknown flags and non-numeric values are startup errors.
func ParseOptions(args []string) (Options, error) {
	o := Options{Root: "."}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-root" || a == "--root":
			if i+1 >= len(args) {
				return o, fmt.Errorf("missing value for %s", a)
			}
			i++
			o.Root = args[i]
		case strings.HasPrefix(a, "-root=") || strings.HasPrefix(a, "--root="):
			o.Root = strings.SplitN(a, "=", 2)[1]
		case a == "-min-findings" || a == "--min-findings":
			if i+1 >= len(args) {
				return o, fmt.Errorf("missing value for %s", a)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return o, fmt.Errorf("non-numeric -min-findings: %q", args[i])
			}
			o.MinFindings = n
		case strings.HasPrefix(a, "-min-findings=") || strings.HasPrefix(a, "--min-findings="):
			v := strings.SplitN(a, "=", 2)[1]
			n, err := strconv.Atoi(v)
			if err != nil {
				return o, fmt.Errorf("non-numeric -min-findings: %q", v)
			}
			o.MinFindings = n
		default:
			return o, fmt.Errorf("unknown flag %q", a)
		}
	}
	if o.MinFindings < 0 {
		return o, fmt.Errorf("-min-findings must be >= 0")
	}
	return o, nil
}

// Finish prints findings and returns Green or Red. A scan that sees fewer
// findings than the declared floor gets SCAN-BLIND instead of a clean verdict.
func Finish(fs []Finding, minFindings int) int {
	if len(fs) < minFindings {
		fs = append(fs, Finding{
			Rule:    "SCAN-BLIND",
			Path:    ".",
			Line:    0,
			Message: fmt.Sprintf("scan saw %d finding(s), floor is %d", len(fs), minFindings),
		})
	}
	SortFindings(fs)
	if len(fs) == 0 {
		fmt.Println("green: no findings")
		return Green
	}
	for _, f := range fs {
		fmt.Println(f.String())
	}
	fmt.Printf("red: %d finding(s)\n", len(fs))
	return Red
}

// RemoveFixture deletes a tree created by Fixture. Gate self-tests call this
// so the only os.RemoveAll in tools/beacon-gates stays in this covered file.
func RemoveFixture(root string) { _ = os.RemoveAll(root) }

// Brokenf prints a startup error to stderr and returns the Broken code.
func Brokenf(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "broken: "+format+"\n", args...)
	return Broken
}

// ---------------------------------------------------------------------------
// Markdown docs gate.

// DocsConfig is the shrinking exception table for the docs gate. It is read
// from disk at runtime, unlike the embedded capabilities baseline, so adding
// rows does not change the committed beacon-gates binary.
type DocsConfig struct {
	BaselineLimit int             `json:"baseline_limit"`
	Baseline      []BaselineEntry `json:"baseline"`
}

// LoadDocsConfig reads the docs baseline. An empty path means no exceptions,
// which is what self-test fixtures use.
func LoadDocsConfig(path string) (DocsConfig, error) {
	if path == "" {
		return DocsConfig{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return DocsConfig{}, fmt.Errorf("read docs baseline: %w", err)
	}
	var cfg DocsConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return DocsConfig{}, fmt.Errorf("parse docs baseline: %w", err)
	}
	for _, e := range cfg.Baseline {
		if e.Task == "" {
			return DocsConfig{}, fmt.Errorf("docs baseline entry %s %s has no task", e.Rule, e.Path)
		}
	}
	return cfg, nil
}

// Statuses are kept in functions, not package vars: the docs gate is a
// standalone main package, and the committed beacon-gates binary must stay
// byte-identical after these helpers are added to the shared gate package.
func isTaskStatus(v string) bool {
	return v == "todo" || v == "done" || v == "dropped"
}

func isADRStatus(v string) bool {
	return v == "accepted" || v == "proposed" || v == "open"
}

// taskFields returns the front matter every task card carries. The first field
// is validated separately as a status, so field checks start at the second one.
func taskFields() []string {
	return []string{
		"Статус", "Фаза", "Тип", "Владелец файлов", "Зависимости", "Ссылка",
		"Агент", "Время", "Цель", "Контекст", "Шаги", "Проверка", "Приёмка",
	}
}

type docsFile struct {
	path  string
	lines []string
}

// readDocsFiles loads every *.md directly under dir, sorted by relative path.
func readDocsFiles(root, dir string) ([]docsFile, error) {
	pattern := filepath.Join(root, filepath.FromSlash(dir), "*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	out := make([]docsFile, 0, len(matches))
	for _, path := range matches {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", dir, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		out = append(out, docsFile{
			path:  filepath.ToSlash(rel),
			lines: strings.Split(string(b), "\n"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// fieldValue returns the value and 1-based line of the first "Key: value" line.
func fieldValue(f docsFile, key string) (string, int) {
	prefix := key + ":"
	for i, line := range f.lines {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), i + 1
		}
	}
	return "", 0
}

func firstHeading(f docsFile) string {
	for _, line := range f.lines {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// headingTaskID returns the card id from "# BS.10. Title" or "# HD-001. ...".
// The id ends at the first ". " or ": " after the leading "# ".
func headingTaskID(heading string) string {
	if !strings.HasPrefix(heading, "# ") {
		return ""
	}
	rest := strings.TrimPrefix(heading, "# ")
	for _, sep := range []string{". ", ": "} {
		if i := strings.Index(rest, sep); i >= 0 {
			return rest[:i]
		}
	}
	return rest
}

// adrNumber reads the numeric prefix of an ADR filename, e.g. 5 from 0005-x.md.
func adrNumber(rel string) (int, bool) {
	base := filepath.Base(rel)
	i := 0
	for i < len(base) && base[i] >= '0' && base[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(base[:i])
	return n, err == nil
}

// headingADRNumber reads the number from "# ADR 5: ..." or "# ADR 0005. ...".
func headingADRNumber(heading string) (int, bool) {
	if !strings.HasPrefix(heading, "# ADR ") {
		return 0, false
	}
	rest := strings.TrimPrefix(heading, "# ADR ")
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:i])
	return n, err == nil
}

func checkStatus(f docsFile, rule string, valid func(string) bool, fs *[]Finding) {
	v, line := fieldValue(f, "Статус")
	if line == 0 {
		*fs = append(*fs, Finding{
			Rule: rule, Path: f.path, Line: 1, Message: "missing Статус field",
		})
		return
	}
	if !valid(v) {
		*fs = append(*fs, Finding{
			Rule: rule, Path: f.path, Line: line, Message: fmt.Sprintf("invalid status %q", v),
		})
	}
}

// applyDocsBaseline suppresses covered findings, reports stale rows and keeps
// the shrinking ratchet honest. Rows are task-cited like the capabilities table.
func applyDocsBaseline(fs []Finding, cfg DocsConfig) []Finding {
	seen := map[string]bool{}
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		key := entryKey(f.Rule, f.Path)
		covered := false
		for _, b := range cfg.Baseline {
			if entryKey(b.Rule, b.Path) == key {
				covered = true
				break
			}
		}
		if covered {
			seen[key] = true
			continue
		}
		out = append(out, f)
	}
	if len(cfg.Baseline) > cfg.BaselineLimit {
		out = append(out, Finding{
			Rule:    "DOC-BASELINE-EXCEEDED",
			Path:    ".",
			Line:    0,
			Message: fmt.Sprintf("docs baseline has %d rows, limit is %d", len(cfg.Baseline), cfg.BaselineLimit),
		})
	}
	for _, b := range cfg.Baseline {
		if !seen[entryKey(b.Rule, b.Path)] {
			out = append(out, Finding{
				Rule:    "DOC-BASELINE-STALE",
				Path:    b.Path,
				Line:    0,
				Message: fmt.Sprintf("docs baseline row %s no longer matches a finding (task %s)", b.Rule, b.Task),
			})
		}
	}
	SortFindings(out)
	return out
}

// CheckDocs validates status, structure and revision numbering of task cards
// and ADRs. Two ADR files sharing a number are stale revisions of one another:
// the alphabetically first is kept and every later file is reported.
func CheckDocs(root string, cfg DocsConfig) ([]Finding, error) {
	var fs []Finding

	tasks, err := readDocsFiles(root, "docs/tasks")
	if err != nil {
		return nil, err
	}
	for _, f := range tasks {
		base := strings.TrimSuffix(filepath.Base(f.path), ".md")
		id := headingTaskID(firstHeading(f))
		if id != base && !strings.HasPrefix(base, id+"-") {
			fs = append(fs, Finding{
				Rule:    "DOC-TASK-HEADING",
				Path:    f.path,
				Line:    1,
				Message: fmt.Sprintf("heading id %q does not match file %q", id, base),
			})
		}
		checkStatus(f, "DOC-TASK-STATUS", isTaskStatus, &fs)
		for _, field := range taskFields()[1:] {
			if _, line := fieldValue(f, field); line == 0 {
				fs = append(fs, Finding{
					Rule:    "DOC-TASK-FIELD",
					Path:    f.path,
					Line:    1,
					Message: "missing field " + field,
				})
			}
		}
	}

	adrs, err := readDocsFiles(root, "docs/adrs")
	if err != nil {
		return nil, err
	}
	byNumber := map[int][]docsFile{}
	for _, f := range adrs {
		if filepath.Base(f.path) == "README.md" {
			continue
		}
		n, ok := adrNumber(f.path)
		if !ok {
			continue
		}
		byNumber[n] = append(byNumber[n], f)

		hn, ok := headingADRNumber(firstHeading(f))
		if !ok || hn != n {
			fs = append(fs, Finding{
				Rule:    "DOC-ADR-HEADING",
				Path:    f.path,
				Line:    1,
				Message: fmt.Sprintf("first line must start with %q", "# ADR "+strconv.Itoa(n)),
			})
		}
		checkStatus(f, "DOC-ADR-STATUS", isADRStatus, &fs)
		hasDecision := false
		for _, line := range f.lines {
			if strings.TrimSpace(line) == "## Решение" {
				hasDecision = true
				break
			}
		}
		if !hasDecision {
			fs = append(fs, Finding{
				Rule:    "DOC-ADR-STRUCTURE",
				Path:    f.path,
				Line:    1,
				Message: "missing ## Решение section",
			})
		}
	}
	for n, group := range byNumber {
		if len(group) < 2 {
			continue
		}
		kept := group[0].path
		for _, f := range group[1:] {
			fs = append(fs, Finding{
				Rule:    "DOC-ADR-DUP",
				Path:    f.path,
				Line:    1,
				Message: fmt.Sprintf("ADR %03d has %d files, stale revision of %s", n, len(group), kept),
			})
		}
	}

	return applyDocsBaseline(fs, cfg), nil
}

// DocsSelfTestRun plants broken markdown in a fixture and checks that the
// production scanner reports every rule. It proves the gate is not vacuous.
func DocsSelfTestRun() int {
	const goodTask = "# GOOD.1. Valid card\nСтатус: todo\nФаза: P0\nТип: task\n" +
		"Владелец файлов: x.go\nЗависимости: \nСсылка: ADR 0001\nАгент: нет\n" +
		"Время: нет\nЦель: c\nКонтекст: k\nШаги: s\nПроверка: v\nПриёмка: a\n"

	check := func(files map[string]string, cfg DocsConfig, want func([]Finding) bool) bool {
		root, err := Fixture(files)
		if err != nil {
			fmt.Println("self-test FAILED: fixture:", err)
			return false
		}
		defer RemoveFixture(root)
		fs, err := CheckDocs(root, cfg)
		if err != nil {
			fmt.Println("self-test FAILED: CheckDocs:", err)
			return false
		}
		if !want(fs) {
			fmt.Printf("self-test FAILED: unexpected findings: %v\n", fs)
			return false
		}
		return true
	}

	ok := true

	// Positive control: a complete card yields no findings.
	ok = check(map[string]string{"docs/tasks/GOOD.1.md": goodTask}, DocsConfig{},
		func(fs []Finding) bool { return len(fs) == 0 }) && ok

	// Planted defects: wrong heading, no status, missing fields.
	ok = check(map[string]string{
		"docs/tasks/BAD.1.md": "# WRONG.1. Broken\nФаза: P0\n",
	}, DocsConfig{}, func(fs []Finding) bool {
		return HasRule(fs, "DOC-TASK-HEADING") &&
			HasRule(fs, "DOC-TASK-STATUS") &&
			HasRule(fs, "DOC-TASK-FIELD")
	}) && ok

	// ADR defects: duplicate number is a stale revision, plus bad status and
	// a missing ## Решение section.
	ok = check(map[string]string{
		"docs/adrs/0001-good.md":  "# ADR 1: good\nСтатус: accepted\n\n## Решение\nok\n",
		"docs/adrs/0001-stale.md": "# ADR 2: wrong\nСтатус: bogus\n",
	}, DocsConfig{}, func(fs []Finding) bool {
		return HasRule(fs, "DOC-ADR-HEADING") &&
			HasRule(fs, "DOC-ADR-STATUS") &&
			HasRule(fs, "DOC-ADR-STRUCTURE") &&
			HasRule(fs, "DOC-ADR-DUP")
	}) && ok

	// Baseline suppresses a covered finding and flags a row that no longer
	// matches anything.
	ok = check(map[string]string{
		"docs/tasks/BAD.1.md": "# BAD.1. Broken\nСтатус: todo\n",
	}, DocsConfig{BaselineLimit: 2, Baseline: []BaselineEntry{
		{Rule: "DOC-TASK-FIELD", Path: "docs/tasks/BAD.1.md", Task: "HD-085"},
		{Rule: "DOC-TASK-STATUS", Path: "docs/tasks/GONE.1.md", Task: "HD-085"},
	}}, func(fs []Finding) bool {
		return len(fs) == 1 && HasRule(fs, "DOC-BASELINE-STALE")
	}) && ok

	if !ok {
		fmt.Println("self-test FAILED: 4 fixtures")
		return Red
	}
	fmt.Println("self-test passed: 4 fixtures")
	return Green
}
