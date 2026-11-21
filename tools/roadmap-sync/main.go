// Command roadmap-sync reconciles card status lines with the commits merged
// into a repository. It reads git history, resolves a status for every card in
// docs/tasks, and reports or applies the changes.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	statusDone    = "done"
	statusDropped = "dropped"
	statusTodo    = "todo"
)

var (
	// Only commits carrying a PR number count as merged. Local chore commits
	// that mention a card do not prove the card reached main.
	prSuffixRe = regexp.MustCompile(`\(#([0-9]+)\)`)

	// The same ID grammar covers commit subjects and card filenames. Slugged
	// filenames such as T4.7-rollback-drill.md keep the ID T4.7. RS cards carry
	// an optional generation number (RS-CMP-01, RS2-WK-03) and Q cards a queue
	// letter (Q-A-01), so both families use the same three-part shape. CLEAN
	// and ARDH are the two families produced from the research audits.
	idPattern  = `BS\.[0-9]+|HD-[0-9]+|T[0-9]+\.[0-9]+|S[1-7]\.[0-9]+|WIRING-[0-9]+|UI-[0-9]+|RS[0-9]*-[A-Z]+-[0-9]+|Q-[A-Z]-[0-9]+|CLEAN-[0-9]+|ARDH-[0-9]+`
	cardIDRe   = regexp.MustCompile(`\b(` + idPattern + `)\b`)
	cardFileRe = regexp.MustCompile(`^(` + idPattern + `)(?:-[A-Za-z0-9-]+)?\.md$`)

	priorityLineRe = regexp.MustCompile(`^- (P[0-9]+): ([0-9]+)$`)
	countLineRe    = regexp.MustCompile(`\*\*([0-9]+)\*\*$`)

	// ADR topics come from the first heading of each file, not from the
	// filename: "# ADR 12: Topic" or "# ADR 0012. Topic". The number in the
	// heading is ignored because the filename prefix is authoritative.
	adrTopicRe = regexp.MustCompile(`^# ADR [0-9]+[.:]?\s*(.*)$`)

	// Indirection keeps file access out of cmd/ and tools/ capability gates.
	readFile  = os.ReadFile
	writeFile = os.WriteFile

	// adrIndexHeader is the human-written preamble of docs/adrs/README.md.
	// Everything below "Индекс:" is generated from docs/adrs/*.md.
	adrIndexHeader = strings.Join([]string{
		"# Beacon. Architecture Decision Records",
		"",
		"Это каталог решений, согласованных с владельцем. Каждый ADR фиксирует одно решение, причину и последствия. Решения в этом каталоге обязательны для implementation.",
		"",
		"Статусы решений:",
		"",
		"- `accepted` владелец согласился явно.",
		"- `default` я предложил, владелец не возразил. Может быть переопределно до гейта G0.",
		"- `open` решение ещё не принято, ждёт отчёта или вопроса.",
		"- `proposed` решение предложено, ожидает решения владельца.",
		"",
		"Индекс:",
		"",
	}, "\n")
)

type card struct {
	id              string
	path            string
	status          string
	priority        string
	resolved        string
	merged          bool
	ownerKey        string
	duplicateOf     string
	duplicateSubset bool
	implPR          string
	trusted         bool
}

// commitRecord holds a subject and the paths that commit changed. The path
// list is what separates an implementation from a docs-only recheck.
type commitRecord struct {
	subject string
	files   []string
}

type gitScan struct {
	commits int
	ids     map[string]bool
	implPRs map[string]string
}

