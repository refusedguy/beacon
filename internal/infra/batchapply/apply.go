// Package batchapply turns structured research JSON into ADR, task and eval
// documents. It lives in infra because it reads and writes files and shells
// out to git and gh; cmd/batch-apply stays a thin flag-parsing entry point.
package batchapply

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Language policy (HD-053, ADR 0304). Every string this tool emits belongs to
// one scope, and each scope has one language:
//
//   - interface: Russian. Flag help and human-readable report prose address
//     the operator in Russian.
//   - artifacts: Russian. Generated ADR, card and eval documents use Russian
//     headings and labels.
//   - technical terms: English. Identifiers, flag names, status values, report
//     keys and token names stay English so code and scripts can rely on them.
//
// A scope keeps its language. A Russian sentence with English identifiers
// follows the rule. Prose in two languages in one string has no rule and is a
// defect.
type languageScope string

const (
	scopeInterface languageScope = "interface"
	scopeArtifacts languageScope = "artifacts"
	scopeTechnical languageScope = "technical"
)

const (
	languageInterface = "ru"
	languageArtifacts = "ru"
	languageTechnical = "en"
)

// languageFor returns the language declared for a scope. An unknown scope has
// no declared language and returns an empty string.
func languageFor(scope languageScope) string {
	switch scope {
	case scopeInterface:
		return languageInterface
	case scopeArtifacts:
		return languageArtifacts
	case scopeTechnical:
		return languageTechnical
	default:
		return ""
	}
}

// Report lines keep the concise style from HD-039: space-separated fields, no
// label prefix, no filler words, and words instead of arrows. The language of
// each line follows the policy above. Structured lines use English keys as
// technical terms; prose lines are Russian. Keeping the formats here lets
// tests pin the exact output a caller sees.
const (
	checkReportFormat   = "valid=%d invalid=%d\n"
	summaryReportFormat = "batches=%d adrs=%d cards=%d evals=%d closures=%d\n"
	// HD-066 (ADR 0307): the iteration report carries the SHA-256 of the
	// artifact set. The next refinement reads it as previous_artifact_hash and
	// can verify what changed. The key stays English as a technical term.
	hashReportFormat  = "hash=%s\n"
	mergeReportFormat = "merged=%d held=%d\n"
	// ATX-13: a merge decision records the JSON file that declared it and the
	// SHA-256 of that file. The digest ties the decision to one research input.
	decisionReportFormat  = "PR %d: решение %s, источник %s, hash=%s\n"
	confirmRequiredFormat = "--merge-plan требует флага --confirm-merge\n"
	renumberReportFormat  = "перенумеровать ADR %d в %d\n"
	skipReportFormat      = "пропуск %s\n"
	warnReportFormat      = "предупреждение: не найдена цель закрытия %s\n"
	// HD-088 (ADR 0313): a number with no source has no owner and breaks the
	// card. The line is Russian prose, so it keeps the interface scope.
	factProblemFormat = "число без владельца: %s\n"
	// Q-A4: a generated card field outside its closed set fails --check before
	// any artifact reaches disk. The field name is a technical token, the
	// complaint is Russian interface prose.
	invalidFieldFormat = "недопустимое значение поля %s: %s\n"
	// ATX-12: an empty card ID or one carrying "..", "/" or "\" escapes
	// docs/tasks once it is joined onto a file path. Reject it before any
	// artifact path is built.
	invalidCardIDFormat = "недопустимый идентификатор карточки: %s\n"
	// CBX-06: chunked writes bound the time the shared lock is held. One
	// progress line is emitted per chunk and one summary line closes the run.
	// Keys stay English as technical terms; held values are Go durations.
	chunkProgressFormat = "chunk=%d/%d records=%d held=%s\n"
	chunkSummaryFormat  = "chunks=%d maxheld=%s\n"
	// CBX-07: a stale row is named in the report and never published. Keys
	// stay English as technical terms; the id list is comma separated.
	staleSkippedFormat  = "StaleSkippedIDs=%s\n"
	updatedIssuesFormat = "UpdatedIssues=%d\n"
	// CBX-08: the remaining lists keep the same key style. UpdatedIDs names
	// the rows whose content changed; the count line above stays unchanged.
	createdIDsFormat   = "CreatedIDs=%s\n"
	updatedIDsFormat   = "UpdatedIDs=%s\n"
	tieKeptLocalFormat = "TieKeptLocalIDs=%s\n"
	// CC-04: the partial write delta names every path that reached disk before
	// the run ended. The line is emitted on success, on dry runs and on the
	// error path, so a failed batch still says which files it created.
	writtenPathsFormat = "WrittenPaths=%s\n"
)

// CBX-07: an incoming row carries the time its source was changed. A local
// artifact is fresh when its file mtime is later than that time. Rows without
// a timestamp are never judged stale, so existing batches keep their meaning.
// The value is a string because research JSON uses RFC3339 and the field is
// optional; parseUpdatedAt parses it once into time.Time for comparison.
const updatedAtLayout = time.RFC3339Nano

// CBX-06 test seams. The values come from the beads measurement: 250 records
// per chunk and a 150 ms pause let a concurrent reader take the lock while a
// 26k import finishes. Tests lower both so a full pass runs in milliseconds.
var (
	chunkSize  = 250
	chunkPause = 150 * time.Millisecond
)

