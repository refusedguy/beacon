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
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Verdict values produced by this pass. They are the same strings the domain
// state machine and cibridge use, kept as a mirror for the layer boundary.
type Verdict string

const (
	CheckVerdictWatching Verdict = "watching"
	CheckVerdictReady    Verdict = "ready"
	CheckVerdictRetry    Verdict = "retry"
)

// Config is the project configuration hook. The schema will come from the
// research card (another agent); this stub keeps the call site stable today.
type Config struct{}

// LoadProjectConfig loads project configuration from dir. It is a stub: no
// file is read and no rule is applied until the research card defines the
// schema. dir is accepted so callers can be wired now without a later edit.
func LoadProjectConfig(dir string) (Config, error) {
	_ = dir
	return Config{}, nil
}

// PR is one open pull request reported by gh. Repo is the slug used as the
// Watch alias; Number and HeadSHA identify the exact head being checked.
type PR struct {
	Repo    string
	Number  int
	HeadSHA string
}

// Check is one normalized status check conclusion.
type Check struct {
	Name       string
	Conclusion string
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

// VerdictFromChecks applies cibridge's rules: no checks means still watching,
// a failing or cancelled check asks for retry, all green is ready, and any
// other pending state keeps the Watch watching.
func VerdictFromChecks(checks []Check) Verdict {
	if len(checks) == 0 {
		return CheckVerdictWatching
	}
	allGreen := true
	for _, c := range checks {
		switch c.Conclusion {
		case "SUCCESS":
			continue
		case "FAILURE", "CANCELLED":
			allGreen = false
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

// FailedChecks returns the sorted names of failing or cancelled checks.
func FailedChecks(checks []Check) []string {
	var out []string
	for _, c := range checks {
		if c.Conclusion == "FAILURE" || c.Conclusion == "CANCELLED" {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
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

	raw, err := s.run("api", "graphql", "-f", "query="+BuildChecksQuery(prs))
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
	return Check{Name: name, Conclusion: normalizeConclusion(conclusion)}, true
}

func normalizeConclusion(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PASS", "SUCCESS", "SUCCESSFUL":
		return "SUCCESS"
	case "FAIL", "FAILURE", "ERROR", "TIMED_OUT", "ACTION_REQUIRED":
		return "FAILURE"
	case "CANCELLED", "CANCELED", "SKIPPED":
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
		b.WriteString(`      statusCheckRollup {
        contexts(first:50) {
          nodes {
            __typename
            ... on CheckRun { name conclusion status }
            ... on StatusContext { context state }
          }
        }
      }
`)
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
}

var _ VerdictWriter = (*SQLVerdictWriter)(nil)

func NewSQLVerdictWriter(w TxWriter) *SQLVerdictWriter {
	return &SQLVerdictWriter{writer: w, now: time.Now}
}

func (w *SQLVerdictWriter) Publish(ctx context.Context, ref WatchRef, verdict Verdict, checks []Check) error {
	if w == nil || w.writer == nil {
		return fmt.Errorf("checkall: verdict writer is nil")
	}
	if verdict == CheckVerdictWatching {
		return nil
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
		_ = json.Unmarshal(watch["verdict"], &currentVerdict)
		_ = json.Unmarshal(watch["headSHA"], &currentHead)
		if currentVerdict != string(CheckVerdictWatching) || currentHead != ref.HeadSHA {
			return nil
		}
		var revision uint64
		_ = json.Unmarshal(watch["revision"], &revision)
		nextRevision := revision + 1

		watch["verdict"], _ = json.Marshal(string(verdict))
		watch["status"], _ = json.Marshal("notifying")
		watch["revision"], _ = json.Marshal(nextRevision)
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
		rows[name] = map[string]any{
			"state":      c.Conclusion,
			"conclusion": c.Conclusion,
			"required":   true,
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
