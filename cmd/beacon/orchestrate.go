package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/github"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// orchestratorRunner holds the command implementation so CLI dispatch can
// call it without depending on the rendering details. RegisterOrchestrator
// replaces it in tests, mirroring RegisterWatch and RegisterGC.
var orchestratorRunner = func(c *CLI, args []string) int { return runOrchestrate(c, args) }

// RegisterOrchestrator installs the orchestrate command implementation.
func RegisterOrchestrator(fn func(*CLI, []string) int) {
	orchestratorRunner = fn
}

// orchestratorDeps is the four-port bundle the loop consumes. Tests install
// fakes with SetOrchestratorDeps; production builds it from the state store
// and the CLI child runner.
type orchestratorDeps struct {
	Store    orchestrator.Store
	Merger   orchestrator.Merger
	Waker    orchestrator.Waker
	Resolver orchestrator.DependencyResolver
}

type orchestratorDepsFactory func(*CLI) (orchestratorDeps, func() error, error)

var makeOrchestratorDeps orchestratorDepsFactory = defaultOrchestratorDeps

// SetOrchestratorDeps replaces the dependency factory. Passing nil restores
// the production factory. Wake delivery lands in another card, so a nil Waker
// is a valid injection: retry/fail Watches are still reported.
func SetOrchestratorDeps(fn orchestratorDepsFactory) {
	if fn == nil {
		fn = defaultOrchestratorDeps
	}
	makeOrchestratorDeps = fn
}

// firstLine keeps only gh's first diagnostic line. The following lines are
// CLI hints that would otherwise be logged by the daemon every three minutes.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return oneLine(s)
}

// ghError builds a single-line error from a failed child process. oneLine is
// the package helper in task.go; firstLine drops the multi-line gh hints.
func ghError(what string, res runResult) error {
	reason := firstLine(string(res.stderr))
	if reason == "" {
		reason = firstLine(string(res.stdout))
	}
	if reason == "" {
		reason = fmt.Sprintf("exit %d", res.code)
	}
	return fmt.Errorf("%s: %s", what, reason)
}

// cliRunner adapts the CLI child-process seam to the Runner port of the
// GitHub adapter. github.BatchProvider always passes --repo on a merge, so the
// merge target no longer depends on the process working directory.
type cliRunner struct{ c *CLI }

var _ github.Runner = cliRunner{}

func (r cliRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.c == nil || r.c.run == nil {
		return nil, fmt.Errorf("runner is not configured")
	}
	res := r.c.run(r.c.root, name, args, nil)
	if res.code == 0 {
		return res.stdout, nil
	}
	return res.stdout, ghError(fmt.Sprintf("%s %s", name, strings.Join(args, " ")), res)
}

// domainMergeAdapter bridges the domain merger to the orchestrator's
// single-error Merger port. github.BatchProvider.MergePR returns the richer
// (merged, held, reason, err) tuple; the adapter treats a hold as an honest
// error so the orchestrator keeps the Watch open and reports the reason.
type domainMergeAdapter struct{ merger domain.Merger }

var _ orchestrator.Merger = domainMergeAdapter{}

func (a domainMergeAdapter) Merge(ctx context.Context, repo string, pr int) error {
	if a.merger == nil {
		return fmt.Errorf("orchestrate: merger is not configured")
	}
	merged, held, reason, err := a.merger.Merge(ctx, repo, pr)
	if err != nil {
		return err
	}
	if merged {
		return nil
	}
	if reason == "" {
		if held {
			reason = "merge held by policy"
		} else {
			reason = "merge was not confirmed"
		}
	}
	return fmt.Errorf("merge %s#%d: %s", repo, pr, reason)
}

// orchWatchReaderFunc adapts a function to the orchestrator's WatchReader
// port. The projection from the domain store happens here in the composition
// root, so the orchestrator app package never needs to name domain types.
type orchWatchReaderFunc func(context.Context) ([]orchestrator.Watch, error)

func (f orchWatchReaderFunc) ActiveWatches(ctx context.Context) ([]orchestrator.Watch, error) {
	return f(ctx)
}