// parseUpdatedAt turns an optional RFC3339 timestamp into a time. An empty or
// malformed value yields the zero time, which means "no timestamp" and never
// makes a row stale.
func parseUpdatedAt(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	t, err := time.Parse(updatedAtLayout, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Options mirrors the command line of cmd/batch-apply.
type Options struct {
	Dir          string
	DryRun       bool
	Check        bool
	MergePlan    bool
	ConfirmMerge bool
	Quiet        bool
	Commit       bool
	Facts        bool
	// ChunkSize overrides chunkSize when positive. ChunkPause overrides
	// chunkPause when positive. Zero keeps the package default, so a caller
	// cannot accidentally hold the lock for a whole import.
	ChunkSize  int
	ChunkPause time.Duration
	// Force is the explicit overwrite path for a manual run. Automatic
	// imports leave it false, so a fresh local card is never replaced.
	Force bool
	// JSONReport receives the machine-readable import result when set. A nil
	// writer keeps the text-only contract of existing callers. The report is
	// always emitted as one JSON object, so a consumer never parses the text
	// report to learn what the import changed (CBX-08).
	JSONReport io.Writer
}

type batch struct {
	SourceReports  []string        `json:"source_reports"`
	ADRs           []adr           `json:"adrs"`
	Cards          []card          `json:"cards"`
	Evals          []evalCase      `json:"evals"`
	Facts          []fact          `json:"facts"`
	MergeDecisions []mergeDecision `json:"merge_decisions"`
	CardClosures   []cardClosure   `json:"card_closures"`
}

type adr struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Context      string `json:"context"`
	Decision     string `json:"decision"`
	Consequences string `json:"consequences"`
	UpdatedAt    string `json:"updated_at"`
}

type card struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Phase      string   `json:"phase"`
	Type       string   `json:"type"`
	OwnerFiles []string `json:"owner_files"`
	Deps       []string `json:"deps"`
	ADR        string   `json:"adr"`
	Agent      string   `json:"agent"`
	Time       string   `json:"time"`
	Goal       string   `json:"goal"`
	Context    string   `json:"context"`
	Steps      string   `json:"steps"`
	Check      string   `json:"check"`
	Acceptance string   `json:"acceptance"`
	UpdatedAt  string   `json:"updated_at"`
}

type evalCase struct {
	Name            string   `json:"name"`
	Scenario        string   `json:"scenario"`
	Assertions      []string `json:"assertions"`
	NegativeControl string   `json:"negative_control"`
	UpdatedAt       string   `json:"updated_at"`
}