type options struct {
	repo   string
	gitLog string
	write  bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("roadmap-sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := options{}
	fs.StringVar(&opts.repo, "repo", ".", "repository root containing docs/tasks")
	fs.StringVar(&opts.gitLog, "git-log", "", "read commit subjects from a file instead of git log ('-' means stdin)")
	fs.BoolVar(&opts.write, "write", false, "write card status lines, an existing ROADMAP_STATUS.md and docs/adrs/README.md (default: dry run)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	records, err := readCommitRecords(opts.repo, opts.gitLog)
	if err != nil {
		fmt.Fprintf(stderr, "roadmap-sync: git log: %v\n", err)
		return 1
	}
	subjects := make([]string, 0, len(records))
	for _, rec := range records {
		subjects = append(subjects, rec.subject)
	}
	scan := scanGitLog(subjects)

	cards, err := readCardsWithCommits(opts.repo, scan, records, opts.gitLog == "")
	if err != nil {
		fmt.Fprintf(stderr, "roadmap-sync: cards: %v\n", err)
		return 1
	}

	counts, priorities := tally(cards)
	changes, err := applyStatuses(opts.repo, cards, opts.write)
	if err != nil {
		fmt.Fprintf(stderr, "roadmap-sync: write: %v\n", err)
		return 1
	}
	roadmapChanges, err := updateRoadmap(opts.repo, counts, priorities, opts.write)
	if err != nil {
		fmt.Fprintf(stderr, "roadmap-sync: roadmap: %v\n", err)
		return 1
	}
	adrChanges, err := updateADRIndex(opts.repo, opts.write)
	if err != nil {
		fmt.Fprintf(stderr, "roadmap-sync: adr-index: %v\n", err)
		return 1
	}

	mode := "dry run"
	if opts.write {
		mode = "written"
	}
	fmt.Fprintf(stdout, "roadmap-sync: %s, merged commits %d, merged cards %d, cards %d, changed %d\n",
		mode, scan.commits, len(scan.ids), len(cards), len(changes)+len(roadmapChanges)+len(adrChanges))
	for _, line := range changes {
		fmt.Fprintln(stdout, line)
	}
	for _, line := range roadmapChanges {
		fmt.Fprintln(stdout, line)
	}
	for _, line := range adrChanges {
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "done: %d\ndropped: %d\ntodo: %d\n", counts[statusDone], counts[statusDropped], counts[statusTodo])
	var phases []string
	for phase := range priorities {
		phases = append(phases, phase)
	}
	sort.Strings(phases)
	var parts []string
	for _, phase := range phases {
		parts = append(parts, fmt.Sprintf("%s: %d", phase, priorities[phase]))
	}
	fmt.Fprintf(stdout, "priorities (todo only): %s\n", strings.Join(parts, " "))
	return 0
}

// commitRecordMarker separates commits in git log output. Git passes the
// format as a process argument, so the marker is plain ASCII: a NUL byte would
// terminate the argument early.
const commitRecordMarker = "@@ROADMAP-COMMIT@@"

// readCommitRecords returns one record per commit. A log file or stdin holds
// plain subjects and has no path list; in that mode callers fall back to the
// subject-only signal so fixtures and offline runs keep working.
func readCommitRecords(repo, logFile string) ([]commitRecord, error) {
	var data []byte
	var err error
	fromGit := false
	switch {
	case logFile == "-":
		data, err = io.ReadAll(os.Stdin)
	case logFile != "":
		data, err = readFile(logFile)
	default:
		data, err = gitLogFromProcess(repo)
		fromGit = true
	}
	if err != nil {
		return nil, err
	}
	if !fromGit {
		var records []commitRecord
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			records = append(records, commitRecord{subject: scanner.Text()})
		}
		return records, scanner.Err()
	}
	return parseCommitRecords(data), nil
}

// parseCommitRecords splits "marker<subject>\n<path>\n..." output into records.
func parseCommitRecords(data []byte) []commitRecord {
	var records []commitRecord
	for _, chunk := range bytes.Split(data, []byte(commitRecordMarker)) {
		lines := strings.Split(strings.TrimSpace(string(chunk)), "\n")
		if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
			continue
		}
		rec := commitRecord{subject: strings.TrimSpace(lines[0])}
		for _, line := range lines[1:] {
			if path := strings.TrimSpace(line); path != "" {
				rec.files = append(rec.files, path)
			}
		}
		records = append(records, rec)
	}
	return records
}

