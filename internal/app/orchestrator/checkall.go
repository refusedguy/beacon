// Package orchestrator implements the automatic pass over open pull requests.
//
// This file owns `beacon ci check --all`: it lists open PRs with one gh call,
// finds or registers a Watch for each head, reads every check in one batched gh
// api call, and persists the verdict with cibridge's publish rules. The layer
// matrix forbids internal/app -> internal/domain, so the domain-shaped values
// are mirrored here and the composition root supplies narrow function ports.
// The gh CLI itself is a runner function: no os/exec reaches this layer.
package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/refusedguy/beacon/internal/infra/server"
)

// Verdict values produced by this pass. This is a mirror of the domain verdict
// contract in internal/domain/types.go. ADR 0005 forbids internal/app from
// importing internal/domain, so the strings are repeated here and a test in
// this package compares them with domain.Verdicts(). CheckVerdictWatching is
// the only value the pass treats as non-terminal.
type Verdict string

const (
	CheckVerdictWatching Verdict = "watching"
	CheckVerdictReady    Verdict = "ready"
	CheckVerdictRetry    Verdict = "retry"

	// wakeSessionPrefix is the prefix OpenCode uses for session ids. The wake
	// adapter rejects any other id before calling the prompt API, so a service
	// name registered as a notify target produces "wake: error" instead of a
	// delivered pointer.
	wakeSessionPrefix = "ses_"
)

// CheckVerdicts returns the verdict values this pass can publish. Keeping the
// set in one value lets the equality test compare it with domain.Verdicts().
func CheckVerdicts() []Verdict {
	return []Verdict{CheckVerdictWatching, CheckVerdictReady, CheckVerdictRetry}
}

// VerdictValidator reports whether a verdict belongs to the domain contract.
// ADR 0005 forbids internal/app from importing internal/domain, so the check
// arrives as a narrow function port. The default reads the mirror set that
// verdict_contract_test.go compares with domain.Verdicts().
type VerdictValidator func(Verdict) bool

func validCheckVerdict(v Verdict) bool {
	for _, known := range CheckVerdicts() {
		if v == known {
			return true
		}
	}
	return false
}

// ValidateWakeTarget checks that a notify target can address an OpenCode
// session. The orchestrator subscriber of a Watch is the wake recipient, so a
// target without a session id cannot receive a verdict pointer.
func ValidateWakeTarget(target string) error {
	if !strings.HasPrefix(target, wakeSessionPrefix) || len(target) == len(wakeSessionPrefix) {
		return fmt.Errorf("checkall: неверный wake target %q: нужен sessionID OpenCode с префиксом %s", target, wakeSessionPrefix)
	}
	return nil
}

// Project configuration. The global layer lives in ~/.config/beacon/config.yaml
// (or at the path in BEACON_CONFIG) and the project layer in
// <repo>/.beacon/config.yaml. A project key overrides a global key; a missing
// file is not an error.
const (
	defaultMaxRebases = 3
	minMaxRebases     = 1
	maxMaxRebases     = 10

	// defaultSlotsTotal mirrors domain.DefaultSlotsTotal. The app layer may not
	// import internal/domain (ADR 0005), so the number is mirrored here.
	defaultSlotsTotal = 4

	configDirName  = ".beacon"
	configFileName = "config.yaml"
	configPathEnv  = "BEACON_CONFIG"
)

// Config is the effective project configuration after the global and project
// layers are merged.
type Config struct {
	MaxRebases     int         `json:"maxRebases" yaml:"maxRebases"`
	Repo           string      `json:"repo,omitempty" yaml:"repo,omitempty"`
	OrchestratorID string      `json:"orchestratorID" yaml:"orchestratorID"`
	SlotsTotal     int         `json:"slotsTotal" yaml:"slotsTotal"`
	WakeTarget     string      `json:"wakeTarget" yaml:"wakeTarget"`
	Flaky          FlakyConfig `json:"flaky,omitempty" yaml:"flaky,omitempty"`
}

// FlakyConfig is the app-layer mirror of the flaky-check settings. Names are
// check-name patterns and MaxRetries is the per-check retry budget. The app
// layer may not import internal/domain (ADR 0005), so the shape is repeated.
type FlakyConfig struct {
	Names      []string `json:"names,omitempty" yaml:"names,omitempty"`
	MaxRetries int      `json:"maxRetries,omitempty" yaml:"maxRetries,omitempty"`
}