type mergeDecision struct {
	PR     int    `json:"pr"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// sourceDecision pairs a research decision with the file that declared it.
// The hash is computed over the raw JSON bytes, so any edit to the source
// changes the digest and the report points at a different input.
type sourceDecision struct {
	decision mergeDecision
	source   string
	hash     string
}

type cardClosure struct {
	ID       string `json:"id"`
	PR       int    `json:"pr"`
	MergedAt string `json:"merged_at"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

// fact is a number declared by a research batch together with the path that
// owns it. A fact without a source has no owner and fails the check (HD-088).
type fact struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

type ghCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// ghPRFiles is the part of gh pr view --json files this gate reads. GitHub
// reports repository-relative paths in the path field.
type ghPRFiles struct {
	Files []struct {
		Path string `json:"path"`
	} `json:"files"`
}

// artifactEntry pairs a rendered document with its path relative to the repo
// root. Both belong to one batch-apply iteration (HD-066, ADR 0307).
type artifactEntry struct {
	path    string
	content string
	// id names this artifact in the stale report. updatedAt is the parsed
	// incoming timestamp; the zero value means the row has no freshness rule.
	id        string
	updatedAt time.Time
}

// derivedArtifact reports whether an artifact is a generated document that
// stays out of git (IDX-11). Both are rendered for the iteration hash and
// FACTS.md is printed by --facts, but a --write or --commit run never puts
// either on disk: their sources are checked in and the documents themselves
// are listed in .gitignore.
func derivedArtifact(path string) bool {
	switch path {
	case filepath.Join("docs", "FACTS.md"), filepath.Join("docs", "adrs", "README.md"):
		return true
	default:
		return false
	}
}

// factEntry pairs a number with the artifact that owns it. Two artifacts that
// print the same number produce two rows, so a copy can point at a source.
type factEntry struct {
	value string
	owner string
}

// iterationHash returns the SHA-256 of the artifact set produced by one
// iteration. Paths and contents feed the digest in a fixed order, so the same
// inputs always give the same hash and a change to any artifact changes it.
func iterationHash(entries []artifactEntry) string {
	sorted := append([]artifactEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })
	h := sha256.New()
	for _, e := range sorted {
		io.WriteString(h, e.path)
		h.Write([]byte{0})
		io.WriteString(h, e.content)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

var (
	nonSlug    = regexp.MustCompile(`[^a-z0-9]+`)
	factNumber = regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)?\b`)
	// cardID accepts the identifiers already used in docs/tasks: an
	// alphanumeric first character followed by alphanumerics, dots,
	// underscores or hyphens. A leading separator or a standalone ".." cannot
	// match, so a valid ID always stays inside the tasks directory.
	cardID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Generated card fields are closed sets. The values match the cards already
// in docs/tasks. An empty value is left to renderCard defaults and is not a
// problem: the check only rejects a value the batch actually declared.
var (
	allowedCardTypes    = []string{"task", "adr", "eval", "doc"}
	allowedCardPhases   = []string{"P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8"}
	allowedCardStatuses = []string{"todo", "in_progress", "blocked", "done", "dropped", "duplicate"}
)

func allowedValue(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// cardFieldProblems returns one line per declared card value outside its
// closed set. Batches and cards keep load order, so the report is reproducible.
func cardFieldProblems(batches []batch) []string {
	var problems []string
	for _, b := range batches {
		for _, c := range b.Cards {
			for _, field := range []struct {
				name, value string
				allowed     []string
			}{
				{"type", c.Type, allowedCardTypes},
				{"phase", c.Phase, allowedCardPhases},
				{"status", c.Status, allowedCardStatuses},
			} {
				if field.value != "" && !allowedValue(field.value, field.allowed) {
					problems = append(problems, fmt.Sprintf(invalidFieldFormat, field.name, field.value))
				}
			}
		}
	}
	return problems
}

// validCardID reports whether id is safe to use as a docs/tasks file name.
// Empty IDs, path separators and ".." segments are rejected explicitly before
// the pattern check, so no identifier can escape the tasks directory when it
// is passed to filepath.Join.
func validCardID(id string) bool {
	if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) {
		return false
	}
	return cardID.MatchString(id)
}

// cardIDProblems returns one line per card or closure ID that is unsafe to
// join onto docs/tasks. Both card IDs and closure IDs reach filepath.Join, so
// both are checked here, before any artifact path is built.
func cardIDProblems(batches []batch) []string {
	var problems []string
	for _, b := range batches {
		for _, c := range b.Cards {
			if !validCardID(c.ID) {
				problems = append(problems, fmt.Sprintf(invalidCardIDFormat, c.ID))
			}
		}
		for _, c := range b.CardClosures {
			if !validCardID(c.ID) {
				problems = append(problems, fmt.Sprintf(invalidCardIDFormat, c.ID))
			}
		}
	}
	return problems
}

// declaredFactProblems returns the values of facts that carry no source. The
// acceptance rule for HD-088 is that a number without an owner fails the card.
func declaredFactProblems(facts []fact) []string {
	var bad []string
	for _, f := range facts {
		if f.Value == "" || f.Source == "" {
			bad = append(bad, f.Value)
		}
	}
	return bad
}

// collectFacts maps every number in the iteration artifacts to the path that
// owns it, then adds numbers declared by the batches. Sorting by value and
// path makes the index reproducible, so the same inputs give the same hash.
func collectFacts(entries []artifactEntry, declared []fact) []factEntry {
	seen := map[factEntry]bool{}
	var facts []factEntry
	add := func(value, owner string) {
		if value == "" || owner == "" {
			return
		}
		f := factEntry{value: value, owner: filepath.ToSlash(owner)}
		if seen[f] {
			return
		}
		seen[f] = true
		facts = append(facts, f)
	}
	for _, e := range entries {
		for _, value := range factNumber.FindAllString(e.content, -1) {
			add(value, e.path)
		}
	}
	for _, f := range declared {
		add(f.Value, f.Source)
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].value != facts[j].value {
			return facts[i].value < facts[j].value
		}
		return facts[i].owner < facts[j].owner
	})
	return facts
}

// factsLink maps an owner path to a link target as seen from docs/FACTS.md.
// Owners under docs/ are relative to that directory already, external URLs
// stay untouched, and owners elsewhere in the repo need one level up.
func factsLink(owner string) string {
	if strings.HasPrefix(owner, "http://") || strings.HasPrefix(owner, "https://") {
		return owner
	}
	if strings.HasPrefix(owner, "docs/") {
		return strings.TrimPrefix(owner, "docs/")
	}
	return "../" + owner
}

// renderFacts builds docs/FACTS.md. Each row links a number to the file that
// owns it, so a copy in another document can name its source. Artifacts and
// prose stay in Russian; paths and numbers are technical tokens.
func renderFacts(facts []factEntry) string {
	var b strings.Builder
	b.WriteString("# Факты\n\n")
	b.WriteString("Владелец числа - файл, где оно напечатано. Копия числа ссылается на этого владельца.\n\n")
	b.WriteString("| Число | Владелец |\n| --- | --- |\n")
	for _, f := range facts {
		link := factsLink(f.owner)
		b.WriteString("| " + f.value + " | [" + f.owner + "](" + link + ") |\n")
	}
	return b.String()
}

// adrIndexHeader is the fixed prose above the generated table. It is kept
// byte-for-byte from the hand-maintained index so regeneration changes only
// the rows.
const adrIndexHeader = "# Beacon. Architecture Decision Records\n\n" +
	"Это каталог решений, согласованных с владельцем. Каждый ADR фиксирует одно решение, причину и последствия. Решения в этом каталоге обязательны для implementation.\n\n" +
	"Статусы решений:\n\n" +
	"- `accepted` владелец согласился явно.\n" +
	"- `default` я предложил, владелец не возразил. Может быть переопределно до гейта G0.\n" +
	"- `open` решение ещё не принято, ждёт отчёта или вопроса.\n" +
	"- `proposed` решение предложено, ожидает решения владельца.\n\n" +
	"Индекс:\n\n" +
	"| ADR | Тема | Статус |\n|---|---|---|\n"

// adrIndexEntry is one row of the generated ADR index. Number comes from the
// file name, Title from its heading and Status from its Статус line.
type adrIndexEntry struct {
	number int
	title  string
	status string
}

var (
	adrFileName = regexp.MustCompile(`^([0-9]{4})-.*\.md$`)
	adrTitle    = regexp.MustCompile(`(?m)^# ADR\s+[0-9]+\s*[.:]\s*(.+?)\s*$`)
	adrStatus   = regexp.MustCompile(`(?m)^Статус:\s*([^\s]+)`)
)

// scanADRIndex reads every numbered ADR file and merges the ADRs produced by
// this iteration. Incoming ADRs win over a file with the same number, so the
// index reflects the artifacts about to be written. Duplicate numbers survive
// because each file gets its own row.
func scanADRIndex(root string, incoming map[int]adr) ([]adrIndexEntry, error) {
	dir := filepath.Join(root, "docs", "adrs")
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	seen := map[adrIndexEntry]bool{}
	var entries []adrIndexEntry
	for _, name := range names {
		if name.IsDir() {
			continue
		}
		match := adrFileName.FindStringSubmatch(name.Name())
		if match == nil {
			continue
		}
		number, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if _, replaced := incoming[number]; replaced {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name.Name()))
		if err != nil {
			return nil, err
		}
		entry := adrIndexEntry{number: number}
		if title := adrTitle.FindSubmatch(raw); title != nil {
			entry.title = string(title[1])
		}
		if status := adrStatus.FindSubmatch(raw); status != nil {
			entry.status = string(status[1])
		}
		if !seen[entry] {
			seen[entry] = true
			entries = append(entries, entry)
		}
	}
	for number, a := range incoming {
		status := a.Status
		if status == "" {
			status = "accepted"
		}
		entries = append(entries, adrIndexEntry{number: number, title: a.Title, status: status})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].number != entries[j].number {
			return entries[i].number < entries[j].number
		}
		return entries[i].title < entries[j].title
	})
	return entries, nil
}

