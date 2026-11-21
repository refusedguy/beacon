package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const mockGitLog = `chore: close BS.1 without a PR number
feat: event log (BS.2) (#101)
feat: triage (BS.3, T3.7) (#102)
feat: wiring (WIRING-001) (#103)
feat: drill (T4.7) (#104)
docs: notes about ADR 0203 (#105)
`

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFixtureFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newFixture builds a small repository with cards, a stale roadmap and a
// commit log file. The IDs and phases are chosen so each status rule has a
// distinct observable result.
func newFixture(t *testing.T) (repo, logPath string) {
	t.Helper()
	repo = t.TempDir()
	logPath = filepath.Join(repo, "git-log.txt")
	writeFixtureFile(t, logPath, mockGitLog)

	cards := map[string]string{
		"BS.1.md":                "# BS.1\nСтатус: todo\nФаза: P1\n",
		"BS.2.md":                "# BS.2\nСтатус: todo\nФаза: P2\n",
		"BS.3.md":                "# BS.3\nСтатус: dropped\nФаза: P3\n",
		"HD-001.md":              "# HD-001\nСтатус: done\nФаза: P2\n",
		"T3.7.md":                "# T3.7\nСтатус: todo\nФаза: P1\n",
		"T4.7-rollback-drill.md": "# T4.7\nСтатус: todo\nФаза: P3\n",
		"UI-01.md":               "# UI-01\nСтатус: todo\nФаза: P2\n",
		"WIRING-001.md":          "# WIRING-001\nСтатус: todo\nФаза: P1\n",
	}
	for name, content := range cards {
		writeFixtureFile(t, filepath.Join(repo, "docs", "tasks", name), content)
	}

	writeFixtureFile(t, filepath.Join(repo, "docs", "ROADMAP_STATUS.md"), `# Beacon roadmap status

- Закрыто done: **110**
- Переведено dropped: **340**
- Осталось реальных todo: **206**

## Приоритеты

- P1: 17
- P2: 71
- P3: 118

## Реестр оставшихся todo

| ID | Приоритет | OwnerFiles |
|---|---|---|
| BS.1 | P1 | internal/x.go |
`)
	writeFixtureFile(t, filepath.Join(repo, ".beacon", "wiring-pending"), "WIRING: WIRING-001\n")
	return repo, logPath
}

// newDuplicateFixture builds cards whose OwnerFiles sets exercise the second
// pass. T3.103 is merged by commit; BS.25 lists the same two files in the
// opposite order and is not named in the commit. BS.24 owns only one of those
// files. The remaining cards guard the dropped, empty and different-OwnerFiles
// cases.
func newDuplicateFixture(t *testing.T) (repo, logPath string) {
	t.Helper()
	repo = t.TempDir()
	logPath = filepath.Join(repo, "git-log.txt")
	writeFixtureFile(t, logPath, "feat: webhook receiver (T3.103) (#201)\n")

	cards := map[string]string{
		"T3.103.md": "# T3.103\nСтатус: todo\nФаза: P1\nВладелец файлов: internal/infra/webhook/receiver.go, internal/infra/webhook/receiver_test.go\n",
		"BS.25.md":  "# BS.25\nСтатус: todo\nФаза: P2\nВладелец файлов: internal/infra/webhook/receiver_test.go, internal/infra/webhook/receiver.go\n",
		"BS.24.md":  "# BS.24\nСтатус: todo\nФаза: P1\nВладелец файлов: internal/infra/webhook/receiver.go\n",
		"BS.19.md":  "# BS.19\nСтатус: dropped\nФаза: P1\nВладелец файлов: internal/infra/webhook/receiver.go, internal/infra/webhook/receiver_test.go\n",
		"BS.23.md":  "# BS.23\nСтатус: todo\nФаза: P1\nВладелец файлов:\n",
		"BS.26.md":  "# BS.26\nСтатус: todo\nФаза: P3\nВладелец файлов: internal/infra/store/webhook_events.go\n",
	}
	for name, content := range cards {
		writeFixtureFile(t, filepath.Join(repo, "docs", "tasks", name), content)
	}
	writeFixtureFile(t, filepath.Join(repo, "docs", "ROADMAP_STATUS.md"), `# Beacon roadmap status

- Закрыто done: **0**
- Переведено dropped: **0**
- Осталось реальных todo: **0**

## Приоритеты
`)
	return repo, logPath
}