// DefaultConfig returns the built-in layer, before either YAML file is read.
func DefaultConfig() Config {
	return Config{MaxRebases: defaultMaxRebases, SlotsTotal: defaultSlotsTotal}
}

// Validate checks the effective configuration. A zero SlotsTotal is valid and
// means no capacity, matching domain.NewOrchestrator.
func (c Config) Validate() error {
	if c.MaxRebases < minMaxRebases || c.MaxRebases > maxMaxRebases {
		return fmt.Errorf("config: maxRebases=%d вне диапазона %d..%d", c.MaxRebases, minMaxRebases, maxMaxRebases)
	}
	if c.SlotsTotal < 0 {
		return fmt.Errorf("config: slotsTotal=%d не может быть отрицательным", c.SlotsTotal)
	}
	if c.OrchestratorID != "" {
		if err := ValidateWakeTarget(c.OrchestratorID); err != nil {
			return fmt.Errorf("config: orchestratorID: %w", err)
		}
	}
	if c.WakeTarget != "" {
		if err := ValidateWakeTarget(c.WakeTarget); err != nil {
			return fmt.Errorf("config: wakeTarget: %w", err)
		}
	}
	return nil
}

// normalizeConfigKey is the lookup form shared by the parser and the merger.
func normalizeConfigKey(key string) string {
	replacer := strings.NewReplacer("_", "", "-", "", " ", "")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(key)))
}

// LoadProjectConfig merges the built-in defaults, the global file and the
// project file in that order. Missing files leave the previous layer intact;
// a malformed or invalid file is an error.
func LoadProjectConfig(dir string) (Config, error) {
	cfg := DefaultConfig()

	globalPath, err := globalConfigPath()
	if err != nil {
		return Config{}, err
	}
	if err := applyConfigFile(&cfg, globalPath); err != nil {
		return Config{}, err
	}
	if root := strings.TrimSpace(dir); root != "" {
		if err := applyConfigFile(&cfg, filepath.Join(root, configDirName, configFileName)); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// globalConfigPath resolves the global layer. BEACON_CONFIG names a file path;
// without it the file lives under ~/.config/beacon.
func globalConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(configPathEnv)); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: домашний каталог: %w", err)
	}
	return filepath.Join(home, ".config", "beacon", configFileName), nil
}

// applyConfigFile reads one YAML file and overlays the keys it contains.
// Missing files leave the previous layer intact. Keys are normalized so
// maxRebases, max_rebases and MAX-REBASES all name the same setting; a nested
// flaky section carries the per-check retry configuration.
// fs.ReadFile over os.DirFS keeps file access inside the capabilities gate's
// infra capability boundary (ADR 0005), the same seam registry_config.go uses.
func applyConfigFile(cfg *Config, path string) error {
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("config: %s: ожидается key: value: %w", path, err)
	}
	for key, raw := range doc {
		switch normalizeConfigKey(key) {
		case "maxrebases":
			n, err := configInt(path, key, raw)
			if err != nil {
				return err
			}
			cfg.MaxRebases = n
		case "repo":
			s, err := configString(path, key, raw)
			if err != nil {
				return err
			}
			cfg.Repo = s
		case "slotstotal":
			n, err := configInt(path, key, raw)
			if err != nil {
				return err
			}
			cfg.SlotsTotal = n
		case "orchestratorid":
			s, err := configString(path, key, raw)
			if err != nil {
				return err
			}
			cfg.OrchestratorID = s
		case "waketarget":
			s, err := configString(path, key, raw)
			if err != nil {
				return err
			}
			cfg.WakeTarget = s
		case "flaky":
			if err := applyFlakySection(cfg, path, raw); err != nil {
				return err
			}
		default:
			return fmt.Errorf("config: %s: неизвестный ключ %q", path, key)
		}
	}
	return nil
}

// configInt accepts a YAML integer or a quoted integer string so values written
// before the YAML migration keep loading. Anything else is a config error.
func configInt(path, key string, raw any) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("config: %s: %s: неверное целое %q", path, key, v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("config: %s: %s: неверное целое %v", path, key, raw)
	}
}

// configString reads a scalar YAML value as a string.
func configString(path, key string, raw any) (string, error) {
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("config: %s: %s: ожидается строка", path, key)
	}
	return strings.TrimSpace(s), nil
}