// renderADRIndex writes docs/adrs/README.md from the scanned entries. Rows
// stay in number order so a rerun with the same inputs writes the same bytes.
func renderADRIndex(entries []adrIndexEntry) string {
	var b strings.Builder
	b.WriteString(adrIndexHeader)
	for _, e := range entries {
		fmt.Fprintf(&b, "| %04d | %s | %s |\n", e.number, e.title, e.status)
	}
	return b.String()
}

// runner joins the three independent parts of one import. Each part owns its
// own state; runner only supplies the options that drive the sequence.
type runner struct {
	opts Options
	output
	runReport
	writeGate
}

// fillDefaults installs the production seams of both the output and the write
// gate. Tests replace any of them on the runner they build before writing.
func (r *runner) fillDefaults() {
	r.output.fillDefaults()
	r.writeGate.fillDefaults()
}

// chunkEntries splits entries into ordered slices of at most size records.
// Concatenating the result reproduces the input, so artifact order inside a
// chunk never changes.
func chunkEntries(entries []artifactEntry, size int) [][]artifactEntry {
	if size <= 0 {
		size = 1
	}
	var chunks [][]artifactEntry
	for start := 0; start < len(entries); start += size {
		end := start + size
		if end > len(entries) {
			end = len(entries)
		}
		chunks = append(chunks, entries[start:end])
	}
	return chunks
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		s = "untitled"
	}
	return s
}

func rootDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// loadBatches is fault tolerant on purpose: one malformed research file must
// not abort a whole wave. Bad files are reported once in the summary instead
// of dumping a parse error into the middle of the run.
func loadBatches(dir string) ([]batch, []string) {
	var problems []string
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, []string{err.Error()}
	}
	sort.Strings(paths)
	var out []batch
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(p), err))
			continue
		}
		var b batch
		if err := json.Unmarshal(raw, &b); err != nil {
			problems = append(problems, fmt.Sprintf("%s: неверный JSON (%v)", filepath.Base(p), err))
			continue
		}
		out = append(out, b)
	}
	return out, problems
}

// loadMergeDecisions collects merge decisions from every research JSON and
// keeps the source file plus the digest of its raw bytes. A malformed file is
// reported and skipped, so one bad batch cannot block the rest of the plan.
func loadMergeDecisions(dir string) ([]sourceDecision, []string) {
	var problems []string
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, []string{err.Error()}
	}
	sort.Strings(paths)
	var out []sourceDecision
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(p), err))
			continue
		}
		var b batch
		if err := json.Unmarshal(raw, &b); err != nil {
			problems = append(problems, fmt.Sprintf("%s: неверный JSON (%v)", filepath.Base(p), err))
			continue
		}
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])
		for _, d := range b.MergeDecisions {
			out = append(out, sourceDecision{decision: d, source: filepath.Base(p), hash: hash})
		}
	}
	return out, problems
}

// staleAgainst is the one freshness rule shared by the prefilter and the
// pre-publish recheck. A row without a timestamp has no rule; a local file
// whose mtime is later than the incoming timestamp wins.
func staleAgainst(localMod, incoming time.Time) bool {
	return !incoming.IsZero() && localMod.After(incoming)
}

