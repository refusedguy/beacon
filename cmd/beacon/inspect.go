package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// inspectRunner and resetRunner are the dispatch seams for the two diagnostic
// commands. Tests replace them like the other command runners.
var (
	inspectRunner = func(c *CLI, args []string) int { return runInspect(c, args) }
	resetRunner   = func(c *CLI, args []string) int { return runReset(c, args) }
)

// RegisterInspect installs the inspect command implementation.
func RegisterInspect(fn func(*CLI, []string) int) { inspectRunner = fn }

// RegisterReset installs the reset command implementation.
func RegisterReset(fn func(*CLI, []string) int) { resetRunner = fn }

// readEscalations reads the escalation journal through a filesystem seam. It
// is the diagnostics port used by both `inspect` and `orchestrator bind`; the
// file is optional and a missing journal is an empty result.
var readEscalations = func(dir string) ([]string, error) {
	raw, err := fs.ReadFile(os.DirFS(dir), "ESCALATIONS.md")
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.TrimPrefix(line, "- "))
	}
	return out, nil
}

// inspectView is the read-only projection returned by `beacon inspect`. Every
// field comes from one store handle or the escalation journal.
type inspectView struct {
	Query          string                      `json:"query,omitempty"`
	Watches        []domain.Watch              `json:"watches"`
	Blockers       []domain.BlockerIncident    `json:"blockers"`
	Orchestrator   *domain.OrchestratorBinding `json:"orchestrator,omitempty"`
	RuntimeStats   json.RawMessage             `json:"runtimeStats,omitempty"`
	GraphReady     *graphReadyView             `json:"graphReady,omitempty"`
	Escalations    []string                    `json:"escalations"`
	DeliveryLadder []deliveryLadderView        `json:"deliveryLadder,omitempty"`
}

// graphReadyView is the typed graph readiness signal lifted from a stored
// stats response. Source explains a negative Ready, so callers do not parse
// the legacy graphReady marker out of loadErrors.
type graphReadyView struct {
	Ready  bool   `json:"ready"`
	Source string `json:"source"`
}

// deliveryLadderView is one pending obligation's delivery marker. It makes the
// wake ladder visible in `beacon inspect` without exposing pointer contents.
type deliveryLadderView struct {
	ObligationID     string    `json:"obligationID"`
	WatchID          string    `json:"watchID"`
	DeliveryRung     int       `json:"deliveryRung"`
	DeliveryAttempts int       `json:"deliveryAttempts"`
	NextRetryAt      time.Time `json:"nextRetryAt,omitempty"`
}

// inspectObligationStore is implemented by stores that can list pending
// obligations. It is a separate interface so the existing inspect store seam
// stays unchanged for callers that only read watches and blockers.
type inspectObligationStore interface {
	PendingObligations() ([]domain.Obligation, error)
}

// inspectStore is the slice of the state store the inspect command needs. The
// concrete *store.DB implements it; tests install a fake with a fixed binding.
type inspectStore interface {
	LiveWatches() ([]domain.Watch, error)
	ActiveBlockers() ([]domain.BlockerIncident, error)
	BoundOrchestrator(projectKey string) (domain.OrchestratorBinding, bool, error)
	LoadRuntimeStats() ([]byte, bool, error)
}

type inspectStoreFactory func(*CLI) (inspectStore, func() error, error)

var makeInspectStore inspectStoreFactory = defaultInspectStore

// SetInspectStore replaces the inspect store factory. Passing nil restores the
// production store.
func SetInspectStore(fn inspectStoreFactory) {
	if fn == nil {
		fn = defaultInspectStore
	}
	makeInspectStore = fn
}

func defaultInspectStore(*CLI) (inspectStore, func() error, error) {
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		return nil, nil, err
	}
	db, ok := deps.Store.(*store.DB)
	if !ok {
		_ = closeStore()
		return nil, nil, errors.New("store is not *store.DB")
	}
	return db, closeStore, nil
}