// projectBlocked copies the domain block record into the orchestrator's view.
// FailedChecks is copied so later mutation of the domain Watch cannot reach the
// decision. Since is deliberately absent: the orchestrator only needs the
// reason, the checks, the blocker and the FIX-attempt budget.
func projectBlocked(b *domain.BlockedInfo) *orchestrator.BlockedInfo {
	if b == nil {
		return nil
	}
	return &orchestrator.BlockedInfo{
		Reason:       b.Reason,
		FailedChecks: append([]string(nil), b.FailedChecks...),
		BlockedBy:    b.BlockedBy,
		FixAttempts:  b.FixAttempts,
	}
}

// projectWatch maps one domain Watch onto the orchestrator's decision shape.
// The verdict was derived by domain.EvaluatePR in the single cibridge loop and
// persisted on the Watch; this function only reshapes persisted facts.
func projectWatch(w domain.Watch) orchestrator.Watch {
	var policy *orchestrator.AutoMergePolicy
	if raw, err := json.Marshal(w); err == nil {
		policy = orchestrator.PolicyFromPayload(raw)
	}
	return orchestrator.Watch{
		ID:       w.ID,
		Repo:     w.Repo,
		PR:       w.PR,
		HeadSHA:  w.HeadSHA,
		Verdict:  string(w.Verdict),
		Revision: w.Revision,
		Policy:   policy,
		Blocked:  projectBlocked(w.Blocked),
	}
}

// orchestratorDepsFromDB builds the orchestrator ports from one already-open
// store handle. Watches come from LiveWatches, which includes blocked Watches
// and excludes closed ones, so no second status filter is needed here.
func orchestratorDepsFromDB(c *CLI, db *store.DB) (orchestratorDeps, error) {
	ghBatch := newGitHubBatch(context.Background(), c)
	reader := orchWatchReaderFunc(func(_ context.Context) ([]orchestrator.Watch, error) {
		live, err := db.LiveWatches()
		if err != nil {
			return nil, err
		}
		out := make([]orchestrator.Watch, 0, len(live))
		for _, w := range live {
			out = append(out, projectWatch(w))
		}
		return out, nil
	})
	dbStore := orchestrator.NewDBStore(reader, db)
	// The resolver reads merged PRs from the SQLite Watch rows and probes file
	// refs with local git. It never starts `gh pr view`, so a dependency check
	// adds no CLI process to the auto-merge gateway.
	resolver := orchestrator.NewStoreDependencyResolver(
		filepath.Join(c.root, "docs", "tasks"), cliRunner{c: c}, dbStore)
	return orchestratorDeps{
		Store:    dbStore,
		Merger:   domainMergeAdapter{merger: ghBatch.provider},
		Resolver: resolver,
	}, nil
}

// defaultOrchestratorDeps opens the real state store from the same directory
// the MCP server uses and combines it with the adapter over the batch merger.
// The concrete *store.DB is needed for the merge ledger and the event log.
func defaultOrchestratorDeps(c *CLI) (orchestratorDeps, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return orchestratorDeps{}, nil, err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return orchestratorDeps{}, nil, err
	}
	deps, err := orchestratorDepsFromDB(c, db)
	if err != nil {
		_ = db.Close()
		return orchestratorDeps{}, nil, err
	}
	return deps, db.Close, nil
}