// changedFields names the lines whose values differ between two rendered
// documents. Rendered cards, ADRs and evals all use "Name: value" lines, so one
// comparison covers every artifact type. The result is sorted for a stable
// report. A line that disappeared is counted too, so a deleted field is not a
// silent change.
func changedFields(local, incoming string) []string {
	parse := func(text string) map[string]string {
		fields := map[string]string{}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			i := strings.Index(line, ": ")
			if i <= 0 {
				continue
			}
			name := strings.TrimSpace(line[:i])
			if name != "" {
				fields[name] = strings.TrimSpace(line[i+2:])
			}
		}
		return fields
	}
	before, after := parse(local), parse(incoming)
	seen := map[string]bool{}
	var out []string
	for name, value := range after {
		if before[name] != value && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// prefilterStale drops incoming rows older than the local artifact before any
// hash, facts or index is built. The same rule runs again per file in
// writeChunked, so a local edit landing after this pass is still caught.
func (r *runner) prefilterStale(root string, entries []artifactEntry) []artifactEntry {
	if r.opts.Force {
		return entries
	}
	kept := make([]artifactEntry, 0, len(entries))
	for _, e := range entries {
		if !e.updatedAt.IsZero() {
			if info, err := os.Stat(filepath.Join(root, e.path)); err == nil && staleAgainst(info.ModTime(), e.updatedAt) {
				r.recordStaleSkipped(e.id)
				continue
			}
		}
		kept = append(kept, e)
	}
	return kept
}

func renderADR(a adr) string {
	status := a.Status
	if status == "" {
		status = "accepted"
	}
	return fmt.Sprintf("# ADR %d: %s\n\nСтатус: %s\n\n## Контекст\n\n%s\n\n## Решение\n\n%s\n\n## Последствия\n\n%s\n",
		a.Number, a.Title, status, a.Context, a.Decision, a.Consequences)
}

// fillCardDefaults keeps generated cards actionable: every card needs an
// owner file and a dependency before an executor can pick it up.
func fillCardDefaults(c card) card {
	if len(c.OwnerFiles) == 0 {
		title := strings.ToLower(c.Title)
		switch {
		case strings.Contains(title, "acp"):
			c.OwnerFiles = []string{"internal/infra/wake/acp.go", "internal/infra/wake/acp_test.go"}
		case strings.Contains(title, "e2e"):
			c.OwnerFiles = []string{"tests/e2e/e2e_test.go"}
		case strings.Contains(title, "eval"):
			c.OwnerFiles = []string{"evals/main.go", "evals/README.md"}
		case strings.Contains(title, "plugin") || strings.Contains(title, "bridge"):
			c.OwnerFiles = []string{".opencode/plugins/beacon-bridge.js"}
		case strings.Contains(title, "verdict") || strings.Contains(title, "ci"):
			c.OwnerFiles = []string{"internal/infra/cibridge/bridge.go", "internal/infra/cibridge/bridge_test.go"}
		default:
			c.OwnerFiles = []string{"internal/infra/wake/deliver.go", "internal/infra/wake/deliver_test.go"}
		}
	}
	if c.Deps == nil {
		c.Deps = []string{}
	}
	return c
}

func renderCard(c card) string {
	c = fillCardDefaults(c)
	status := c.Status
	if status == "" {
		status = "todo"
	}
	phase := c.Phase
	if phase == "" {
		phase = "P3"
	}
	kind := c.Type
	if kind == "" {
		kind = "task"
	}
	agent := c.Agent
	if agent == "" {
		agent = "нет"
	}
	when := c.Time
	if when == "" {
		when = "нет"
	}
	return fmt.Sprintf("# %s. %s\nСтатус: %s\nФаза: %s\nТип: %s\nВладелец файлов: %s\nЗависимости: %s\nСсылка: %s\nАгент: %s\nВремя: %s\nЦель: %s\nКонтекст: %s\nШаги: %s\nПроверка: %s\nПриёмка: %s\n",
		c.ID, c.Title, status, phase, kind, strings.Join(c.OwnerFiles, ", "),
		strings.Join(c.Deps, ", "), c.ADR, agent, when, c.Goal, c.Context, c.Steps, c.Check, c.Acceptance)
}

func renderEval(e evalCase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n## Сценарий\n\n%s\n\n## Проверки\n\n", e.Name, e.Scenario)
	for _, a := range e.Assertions {
		fmt.Fprintf(&b, "- %s\n", a)
	}
	fmt.Fprintf(&b, "\n## Негативный контроль\n\n%s\n", e.NegativeControl)
	return b.String()
}

// closureConfirmation renders the body of the "## Подтверждение" section.
// A dropped card records why it was closed so the decision stays auditable;
// the done variant is byte-for-byte the original MERGED line.
func closureConfirmation(c cardClosure, status string) string {
	if status == "dropped" {
		line := fmt.Sprintf("- PR #%d DROPPED", c.PR)
		if reason := strings.TrimRight(strings.TrimSpace(c.Reason), "."); reason != "" {
			line += ": " + reason
		}
		return line + ".\n"
	}
	return fmt.Sprintf("- PR #%d MERGED: %s (squash).\n", c.PR, c.MergedAt)
}

// applyClosureToCard rewrites one card for a closure. Only "dropped" changes
// the status line; every other value keeps the original "done" behaviour.
// The function is idempotent: a card that already has a confirmation section
// is returned unchanged, so reapplying a batch never duplicates a reason.
func applyClosureToCard(text string, c cardClosure) string {
	if strings.Contains(text, "## Подтверждение") {
		return text
	}
	status := c.Status
	if status != "dropped" {
		status = "done"
	}
	text = strings.Replace(text, "Статус: todo", "Статус: "+status, 1)
	return text + "\n## Подтверждение\n" + closureConfirmation(c, status)
}

func (r *runner) applyClosures(root string, closures map[string]cardClosure) error {
	for id, c := range closures {
		rel := filepath.Join("docs", "tasks", id+".md")
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			r.errf(warnReportFormat, rel)
			continue
		}
		original := string(raw)
		text := applyClosureToCard(original, c)
		if text == original {
			continue
		}
		if err := r.writeFileMode(root, rel, text, true); err != nil {
			return err
		}
	}
	return nil
}

// requiredCheckOrder is the fixed set of checks a code PR must carry before
// auto-merge. The order keeps reports and tests deterministic.
var requiredCheckOrder = []string{"build", "test", "gates", "gates-binary"}

// Hold reasons for a code PR that fails the gate. Both are Russian interface
// prose, so they follow the language policy above.
const (
	holdMissingCheck = "удержан, обязательная проверка отсутствует"
	holdNotGreen     = "удержан, обязательная проверка не зелёная"
	// ATX-13: a code PR with no reported checks is held. The old rule read
	// "no checks reported" as proof of a doc-only PR and merged it.
	holdNoChecks = "удержан, список проверок пуст"
)

// isRequiredCheck reports whether name is one of the checks this tool gates on.
func isRequiredCheck(name string) bool {
	for _, required := range requiredCheckOrder {
		if name == required {
			return true
		}
	}
	return false
}

// greenCheckState accepts only a genuine success. GitHub counts skipped and
// neutral as satisfying a required check on its side, so this gate refuses to
// treat either as green. A tool that did not run must not look like a passed
// check.
func greenCheckState(state string) bool {
	return strings.EqualFold(state, "SUCCESS") || strings.EqualFold(state, "PASS")
}

// evaluateChecks returns an empty string when the reported checks allow a
// merge. It judges only the checks it receives. An empty list passes here;
// evaluateMergeGate decides whether that empty list belongs to a doc-only PR.
// A non-empty list must carry every required check in a green state. A missing
// check and a non-green check are held with different reasons. Every reported
// check is inspected, so a green group cannot hide one neutral or failed run.
func evaluateChecks(checks []ghCheck) string {
	if len(checks) == 0 {
		return ""
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if !isRequiredCheck(c.Name) {
			continue
		}
		seen[c.Name] = true
		if !greenCheckState(c.State) {
			return holdNotGreen
		}
	}
	for _, name := range requiredCheckOrder {
		if !seen[name] {
			return holdMissingCheck
		}
	}
	return ""
}

