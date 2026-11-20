// Package batchapply turns structured research JSON into ADR, task and eval
// documents. It lives in infra because it reads and writes files and shells
// out to git and gh; cmd/batch-apply stays a thin flag-parsing entry point.
package batchapply

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
	checkReportFormat    = "valid=%d invalid=%d\n"
	summaryReportFormat  = "batches=%d adrs=%d cards=%d evals=%d closures=%d\n"
	mergeReportFormat    = "merged=%d held=%d\n"
	renumberReportFormat = "перенумеровать ADR %d в %d\n"
	skipReportFormat     = "пропуск %s\n"
	warnReportFormat     = "предупреждение: не найдена цель закрытия %s\n"
)

// Options mirrors the command line of cmd/batch-apply.
type Options struct {
	Dir       string
	DryRun    bool
	Check     bool
	MergePlan bool
	Quiet     bool
	Commit    bool
}

type batch struct {
	SourceReports  []string        `json:"source_reports"`
	ADRs           []adr           `json:"adrs"`
	Cards          []card          `json:"cards"`
	Evals          []evalCase      `json:"evals"`
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

type ghCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

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

// applyMergePlan merges only PRs whose required checks are all PASS. A PR
// with no checks is doc-only by the repository's paths-ignore rule. The plan
// itself comes from a research agent; this function re-verifies every gate
// with gh before acting.
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
		required := map[string]bool{"build": true, "test": true, "gates": true, "gates-binary": true}
		green := 0
		blocked := false
		for _, c := range checks {
			if !required[c.Name] {
				continue
			}
			if strings.EqualFold(c.State, "PASS") || strings.EqualFold(c.State, "SUCCESS") {
				green++
			} else {
				blocked = true
			}
		}
		if blocked || (len(checks) > 0 && green < len(required)) {
			r.printf("PR %d: удержан, обязательные проверки не зелёные\n", d.PR)
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

	r.printf(summaryReportFormat,
		len(batches), len(adrs), len(cards), len(evals), len(closures))
	for _, n := range nums {
		a := adrs[n]
		if err := r.writeFile(root, filepath.Join("docs", "adrs", fmt.Sprintf("%04d-%s.md", n, slug(a.Title))), renderADR(a)); err != nil {
			r.errf("%v\n", err)
			return 1
		}
	}
	for _, id := range ids {
		if err := r.writeFile(root, filepath.Join("docs", "tasks", id+".md"), renderCard(cards[id])); err != nil {
			r.errf("%v\n", err)
			return 1
		}
	}
	for _, name := range names {
		if err := r.writeFile(root, filepath.Join("docs", "evals", slug(name)+".md"), renderEval(evals[name])); err != nil {
			r.errf("%v\n", err)
			return 1
		}
	}
	if err := r.applyClosures(root, closures); err != nil {
		r.errf("%v\n", err)
		return 1
	}

	if opts.Commit && !opts.DryRun {
		cmd := exec.Command("git", "add", "docs/adrs", "docs/tasks", "docs/evals")
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			r.errf("%v\n", err)
			return 1
		}
		cmd = exec.Command("git", "commit", "-m",
			fmt.Sprintf("docs: batch apply %d cards, %d ADRs, %d evals", len(cards), len(adrs), len(evals)))
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			r.errf("%v\n", err)
			return 1
		}
	}
	return 0
}
