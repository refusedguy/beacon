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
	"path/filepath"
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
	regradeRunner = func(c *CLI, args []string) int { return runRegrade(c, args) }
)

// RegisterInspect installs the inspect command implementation.
func RegisterInspect(fn func(*CLI, []string) int) { inspectRunner = fn }

// RegisterReset installs the reset command implementation.
func RegisterReset(fn func(*CLI, []string) int) { resetRunner = fn }

// RegisterRegrade installs the regrade command implementation.
func RegisterRegrade(fn func(*CLI, []string) int) { regradeRunner = fn }

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
	Ready          *readyExplainView           `json:"ready,omitempty"`
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

// loadReadyExplainForInspect builds the ready-queue explanation from task
// cards. It is a seam so tests can inject a fixed view without touching disk.
var loadReadyExplainForInspect = func() (readyExplainView, error) {
	cfg, err := defaultTaskConfig()
	if err != nil {
		return readyExplainView{}, err
	}
	return buildReadyExplain(cfg.TasksDir)
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
	readyQuery := flags.Bool("ready", false, "показать объяснение ready-очереди карточек")
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
	if *readyQuery {
		ready, err := loadReadyExplainForInspect()
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon inspect: ready: %v\n", err)
			return 2
		}
		view.Ready = &ready
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
	if view.Ready != nil {
		for _, e := range view.Ready {
			fmt.Fprintf(w, "READY %s resolved=%s pinned=%s open=%s\n",
				e.ID, readyDepIDs(e.ResolvedBlockers), readyDepIDs(e.PinnedDependencies), readyDepIDs(e.OpenDependencies))
		}
		for _, e := range view.Blocked {
			fmt.Fprintf(w, "BLOCKED %s resolved=%s pinned=%s open=%s\n",
				e.ID, readyDepIDs(e.ResolvedBlockers), readyDepIDs(e.PinnedDependencies), readyDepIDs(e.OpenDependencies))
		}
	}
	for _, line := range view.Escalations {
		fmt.Fprintf(w, "ESCALATED %s\n", line)
	}
}

// regradeEventType is the journal event kind that carries a saved external
// evidence artifact. Other events in the same Watch journal are never treated
// as evidence.
const regradeEventType = "regrade.evidence"

// readRegradeArtifact reads one artifact file through an fs.FS seam. File
// access stays in a port so cmd/beacon never opens a file directly and tests
// can supply bytes without touching the disk.
var readRegradeArtifact = func(path string) ([]byte, error) {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	return fs.ReadFile(os.DirFS(dir), name)
}

// regradeJournalStore is the journal slice of the state store used by regrade.
// The concrete *store.DB satisfies it; tests can install a fake through
// SetInspectStore as long as it implements these two methods.
type regradeJournalStore interface {
	Append(scope, eventType string, payload []byte) (int64, error)
	ReadAfter(scope string, cursor int64) ([]store.LogEvent, error)
}

// loadRegradeArtifact returns the newest saved artifact for a Watch. A damaged
// artifact of the right type is an error, not a skip: grading an older event
// after a corrupt append would hide the damage.
func loadRegradeArtifact(j regradeJournalStore, watchID string) (domain.RegradeArtifact, bool, error) {
	events, err := j.ReadAfter(watchID, 0)
	if err != nil {
		return domain.RegradeArtifact{}, false, err
	}
	var found domain.RegradeArtifact
	ok := false
	for _, e := range events {
		if e.Type != regradeEventType {
			continue
		}
		var a domain.RegradeArtifact
		if err := json.Unmarshal(e.Payload, &a); err != nil {
			return domain.RegradeArtifact{}, false, err
		}
		found, ok = a, true
	}
	return found, ok, nil
}