func TestNormalizeOwnerFilesIgnoresOrderAndDecoration(t *testing.T) {
	want := normalizeOwnerFiles("a.go, b.go")
	for _, value := range []string{
		"a.go, b.go",
		"b.go, a.go",
		" b.go , `a.go` ",
		"- b.go\n- a.go",
	} {
		if got := normalizeOwnerFiles(value); got != want {
			t.Errorf("normalizeOwnerFiles(%q) = %q, want %q", value, got, want)
		}
	}
	for _, value := range []string{"", "   ", ", ,"} {
		if got := normalizeOwnerFiles(value); got != "" {
			t.Errorf("normalizeOwnerFiles(%q) = %q, want empty", value, got)
		}
	}
}

func TestResolveDuplicateCardsByOwnerFiles(t *testing.T) {
	repo, logPath := newDuplicateFixture(t)
	ids := scanGitLog(strings.Split(strings.TrimSpace(readFixtureFile(t, logPath)), "\n")).ids
	cards, err := readCards(repo, ids)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]card{}
	for _, c := range cards {
		byID[c.id] = c
	}

	if got := byID["T3.103"].resolved; got != statusDone {
		t.Errorf("merged card T3.103 resolved = %q, want done", got)
	}
	if got := byID["BS.25"].resolved; got != statusDone {
		t.Errorf("duplicate BS.25 resolved = %q, want done", got)
	}
	if got := byID["BS.25"].duplicateOf; got != "T3.103" {
		t.Errorf("BS.25 duplicateOf = %q, want T3.103", got)
	}
	if byID["BS.25"].duplicateSubset {
		t.Error("BS.25 is an exact match and must not be reported as a subset")
	}
	if got := byID["BS.24"].resolved; got != statusDone {
		t.Errorf("subset BS.24 resolved = %q, want done", got)
	}
	if got := byID["BS.24"].duplicateOf; got != "T3.103" {
		t.Errorf("BS.24 duplicateOf = %q, want T3.103", got)
	}
	if !byID["BS.24"].duplicateSubset {
		t.Error("BS.24 must be reported as a subset duplicate")
	}
	if got := byID["BS.19"].resolved; got != statusDropped {
		t.Errorf("dropped BS.19 resolved = %q, want dropped", got)
	}
	if got := byID["BS.23"].resolved; got != statusTodo {
		t.Errorf("empty OwnerFiles BS.23 resolved = %q, want todo", got)
	}
	if got := byID["BS.26"].resolved; got != statusTodo {
		t.Errorf("different OwnerFiles BS.26 resolved = %q, want todo", got)
	}
}

func TestRunDryRunReportsResolvedDuplicates(t *testing.T) {
	repo, logPath := newDuplicateFixture(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"-repo", repo, "-git-log", logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	for _, line := range []string{
		"docs/tasks/T3.103.md: todo -> done",
		"docs/tasks/BS.25.md: todo -> done (duplicate of T3.103 by OwnerFiles)",
		"docs/tasks/BS.24.md: todo -> done (duplicate of T3.103 by OwnerFiles subset)",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output missing %q:\n%s", line, out)
		}
	}
	for _, id := range []string{"BS.19.md", "BS.23.md", "BS.26.md"} {
		if strings.Contains(out, id) {
			t.Errorf("dry run reported unchanged card %s:\n%s", id, out)
		}
	}
}