// applyFlakySection overlays the nested flaky settings. The section is optional
// on both config layers, so a project can override only Names or only MaxRetries.
func applyFlakySection(cfg *Config, path string, raw any) error {
	section, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("config: %s: flaky: ожидается key: value", path)
	}
	for key, value := range section {
		switch normalizeConfigKey(key) {
		case "names":
			items, ok := value.([]any)
			if !ok {
				return fmt.Errorf("config: %s: flaky.names: ожидается список", path)
			}
			names := make([]string, 0, len(items))
			for _, item := range items {
				name, err := configString(path, "flaky.names", item)
				if err != nil {
					return err
				}
				names = append(names, name)
			}
			cfg.Flaky.Names = names
		case "maxretries":
			n, err := configInt(path, "flaky.maxRetries", value)
			if err != nil {
				return err
			}
			cfg.Flaky.MaxRetries = n
		default:
			return fmt.Errorf("config: %s: flaky: неизвестный ключ %q", path, key)
		}
	}
	return nil
}

// PR is one open pull request reported by gh. Repo is the slug used as the
// Watch alias; Number and HeadSHA identify the exact head being checked.
type PR struct {
	Repo    string
	Number  int
	HeadSHA string
}

// Check is one normalized status check conclusion. Required is nil when the
// provider did not report the flag. An unknown value never makes a check
// mandatory in the Watch payload.
type Check struct {
	Name       string
	Conclusion string
	Required   *bool
}

// WatchRef is the small projection of a Beacon Watch this pass needs.
type WatchRef struct {
	ID       string
	Repo     string
	PR       int
	HeadSHA  string
	Revision uint64
}