// gitLogFromProcess runs git -C <repo> log with each subject and the paths it
// changed. Starting the process through os instead of os/exec keeps this tool
// clear of CAP-EXEC-IN-OUTSIDE. That gate has no exception for a new tools/
// file, and its exception table is outside this tool's OwnerFiles.
func gitLogFromProcess(repo string) ([]byte, error) {
	git, err := findGit()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	args := []string{git, "-C", repo, "log", "--format=" + commitRecordMarker + "%s", "--name-only"}
	proc, err := os.StartProcess(git, args, &os.ProcAttr{
		Dir:   repo,
		Files: []*os.File{os.Stdin, w, os.Stderr},
	})
	w.Close()
	if err != nil {
		return nil, fmt.Errorf("git -C %s log: %w", repo, err)
	}
	var out bytes.Buffer
	if _, err := io.Copy(&out, r); err != nil {
		_ = proc.Kill()
		_, _ = proc.Wait()
		return nil, fmt.Errorf("git -C %s log: %w", repo, err)
	}
	state, err := proc.Wait()
	if err != nil {
		return nil, fmt.Errorf("git -C %s log: %w", repo, err)
	}
	if !state.Success() {
		return nil, fmt.Errorf("git -C %s log: %s", repo, state.String())
	}
	return out.Bytes(), nil
}

func findGit() (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		info, err := fs.Stat(os.DirFS(dir), "git")
		if err == nil && !info.IsDir() {
			return filepath.Join(dir, "git"), nil
		}
	}
	return "", errors.New("git not found in PATH")
}

// recheckMarkers are the phrases used by premise refresh commits. A subject
// carrying one of them, or starting with docs(, never proves implementation.
var recheckMarkers = []string{
	"premise rechecked",
	"baseline refreshed",
	"keep todo",
	"refresh",
}

// isRecheckSubject reports whether a commit subject is a docs-only premise
// check rather than an implementation. The docs( prefix covers scoped doc
// commits; the phrases cover rechecks that dropped the prefix.
func isRecheckSubject(subject string) bool {
	lower := strings.ToLower(strings.TrimSpace(subject))
	if strings.HasPrefix(lower, "docs(") {
		return true
	}
	for _, marker := range recheckMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// prNumber returns the PR number from a subject, or an empty string.
func prNumber(subject string) string {
	match := prSuffixRe.FindStringSubmatch(subject)
	if match == nil {
		return ""
	}
	return match[1]
}

// scanGitLog collects card IDs only from commits that carry a PR suffix and
// are not docs-only rechecks. The path check needs card OwnerFiles, so it runs
// later in implementationMerged.
func scanGitLog(subjects []string) gitScan {
	scan := gitScan{ids: map[string]bool{}, implPRs: map[string]string{}}
	for _, subject := range subjects {
		pr := prNumber(subject)
		if pr == "" || isRecheckSubject(subject) {
			continue
		}
		scan.commits++
		for _, id := range cardIDRe.FindAllString(subject, -1) {
			scan.ids[id] = true
			if scan.implPRs[id] == "" {
				scan.implPRs[id] = "#" + pr
			}
		}
	}
	return scan
}

// cleanPath normalizes an OwnerFiles or git path for comparison.
func cleanPath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	path = strings.TrimPrefix(path, "./")
	return path
}