func TestResolveDuplicateCardsWithSeveralDoneOwners(t *testing.T) {
	repo := t.TempDir()
	cards := map[string]string{
		"BS.32.md": "# BS.32\nСтатус: done\nВладелец файлов: a.go, b.go\n",
		"T3.99.md": "# T3.99\nСтатус: done\nВладелец файлов: b.go, a.go\n",
		"BS.23.md": "# BS.23\nСтатус: todo\nВладелец файлов: b.go, a.go\n",
		"BS.25.md": "# BS.25\nСтатус: todo\nВладелец файлов: a.go, b.go\n",
	}
	for name, content := range cards {
		writeFixtureFile(t, filepath.Join(repo, "docs", "tasks", name), content)
	}
	got, err := readCards(repo, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c.resolved != statusDone {
			t.Errorf("%s resolved = %q, want done", c.id, c.resolved)
		}
		if c.duplicateOf != "" && c.duplicateOf != "BS.32" && c.duplicateOf != "T3.99" {
			t.Errorf("%s duplicateOf = %q, want a done owner", c.id, c.duplicateOf)
		}
	}
}

func TestResolveDuplicateCardsByOwnerFilesSubset(t *testing.T) {
	repo := t.TempDir()
	cards := map[string]string{
		"T3.103.md": "# T3.103\nСтатус: done\nВладелец файлов: a.go, b.go\n",
		"T3.104.md": "# T3.104\nСтатус: done\nВладелец файлов: x.go\n",
		"BS.19.md":  "# BS.19\nСтатус: todo\nВладелец файлов: a.go, q.go\n",
		"BS.23.md":  "# BS.23\nСтатус: todo\nВладелец файлов: a.go\n",
		"BS.25.md":  "# BS.25\nСтатус: todo\nВладелец файлов: x.go, y.go\n",
		"BS.26.md":  "# BS.26\nСтатус: todo\nВладелец файлов: z.go\n",
		"BS.27.md":  "# BS.27\nСтатус: dropped\nВладелец файлов: a.go\n",
		"BS.28.md":  "# BS.28\nСтатус: todo\nВладелец файлов:\n",
	}
	for name, content := range cards {
		writeFixtureFile(t, filepath.Join(repo, "docs", "tasks", name), content)
	}
	got, err := readCards(repo, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]card{}
	for _, c := range got {
		byID[c.id] = c
	}

	// The todo set is contained in the done set.
	if g := byID["BS.23"]; g.resolved != statusDone || g.duplicateOf != "T3.103" || !g.duplicateSubset {
		t.Errorf("BS.23 = %+v, want done as subset of T3.103", g)
	}
	// The done set is contained in the todo set.
	if g := byID["BS.25"]; g.resolved != statusDone || g.duplicateOf != "T3.104" || !g.duplicateSubset {
		t.Errorf("BS.25 = %+v, want done as superset of T3.104", g)
	}
	// A shared file alone is not enough without containment.
	if g := byID["BS.19"]; g.resolved != statusTodo {
		t.Errorf("BS.19 = %+v, want todo when sets only intersect", g)
	}
	if g := byID["BS.26"]; g.resolved != statusTodo {
		t.Errorf("BS.26 = %+v, want todo with disjoint OwnerFiles", g)
	}
	if g := byID["BS.27"]; g.resolved != statusDropped {
		t.Errorf("BS.27 = %+v, want dropped card untouched", g)
	}
	if g := byID["BS.28"]; g.resolved != statusTodo {
		t.Errorf("BS.28 = %+v, want todo with empty OwnerFiles", g)
	}
}

func TestScanGitLogExtractsOnlyMergedCards(t *testing.T) {
	scan := scanGitLog(strings.Split(strings.TrimSpace(mockGitLog), "\n"))
	if scan.commits != 5 {
		t.Fatalf("merged commits = %d, want 5 (PR-suffixed lines)", scan.commits)
	}
	want := []string{"BS.2", "BS.3", "T3.7", "WIRING-001", "T4.7"}
	for _, id := range want {
		if !scan.ids[id] {
			t.Errorf("missing merged ID %s", id)
		}
	}
	if scan.ids["BS.1"] {
		t.Error("BS.1 was mentioned only without a PR suffix and must not count as merged")
	}
	if scan.ids["0203"] || scan.ids["ADR"] {
		t.Errorf("unexpected IDs extracted: %v", scan.ids)
	}
}

