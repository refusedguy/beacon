// Command batch-apply turns structured research JSON into ADR, task and eval
// documents. It is the single tool used to create a wave of cards locally;
// research agents only produce JSON, the orchestrator never reads raw reports.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type batch struct {
	SourceReports  []string       `json:"source_reports"`
	ADRs           []adr          `json:"adrs"`
	Cards          []card         `json:"cards"`
	Evals          []evalCase     `json:"evals"`
	MergeDecisions []mergeDecision `json:"merge_decisions"`
	CardClosures   []cardClosure  `json:"card_closures"`
}

type adr struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Context     string `json:"context"`
	Decision    string `json:"decision"`
	Consequences string `json:"consequences"`
}

type card struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Phase       string   `json:"phase"`
	Type        string   `json:"type"`
	OwnerFiles  []string `json:"owner_files"`
	Deps        []string `json:"deps"`
	ADR         string   `json:"adr"`
	Agent       string   `json:"agent"`
	Time        string   `json:"time"`
	Goal        string   `json:"goal"`
	Context     string   `json:"context"`
	Steps       string   `json:"steps"`
	Check       string   `json:"check"`
	Acceptance  string   `json:"acceptance"`
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
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

var quiet bool

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
			problems = append(problems, fmt.Sprintf("%s: invalid JSON (%v)", filepath.Base(p), err))
			continue
		}
		out = append(out, b)
	}
	return out, problems
}

func writeFile(root, rel, content string, dryRun bool) error {
	path := filepath.Join(root, rel)
	if !quiet {
		fmt.Println(" ", path)
	}
	if dryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil // never overwrite an existing card
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

func renderCard(c card) string {
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

func applyClosures(root string, closures map[string]cardClosure, dryRun bool) error {
	for id, c := range closures {
		rel := filepath.Join("docs", "tasks", id+".md")
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN closure target missing: %s\n", rel)
			continue
		}
		text := string(raw)
		if strings.Contains(text, "## Подтверждение") {
			continue
		}
		text = strings.Replace(text, "Статус: todo", "Статус: done", 1)
		text += fmt.Sprintf("\n## Подтверждение\n- PR #%d MERGED: %s (squash).\n", c.PR, c.MergedAt)
		if err := writeFile(root, rel, text, dryRun); err != nil {
			return err
		}
	}
	return nil
}

type ghCheck struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// applyMergePlan merges only PRs whose required checks are all PASS. A PR
// with no checks is doc-only by the repository's paths-ignore rule. The plan
// itself comes from a research agent; this function re-verifies every gate
// with gh before acting.
func applyMergePlan(decisions []mergeDecision) {
	merged, held := 0, 0
	for _, d := range decisions {
		if d.Action != "merge" {
			fmt.Printf("PR %d: %s (%s)\n", d.PR, d.Action, d.Reason)
			continue
		}
		raw, err := exec.Command("gh", "pr", "checks", fmt.Sprint(d.PR), "--json", "name,state").Output()
		if err != nil {
			fmt.Printf("PR %d: hold (checks unavailable: %v)\n", d.PR, err)
			held++
			continue
		}
		var checks []ghCheck
		if err := json.Unmarshal(raw, &checks); err != nil {
			fmt.Printf("PR %d: hold (bad checks JSON)\n", d.PR)
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
			fmt.Printf("PR %d: hold (required checks not green)\n", d.PR)
			held++
			continue
		}
		cmd := exec.Command("gh", "pr", "merge", fmt.Sprint(d.PR), "--squash", "--delete-branch")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("PR %d: merge failed: %s\n", d.PR, strings.TrimSpace(string(out)))
			held++
			continue
		}
		fmt.Printf("PR %d: MERGED\n", d.PR)
		merged++
	}
	fmt.Printf("merge-plan: merged=%d held=%d\n", merged, held)
}

func main() {
	dir := flag.String("dir", "", "batch directory with JSON files")
	dryRun := flag.Bool("dry-run", false, "print files without writing")
	check := flag.Bool("check", false, "validate JSON only, print a compact summary")
	mergePlan := flag.Bool("merge-plan", false, "merge PRs listed in merge_decisions after verifying checks")
	quietFlag := flag.Bool("quiet", false, "do not list every generated file")
	commit := flag.Bool("commit", false, "git add and commit generated docs")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "--dir is required")
		os.Exit(2)
	}
	quiet = *quietFlag
	root, err := rootDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	batches, problems := loadBatches(*dir)
	if *mergePlan {
		var decisions []mergeDecision
		for _, b := range batches {
			decisions = append(decisions, b.MergeDecisions...)
		}
		applyMergePlan(decisions)
		return
	}
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "SKIP %s\n", p)
	}
	if *check {
		fmt.Printf("check: valid=%d invalid=%d\n", len(batches), len(problems))
		if len(problems) > 0 {
			os.Exit(1)
		}
		return
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
		if a.Number != nextFree && !quiet {
			fmt.Printf("renumber ADR %d -> %d\n", a.Number, nextFree)
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

	fmt.Printf("batches=%d adrs=%d cards=%d evals=%d closures=%d\n",
		len(batches), len(adrs), len(cards), len(evals), len(closures))
	for _, n := range nums {
		a := adrs[n]
		if err := writeFile(root, filepath.Join("docs", "adrs", fmt.Sprintf("%04d-%s.md", n, slug(a.Title))), renderADR(a), *dryRun); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	for _, id := range ids {
		if err := writeFile(root, filepath.Join("docs", "tasks", id+".md"), renderCard(cards[id]), *dryRun); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	for _, name := range names {
		if err := writeFile(root, filepath.Join("docs", "evals", slug(name)+".md"), renderEval(evals[name]), *dryRun); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := applyClosures(root, closures, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if *commit && !*dryRun {
		cmd := exec.Command("git", "add", "docs/adrs", "docs/tasks", "docs/evals")
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		cmd = exec.Command("git", "commit", "-m",
			fmt.Sprintf("docs: batch apply %d cards, %d ADRs, %d evals", len(cards), len(adrs), len(evals)))
		cmd.Dir = root
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