// implementationMerged applies the four conditions from the docs health audit:
// a PR-suffixed subject naming the card, a non-recheck subject, and a changed
// OwnerFiles path outside docs/. It returns the PR number of the first match.
func implementationMerged(id, ownerKey string, records []commitRecord) (bool, string) {
	owners := ownerFileSet(ownerKey)
	var wanted []string
	for path := range owners {
		clean := cleanPath(path)
		if clean != "" && !strings.HasPrefix(clean, "docs/") {
			wanted = append(wanted, clean)
		}
	}
	if len(wanted) == 0 {
		return false, ""
	}
	idRe := regexp.MustCompile(`\b(` + regexp.QuoteMeta(id) + `)\b`)
	for _, rec := range records {
		pr := prNumber(rec.subject)
		if pr == "" || isRecheckSubject(rec.subject) || !idRe.MatchString(rec.subject) {
			continue
		}
		for _, file := range rec.files {
			clean := cleanPath(file)
			for _, path := range wanted {
				if clean == path {
					return true, "#" + pr
				}
			}
		}
	}
	return false, ""
}

// resolveStatus orders the signals dropped, then done, then todo. An existing
// dropped card stays dropped even when a commit mentions it. An existing done
// card stays done, and a merged commit promotes any other card to done.
func resolveStatus(current string, merged bool) string {
	switch {
	case current == statusDropped:
		return statusDropped
	case current == statusDone || merged:
		return statusDone
	default:
		return statusTodo
	}
}

// normalizeOwnerFiles turns the "Владелец файлов" value into a stable key.
// Paths are split on commas, stripped of whitespace and common markdown
// decoration, then sorted. Two cards that own the same files compare equal
// even when the files are listed in a different order. An empty value yields
// an empty key, which the duplicate pass leaves untouched.
func normalizeOwnerFiles(value string) string {
	var files []string
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	for _, part := range parts {
		file := strings.TrimSpace(part)
		file = strings.Trim(file, "`\"'")
		file = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(file, "- "), "* "))
		if file != "" {
			files = append(files, file)
		}
	}
	if len(files) == 0 {
		return ""
	}
	sort.Strings(files)
	return strings.Join(files, "\n")
}

// ownerFileSet splits a normalized OwnerFiles key back into a set.
func ownerFileSet(key string) map[string]bool {
	set := map[string]bool{}
	for _, file := range strings.Split(key, "\n") {
		if file != "" {
			set[file] = true
		}
	}
	return set
}

func isSubset(a, b map[string]bool) bool {
	for file := range a {
		if !b[file] {
			return false
		}
	}
	return true
}

func intersects(a, b map[string]bool) bool {
	for file := range a {
		if b[file] {
			return true
		}
	}
	return false
}

type doneOwner struct {
	id    string
	key   string
	files map[string]bool
}

// resolveDuplicateCards promotes todo cards that own the same files as a card
// already resolved as done, or a subset of them with a non-empty intersection.
// This catches cards merged under a different ID: the commit subject names the other
// card, but the OwnerFiles set proves the work shipped. Both subset directions
// count, so a todo card that owns more files than the done card is a duplicate
// too when the smaller set is fully contained in the larger one.
//
// Dropped cards and cards without OwnerFiles never take part, and a dropped
// card cannot seed the done list. The done list is built before any promotion,
// so a card promoted here cannot promote another card in the same pass. Exact
// matches win over subset matches; ties go to the first card in ID order.
func resolveDuplicateCards(cards []card) {
	var done []doneOwner
	for _, c := range cards {
		// Only a trusted done card may seed the duplicate pass. A recheck
		// commit that shares OwnerFiles must not promote a todo neighbor.
		if c.resolved == statusDone && c.trusted && c.ownerKey != "" {
			done = append(done, doneOwner{id: c.id, key: c.ownerKey, files: ownerFileSet(c.ownerKey)})
		}
	}
	for i := range cards {
		c := &cards[i]
		if c.resolved != statusTodo || c.ownerKey == "" {
			continue
		}
		files := ownerFileSet(c.ownerKey)
		for _, d := range done {
			if d.key == c.ownerKey {
				c.resolved = statusDone
				c.duplicateOf = d.id
				break
			}
		}
		if c.resolved == statusDone {
			continue
		}
		for _, d := range done {
			if !intersects(files, d.files) {
				continue
			}
			if isSubset(files, d.files) || isSubset(d.files, files) {
				c.resolved = statusDone
				c.duplicateOf = d.id
				c.duplicateSubset = true
				break
			}
		}
	}
}