// saveRegradeArtifact appends one artifact to the Watch journal. It is the only
// write in this command: the recomputation path below never writes a Watch,
// runs a check or advances a revision.
func saveRegradeArtifact(j regradeJournalStore, a domain.RegradeArtifact) error {
	if strings.TrimSpace(a.WatchID) == "" {
		return errors.New("regrade: artifact has no watch id")
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = j.Append(a.WatchID, regradeEventType, payload)
	return err
}

// printRegradeRefusal prints a stable machine reason. A deleted raw log reads
// as evidence_missing, never as PASS.
func printRegradeRefusal(c *CLI, format, reason, detail string) {
	if format == "json" {
		_ = json.NewEncoder(c.out).Encode(map[string]any{
			"refused": true, "reason": reason, "detail": detail,
		})
		return
	}
	fmt.Fprintf(c.out, "REGRADE_REFUSED reason=%s detail=%s\n", reason, detail)
}

// runRegrade implements `beacon regrade`. With --save it records an artifact
// in the Watch journal; otherwise it recomputes the verdict from the newest
// saved artifact alone. It runs no check and changes no Watch state.
func runRegrade(c *CLI, args []string) int {
	flags := flag.NewFlagSet("regrade", flag.ContinueOnError)
	watchID := flags.String("watch", "", "идентификатор Watch, артефакты которого пересчитываются")
	dataset := flags.String("dataset", "", "набор эталона для переоценки")
	split := flags.String("split", "", "сплит эталона для переоценки")
	declared := flags.String("declared", "", "объявленные проверки через запятую")
	savePath := flags.String("save", "", "путь к JSON-артефакту для сохранения в журнале")
	format := flags.String("format", "table", "формат вывода: table или json")
	jsonAlias := flags.Bool("json", false, "псевдоним --format=json")
	flags.SetOutput(c.errOut)
	flags.Usage = func() {}
	if err := flags.Parse(args); err != nil {
		writeUsage(c.errOut)
		return 2
	}
	if *jsonAlias {
		*format = "json"
	}
	if !parseCommandFormat("regrade", *format, c.errOut, false) {
		return 2
	}

	s, closeStore, err := makeInspectStore(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon regrade: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()
	j, ok := s.(regradeJournalStore)
	if !ok {
		fmt.Fprintln(c.errOut, "beacon regrade: store does not support the artifact journal")
		return 2
	}

	if *savePath != "" {
		raw, err := readRegradeArtifact(*savePath)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon regrade: read artifact: %v\n", err)
			return 2
		}
		var a domain.RegradeArtifact
		if err := json.Unmarshal(raw, &a); err != nil {
			fmt.Fprintf(c.errOut, "beacon regrade: decode artifact: %v\n", err)
			return 2
		}
		if *watchID != "" {
			a.WatchID = *watchID
		}
		if err := saveRegradeArtifact(j, a); err != nil {
			fmt.Fprintf(c.errOut, "beacon regrade: save artifact: %v\n", err)
			return 2
		}
		if *format == "json" {
			_ = json.NewEncoder(c.out).Encode(map[string]any{"saved": true, "watchId": a.WatchID})
		} else {
			fmt.Fprintf(c.out, "SAVED watch=%s revision=%d\n", a.WatchID, a.Revision)
		}
		return 0
	}

	if strings.TrimSpace(*watchID) == "" {
		fmt.Fprintln(c.errOut, "beacon regrade: --watch is required")
		return 2
	}
	a, found, err := loadRegradeArtifact(j, *watchID)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon regrade: load artifact: %v\n", err)
		return 2
	}
	if !found {
		printRegradeRefusal(c, *format, domain.RegradeReasonMissing, "no saved artifact for watch "+*watchID)
		return 1
	}
	ref := domain.RegradeReference{Dataset: *dataset, Split: *split}
	if *declared != "" {
		parts := strings.Split(*declared, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		ref.Declared = parts
	}
	result, err := domain.RegradeWithReference(a, ref)
	if err != nil {
		var refuse *domain.RegradeError
		if errors.As(err, &refuse) {
			printRegradeRefusal(c, *format, refuse.Reason, refuse.Detail)
			return 1
		}
		fmt.Fprintf(c.errOut, "beacon regrade: %v\n", err)
		return 2
	}
	if *format == "json" {
		if err := json.NewEncoder(c.out).Encode(result); err != nil {
			fmt.Fprintf(c.errOut, "beacon regrade: %v\n", err)
			return 2
		}
		return 0
	}
	fmt.Fprintf(c.out, "REGRADE watch=%s revision=%d verdict=%s matches=%t dataset=%s split=%s config=%s\n",
		result.WatchID, result.Revision, result.Evidence.Verdict, result.Matches,
		result.Dataset, result.Split, result.ConfigID)
	return 0
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