// runInspect implements `beacon inspect [<pr|watchID>] [--format table|json]`.
// A numeric selector matches the PR number; any other selector matches a Watch id.
func runInspect(c *CLI, args []string) int {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	format := flags.String("format", "table", "формат вывода: table или json")
	jsonAlias := flags.Bool("json", false, "псевдоним --format=json")
	flags.SetOutput(c.errOut)
	flags.Usage = func() {}
	if err := flags.Parse(args); err != nil {
		writeUsage(c.errOut)
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprintf(c.errOut, "beacon inspect: лишние аргументы: %v\n", flags.Args())
		writeUsage(c.errOut)
		return 2
	}
	if *jsonAlias {
		*format = "json"
	}
	if !parseCommandFormat("inspect", *format, c.errOut, false) {
		return 2
	}
	query := flags.Arg(0)

	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon inspect: %v\n", err)
		return 2
	}
	s, closeStore, err := makeInspectStore(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon inspect: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	view := inspectView{
		Query: query, Watches: []domain.Watch{},
		Escalations: []string{}, DeliveryLadder: []deliveryLadderView{},
	}
	live, err := s.LiveWatches()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon inspect: live watches: %v\n", err)
		return 2
	}
	for _, w := range live {
		if matchInspect(query, w) {
			view.Watches = append(view.Watches, w)
		}
	}
	view.Blockers, err = s.ActiveBlockers()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon inspect: blockers: %v\n", err)
		return 2
	}
	if view.Blockers == nil {
		view.Blockers = []domain.BlockerIncident{}
	}
	if binding, bound, err := s.BoundOrchestrator(orchestratorProjectKey()); err != nil {
		fmt.Fprintf(c.errOut, "beacon inspect: orchestrator: %v\n", err)
		return 2
	} else if bound {
		view.Orchestrator = &binding
	}
	if raw, ok, err := s.LoadRuntimeStats(); err != nil {
		fmt.Fprintf(c.errOut, "beacon inspect: runtime_stats: %v\n", err)
		return 2
	} else if ok && json.Valid(raw) {
		view.RuntimeStats = json.RawMessage(raw)
		var stats mcp.StatsOutput
		if err := json.Unmarshal(raw, &stats); err == nil {
			sig := stats.GraphReady()
			view.GraphReady = &graphReadyView{Ready: sig.Ready, Source: string(sig.Source)}
		}
	}
	view.Escalations, err = readEscalations(dir)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon inspect: ESCALATIONS.md: %v\n", err)
		return 2
	}
	if ostore, ok := s.(inspectObligationStore); ok {
		pending, err := ostore.PendingObligations()
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon inspect: obligations: %v\n", err)
			return 2
		}
		for _, o := range pending {
			if query != "" && o.WatchID != query && !strings.Contains(o.WatchID, query) {
				continue
			}
			view.DeliveryLadder = append(view.DeliveryLadder, deliveryLadderView{
				ObligationID:     o.ID,
				WatchID:          o.WatchID,
				DeliveryRung:     o.DeliveryRung,
				DeliveryAttempts: o.DeliveryAttempts,
				NextRetryAt:      o.NextRetryAt,
			})
		}
	}

	if *format == "json" {
		if err := json.NewEncoder(c.out).Encode(view); err != nil {
			fmt.Fprintf(c.errOut, "beacon inspect: %v\n", err)
			return 2
		}
		return 0
	}
	renderInspectTable(c.out, view)
	return 0
}

// matchInspect reports whether w matches the optional selector.
func matchInspect(query string, w domain.Watch) bool {
	if query == "" {
		return true
	}
	if pr, err := strconv.Atoi(query); err == nil {
		return w.PR == pr
	}
	return w.ID == query || strings.Contains(w.ID, query)
}

func renderInspectTable(w io.Writer, view inspectView) {
	if view.Query != "" {
		fmt.Fprintf(w, "QUERY: %s\n", view.Query)
	}
	for _, watch := range view.Watches {
		reason := ""
		if watch.Blocked != nil {
			reason = string(watch.Blocked.Reason)
		}
		fmt.Fprintf(w, "WATCH id=%s pr=%d repo=%s status=%s verdict=%s revision=%d blocked=%s\n",
			watch.ID, watch.PR, watch.Repo, watch.Status, watch.Verdict, watch.Revision, reason)
	}
	for _, b := range view.Blockers {
		fmt.Fprintf(w, "BLOCKER id=%s repo=%s status=%s checks=%s fixAttempts=%d\n",
			b.ID, b.Repo, b.Status, strings.Join(b.FailedChecks, ","), b.FixAttempts)
	}
	for _, d := range view.DeliveryLadder {
		next := "-"
		if !d.NextRetryAt.IsZero() {
			next = d.NextRetryAt.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "DELIVERY obligation=%s watch=%s rung=%d attempts=%d nextRetryAt=%s\n",
			d.ObligationID, d.WatchID, d.DeliveryRung, d.DeliveryAttempts, next)
	}
	if view.Orchestrator != nil {
		fmt.Fprintf(w, "Orchestrator: %s (%s, %s)\n",
			view.Orchestrator.SessionID,
			view.Orchestrator.Adapter,
			view.Orchestrator.BoundAt.Format(time.RFC3339))
	} else {
		fmt.Fprintln(w, "WARNING: orchestrator not bound; run beacon orchestrator bind --session <ses_>")
	}
	if len(view.RuntimeStats) > 0 {
		fmt.Fprintf(w, "RUNTIME_STATS %s\n", string(view.RuntimeStats))
	}
	for _, line := range view.Escalations {
		fmt.Fprintf(w, "ESCALATED %s\n", line)
	}
}

// runReset implements `beacon reset --yes`. It clears the control-plane rows
// of the project store and keeps meta, receipts and the agent registry.
func runReset(c *CLI, args []string) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "подтвердить очистку состояния")
	if !parseFlags("reset", args, c.errOut, fs) {
		return 2
	}
	if !*yes {
		fmt.Fprintln(c.errOut, "beacon reset: нужен --yes для подтверждения")
		return 2
	}
	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon reset: %v\n", err)
		return 2
	}
	db, err := openProcessStore(dir)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon reset: %v\n", err)
		return 2
	}
	defer func() { _ = db.Close() }()
	if err := db.ResetControlPlaneState(context.Background()); err != nil {
		fmt.Fprintf(c.errOut, "beacon reset: %v\n", err)
		return 2
	}
	fmt.Fprintln(c.out, "control-plane state reset")
	return 0
}
