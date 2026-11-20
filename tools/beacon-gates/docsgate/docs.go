package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

// docsConfig is the shrinking exception table for the docs gate. It is read
// from disk at runtime, so adding rows does not change the committed
// beacon-gates binary.
type docsConfig struct {
	BaselineLimit int                  `json:"baseline_limit"`
	Baseline      []gate.BaselineEntry `json:"baseline"`
}

func loadDocsConfig(path string) (docsConfig, error) {
	if path == "" {
		return docsConfig{}, nil
	}
	b, err := fs.ReadFile(os.DirFS("."), filepath.ToSlash(path))
	if err != nil {
		return docsConfig{}, fmt.Errorf("read docs baseline: %w", err)
	}
	var cfg docsConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return docsConfig{}, fmt.Errorf("parse docs baseline: %w", err)
	}
	for _, e := range cfg.Baseline {
		if e.Task == "" {
			return docsConfig{}, fmt.Errorf("docs baseline entry %s %s has no task", e.Rule, e.Path)
		}
	}
	return cfg, nil
}

func isTaskStatus(v string) bool {
	return v == "todo" || v == "done" || v == "dropped"
}

func isADRStatus(v string) bool {
	return v == "accepted" || v == "proposed" || v == "open"
}

// taskFields lists the front matter every task card carries. The first field
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
	fsys := os.DirFS(root)
	pattern := filepath.ToSlash(filepath.Join(dir, "*.md"))
	matches, err := fs.Glob(fsys, pattern)
	if err != nil {
		return nil, err
	}
	out := make([]docsFile, 0, len(matches))
	for _, rel := range matches {
		b, err := fs.ReadFile(fsys, rel)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", dir, err)
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

func checkStatus(f docsFile, rule string, valid func(string) bool, fs *[]gate.Finding) {
	v, line := fieldValue(f, "Статус")
	if line == 0 {
		*fs = append(*fs, gate.Finding{
			Rule: rule, Path: f.path, Line: 1, Message: "missing Статус field",
		})
		return
	}
	if !valid(v) {
		*fs = append(*fs, gate.Finding{
			Rule: rule, Path: f.path, Line: line, Message: fmt.Sprintf("invalid status %q", v),
		})
	}
}

func docsEntryKey(rule, path string) string { return rule + "\x00" + path }

// applyDocsBaseline suppresses covered findings, reports stale rows and keeps
// the shrinking limit honest. Every row cites the task that owes its removal.
func applyDocsBaseline(fs []gate.Finding, cfg docsConfig) []gate.Finding {
	seen := map[string]bool{}
	out := make([]gate.Finding, 0, len(fs))
	for _, f := range fs {
		key := docsEntryKey(f.Rule, f.Path)
		covered := false
		for _, b := range cfg.Baseline {
			if docsEntryKey(b.Rule, b.Path) == key {
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
		out = append(out, gate.Finding{
			Rule:    "DOC-BASELINE-EXCEEDED",
			Path:    ".",
			Line:    0,
			Message: fmt.Sprintf("docs baseline has %d rows, limit is %d", len(cfg.Baseline), cfg.BaselineLimit),
		})
	}
	for _, b := range cfg.Baseline {
		if !seen[docsEntryKey(b.Rule, b.Path)] {
			out = append(out, gate.Finding{
				Rule:    "DOC-BASELINE-STALE",
				Path:    b.Path,
				Line:    0,
				Message: fmt.Sprintf("docs baseline row %s no longer matches a finding (task %s)", b.Rule, b.Task),
			})
		}
	}
	gate.SortFindings(out)
	return out
}

// checkDocs validates status, structure and revision numbering of task cards
// and ADRs. Two ADR files sharing a number are stale revisions of one another:
// the alphabetically first is kept and every later file is reported.
func checkDocs(root string, cfg docsConfig) ([]gate.Finding, error) {
	var fs []gate.Finding

	tasks, err := readDocsFiles(root, "docs/tasks")
	if err != nil {
		return nil, err
	}
	for _, f := range tasks {
		base := strings.TrimSuffix(filepath.Base(f.path), ".md")
		id := headingTaskID(firstHeading(f))
		if id != base && !strings.HasPrefix(base, id+"-") {
			fs = append(fs, gate.Finding{
				Rule:    "DOC-TASK-HEADING",
				Path:    f.path,
				Line:    1,
				Message: fmt.Sprintf("heading id %q does not match file %q", id, base),
			})
		}
		checkStatus(f, "DOC-TASK-STATUS", isTaskStatus, &fs)
		for _, field := range taskFields()[1:] {
			if _, line := fieldValue(f, field); line == 0 {
				fs = append(fs, gate.Finding{
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
			fs = append(fs, gate.Finding{
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
			fs = append(fs, gate.Finding{
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
			fs = append(fs, gate.Finding{
				Rule:    "DOC-ADR-DUP",
				Path:    f.path,
				Line:    1,
				Message: fmt.Sprintf("ADR %03d has %d files, stale revision of %s", n, len(group), kept),
			})
		}
	}

	return applyDocsBaseline(fs, cfg), nil
}

// docsSelfTestRun plants broken markdown in a fixture and checks that the
// production scanner reports every rule. It proves the gate is not vacuous.
func docsSelfTestRun() int {
	const goodTask = "# GOOD.1. Valid card\nСтатус: todo\nФаза: P0\nТип: task\n" +
		"Владелец файлов: x.go\nЗависимости: \nСсылка: ADR 0001\nАгент: нет\n" +
		"Время: нет\nЦель: c\nКонтекст: k\nШаги: s\nПроверка: v\nПриёмка: a\n"

	check := func(files map[string]string, cfg docsConfig, want func([]gate.Finding) bool) bool {
		root, err := gate.Fixture(files)
		if err != nil {
			fmt.Println("self-test FAILED: fixture:", err)
			return false
		}
		defer gate.RemoveFixture(root)
		fs, err := checkDocs(root, cfg)
		if err != nil {
			fmt.Println("self-test FAILED: checkDocs:", err)
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
	ok = check(map[string]string{"docs/tasks/GOOD.1.md": goodTask}, docsConfig{},
		func(fs []gate.Finding) bool { return len(fs) == 0 }) && ok

	// Planted defects: wrong heading, no status, missing fields.
	ok = check(map[string]string{
		"docs/tasks/BAD.1.md": "# WRONG.1. Broken\nФаза: P0\n",
	}, docsConfig{}, func(fs []gate.Finding) bool {
		return gate.HasRule(fs, "DOC-TASK-HEADING") &&
			gate.HasRule(fs, "DOC-TASK-STATUS") &&
			gate.HasRule(fs, "DOC-TASK-FIELD")
	}) && ok

	// ADR defects: duplicate number is a stale revision, plus bad status and
	// a missing ## Решение section.
	ok = check(map[string]string{
		"docs/adrs/0001-good.md":  "# ADR 1: good\nСтатус: accepted\n\n## Решение\nok\n",
		"docs/adrs/0001-stale.md": "# ADR 2: wrong\nСтатус: bogus\n",
	}, docsConfig{}, func(fs []gate.Finding) bool {
		return gate.HasRule(fs, "DOC-ADR-HEADING") &&
			gate.HasRule(fs, "DOC-ADR-STATUS") &&
			gate.HasRule(fs, "DOC-ADR-STRUCTURE") &&
			gate.HasRule(fs, "DOC-ADR-DUP")
	}) && ok

	// Baseline suppresses a covered finding and flags a row that no longer
	// matches anything.
	ok = check(map[string]string{
		"docs/tasks/BAD.1.md": "# BAD.1. Broken\nСтатус: todo\n",
	}, docsConfig{BaselineLimit: 2, Baseline: []gate.BaselineEntry{
		{Rule: "DOC-TASK-FIELD", Path: "docs/tasks/BAD.1.md", Task: "HD-085"},
		{Rule: "DOC-TASK-STATUS", Path: "docs/tasks/GONE.1.md", Task: "HD-085"},
	}}, func(fs []gate.Finding) bool {
		return len(fs) == 1 && gate.HasRule(fs, "DOC-BASELINE-STALE")
	}) && ok

	if !ok {
		fmt.Println("self-test FAILED: 4 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 4 fixtures")
	return gate.Green
}