func TestIDPatternParsesRSAndQCards(t *testing.T) {
	// Filenames: the ID is the stem before any slug suffix.
	for name, want := range map[string]string{
		"RS-CMP-01.md":       "RS-CMP-01",
		"RS2-WK-03.md":       "RS2-WK-03",
		"RS-MCP-07-notes.md": "RS-MCP-07",
		"Q-A-01.md":          "Q-A-01",
		"Q-D-03.md":          "Q-D-03",
	} {
		match := cardFileRe.FindStringSubmatch(name)
		if match == nil || match[1] != want {
			t.Errorf("cardFileRe(%q) = %v, want ID %q", name, match, want)
		}
	}

	// Commit subjects: each RS/Q ID in a PR-suffixed subject counts as merged.
	subject := "feat: pass base SHA (RS2-CMP-01), add probe (RS-OBS-03), queue gate (Q-A-02) (#999)"
	scan := scanGitLog([]string{subject})
	for _, id := range []string{"RS2-CMP-01", "RS-OBS-03", "Q-A-02"} {
		if !scan.ids[id] {
			t.Errorf("scanGitLog merged IDs missing %s: %v", id, scan.ids)
		}
	}
	if len(scan.ids) != 3 {
		t.Errorf("scanGitLog merged IDs = %v, want exactly three RS/Q IDs", scan.ids)
	}
}

func TestReadCardsIncludesRSAndQ(t *testing.T) {
	repo := t.TempDir()
	fixtures := map[string]string{
		"RS-CMP-01.md": "# RS-CMP-01\nСтатус: done\nФаза: P3\n",
		"RS2-WK-03.md": "# RS2-WK-03\nСтатус: todo\nФаза: P3\n",
		"Q-A-01.md":    "# Q-A-01\nСтатус: todo\nФаза: P0\n",
	}
	for name, content := range fixtures {
		writeFixtureFile(t, filepath.Join(repo, "docs", "tasks", name), content)
	}
	got, err := readCards(repo, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]card{}
	for _, c := range got {
		byID[c.id] = c
	}
	for name := range fixtures {
		id := strings.TrimSuffix(name, ".md")
		c, ok := byID[id]
		if !ok {
			t.Errorf("readCards skipped %s: got IDs %v", id, keys(byID))
			continue
		}
		wantStatus := statusTodo
		if id == "RS-CMP-01" {
			wantStatus = statusDone
		}
		if c.resolved != wantStatus {
			t.Errorf("%s resolved = %q, want %q", id, c.resolved, wantStatus)
		}
	}
}

func keys(m map[string]card) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestResolveStatusPriority(t *testing.T) {
	tests := []struct {
		name    string
		current string
		merged  bool
		want    string
	}{
		{"dropped beats merged", statusDropped, true, statusDropped},
		{"dropped beats done", statusDropped, false, statusDropped},
		{"done stays done without a commit", statusDone, false, statusDone},
		{"merged promotes todo", statusTodo, true, statusDone},
		{"todo stays todo", statusTodo, false, statusTodo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveStatus(tt.current, tt.merged); got != tt.want {
				t.Errorf("resolveStatus(%q, %v) = %q, want %q", tt.current, tt.merged, got, tt.want)
			}
		})
	}
}