// docOnlyPR reports whether every changed file is documentation. A path under
// docs/ or a Markdown file counts as a document. Any other path means the PR
// changes code and needs the full check set. An empty list is not doc-only.
func docOnlyPR(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		path := filepath.ToSlash(strings.TrimSpace(p))
		if strings.HasPrefix(path, "docs/") || strings.EqualFold(filepath.Ext(path), ".md") {
			continue
		}
		return false
	}
	return true
}

// evaluateMergeGate adds the ATX-13 rule to evaluateChecks. An empty check
// list is allowed only when the changed files prove the PR is doc-only. A code
// PR with no checks is held instead of merged on an error string from gh.
func evaluateMergeGate(checks []ghCheck, docOnly bool) string {
	if docOnly {
		return ""
	}
	if len(checks) == 0 {
		return holdNoChecks
	}
	return evaluateChecks(checks)
}

// changedFiles reads the paths a PR changes. The gate uses files, not the text
// of a gh error, to decide whether a PR is doc-only.
func (r *runner) changedFiles(pr int) ([]string, error) {
	cmd := exec.Command("gh", "pr", "view", fmt.Sprint(pr), "--json", "files")
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var payload ghPRFiles
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(payload.Files))
	for _, f := range payload.Files {
		paths = append(paths, f.Path)
	}
	return paths, nil
}

// applyMergePlan merges only PRs whose changed files and checks allow it. A
// doc-only PR may have no checks; a code PR must carry every required check in
// a green state. The plan comes from research, so every decision is logged with
// the hash of the JSON that declared it and re-verified with gh before acting.
func (r *runner) applyMergePlan(decisions []sourceDecision) {
	merged, held := 0, 0
	for _, d := range decisions {
		r.printf(decisionReportFormat, d.decision.PR, d.decision.Action, d.source, d.hash)
		if d.decision.Action != "merge" {
			r.printf("PR %d: %s (%s)\n", d.decision.PR, d.decision.Action, d.decision.Reason)
			continue
		}
		paths, err := r.changedFiles(d.decision.PR)
		if err != nil {
			r.printf("PR %d: удержан, список файлов недоступен: %v\n", d.decision.PR, err)
			held++
			continue
		}
		docOnly := docOnlyPR(paths)
		cmd := exec.Command("gh", "pr", "checks", fmt.Sprint(d.decision.PR), "--json", "name,state")
		raw, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "no checks reported") {
				raw = []byte("[]") // no reported checks, not proof of a doc-only PR
			} else {
				r.printf("PR %d: удержан, проверки недоступны: %v\n", d.decision.PR, err)
				held++
				continue
			}
		}
		var checks []ghCheck
		if err := json.Unmarshal(raw, &checks); err != nil {
			r.printf("PR %d: удержан, неверный JSON проверок\n", d.decision.PR)
			held++
			continue
		}
		if reason := evaluateMergeGate(checks, docOnly); reason != "" {
			r.printf("PR %d: %s\n", d.decision.PR, reason)
			held++
			continue
		}
		mergeCmd := exec.Command("gh", "pr", "merge", fmt.Sprint(d.decision.PR), "--squash", "--delete-branch")
		if out, err := mergeCmd.CombinedOutput(); err != nil {
			r.printf("PR %d: слияние не удалось: %s\n", d.decision.PR, strings.TrimSpace(string(out)))
			held++
			continue
		}
		r.printf("PR %d: MERGED\n", d.decision.PR)
		merged++
	}
	r.printf(mergeReportFormat, merged, held)
}

// commitArtifacts stages and commits the generated documentation. Since
// IDX-11 the derived documents (FACTS.md, ROADMAP_STATUS.md and adrs/README.md)
// are gitignored. Naming an ignored path in git add makes it exit 1 and aborts
// --commit, so stage the three source trees by directory instead. Git skips
// ignored files inside a directory without failing, and still stages cards
// changed by closures.
func commitArtifacts(root string, cards, adrs, evals int) error {
	add := exec.Command("git", "add", "--", "docs/adrs", "docs/tasks", "docs/evals")
	add.Dir = root
	if err := add.Run(); err != nil {
		return err
	}
	// CBX-06: a chunk or a closure pass can leave the index clean. git commit
	// exits 1 in that case, which must not fail an otherwise successful run.
	staged := exec.Command("git", "diff", "--cached", "--quiet")
	staged.Dir = root
	if err := staged.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return err
		}
	} else {
		return nil
	}
	commit := exec.Command("git", "commit", "-m",
		fmt.Sprintf("docs: batch apply %d cards, %d ADRs, %d evals", cards, adrs, evals))
	commit.Dir = root
	return commit.Run()
}