// Row is one line of the summary returned to the CLI.
type Row struct {
	PR           int      `json:"pr"`
	Repo         string   `json:"repo"`
	WatchID      string   `json:"watchID"`
	Registered   bool     `json:"registered,omitempty"`
	Verdict      Verdict  `json:"verdict"`
	FailedChecks []string `json:"failedChecks,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Summary is the whole pass. Registered and Published count writes that were
// applied; under DryRun they count the writes that would have been applied.
type Summary struct {
	DryRun     bool  `json:"dryRun"`
	Registered int   `json:"registered"`
	Published  int   `json:"published"`
	Rows       []Row `json:"rows"`
}

// GHRunner runs one gh command and returns its stdout. The CLI owns the real
// process; tests replace this function with a fake that records the commands.
type GHRunner func(args ...string) ([]byte, error)

// PRSource supplies open PRs and one batched read of all their checks.
type PRSource interface {
	OpenPRs(ctx context.Context) ([]PR, error)
	Checks(ctx context.Context, prs []PR) (map[int][]Check, error)
}

// WatchRegistry finds an existing Watch by (repo, pr, headSHA) or registers a
// new one with the orchestrator role.
type WatchRegistry interface {
	Find(ctx context.Context, pr PR) (WatchRef, bool, error)
	Register(ctx context.Context, pr PR) (WatchRef, error)
}

// VerdictWriter persists a terminal verdict for one Watch revision.
type VerdictWriter interface {
	Publish(ctx context.Context, ref WatchRef, verdict Verdict, checks []Check) error
}

// Deps bundles the ports CheckAll consumes. A nil port is rejected up front.
type Deps struct {
	Source    PRSource
	Watches   WatchRegistry
	Writer    VerdictWriter
	ConfigDir string
	DryRun    bool
}

// CheckAll runs one full pass. A per-PR registration or publish failure is
// recorded on that row and does not stop the other PRs; a failure to list PRs
// or to read the batched checks is fatal because there is nothing to report.
func CheckAll(ctx context.Context, deps Deps) (Summary, error) {
	if _, err := LoadProjectConfig(deps.ConfigDir); err != nil {
		return Summary{}, err
	}
	if deps.Source == nil || deps.Watches == nil || deps.Writer == nil {
		return Summary{}, fmt.Errorf("checkall: источники не подключены")
	}

	prs, err := deps.Source.OpenPRs(ctx)
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{DryRun: deps.DryRun, Rows: make([]Row, len(prs))}
	refs := make([]WatchRef, len(prs))
	for i, pr := range prs {
		ref, found, err := deps.Watches.Find(ctx, pr)
		if err != nil {
			summary.Rows[i] = Row{PR: pr.Number, Repo: pr.Repo, Error: err.Error()}
			continue
		}
		if !found {
			if !deps.DryRun {
				ref, err = deps.Watches.Register(ctx, pr)
				if err != nil {
					summary.Rows[i] = Row{PR: pr.Number, Repo: pr.Repo, Error: err.Error()}
					continue
				}
			}
			summary.Registered++
			summary.Rows[i].Registered = true
		}
		refs[i] = ref
		summary.Rows[i].PR = pr.Number
		summary.Rows[i].Repo = pr.Repo
		summary.Rows[i].WatchID = ref.ID
	}

	// One batched read for every PR, never gh pr checks in a loop.
	checksByPR, err := deps.Source.Checks(ctx, prs)
	if err != nil {
		return Summary{}, err
	}
	for i, pr := range prs {
		if summary.Rows[i].Error != "" {
			continue
		}
		checks := checksByPR[pr.Number]
		verdict := VerdictFromChecks(checks)
		summary.Rows[i].Verdict = verdict
		summary.Rows[i].FailedChecks = FailedChecks(checks)
		if verdict == CheckVerdictWatching {
			continue
		}
		if !deps.DryRun {
			if err := deps.Writer.Publish(ctx, refs[i], verdict, checks); err != nil {
				summary.Rows[i].Error = err.Error()
				continue
			}
		}
		summary.Published++
	}
	return summary, nil
}

// informational reports whether the provider explicitly marked a check as
// non-required. The domain verdict engine drops an explicitly non-required
// check and stays fail-closed when the flag is absent, so the parallel app
// engine uses the same rule.
func informational(c Check) bool {
	return c.Required != nil && !*c.Required
}

// VerdictFromChecks applies cibridge's rules: no checks means still watching,
// a failing or cancelled check asks for retry, all green is ready, and any
// other pending state keeps the Watch watching. SKIPPED and NEUTRAL count as
// green, matching the domain's harmless-conclusion rule. A check marked
// non-required is informational: it never asks for retry, but it keeps the
// Watch watching instead of letting the pass publish ready.
func VerdictFromChecks(checks []Check) Verdict {
	if len(checks) == 0 {
		return CheckVerdictWatching
	}
	allGreen := true
	for _, c := range checks {
		switch normalizeConclusion(c.Conclusion) {
		case "SUCCESS":
			continue
		case "FAILURE", "CANCELLED":
			allGreen = false
			if informational(c) {
				continue
			}
			return CheckVerdictRetry
		default:
			allGreen = false
		}
	}
	if allGreen {
		return CheckVerdictReady
	}
	return CheckVerdictWatching
}

// FailedChecks returns the sorted names of failing or cancelled checks that
// can block the Watch. Checks explicitly marked non-required are omitted.
func FailedChecks(checks []Check) []string {
	var out []string
	for _, c := range checks {
		if informational(c) {
			continue
		}
		canonical := normalizeConclusion(c.Conclusion)
		if canonical == "FAILURE" || canonical == "CANCELLED" {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// ---- artifact categories ------------------------------------------------

// ArtifactCategory is one of the three Beacon artifact categories. Unknown is
// the bucket for paths that belong to no category, so every path lands in
// exactly one place.
type ArtifactCategory string

const (
	// ArtifactWorkflow covers flat workflow files under .github/workflows.
	ArtifactWorkflow ArtifactCategory = "workflow"
	// ArtifactKnowledge covers the skill directories under .opencode/skills.
	ArtifactKnowledge ArtifactCategory = "knowledge"
	// ArtifactRules covers always-active rule files and anything in a rules dir.
	ArtifactRules   ArtifactCategory = "rules"
	ArtifactUnknown ArtifactCategory = "unknown"
)

// ArtifactClassification groups paths by category. The four buckets never
// overlap: a path appears in exactly one of them.
type ArtifactClassification struct {
	Workflow  []string
	Knowledge []string
	Rules     []string
	Unknown   []string
}

// ClassifyArtifacts sorts repo-relative paths into the three Beacon artifact
// categories. A nil or empty list yields an empty classification. Paths that
// match no category go to Unknown.
func ClassifyArtifacts(paths []string) ArtifactClassification {
	var out ArtifactClassification
	for _, path := range paths {
		switch classifyArtifactPath(path) {
		case ArtifactWorkflow:
			out.Workflow = append(out.Workflow, path)
		case ArtifactKnowledge:
			out.Knowledge = append(out.Knowledge, path)
		case ArtifactRules:
			out.Rules = append(out.Rules, path)
		default:
			out.Unknown = append(out.Unknown, path)
		}
	}
	return out
}

// classifyArtifactPath maps one repo-relative path to its single category.
// Workflows are flat files directly under .github/workflows. Knowledge is any
// path inside a skill directory under .opencode/skills. Rules are always-active
// files or anything under a rules directory. Everything else is unknown.
func classifyArtifactPath(path string) ArtifactCategory {
	p := strings.TrimSpace(path)
	if p == "" {
		return ArtifactUnknown
	}
	p = filepath.ToSlash(filepath.Clean(strings.ReplaceAll(p, "\\", "/")))
	if p == "." || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") {
		return ArtifactUnknown
	}
	parts := strings.Split(p, "/")

	if len(parts) >= 3 && parts[0] == ".opencode" && parts[1] == "skills" {
		return ArtifactKnowledge
	}
	if len(parts) == 3 && parts[0] == ".github" && parts[1] == "workflows" {
		return ArtifactWorkflow
	}
	if isAlwaysActiveRuleName(parts[len(parts)-1]) {
		return ArtifactRules
	}
	for _, dir := range parts[:len(parts)-1] {
		if dir == "rules" {
			return ArtifactRules
		}
	}
	return ArtifactUnknown
}

// isAlwaysActiveRuleName reports whether the file name is a rule file that
// applies to every task, such as AGENTS.md or .cursorrules.
func isAlwaysActiveRuleName(base string) bool {
	switch strings.ToLower(base) {
	case "agents.md", "claude.md", ".cursorrules":
		return true
	default:
		return false
	}
}

// ---- gh adapter ---------------------------------------------------------

// GHSource turns gh output into the pass's input. OpenPRs makes exactly one
// `gh pr list` call; Checks makes exactly one `gh api graphql` call.
type GHSource struct {
	repo string
	run  GHRunner
}

var _ PRSource = (*GHSource)(nil)

func NewGHSource(repo string, run GHRunner) *GHSource {
	return &GHSource{repo: repo, run: run}
}

// OpenPRs lists open PRs. The argument list is fixed so callers can assert the
// single call and the exact fields gh was asked for.
func (s *GHSource) OpenPRs(ctx context.Context) ([]PR, error) {
	if s == nil || s.run == nil {
		return nil, fmt.Errorf("checkall: gh runner is nil")
	}
	_ = ctx // process lifetime is owned by the CLI runner
	out, err := s.run("pr", "list", "--json", "number,headRefOid")
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var rows []struct {
		Number     int    `json:"number"`
		HeadRefOid string `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("gh pr list decode: %w", err)
	}
	prs := make([]PR, 0, len(rows))
	for _, r := range rows {
		if r.Number <= 0 || r.HeadRefOid == "" {
			continue
		}
		prs = append(prs, PR{Repo: s.repo, Number: r.Number, HeadSHA: r.HeadRefOid})
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number < prs[j].Number })
	return prs, nil
}