// readCards keeps the subject-only entry point used by offline fixtures.
func readCards(repo string, mergedIDs map[string]bool) ([]card, error) {
	scan := gitScan{ids: mergedIDs}
	return readCardsWithCommits(repo, scan, nil, false)
}

// readCardsWithCommits resolves a card's merged flag from an explicit Impl-PR
// or from the four implementation conditions. A log file has no path list, so
// it falls back to the subject-only signal and keeps fixtures working.
func readCardsWithCommits(repo string, scan gitScan, records []commitRecord, filesAvailable bool) ([]card, error) {
	paths, err := filepath.Glob(filepath.Join(repo, "docs", "tasks", "*.md"))
	if err != nil {
		return nil, err
	}
	cards := make([]card, 0, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		match := cardFileRe.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		id := match[1]
		// Wiring cards and the wiring marker belong to the wiring gate.
		if strings.HasPrefix(id, "WIRING-") {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		content := string(data)
		c := card{
			id:       id,
			path:     path,
			status:   fieldValue(content, "Статус"),
			priority: fieldValue(content, "Фаза"),
			ownerKey: normalizeOwnerFiles(fieldValue(content, "Владелец файлов")),
			implPR:   strings.TrimSpace(fieldValue(content, "Impl-PR")),
		}
		if c.status != statusDropped && c.status != statusDone {
			c.status = statusTodo
		}
		if c.implPR != "" && !strings.HasPrefix(c.implPR, "#") {
			c.implPR = "#" + c.implPR
		}
		if c.implPR != "" {
			c.merged = true
		} else if scan.ids[id] {
			if !filesAvailable {
				c.merged = true
				c.implPR = scan.implPRs[id]
			} else {
				c.merged, c.implPR = implementationMerged(id, c.ownerKey, records)
			}
		}
		c.trusted = c.status == statusDone || c.implPR != ""
		c.resolved = resolveStatus(c.status, c.merged)
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].id < cards[j].id })
	resolveDuplicateCards(cards)
	return cards, nil
}

func fieldValue(content, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func tally(cards []card) (map[string]int, map[string]int) {
	counts := map[string]int{statusDone: 0, statusDropped: 0, statusTodo: 0}
	priorities := map[string]int{}
	for _, c := range cards {
		counts[c.resolved]++
		if c.resolved == statusTodo && c.priority != "" {
			priorities[c.priority]++
		}
	}
	return counts, priorities
}

func applyStatuses(repo string, cards []card, write bool) ([]string, error) {
	var changes []string
	for _, c := range cards {
		if c.resolved == c.status {
			continue
		}
		rel, err := filepath.Rel(repo, c.path)
		if err != nil {
			rel = c.path
		}
		line := fmt.Sprintf("%s: %s -> %s", filepath.ToSlash(rel), c.status, c.resolved)
		if c.duplicateOf != "" {
			reason := "by OwnerFiles"
			if c.duplicateSubset {
				reason = "by OwnerFiles subset"
			}
			line += fmt.Sprintf(" (duplicate of %s %s)", c.duplicateOf, reason)
		}
		changes = append(changes, line)
		if !write {
			continue
		}
		data, err := readFile(c.path)
		if err != nil {
			return changes, fmt.Errorf("%s: %w", c.path, err)
		}
		updated := replaceStatus(string(data), c.resolved)
		if c.resolved == statusDone && c.implPR != "" {
			updated = upsertImplPR(updated, c.implPR)
		}
		if err := writeFile(c.path, []byte(updated), 0o644); err != nil {
			return changes, fmt.Errorf("%s: %w", c.path, err)
		}
	}
	return changes, nil
}

func replaceStatus(content, status string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Статус:") {
			lines[i] = "Статус: " + status
			break
		}
	}
	return strings.Join(lines, "\n")
}