// Run executes the batch apply and returns the process exit code. All file
// and subprocess work happens in this infra package, not in cmd.
func Run(opts Options, out, errOut io.Writer) int {
	r := &runner{opts: opts, output: output{out: out, err: errOut}}
	r.fillDefaults()
	// CCx-24: the violation line still reaches the report when a write fails.
	defer r.emitSandboxDenials()
	// ATX-13: a research document never authorizes a merge by itself. Require
	// the owner flag before any file read or gh call in the merge path.
	if opts.MergePlan && !opts.ConfirmMerge {
		r.errf(confirmRequiredFormat)
		return 2
	}
	if opts.Dir == "" {
		r.errf("--dir обязателен\n")
		return 2
	}
	root, err := rootDir()
	if err != nil {
		r.errf("%v\n", err)
		return 1
	}
	batches, problems := loadBatches(opts.Dir)
	var declaredFacts []fact
	for _, b := range batches {
		declaredFacts = append(declaredFacts, b.Facts...)
	}
	badFacts := declaredFactProblems(declaredFacts)
	if len(badFacts) > 0 {
		for _, value := range badFacts {
			r.errf(factProblemFormat, value)
		}
		return 1
	}
	// Q-A4: reject an invalid Тип, Фаза or Статус here, before --check returns
	// and before the write loop below. A bad card can never reach disk.
	badFields := cardFieldProblems(batches)
	for _, line := range badFields {
		r.errf("%s", line)
	}
	if len(badFields) > 0 {
		return 1
	}
	// ATX-12: reject unsafe card and closure IDs before they become map keys
	// and reach filepath.Join in the write loop or applyClosures.
	badIDs := cardIDProblems(batches)
	for _, line := range badIDs {
		r.errf("%s", line)
	}
	if len(badIDs) > 0 {
		return 1
	}
	if opts.MergePlan {
		decisions, mergeProblems := loadMergeDecisions(opts.Dir)
		for _, p := range mergeProblems {
			r.errf(skipReportFormat, p)
		}
		r.applyMergePlan(decisions)
		return 0
	}
	for _, p := range problems {
		r.errf(skipReportFormat, p)
	}
	if opts.Check {
		r.printf(checkReportFormat, len(batches), len(problems))
		if len(problems) > 0 {
			return 1
		}
		return 0
	}

	adrs := map[int]adr{}
	cards := map[string]card{}
	evals := map[string]evalCase{}
	closures := map[string]cardClosure{}
	for _, b := range batches {
		for _, a := range b.ADRs {
			adrs[a.Number] = a
		}
		for _, c := range b.Cards {
			cards[c.ID] = c
		}
		for _, e := range b.Evals {
			evals[e.Name] = e
		}
		for _, c := range b.CardClosures {
			closures[c.ID] = c
		}
	}

	// Research agents number their ADRs locally and often collide with ADRs
	// already in the repo. Reassign colliding numbers to the next free slot so
	// an existing ADR is never overwritten and no new decision is lost.
	used := map[int]bool{}
	matches, _ := filepath.Glob(filepath.Join(root, "docs", "adrs", "[0-9][0-9][0-9][0-9]-*.md"))
	nextFree := 1
	for _, m := range matches {
		var n int
		if _, err := fmt.Sscanf(filepath.Base(m), "%04d-", &n); err == nil {
			used[n] = true
			if n >= nextFree {
				nextFree = n + 1
			}
		}
	}
	allADRs := make([]adr, 0, len(adrs))
	for _, a := range adrs {
		allADRs = append(allADRs, a)
	}
	sort.Slice(allADRs, func(i, j int) bool { return allADRs[i].Number < allADRs[j].Number })
	assigned := map[int]adr{}
	for _, a := range allADRs {
		for used[nextFree] {
			nextFree++
		}
		if a.Number != nextFree && !r.opts.Quiet {
			r.printf(renumberReportFormat, a.Number, nextFree)
		}
		a.Number = nextFree
		used[nextFree] = true
		assigned[nextFree] = a
		nextFree++
	}
	adrs = assigned

	nums := make([]int, 0, len(adrs))
	for n := range adrs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	ids := make([]string, 0, len(cards))
	for id := range cards {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	names := make([]string, 0, len(evals))
	for n := range evals {
		names = append(names, n)
	}
	sort.Strings(names)

	var entries []artifactEntry
	for _, n := range nums {
		a := adrs[n]
		entries = append(entries, artifactEntry{
			path:      filepath.Join("docs", "adrs", fmt.Sprintf("%04d-%s.md", n, slug(a.Title))),
			content:   renderADR(a),
			id:        fmt.Sprintf("ADR-%d", n),
			updatedAt: parseUpdatedAt(a.UpdatedAt),
		})
	}
	for _, id := range ids {
		c := cards[id]
		entries = append(entries, artifactEntry{
			path:      filepath.Join("docs", "tasks", id+".md"),
			content:   renderCard(c),
			id:        c.ID,
			updatedAt: parseUpdatedAt(c.UpdatedAt),
		})
	}
	for _, name := range names {
		e := evals[name]
		entries = append(entries, artifactEntry{
			path:      filepath.Join("docs", "evals", slug(name)+".md"),
			content:   renderEval(e),
			id:        e.Name,
			updatedAt: parseUpdatedAt(e.UpdatedAt),
		})
	}

	// CBX-07: remove rows older than their local artifact before the hash,
	// facts and index are built. writeChunked repeats the same rule per file.
	entries = r.prefilterStale(root, entries)

	facts := collectFacts(entries, declaredFacts)
	factsDoc := renderFacts(facts)
	if opts.Facts {
		r.printf("%s", factsDoc)
		return 0
	}
	entries = append(entries, artifactEntry{
		path:    filepath.Join("docs", "FACTS.md"),
		content: factsDoc,
	})

	// Q-A4: the ADR index is generated from the numbered ADR files plus the
	// ADRs in this batch. The index is no longer maintained by hand.
	indexEntries, err := scanADRIndex(root, adrs)
	if err != nil {
		r.errf("%v\n", err)
		return 1
	}
	entries = append(entries, artifactEntry{
		path:    filepath.Join("docs", "adrs", "README.md"),
		content: renderADRIndex(indexEntries),
	})

	r.printf(summaryReportFormat,
		len(batches), len(adrs), len(cards), len(evals), len(closures))
	r.printf(hashReportFormat, iterationHash(entries))
	if err := r.writeChunked(root, entries, len(cards), len(adrs), len(evals)); err != nil {
		r.emitReport()
		r.errf("%v\n", err)
		return 1
	}
	if !opts.DryRun {
		r.printf(createdIDsFormat, strings.Join(r.createdIDs, ","))
		r.printf(updatedIDsFormat, strings.Join(r.updatedIDs, ","))
		r.printf(staleSkippedFormat, strings.Join(r.staleSkippedIDs, ","))
		r.printf(tieKeptLocalFormat, strings.Join(r.tieKeptLocalIDs, ","))
		r.printf(updatedIssuesFormat, r.updatedIssues)
	}
	r.emitReport()
	if err := r.applyClosures(root, closures); err != nil {
		r.emitReport()
		r.errf("%v\n", err)
		return 1
	}
	// Closures land after the chunk commits. A final idempotent commit carries
	// them without holding the lock across the whole write pass.
	if opts.Commit && !opts.DryRun {
		if err := r.commit(root, len(cards), len(adrs), len(evals)); err != nil {
			r.errf("%v\n", err)
			return 1
		}
	}
	return 0
}

// CA-08 (docs/research/comp-aider.md, aider/coders/editblock_coder.py:602-630
// and :82-124): a search/replace block that does not match returns a reason
// and similar lines instead of a bare refusal. Successful blocks are marked
// and never sent again. Similar lines are advice only; the edit cascade
// (CA-03) decides success by exact match and never applies a fuzzy match.
// Technical tokens stay English, prose stays Russian (HD-053, ADR 0304).

// editSimilarityThreshold is the aider threshold for a "did you mean" line.
const editSimilarityThreshold = 0.6

// editFailureCategory names why one edit block did not apply. The values are
// English technical tokens so a caller can branch on them.
type editFailureCategory string

const (
	// editCategoryFailedBlock means no file line reached the threshold.
	editCategoryFailedBlock editFailureCategory = "failed-block"
	// editCategoryAlreadyApplied means the REPLACE text is already in the file.
	editCategoryAlreadyApplied editFailureCategory = "already-applied"
)

// editBlock is one search/replace pair submitted by an agent.
type editBlock struct {
	Index   int    `json:"index"`
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

// editBlockResult is the outcome of one block. An applied block has applied
// true and no category. A failed block has a category and up to limit similar
// lines as advice.
type editBlockResult struct {
	Index    int                 `json:"index"`
	Applied  bool                `json:"applied"`
	Category editFailureCategory `json:"category,omitempty"`
	Similar  []string            `json:"similar,omitempty"`
}

// editResponse is the structured reply for a batch of blocks. Resend carries
// only failed-block indexes that need a corrected edit. Applied blocks and
// already-applied blocks are never resent.
type editResponse struct {
	Results []editBlockResult `json:"results"`
	Resend  []int             `json:"resend"`
}

// normalizeEditLine collapses whitespace runs to one space and trims the
// ends. An indent shift or a different number of spaces then compares equal.
// It is used for scoring and the already-applied check only, never to apply
// an edit.
func normalizeEditLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// editLineSimilarity returns a ratio in [0,1] for two lines using the longest
// common subsequence of their normalized forms. An exact normalized match
// scores 1; two lines with nothing in common score 0.
func editLineSimilarity(a, b string) float64 {
	a, b = normalizeEditLine(a), normalizeEditLine(b)
	if a == b {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for _, ac := range a {
		for j, bc := range b {
			if ac == bc {
				cur[j+1] = prev[j] + 1
			} else if prev[j+1] >= cur[j] {
				cur[j+1] = prev[j+1]
			} else {
				cur[j+1] = cur[j]
			}
		}
		prev, cur = cur, prev
		for j := range cur {
			cur[j] = 0
		}
	}
	return float64(2*prev[len(b)]) / float64(len(a)+len(b))
}

// similarEditLines returns up to limit file lines whose similarity to any
// line of target reaches threshold. Lines keep file order and duplicates are
// removed. A shorter target line contained in a file line scores 1 so a
// distorted indent still finds its source.
func similarEditLines(target string, fileLines []string, threshold float64, limit int) []string {
	targetLines := strings.Split(target, "\n")
	seen := map[string]bool{}
	var out []string
	for _, line := range fileLines {
		text := strings.TrimRight(line, "\r")
		if strings.TrimSpace(text) == "" {
			continue
		}
		best := 0.0
		for _, want := range targetLines {
			w := normalizeEditLine(want)
			if w == "" {
				continue
			}
			score := editLineSimilarity(want, text)
			if strings.Contains(normalizeEditLine(text), w) {
				score = 1
			}
			if score > best {
				best = score
			}
		}
		if best >= threshold && !seen[text] {
			seen[text] = true
			out = append(out, text)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// replaceAlreadyInFile reports whether the REPLACE text is already present
// in the file after whitespace normalization. Such a block needs no resend.
func replaceAlreadyInFile(replace, content string) bool {
	want := normalizeEditLine(replace)
	if want == "" {
		return false
	}
	return strings.Contains(normalizeEditLine(content), want)
}

// evaluateEditBlocks builds the structured response for a batch of edit
// blocks against the target file. A block succeeds when its SEARCH appears in
// the file after normalization. A block whose REPLACE is already present gets
// the already-applied category. Every other failure gets the failed-block
// category and similar lines as advice. Only failed blocks enter Resend.
func evaluateEditBlocks(blocks []editBlock, content string, threshold float64, similarLimit int) editResponse {
	if threshold <= 0 {
		threshold = editSimilarityThreshold
	}
	fileLines := strings.Split(content, "\n")
	normFile := normalizeEditLine(content)
	resp := editResponse{Results: make([]editBlockResult, 0, len(blocks))}
	for _, b := range blocks {
		search := normalizeEditLine(b.Search)
		if search != "" && strings.Contains(normFile, search) {
			resp.Results = append(resp.Results, editBlockResult{Index: b.Index, Applied: true})
			continue
		}
		if replaceAlreadyInFile(b.Replace, content) {
			resp.Results = append(resp.Results, editBlockResult{
				Index:    b.Index,
				Category: editCategoryAlreadyApplied,
			})
			continue
		}
		resp.Results = append(resp.Results, editBlockResult{
			Index:    b.Index,
			Category: editCategoryFailedBlock,
			Similar:  similarEditLines(b.Search, fileLines, threshold, similarLimit),
		})
		resp.Resend = append(resp.Resend, b.Index)
	}
	return resp
}