// runOrchestrate implements `beacon orchestrate [--dry-run] [--format table|json]`.
// It makes one pass over live Watches, prints the actions and returns the
// PRs that need a rebase in the NeedsRebase field of the report.
func runOrchestrate(c *CLI, args []string) int {
	fs := flag.NewFlagSet("orchestrate", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "печатать действия без вызова gh и мержа")
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("orchestrate", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon orchestrate: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	deps, closeStore, err := makeOrchestratorDeps(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrate: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	result, err := orchestrator.Run(context.Background(), deps.Store, deps.Merger, deps.Waker,
		orchestrator.Options{DryRun: *dryRun, Resolver: deps.Resolver})
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon orchestrate: %v\n", err)
	}
	if *format == "json" {
		if encErr := renderOrchestratorJSON(c.out, result); encErr != nil {
			fmt.Fprintf(c.errOut, "beacon orchestrate: %v\n", encErr)
			return 2
		}
	} else {
		renderOrchestratorTable(c.out, result)
	}
	if err != nil {
		return 2
	}
	return 0
}

func renderOrchestratorTable(w io.Writer, result orchestrator.Result) {
	if result.DryRun {
		fmt.Fprintln(w, "РЕЖИМ: просмотр без мержа")
	}
	for _, a := range result.Actions {
		line := fmt.Sprintf("%s watch=%s pr=%d revision=%d", a.Action, a.WatchID, a.PR, a.Revision)
		if a.Command != "" {
			line += " cmd=" + a.Command
		}
		if a.Message != "" {
			line += " msg=" + a.Message
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "MERGED: %d\n", len(result.Merged))
	fmt.Fprintf(w, "SKIPPED: %d\n", len(result.Skipped))
	fmt.Fprintf(w, "NEEDS REBASE: %d\n", len(result.NeedsRebase))
	for _, pr := range result.NeedsRebase {
		fmt.Fprintf(w, "REBASE pr=%d\n", pr)
	}
}

func renderOrchestratorJSON(w io.Writer, result orchestrator.Result) error {
	return json.NewEncoder(w).Encode(result)
}

// --- HD-009: issue evidence verification (ADR 0292) -----------------------

// evidenceCheckStep is the brief step an executor performs before it starts
// work: cross-check every file:line proof in the issue against the current
// tree, mark a claim that no longer matches as outdated first, and never take
// a claim with no proof on faith. Blind acceptance fails the card.
const evidenceCheckStep = "Сверь каждую ссылку file:line из issue с текущим кодом; устаревшее утверждение отметь до начала работы; утверждение без ссылки не принимай вслепую."

// evidenceStatus is the outcome of one cross-check. Only EvidenceCurrent
// clears a claim for work. The other two must be reported before proceeding.
type evidenceStatus string

const (
	EvidenceCurrent  evidenceStatus = "current"  // file:line resolves and still matches
	EvidenceOutdated evidenceStatus = "outdated" // file:line is gone or its content drifted
	EvidenceNoRef    evidenceStatus = "no-ref"   // no file:line proof to cross-check
)

// issueEvidence is one issue claim together with the file:line proof the
// issue offers. Expected is the fragment the claim says lives at File:Line;
// empty means only the location itself is asserted. An empty File or a
// non-positive Line means the claim carries no proof at all.
type issueEvidence struct {
	Claim    string
	File     string
	Line     int
	Expected string
}

// currentCode is the current tree as file path -> lines, with index 0 being
// line 1. Callers supply it, so verifyIssueEvidence stays pure and tests can
// drive it with mock data instead of touching the filesystem.
type currentCode map[string][]string

// evidenceVerdict is the result of cross-checking one claim. Evidence and
// Actual are copied through so the caller can show what was checked and what
// the code actually says.
type evidenceVerdict struct {
	Status   evidenceStatus
	Evidence issueEvidence
	Actual   string
	Note     string
}

// verifyIssueEvidence cross-checks a claim's file:line proof against the
// current code. A reference that resolves and still matches is current. A
// reference whose file or line is gone, or whose content drifted from
// Expected, is outdated and must be marked before work. A claim with no
// file:line is reported as no-ref and is never accepted blindly (ADR 0292).
// The function reads no files and has no side effects.
func verifyIssueEvidence(ev issueEvidence, code currentCode) evidenceVerdict {
	v := evidenceVerdict{Evidence: ev}
	if ev.File == "" || ev.Line <= 0 {
		v.Status = EvidenceNoRef
		v.Note = "нет ссылки file:line: не принимать вслепую, запросить доказательство до работы"
		return v
	}
	lines, ok := code[ev.File]
	if !ok || ev.Line > len(lines) {
		v.Status = EvidenceOutdated
		v.Note = "ссылка file:line отсутствует в текущем коде: пометить утверждение устаревшим до работы"
		return v
	}
	v.Actual = lines[ev.Line-1]
	if ev.Expected != "" && !strings.Contains(v.Actual, ev.Expected) {
		v.Status = EvidenceOutdated
		v.Note = "код по ссылке file:line изменился: пометить утверждение устаревшим до работы"
		return v
	}
	v.Status = EvidenceCurrent
	v.Note = "ссылка file:line совпадает с текущим кодом; " + evidenceCheckStep
	return v
}

// clearsForWork reports whether a verdict may be acted on. Only a current
// reference clears a claim; the agent must surface the other two first.
func clearsForWork(v evidenceVerdict) bool {
	return v.Status == EvidenceCurrent
}

// --- HD-041: stable finding id (ADR 0301) -----------------------------------

// generateFindingID derives a permanent identifier for one finding from the
// fields that name the defect: the cited file, its line and the rule that
// fired. The same defect gets the same id on every run, so a blocker can carry
// an id that stays valid across CI passes. The fields are length-prefixed
// before hashing so two different (file, line, rule) triples cannot produce
// the same byte stream by concatenation, and the digest is hex-encoded so the
// id is safe to print and to embed in JSON.
func generateFindingID(file string, line int, rule string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s;%d;%d:%s;", len(file), file, line, len(rule), rule)
	return "finding-" + hex.EncodeToString(h.Sum(nil)[:8])
}

// --- HD-054: config fallback (ADR 0304) ------------------------------------

// fallbackConfig is the effective set of workflow limits after config
// resolution. The numbers match ADR 0259: a project with no config file gets
// the same values as a project that ships the default file.
type fallbackConfig struct {
	MaxRebases int
	SlotsTotal int
}

const (
	fallbackDefaultMaxRebases = 3
	fallbackDefaultSlotsTotal = 4
	fallbackMaxRebasesCeiling = 10
)

// configOrigin says where resolved values came from. A caller can report it
// when it needs to explain a limit; it never changes control flow.
type configOrigin string

const (
	configFromDefaults configOrigin = "defaults"
	configFromFile     configOrigin = "file"
)

// resolvedConfig pairs the effective values with their origin.
type resolvedConfig struct {
	Config fallbackConfig
	Origin configOrigin
}

// fallbackConfigDefaults returns the built-in layer without reading a file.
func fallbackConfigDefaults() fallbackConfig {
	return fallbackConfig{
		MaxRebases: fallbackDefaultMaxRebases,
		SlotsTotal: fallbackDefaultSlotsTotal,
	}
}

// resolveFallbackConfig resolves config values from optional file bytes. The
// function is pure: the caller owns file access, so the same input gives the
// same result in a test and in a workflow pass. Four inputs matter. An empty
// path skips the file layer. Missing data means no file was found. Malformed
// content means the file cannot be trusted. Each of those three cases returns
// the built-in defaults. Valid content overlays only the limits it sets.
func resolveFallbackConfig(path string, data []byte) resolvedConfig {
	fallback := resolvedConfig{Config: fallbackConfigDefaults(), Origin: configFromDefaults}
	if strings.TrimSpace(path) == "" || data == nil {
		return fallback
	}
	cfg, err := applyFallbackLines(fallback.Config, string(data))
	if err != nil {
		return fallback
	}
	return resolvedConfig{Config: cfg, Origin: configFromFile}
}

// applyFallbackLines overlays the numeric limits from one flat key: value
// document. Blank lines, comments and unknown keys are ignored because other
// config consumers own them. A line without a colon, a non-integer value or a
// value outside the accepted range makes the whole file unusable, so the caller
// keeps the defaults.
func applyFallbackLines(base fallbackConfig, content string) (fallbackConfig, error) {
	cfg := base
	for i, raw := range strings.Split(content, "\n") {
		text := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		idx := strings.Index(text, ":")
		if idx < 0 {
			return fallbackConfig{}, fmt.Errorf("line %d: expected key: value", i+1)
		}
		key := normalizeFallbackKey(text[:idx])
		value := trimFallbackValue(text[idx+1:])
		switch key {
		case "maxrebases":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > fallbackMaxRebasesCeiling {
				return fallbackConfig{}, fmt.Errorf("line %d: maxRebases=%q", i+1, value)
			}
			cfg.MaxRebases = n
		case "slotstotal":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return fallbackConfig{}, fmt.Errorf("line %d: slotsTotal=%q", i+1, value)
			}
			cfg.SlotsTotal = n
		}
	}
	return cfg, nil
}

// normalizeFallbackKey lets maxRebases, max_rebases and MAX-REBASES name the
// same setting, matching the other config loaders in the repository.
func normalizeFallbackKey(key string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(
		strings.ToLower(strings.TrimSpace(key)))
}

// trimFallbackValue removes surrounding quotes and a trailing inline comment
// from one config value.
func trimFallbackValue(raw string) string {
	value := strings.TrimSpace(raw)
	if idx := strings.Index(value, " #"); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// --- HD-079: sequential structural giants (ADR 0310) -----------------------

// giantLoCThreshold is the change size at which a work item becomes a
// structural giant. The strict comparison leaves 1000 changed lines on the
// ordinary side and puts 1001 on the giant side.
const giantLoCThreshold = 1000

// giantWork is one proposed work item in a wave. Files is the seam it owns:
// the paths the item may change. Two items that list the same path do not have
// disjoint seams.
type giantWork struct {
	Card  string
	LoC   int
	Files []string
}

// giantStep is one phase of the execution order. A sequential step holds one
// structural giant and no other work.
type giantStep struct {
	Cards      []string
	Sequential bool
}

// wavePlan is the admission result for one proposed wave. Valid stays true
// while every giant can be isolated. Reason names the shared seam when a giant
// cannot be separated from parallel work.
type wavePlan struct {
	Valid  bool
	Steps  []giantStep
	Reason string
}

// isGiant reports whether the change size crosses the structural threshold.
func isGiant(w giantWork) bool {
	return w.LoC > giantLoCThreshold
}

// sharedSeamFile returns the first path both items own. An empty result means
// their seams are disjoint.
func sharedSeamFile(a, b giantWork) string {
	for _, f := range a.Files {
		for _, g := range b.Files {
			if f == g {
				return f
			}
		}
	}
	return ""
}

// disjointSeam reports whether item i owns no path that another item in the
// wave also owns. A giant needs this property before it can leave the parallel
// group and run on its own.
func disjointSeam(i int, items []giantWork) (string, bool) {
	for j, other := range items {
		if j == i {
			continue
		}
		if f := sharedSeamFile(items[i], other); f != "" {
			return f, false
		}
	}
	return "", true
}

// planWave admits one proposed wave and returns the order in which its items
// may run. Ordinary items share one parallel step. Each giant gets a step of
// its own, so a large change runs alone. A giant with a seam that overlaps
// another item has no way to be isolated and is rejected; such a giant would
// have to run in parallel with work on the same files, and that fails the card.
func planWave(items []giantWork) wavePlan {
	plan := wavePlan{Valid: true}
	var parallel []string
	var giants []int
	for i, w := range items {
		if isGiant(w) {
			giants = append(giants, i)
			continue
		}
		parallel = append(parallel, w.Card)
	}
	if len(giants) == 0 {
		if len(parallel) > 0 {
			plan.Steps = []giantStep{{Cards: parallel}}
		}
		return plan
	}
	for _, i := range giants {
		if f, ok := disjointSeam(i, items); !ok {
			return wavePlan{
				Reason: fmt.Sprintf("parallel giant %s: seam file %q is shared; large change needs a disjoint seam",
					items[i].Card, f),
			}
		}
	}
	if len(parallel) > 0 {
		plan.Steps = append(plan.Steps, giantStep{Cards: parallel})
	}
	for _, i := range giants {
		plan.Steps = append(plan.Steps, giantStep{
			Cards:      []string{items[i].Card},
			Sequential: true,
		})
	}
	return plan
}