// Checks reads every PR's rollup in one GraphQL request with one alias per PR.
func (s *GHSource) Checks(ctx context.Context, prs []PR) (map[int][]Check, error) {
	if s == nil || s.run == nil {
		return nil, fmt.Errorf("checkall: gh runner is nil")
	}
	_ = ctx
	out := make(map[int][]Check, len(prs))
	if len(prs) == 0 {
		return out, nil
	}

	// One batched request is one observation, even when the query carries an
	// alias per PR. The timer covers the gh call only, not response decoding.
	start := time.Now()
	raw, err := s.run("api", "graphql", "-f", "query="+BuildChecksQuery(prs))
	server.ObserveGraphQLBatch(time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("gh api graphql: %w", err)
	}
	var body graphQLChecksResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("gh api graphql decode: %w", err)
	}
	if len(body.Errors) > 0 {
		return nil, fmt.Errorf("gh api graphql: %s", body.Errors[0].Message)
	}
	for i, pr := range prs {
		rawRepo, ok := body.Data["p"+strconv.Itoa(i)]
		if !ok || string(rawRepo) == "null" {
			continue
		}
		var repo graphQLRepository
		if err := json.Unmarshal(rawRepo, &repo); err != nil {
			return nil, fmt.Errorf("gh api graphql p%d decode: %w", i, err)
		}
		if repo.PullRequest == nil || repo.PullRequest.StatusCheckRollup == nil {
			continue
		}
		for _, node := range repo.PullRequest.StatusCheckRollup.Contexts.Nodes {
			check, ok := checkFromNode(node)
			if ok {
				out[pr.Number] = append(out[pr.Number], check)
			}
		}
	}
	return out, nil
}

type graphQLChecksResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type graphQLRepository struct {
	PullRequest *graphQLPullRequest `json:"pullRequest"`
}

type graphQLPullRequest struct {
	StatusCheckRollup *graphQLRollup `json:"statusCheckRollup"`
}

type graphQLRollup struct {
	Contexts struct {
		Nodes []json.RawMessage `json:"nodes"`
	} `json:"contexts"`
}

type checkNode struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	State      string `json:"state"`
	IsRequired *bool  `json:"isRequired"`
}

func checkFromNode(raw json.RawMessage) (Check, bool) {
	var n checkNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return Check{}, false
	}
	name := n.Name
	if name == "" {
		name = n.Context
	}
	if name == "" {
		return Check{}, false
	}
	// A CheckRun carries conclusion and status; only StatusContext carries
	// state. An unfinished CheckRun has no conclusion, so its status maps to
	// pending.
	conclusion := n.Conclusion
	if conclusion == "" {
		if n.Typename == "StatusContext" {
			conclusion = n.State
		} else {
			conclusion = n.Status
		}
	}
	return Check{Name: name, Conclusion: normalizeConclusion(conclusion), Required: n.IsRequired}, true
}

// normalizeConclusion is the app-layer mirror of domain.NormalizeConclusion.
// ADR 0005 forbids an app import of domain, so the vocabulary is repeated here.
// Every check conclusion in this pass goes through this one function: parsing,
// verdict selection and failure listing.
func normalizeConclusion(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PASS", "SUCCESS", "SUCCESSFUL", "SKIPPED", "NEUTRAL":
		return "SUCCESS"
	case "FAIL", "FAILURE", "ERROR", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return "FAILURE"
	case "CANCELLED", "CANCELED":
		return "CANCELLED"
	default:
		return "PENDING"
	}
}

