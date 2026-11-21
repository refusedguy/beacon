// Package batchapply turns structured research JSON into ADR, task and eval
// documents. It lives in infra because it reads and writes files and shells
// out to git and gh; cmd/batch-apply stays a thin flag-parsing entry point.
package batchapply

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	hashReportFormat     = "hash=%s\n"
	mergeReportFormat    = "merged=%d held=%d\n"
	renumberReportFormat = "перенумеровать ADR %d в %d\n"
	skipReportFormat     = "пропуск %s\n"
	warnReportFormat     = "предупреждение: не найдена цель закрытия %s\n"
	// HD-088 (ADR 0313): a number with no source has no owner and breaks the
	// card. The line is Russian prose, so it keeps the interface scope.
	factProblemFormat = "число без владельца: %s\n"
	// Q-A4: a generated card field outside its closed set fails --check before
	// any artifact reaches disk. The field name is a technical token, the
	// complaint is Russian interface prose.
	invalidFieldFormat = "недопустимое значение поля %s: %s\n"
)

// Options mirrors the command line of cmd/batch-apply.
type Options struct {
	Dir       string
	DryRun    bool
	Check     bool
	MergePlan bool
	Quiet     bool
	Commit    bool
	Facts     bool
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
}

type evalCase struct {
	Name            string   `json:"name"`
	Scenario        string   `json:"scenario"`
	Assertions      []string `json:"assertions"`
	NegativeControl string   `json:"negative_control"`
}

type mergeDecision struct {
	PR     int    `json:"pr"`
	Action string `json:"action"`
	Reason string `json:"reason"`
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

// artifactEntry pairs a rendered document with its path relative to the repo
// root. Both belong to one batch-apply iteration (HD-066, ADR 0307).
type artifactEntry struct {
	path    string
	content string
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
)

// Generated card fields are closed sets. The values match the cards already
// in docs/tasks. An empty value is left to renderCard defaults and is not a
// problem: the check only rejects a value the batch actually declared.
var (
	allowedCardTypes    = []string{"task", "adr", "eval", "doc"}
	allowedCardPhases   = []string{"P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8"}
	allowedCardStatuses = []string{"todo", "done", "dropped"}
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

// renderFacts builds docs/FACTS.md. Each row links a number to the file that
// owns it, so a copy in another document can name its source. Artifacts and
// prose stay in Russian; paths and numbers are technical tokens.
func renderFacts(facts []factEntry) string {
	var b strings.Builder
	b.WriteString("# Факты\n\n")
	b.WriteString("Владелец числа - файл, где оно напечатано. Копия числа ссылается на этого владельца.\n\n")
	b.WriteString("| Число | Владелец |\n| --- | --- |\n")
	for _, f := range facts {
		link := strings.TrimPrefix(f.owner, "docs/")
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

// runner keeps the output streams and options together so the pure rendering
// helpers stay free of I/O state.
type runner struct {
	opts Options
	out  io.Writer
	err  io.Writer
}

func (r *runner) printf(format string, args ...any) {
	fmt.Fprintf(r.out, format, args...)
}

func (r *runner) errf(format string, args ...any) {
	fmt.Fprintf(r.err, format, args...)
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

func (r *runner) writeFile(root, rel, content string) error {
	return r.writeFileMode(root, rel, content, false)
}

// writeFileMode writes a generated file. overwrite is true only for card
// closures, which update an existing card in place. New cards keep the
// never-overwrite rule.
func (r *runner) writeFileMode(root, rel, content string, overwrite bool) error {
	path := filepath.Join(root, rel)
	if !r.opts.Quiet {
		fmt.Fprintln(r.out, " ", path)
	}
	if r.opts.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil // never overwrite an existing card
		}
	}
	return os.WriteFile(path, []byte(content), 0o644)
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
// merge. No checks means a doc-only PR under paths-ignore: the workflows did
// not run, so there is nothing to verify. A non-empty list means a code PR,
// which must carry every required check in a green state. A missing check and
// a non-green check are held with different reasons. Every reported check is
// inspected, so a green group cannot hide one neutral or failed run.
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

// applyMergePlan merges only PRs whose required checks are all present and
// green. A PR with no checks is doc-only by the repository's paths-ignore rule.
// The plan itself comes from a research agent; this function re-verifies every
// gate with gh before acting.
func (r *runner) applyMergePlan(decisions []mergeDecision) {
	merged, held := 0, 0
	for _, d := range decisions {
		if d.Action != "merge" {
			r.printf("PR %d: %s (%s)\n", d.PR, d.Action, d.Reason)
			continue
		}
		cmd := exec.Command("gh", "pr", "checks", fmt.Sprint(d.PR), "--json", "name,state")
		raw, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "no checks reported") {
				raw = []byte("[]") // doc-only PR under paths-ignore
			} else {
				r.printf("PR %d: удержан, проверки недоступны: %v\n", d.PR, err)
				held++
				continue
			}
		}
		var checks []ghCheck
		if err := json.Unmarshal(raw, &checks); err != nil {
			r.printf("PR %d: удержан, неверный JSON проверок\n", d.PR)
			held++
			continue
		}
		if reason := evaluateChecks(checks); reason != "" {
			r.printf("PR %d: %s\n", d.PR, reason)
			held++
			continue
		}
		mergeCmd := exec.Command("gh", "pr", "merge", fmt.Sprint(d.PR), "--squash", "--delete-branch")
		if out, err := mergeCmd.CombinedOutput(); err != nil {
			r.printf("PR %d: слияние не удалось: %s\n", d.PR, strings.TrimSpace(string(out)))
			held++
			continue
		}
		r.printf("PR %d: MERGED\n", d.PR)
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
	commit := exec.Command("git", "commit", "-m",
		fmt.Sprintf("docs: batch apply %d cards, %d ADRs, %d evals", cards, adrs, evals))
	commit.Dir = root
	return commit.Run()
}

// Run executes the batch apply and returns the process exit code. All file
// and subprocess work happens in this infra package, not in cmd.
func Run(opts Options, out, errOut io.Writer) int {
	r := &runner{opts: opts, out: out, err: errOut}
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
	if opts.MergePlan {
		var decisions []mergeDecision
		for _, b := range batches {
			decisions = append(decisions, b.MergeDecisions...)
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
			path:    filepath.Join("docs", "adrs", fmt.Sprintf("%04d-%s.md", n, slug(a.Title))),
			content: renderADR(a),
		})
	}
	for _, id := range ids {
		entries = append(entries, artifactEntry{
			path:    filepath.Join("docs", "tasks", id+".md"),
			content: renderCard(cards[id]),
		})
	}
	for _, name := range names {
		entries = append(entries, artifactEntry{
			path:    filepath.Join("docs", "evals", slug(name)+".md"),
			content: renderEval(evals[name]),
		})
	}

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
	for _, e := range entries {
		if derivedArtifact(e.path) {
			// Keep the stdout listing unchanged while leaving the derived
			// document off disk. The content stays available through --facts
			// and through the iteration hash printed above.
			if !r.opts.Quiet {
				fmt.Fprintln(r.out, " ", e.path)
			}
			continue
		}
		if err := r.writeFile(root, e.path, e.content); err != nil {
			r.errf("%v\n", err)
			return 1
		}
	}
	if err := r.applyClosures(root, closures); err != nil {
		r.errf("%v\n", err)
		return 1
	}

	if opts.Commit && !opts.DryRun {
		if err := commitArtifacts(root, len(cards), len(adrs), len(evals)); err != nil {
			r.errf("%v\n", err)
			return 1
		}
	}
	return 0
}