func TestRunDryRunReportsWithoutWriting(t *testing.T) {
	repo, logPath := newFixture(t)
	cardPath := filepath.Join(repo, "docs", "tasks", "BS.2.md")
	roadmapPath := filepath.Join(repo, "docs", "ROADMAP_STATUS.md")
	beforeCard := readFixtureFile(t, cardPath)
	beforeRoadmap := readFixtureFile(t, roadmapPath)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-repo", repo, "-git-log", logPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	for _, line := range []string{
		"docs/tasks/BS.2.md: todo -> done",
		"docs/tasks/T3.7.md: todo -> done",
		"docs/tasks/T4.7-rollback-drill.md: todo -> done",
		"done: 4",
		"dropped: 1",
		"todo: 2",
		"P1: 1 P2: 1",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output missing %q:\n%s", line, out)
		}
	}
	// An existing dropped card and an existing done card must not be reported.
	for _, line := range []string{"BS.3.md", "HD-001.md"} {
		if strings.Contains(out, line) {
			t.Errorf("dry run reported unchanged card %s:\n%s", line, out)
		}
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("dry-run mode not reported:\n%s", out)
	}
	if got := readFixtureFile(t, cardPath); got != beforeCard {
		t.Errorf("dry run changed the card:\n%s", got)
	}
	if got := readFixtureFile(t, roadmapPath); got != beforeRoadmap {
		t.Errorf("dry run changed the roadmap:\n%s", got)
	}
}

func TestRunWriteUpdatesCardsAndRoadmapOnly(t *testing.T) {
	repo, logPath := newFixture(t)
	wiringPath := filepath.Join(repo, "docs", "tasks", "WIRING-001.md")
	markerPath := filepath.Join(repo, ".beacon", "wiring-pending")
	beforeWiring := readFixtureFile(t, wiringPath)
	beforeMarker := readFixtureFile(t, markerPath)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-repo", repo, "-git-log", logPath, "-write"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "written") {
		t.Errorf("write mode not reported:\n%s", stdout.String())
	}

	statusOf := func(name string) string {
		content := readFixtureFile(t, filepath.Join(repo, "docs", "tasks", name))
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(line, "Статус:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "Статус:"))
			}
		}
		return ""
	}
	for name, want := range map[string]string{
		"BS.1.md":                "todo",
		"BS.2.md":                "done",
		"BS.3.md":                "dropped",
		"HD-001.md":              "done",
		"T3.7.md":                "done",
		"T4.7-rollback-drill.md": "done",
		"UI-01.md":               "todo",
	} {
		if got := statusOf(name); got != want {
			t.Errorf("%s status = %q, want %q", name, got, want)
		}
	}

	roadmap := readFixtureFile(t, filepath.Join(repo, "docs", "ROADMAP_STATUS.md"))
	for _, line := range []string{
		"- Закрыто done: **4**",
		"- Переведено dropped: **1**",
		"- Осталось реальных todo: **2**",
		"- P1: 1",
		"- P2: 1",
	} {
		if !strings.Contains(roadmap, line) {
			t.Errorf("roadmap missing %q:\n%s", line, roadmap)
		}
	}
	if strings.Contains(roadmap, "- P3: 118") {
		t.Errorf("stale priority line survived:\n%s", roadmap)
	}
	if !strings.Contains(roadmap, "| BS.1 | P1 | internal/x.go |") {
		t.Errorf("registry table was rewritten:\n%s", roadmap)
	}
	if got := readFixtureFile(t, wiringPath); got != beforeWiring {
		t.Errorf("wiring card changed:\n%s", got)
	}
	if got := readFixtureFile(t, markerPath); got != beforeMarker {
		t.Errorf("wiring marker changed:\n%s", got)
	}
}

func TestRunCountsAllCardKinds(t *testing.T) {
	repo, _ := newFixture(t)
	cards, err := readCards(repo, scanGitLog(strings.Split(strings.TrimSpace(mockGitLog), "\n")).ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 7 {
		t.Fatalf("cards = %d, want 7 (WIRING cards are excluded)", len(cards))
	}
	counts, priorities := tally(cards)
	if counts[statusDone] != 4 || counts[statusDropped] != 1 || counts[statusTodo] != 2 {
		t.Errorf("counts = %v, want done=4 dropped=1 todo=2", counts)
	}
	if priorities["P1"] != 1 || priorities["P2"] != 1 || len(priorities) != 2 {
		t.Errorf("priorities = %v, want only P1=1 P2=1", priorities)
	}
}