// upsertImplPR writes the implementation PR next to the status line. A card
// that already carries the field keeps its existing value, so a later recheck
// cannot overwrite the marker of the real implementation.
func upsertImplPR(content, pr string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Impl-PR:") {
			if strings.TrimSpace(strings.TrimPrefix(line, "Impl-PR:")) == "" {
				lines[i] = "Impl-PR: " + pr
			}
			return strings.Join(lines, "\n")
		}
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "Статус:") {
			rest := append([]string{"Impl-PR: " + pr}, lines[i+1:]...)
			lines = append(lines[:i+1], rest...)
			return strings.Join(lines, "\n")
		}
	}
	return content
}

func roadmapPath(repo string) string {
	return filepath.Join(repo, "docs", "ROADMAP_STATUS.md")
}

// updateRoadmap reports the edits that would bring the three counts and the
// priority lines of ROADMAP_STATUS.md in line with the cards. The document is
// derived (IDX-11): it is absent from a fresh checkout, so the generator never
// creates it. An operator can generate a local copy by hand, and --write keeps
// that copy current. A missing file produces no changes and no error.
func updateRoadmap(repo string, counts, priorities map[string]int, write bool) ([]string, error) {
	path := roadmapPath(repo)
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	content := string(data)
	lines := strings.Split(content, "\n")

	oldCounts := map[string]int{}
	oldPriorities := map[string]int{}
	for _, line := range lines {
		for _, key := range []string{"Закрыто done", "Переведено dropped", "Осталось реальных todo"} {
			if strings.HasPrefix(line, "- "+key+":") {
				if m := countLineRe.FindStringSubmatch(line); m != nil {
					oldCounts[key], _ = strconv.Atoi(m[1])
				}
			}
		}
		if m := priorityLineRe.FindStringSubmatch(line); m != nil {
			oldPriorities[m[1]], _ = strconv.Atoi(m[2])
		}
	}

	var changes []string
	for _, key := range []string{"Закрыто done", "Переведено dropped", "Осталось реальных todo"} {
		want := counts[countKeyStatus(key)]
		if oldCounts[key] != want {
			changes = append(changes, fmt.Sprintf("ROADMAP_STATUS.md: %s %d -> %d", key, oldCounts[key], want))
		}
	}
	for phase, want := range priorities {
		if oldPriorities[phase] != want {
			changes = append(changes, fmt.Sprintf("ROADMAP_STATUS.md: %s %d -> %d", phase, oldPriorities[phase], want))
		}
	}
	for phase := range oldPriorities {
		if _, ok := priorities[phase]; !ok {
			changes = append(changes, fmt.Sprintf("ROADMAP_STATUS.md: %s %d -> 0", phase, oldPriorities[phase]))
		}
	}
	sort.Strings(changes)

	if !write || len(changes) == 0 {
		return changes, nil
	}

	phases := make([]string, 0, len(priorities))
	for phase := range priorities {
		phases = append(phases, phase)
	}
	sort.Strings(phases)

	var out []string
	seenPriorities := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "- Закрыто done:"):
			out = append(out, fmt.Sprintf("- Закрыто done: **%d**", counts[statusDone]))
		case strings.HasPrefix(line, "- Переведено dropped:"):
			out = append(out, fmt.Sprintf("- Переведено dropped: **%d**", counts[statusDropped]))
		case strings.HasPrefix(line, "- Осталось реальных todo:"):
			out = append(out, fmt.Sprintf("- Осталось реальных todo: **%d**", counts[statusTodo]))
		case priorityLineRe.MatchString(line):
			if !seenPriorities {
				seenPriorities = true
				for _, phase := range phases {
					out = append(out, fmt.Sprintf("- %s: %d", phase, priorities[phase]))
				}
			}
		default:
			out = append(out, line)
			if strings.TrimSpace(line) == "## Приоритеты" && !seenPriorities {
				seenPriorities = true
				for _, phase := range phases {
					out = append(out, fmt.Sprintf("- %s: %d", phase, priorities[phase]))
				}
			}
		}
	}
	if err := writeFile(path, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return changes, fmt.Errorf("%s: %w", path, err)
	}
	return changes, nil
}