// BuildChecksQuery renders one GraphQL document with a unique alias per PR.
func BuildChecksQuery(prs []PR) string {
	var b strings.Builder
	b.WriteString("query {\n")
	for i, pr := range prs {
		owner, name := splitRepo(pr.Repo)
		fmt.Fprintf(&b, "  p%d: repository(owner:%s, name:%s) {\n", i, strconv.Quote(owner), strconv.Quote(name))
		fmt.Fprintf(&b, "    pullRequest(number:%d) {\n", pr.Number)
		required := "isRequired(pullRequestNumber:" + strconv.Itoa(pr.Number) + ")"
		// A second inline fragment per type keeps the original projections
		// byte-identical for the query contract guard. GraphQL merges the two.
		fmt.Fprintf(&b, `      statusCheckRollup {
        contexts(first:50) {
          nodes {
            __typename
            ... on CheckRun { name conclusion status }
            ... on CheckRun { %s }
            ... on StatusContext { context state }
            ... on StatusContext { %s }
          }
        }
      }
`, required, required)
		b.WriteString("    }\n  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func splitRepo(repo string) (string, string) {
	owner, name, found := strings.Cut(repo, "/")
	if !found {
		return "", repo
	}
	return owner, name
}

// ---- function ports -----------------------------------------------------

// FuncRegistry adapts two closures to WatchRegistry so the composition root
// can use the real store and the MCP watch_pr handler without naming domain
// types in this layer.
type FuncRegistry struct {
	FindFn     func(context.Context, PR) (WatchRef, bool, error)
	RegisterFn func(context.Context, PR) (WatchRef, error)
}

var _ WatchRegistry = FuncRegistry{}

func (f FuncRegistry) Find(ctx context.Context, pr PR) (WatchRef, bool, error) {
	return f.FindFn(ctx, pr)
}

func (f FuncRegistry) Register(ctx context.Context, pr PR) (WatchRef, error) {
	return f.RegisterFn(ctx, pr)
}

// FuncWriter adapts one closure to VerdictWriter.
type FuncWriter struct {
	PublishFn func(context.Context, WatchRef, Verdict, []Check) error
}

var _ VerdictWriter = FuncWriter{}

func (f FuncWriter) Publish(ctx context.Context, ref WatchRef, verdict Verdict, checks []Check) error {
	return f.PublishFn(ctx, ref, verdict, checks)
}

// ---- verdict persistence ------------------------------------------------

// TxWriter is the store's single-writer surface. *store.DB satisfies it; the
// app layer needs no domain type to update a Watch and stage its obligations.
type TxWriter interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// SQLVerdictWriter mirrors cibridge's publish: it only moves a still-watching
// Watch at the same head, bumps the revision, stores the observed checks and
// stages one obligation per subscriber so get_result can project the verdict.
type SQLVerdictWriter struct {
	writer TxWriter
	now    func() time.Time
	valid  VerdictValidator
}

var _ VerdictWriter = (*SQLVerdictWriter)(nil)

func NewSQLVerdictWriter(w TxWriter) *SQLVerdictWriter {
	return &SQLVerdictWriter{writer: w, now: time.Now, valid: validCheckVerdict}
}

func (w *SQLVerdictWriter) Publish(ctx context.Context, ref WatchRef, verdict Verdict, checks []Check) error {
	if w == nil || w.writer == nil {
		return fmt.Errorf("checkall: verdict writer is nil")
	}
	if verdict == CheckVerdictWatching {
		return nil
	}
	valid := w.valid
	if valid == nil {
		valid = validCheckVerdict
	}
	if !valid(verdict) {
		return fmt.Errorf("checkall: вердикт %q вне доменного контракта", verdict)
	}
	checksPayload, err := checksPayload(checks)
	if err != nil {
		return err
	}
	now := time.Now()
	if w.now != nil {
		now = w.now()
	}

	return w.writer.Write(ctx, func(tx *sql.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM watches WHERE id = ?`, ref.ID).Scan(&raw); err != nil {
			return err
		}
		var watch map[string]json.RawMessage
		if err := json.Unmarshal(raw, &watch); err != nil {
			return err
		}
		var currentVerdict, currentHead string
		if err := json.Unmarshal(watch["verdict"], &currentVerdict); err != nil {
			return fmt.Errorf("checkall: разбор verdict Watch %s: %w", ref.ID, err)
		}
		if err := json.Unmarshal(watch["headSHA"], &currentHead); err != nil {
			return fmt.Errorf("checkall: разбор headSHA Watch %s: %w", ref.ID, err)
		}
		if currentVerdict != string(CheckVerdictWatching) || currentHead != ref.HeadSHA {
			return nil
		}
		var revision uint64
		if err := json.Unmarshal(watch["revision"], &revision); err != nil {
			return fmt.Errorf("checkall: разбор revision Watch %s: %w", ref.ID, err)
		}
		nextRevision := revision + 1

		verdictJSON, err := json.Marshal(string(verdict))
		if err != nil {
			return fmt.Errorf("checkall: сериализация verdict Watch %s: %w", ref.ID, err)
		}
		statusJSON, err := json.Marshal("notifying")
		if err != nil {
			return fmt.Errorf("checkall: сериализация status Watch %s: %w", ref.ID, err)
		}
		revisionJSON, err := json.Marshal(nextRevision)
		if err != nil {
			return fmt.Errorf("checkall: сериализация revision Watch %s: %w", ref.ID, err)
		}
		watch["verdict"], watch["status"], watch["revision"] = verdictJSON, statusJSON, revisionJSON
		watch["checks"] = checksPayload
		updated, err := json.Marshal(watch)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE watches SET payload = ?, revision = ?, status = ? WHERE id = ?`,
			updated, nextRevision, "notifying", ref.ID,
		); err != nil {
			return err
		}

		var subscribers []struct {
			ID string `json:"id"`
		}
		if rawSubs := watch["subscribers"]; len(rawSubs) != 0 {
			if err := json.Unmarshal(rawSubs, &subscribers); err != nil {
				return err
			}
		}
		for _, sub := range subscribers {
			if sub.ID == "" {
				continue
			}
			id := obligationKey(ref.ID, nextRevision, sub.ID)
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM obligations WHERE id = ?`, id).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			payload, err := json.Marshal(struct {
				ID             string    `json:"id"`
				WatchID        string    `json:"watchID"`
				Revision       uint64    `json:"revision"`
				SubscriberID   string    `json:"subscriberID"`
				VerdictPointer Verdict   `json:"verdictPointer"`
				StagedAt       time.Time `json:"stagedAt"`
			}{id, ref.ID, nextRevision, sub.ID, verdict, now})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO obligations
				  (id, watch_id, revision, subscriber_id, verdict_pointer, delivered_at, payload)
				VALUES (?, ?, ?, ?, ?, 0, ?)`,
				id, ref.ID, nextRevision, sub.ID, string(verdict), payload,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func checksPayload(checks []Check) (json.RawMessage, error) {
	rows := make(map[string]map[string]any, len(checks))
	for _, c := range checks {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		required := false
		if c.Required != nil {
			required = *c.Required
		}
		rows[name] = map[string]any{
			"state":      c.Conclusion,
			"conclusion": c.Conclusion,
			"required":   required,
			"detailsUrl": "",
		}
	}
	return json.Marshal(rows)
}

// obligationKeyEscaper keeps the ':' separator unambiguous, matching the
// domain's ObligationKey so get_result resolves the staged obligation.
var obligationKeyEscaper = strings.NewReplacer("\\", "\\\\", ":", "\\:")

func obligationKey(watchID string, revision uint64, subscriberID string) string {
	return obligationKeyEscaper.Replace(watchID) + ":" +
		strconv.FormatUint(revision, 10) + ":" +
		obligationKeyEscaper.Replace(subscriberID)
}

// ---- strict gate-result JSON (HD-046, ADR 0301) --------------------------

// ValidateStrictJSON enforces the gate-result fence contract. The body between
// the fence markers must be exactly one JSON value and nothing else. Harbor
// accepts JSON only, so the check rejects comments, trailing commas, prose
// around the value and an empty fence. encoding/json rejects all four, so no
// permissive decoder is needed and the function stays pure.
func ValidateStrictJSON(body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("aif-gate-result: пустой fence")
	}
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return fmt.Errorf("aif-gate-result: strict JSON: %w", err)
	}
	return nil
}

// ---- generated documentation index (HD-089, ADR 0313) -------------------

// docsIndexPreamble marks the file as generated. A hand-written index does not
// carry this marker, so --check rejects it together with a stale document list.
const docsIndexPreamble = "<!-- generated: beacon docs index. Do not edit by hand. -->\n\n# Documentation index\n\n"

// BuildDocsIndex renders docs/INDEX.md from a set of documentation paths.
// Paths are normalized to slash-separated, cleaned, de-duplicated and sorted,
// so the same document set always produces the same bytes. The generated file
// never links to itself and paths outside the repository are ignored rather
// than guessed.
func BuildDocsIndex(paths []string) string {
	seen := make(map[string]bool, len(paths))
	unique := make([]string, 0, len(paths))
	for _, p := range paths {
		norm := filepath.ToSlash(filepath.Clean(strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))))
		if norm == "" || norm == "." || strings.HasPrefix(norm, "/") || strings.HasPrefix(norm, "../") {
			continue
		}
		if strings.EqualFold(filepath.Base(norm), "INDEX.md") {
			continue
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		unique = append(unique, norm)
	}
	sort.Strings(unique)

	var b strings.Builder
	b.WriteString(docsIndexPreamble)
	for _, p := range unique {
		fmt.Fprintf(&b, "- [%s](%s)\n", p, p)
	}
	return b.String()
}

// GenerateDocsIndex writes the generated index to w. The caller owns the
// writer: this layer never opens a file itself (ADR 0005).
func GenerateDocsIndex(w io.Writer, paths []string) error {
	if w == nil {
		return fmt.Errorf("docs index: writer is nil")
	}
	_, err := io.WriteString(w, BuildDocsIndex(paths))
	return err
}

// CheckDocsIndex is the --check half of the generator. It fails when the
// committed index differs from the bytes the current document set produces,
// so a stale list or a hand-edited file is never accepted as current state.
func CheckDocsIndex(current string, paths []string) error {
	want := BuildDocsIndex(paths)
	if current != want {
		return fmt.Errorf("docs index stale: regenerate docs/INDEX.md (want %d bytes, got %d)", len(want), len(current))
	}
	return nil
}

// DocsIndexPaths discovers markdown files under dir in fsys. It skips the
// generated index itself, returns sorted slash-separated paths, and returns an
// empty slice when there is nothing to index: discovery never widens its input
// silently (hd52-empty-index).
func DocsIndexPaths(fsys fs.FS, dir string) ([]string, error) {
	if fsys == nil {
		return nil, fmt.Errorf("docs index: fs is nil")
	}
	root := strings.TrimSpace(strings.ReplaceAll(dir, "\\", "/"))
	if root == "" {
		root = "."
	}
	var paths []string
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		norm := filepath.ToSlash(filepath.Clean(p))
		if strings.EqualFold(filepath.Base(norm), "INDEX.md") {
			return nil
		}
		paths = append(paths, norm)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no docs directory means nothing to index, not a discovery error
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}