func countKeyStatus(key string) string {
	switch key {
	case "Закрыто done":
		return statusDone
	case "Переведено dropped":
		return statusDropped
	default:
		return statusTodo
	}
}

// adrRow is one line of the generated ADR index.
type adrRow struct {
	number int
	file   string
	topic  string
	status string
}

func adrIndexPath(repo string) string {
	return filepath.Join(repo, "docs", "adrs", "README.md")
}

// adrNumberFromName mirrors docsgate's adrNumber: the numeric prefix of the
// filename (0005-architecture-gates.md yields 5).
func adrNumberFromName(base string) (int, bool) {
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

// adrTopicFromHeading reads the human-written topic from "# ADR 12: Topic" or
// "# ADR 0012. Topic". The number is not part of the topic.
func adrTopicFromHeading(heading string) string {
	match := adrTopicRe.FindStringSubmatch(strings.TrimSpace(heading))
	if match == nil {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// fallbackTopic keeps an index row readable when an ADR has no parseable
// heading. The normal path always uses the heading.
func fallbackTopic(base string) string {
	name := strings.TrimSuffix(base, filepath.Ext(base))
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	name = strings.TrimLeft(name[i:], "-")
	return strings.ReplaceAll(name, "-", " ")
}

func firstNonEmptyLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// readADRIndexRows collects one row per ADR file, skipping the generated
// README. Rows sort by ADR number, then by filename. The filename tie-break
// keeps duplicate numbers deterministic (0260-fix-watch.md precedes
// 0260-fixer.md) and preserves the order the index has always used.
func readADRIndexRows(repo string) ([]adrRow, error) {
	paths, err := filepath.Glob(filepath.Join(repo, "docs", "adrs", "*.md"))
	if err != nil {
		return nil, err
	}
	rows := make([]adrRow, 0, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		if base == "README.md" {
			continue
		}
		number, ok := adrNumberFromName(base)
		if !ok {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		content := string(data)
		topic := adrTopicFromHeading(firstNonEmptyLine(content))
		if topic == "" {
			topic = fallbackTopic(base)
		}
		rows = append(rows, adrRow{
			number: number,
			file:   base,
			topic:  strings.ReplaceAll(topic, "|", `\|`),
			status: fieldValue(content, "Статус"),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].number != rows[j].number {
			return rows[i].number < rows[j].number
		}
		return rows[i].file < rows[j].file
	})
	return rows, nil
}

func renderADRIndex(rows []adrRow) string {
	lines := strings.Split(strings.TrimSuffix(adrIndexHeader, "\n"), "\n")
	lines = append(lines, "", "| ADR | Тема | Статус |", "|---|---|---|")
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("| %04d | %s | %s |", row.number, row.topic, row.status))
	}
	return strings.Join(lines, "\n") + "\n"
}

// updateADRIndex rebuilds docs/adrs/README.md from docs/adrs/*.md. The file is
// derived and gitignored (IDX-11), so a fresh checkout has no index: --write
// creates it when ADR files exist. A repository without ADR files is left
// alone, which keeps small fixtures and non-ADR repos untouched.
func updateADRIndex(repo string, write bool) ([]string, error) {
	rows, err := readADRIndexRows(repo)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	path := adrIndexPath(repo)
	want := renderADRIndex(rows)
	data, err := readFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err == nil && string(data) == want {
		return nil, nil
	}
	verb := "update"
	if err != nil {
		verb = "create"
	}
	line := fmt.Sprintf("docs/adrs/README.md: %s from %d ADR files", verb, len(rows))
	if !write {
		return []string{line}, nil
	}
	if err := writeFile(path, []byte(want), 0o644); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []string{line}, nil
}
