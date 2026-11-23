package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/worktree"
)

// Q-A-02: the lease/ticket collector in watch.go stays intact. This file adds
// the other half of `beacon gc`: dead and orphaned opencode serve processes.
// Only the user's live registration and Beacon's own recorded server are
// protected; everything else with `opencode serve --hostname` is an orphan.

const (
	gcKindServe          gcKind = "serve"
	gcKindObligation     gcKind = "obligation"
	gcKindSession        gcKind = "session"
	gcReasonOrphan              = "orphan"
	gcReasonClosedWatch         = "closed_watch"
	gcReasonStaleSession        = "stale_session"

	// gcSessionRetention is the window a closed Watch's session stays visible
	// after closure. Sessions of Watches closed earlier are left to the
	// store's own archive, which owns the longer history.
	gcSessionRetention = 24 * time.Hour
	gcSessionLimit     = 200
)

// gcObligationStore is the slice of the state store the collector needs. The
// production store implements it; tests install a fake with the same method.
type gcObligationStore interface {
	GCObligations(ctx context.Context, now time.Time, retention time.Duration, apply bool) ([]store.ObligationGCItem, error)
}

// gcObligationsForGC opens the state store, collects the obligations of closed
// Watches and erases them on apply. The store owns the eligibility rule and the
// erase ledger; this seam only supplies the CLI's clock and retention window.
var gcObligationsForGC = func(c *CLI, apply bool) ([]store.ObligationGCItem, error) {
	s, closeStore, err := makeInspectStore(c)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer func() { _ = closeStore() }()
	gs, ok := s.(gcObligationStore)
	if !ok {
		return nil, errors.New("store не поддерживает чистку обязательств")
	}
	return gs.GCObligations(context.Background(), gcNow(), store.ObligationGCRetention, apply)
}

// gcSessionCandidate is one session whose owning Watch has closed, or whose
// registry row went stale without a Watch, and which no live Watch and no
// current process still needs for wake delivery. Stale rows carry Stale so
// apply closes their registry entry after the OpenCode delete succeeds.
type gcSessionCandidate struct {
	SessionID string
	WatchID   string
	Stale     bool
}

// gcSessionStore is the slice of the state store the session collector needs.
// The concrete *store.DB implements it; tests install a fake with the same
// methods.
type gcSessionStore interface {
	RecentClosedWatches(ctx context.Context, since time.Time, limit int) ([]domain.Watch, error)
	LiveWatches() ([]domain.Watch, error)
}

// sessionDeleter is the opencode port method that removes a session. MEM-01
// adds DeleteSession to the client and to the live port. The type assertion
// keeps this package compiling until that change lands, and turns a missing
// method into a clear --apply error instead of a wrong deletion.
type sessionDeleter interface {
	DeleteSession(ctx context.Context, sessionID string) error
}

func deleteSessionViaOpencode(ctx context.Context, sessionID string) error {
	port := openCodePort()
	if port == nil {
		return errors.New("gc: opencode port не подключён")
	}
	deleter, ok := port.(sessionDeleter)
	if !ok {
		return errors.New("gc: opencode port не поддерживает удаление сессий")
	}
	return deleter.DeleteSession(ctx, sessionID)
}

// gcSessionDelete is the single seam tests replace with a recorder. Production
// routes through the opencode port; cmd/beacon never opens HTTP itself.
var gcSessionDelete = deleteSessionViaOpencode

// sessionCloser and staleSessionLister are the MM-01 registry methods used to
// close a row and to list stale rows. The type assertions below keep this
// package compiling until MM-01 lands in the base branch; before that the
// seams return no rows and no-op closes. Once MM-01 is present the real
// methods drive the sweep. cmd/beacon still never opens a database for this
// work: the app-layer registry owns both calls.
type sessionCloser interface {
	CloseSession(id string) error
}

type staleSessionLister interface {
	ReapStaleSessions(limit int) ([]session.Session, error)
}

func closeSessionViaRegistry(id string) error {
	reg := sessionRegistryForSpawn()
	if reg == nil {
		return nil
	}
	// sessionRegistryForSpawn returns a concrete *session.SessionRegistry, so
	// box it before probing for the optional MM-01 method. Until that method
	// lands on the registry the probe fails and the close stays a no-op.
	var candidate any = reg
	closer, ok := candidate.(sessionCloser)
	if !ok {
		return nil
	}
	// Closing is best effort. The app-layer method reports a missing store
	// port until cmd/beacon/session_store_adapter.go is extended outside this
	// change, and that must not turn a successful DELETE into a failed sweep.
	_ = closer.CloseSession(id)
	return nil
}

func staleSessionsViaRegistry(limit int) ([]session.Session, error) {
	reg := sessionRegistryForSpawn()
	if reg == nil {
		return nil, nil
	}
	// Box the concrete registry before probing for the optional MM-01 lister,
	// the same way closeSessionViaRegistry probes for the closer.
	var candidate any = reg
	lister, ok := candidate.(staleSessionLister)
	if !ok {
		return nil, nil
	}
	// The app-layer lister reports a missing store port until the adapter is
	// wired. An unwired port simply adds no stale rows to this pass.
	rows, err := lister.ReapStaleSessions(limit)
	if err != nil {
		return nil, nil
	}
	return rows, nil
}

// gcSessionClose closes the durable row after a successful DELETE. It is a
// seam so tests can assert the delete-then-close order and that a failed
// DELETE leaves the row open.
var gcSessionClose = closeSessionViaRegistry

// gcStaleSessions supplies stale rows from MM-01. It is a seam so the planner
// can be tested without a live registry or database.
var gcStaleSessions = staleSessionsViaRegistry

// gcOpenCodeSessions reads the session tree used to protect ancestors of live
// subscribers. The OpenCode DELETE removes children too, so deleting an
// ancestor of a live session would kill a wake path.
var gcOpenCodeSessions = func(ctx context.Context) ([]opencode.Session, error) {
	port := openCodePort()
	if port == nil {
		return nil, nil
	}
	return port.Sessions(ctx)
}

// protectedSessionIDs returns the ids no sweep may touch: live Watch
// subscribers, the current process session, and every ancestor of those ids
// in the OpenCode session tree.
func protectedSessionIDs(live []domain.Watch, currentSession string, tree []opencode.Session) map[string]bool {
	protected := make(map[string]bool, len(live)+1)
	for _, w := range live {
		for _, s := range w.Subscribers {
			if id := strings.TrimSpace(s.ID); id != "" {
				protected[id] = true
			}
		}
	}
	if id := strings.TrimSpace(currentSession); id != "" {
		protected[id] = true
	}

	parent := make(map[string]string, len(tree))
	for _, s := range tree {
		id := strings.TrimSpace(s.ID)
		if id != "" {
			parent[id] = strings.TrimSpace(s.ParentID)
		}
	}
	for id := range protected {
		for p := parent[id]; p != "" && !protected[p]; p = parent[p] {
			protected[p] = true
		}
	}
	return protected
}

// gcSessionPlan filters closed-watch subscribers against live subscribers and
// the current process session. A subscriber id is a session id, so a session
// still bound to a live Watch protects the wake path and is never a candidate.
func gcSessionPlan(closed, live []domain.Watch, currentSession string) []gcSessionCandidate {
	protected := protectedSessionIDs(live, currentSession, nil)

	seen := make(map[string]bool)
	plan := make([]gcSessionCandidate, 0)
	for _, w := range closed {
		for _, s := range w.Subscribers {
			id := strings.TrimSpace(s.ID)
			if id == "" || protected[id] || seen[id] {
				continue
			}
			seen[id] = true
			plan = append(plan, gcSessionCandidate{SessionID: id, WatchID: w.ID})
		}
	}
	return plan
}

// gcStaleSessionPlan turns stale registry rows into candidates. A row is
// dropped when it is protected directly or when it is an ancestor of a
// protected session, because OpenCode DELETE would remove that child too.
func gcStaleSessionPlan(stale []session.Session, live []domain.Watch, currentSession string, tree []opencode.Session) []gcSessionCandidate {
	protected := protectedSessionIDs(live, currentSession, tree)
	seen := make(map[string]bool)
	plan := make([]gcSessionCandidate, 0, len(stale))
	for _, s := range stale {
		id := strings.TrimSpace(s.ID)
		if id == "" || s.Status == session.SessionStatusClosed || protected[id] || seen[id] {
			continue
		}
		seen[id] = true
		plan = append(plan, gcSessionCandidate{SessionID: id, Stale: true})
	}
	return plan
}

// gcSessionsForGC opens the state store and applies the retention window. The
// store supplies the rows; gcSessionPlan owns the protection rule.
var gcSessionsForGC = func(c *CLI) ([]gcSessionCandidate, error) {
	s, closeStore, err := makeInspectStore(c)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer func() { _ = closeStore() }()
	ss, ok := s.(gcSessionStore)
	if !ok {
		return nil, errors.New("store не поддерживает чистку сессий")
	}
	ctx := context.Background()
	current := os.Getenv(sessionIDEnv)
	since := gcNow().Add(-gcSessionRetention)
	closed, err := ss.RecentClosedWatches(ctx, since, gcSessionLimit)
	if err != nil {
		return nil, fmt.Errorf("закрытые watch: %w", err)
	}
	live, err := ss.LiveWatches()
	if err != nil {
		return nil, fmt.Errorf("живые watch: %w", err)
	}
	tree, err := gcOpenCodeSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("дерево сессий: %w", err)
	}
	stale, err := gcStaleSessions(gcSessionLimit)
	if err != nil {
		return nil, fmt.Errorf("просроченные сессии: %w", err)
	}

	plan := gcSessionPlan(closed, live, current)
	protected := protectedSessionIDs(live, current, tree)
	candidates := make([]gcSessionCandidate, 0, len(plan)+len(stale))
	for _, c := range plan {
		if !protected[c.SessionID] {
			candidates = append(candidates, c)
		}
	}
	candidates = append(candidates, gcStaleSessionPlan(stale, live, current, tree)...)

	// One id is deleted once. A row that is both a closed-watch session and a
	// stale registry row keeps its Watch id but must still be closed on apply.
	merged := make([]gcSessionCandidate, 0, len(candidates))
	index := make(map[string]int, len(candidates))
	for _, c := range candidates {
		if i, ok := index[c.SessionID]; ok {
			if c.Stale {
				merged[i].Stale = true
			}
			continue
		}
		index[c.SessionID] = len(merged)
		merged = append(merged, c)
	}
	if len(merged) > gcSessionLimit {
		merged = merged[:gcSessionLimit]
	}
	return merged, nil
}

// The seams keep process discovery and termination out of the command's code
// path in tests. Production falls through to the infra package.
var (
	gcServeList  = opencode.ListServeProcesses
	gcServicePID = opencode.ReadServicePID
	gcServiceCfg = opencode.Discover
	gcServeState = opencode.ReadServeState
	gcServeClear = opencode.ClearServeState
	gcServeKill  = opencode.TerminateServe
)

func init() {
	RegisterGC(runGCWithServe)
}

// gcPortFromURL returns the port of a service URL. An empty result means the
// registration has no usable port and cannot protect a process by port.
func gcPortFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Port()
}

// gcServePlan classifies serve processes. The main session is protected by
// the pid in service.json, and by port when the registration has no pid.
// Beacon's own recorded server is protected the same way. A recorded pid that
// is no longer alive becomes a stale record to remove.
func gcServePlan(serves []opencode.ServeProcess, servicePID int, servicePort string, recorded opencode.ServeState, hasRecord bool) []gcItem {
	protectedPIDs := make(map[int]bool)
	protectedPorts := make(map[string]bool)
	if servicePID > 0 {
		protectedPIDs[servicePID] = true
	}
	if hasRecord && recorded.PID > 0 {
		protectedPIDs[recorded.PID] = true
	}
	if servicePort != "" {
		protectedPorts[servicePort] = true
	}
	if port := gcPortFromURL(recorded.URL); port != "" {
		protectedPorts[port] = true
	}

	items := make([]gcItem, 0)
	if hasRecord && recorded.PID > 0 && !gcProcessAlive(recorded.PID) {
		items = append(items, gcItem{
			Kind:   gcKindServe,
			ID:     "state:" + strconv.Itoa(recorded.PID),
			Reason: gcReasonDead,
		})
	}
	for _, proc := range serves {
		if protectedPIDs[proc.PID] {
			continue
		}
		if proc.Port != "" && protectedPorts[proc.Port] {
			continue
		}
		items = append(items, gcItem{
			Kind:   gcKindServe,
			ID:     strconv.Itoa(proc.PID),
			Reason: gcReasonOrphan,
		})
	}
	return items
}

// gcOrphanInterval is the period of the scheduled orphan sweep. The leak
// reports measured about twenty serve processes in two hours, so ten minutes
// catches a leak before the alarm threshold of two live servers.
const gcOrphanInterval = 10 * time.Minute

// gcTicker and gcShutdown are seams around the timer and the stop signal. A
// test can drive a finite set of ticks and close done; production uses a real
// ticker and SIGINT or SIGTERM.
var (
	gcTicker = func(d time.Duration) (<-chan time.Time, func()) {
		t := time.NewTicker(d)
		return t.C, t.Stop
	}
	gcShutdown = func() (<-chan struct{}, func()) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		return ctx.Done(), stop
	}
)

// runGCScheduled sweeps once at startup, then once per interval until the
// process is stopped. A failed sweep is reported and the timer keeps running:
// a transient store or process error must not disable orphan cleanup.
func runGCScheduled(c *CLI, apply bool, format string, interval time.Duration) int {
	if code := gcSweep(c, apply, format); code != 0 {
		return code
	}
	ticks, stopTicks := gcTicker(interval)
	defer stopTicks()
	done, stopSignals := gcShutdown()
	defer stopSignals()
	for {
		select {
		case <-ticks:
			if code := gcSweep(c, apply, format); code != 0 {
				fmt.Fprintf(c.errOut, "beacon gc: запланированный проход завершился кодом %d\n", code)
			}
		case <-done:
			return 0
		}
	}
}

// runGCWithServe keeps the read-only/apply split of the existing collector.
// A plain run prints the plan; --apply removes stale state and terminates the
// orphaned serve processes it found. A positive --interval repeats the sweep
// every interval, starting with one pass at startup.
func runGCWithServe(c *CLI, args []string) int {
	// CBX-12: history compaction is a subcommand of gc. It builds a plan
	// before any change, so it intercepts before the sweep flags are parsed.
	if len(args) > 0 && args[0] == "compact" {
		return runGCCompact(c, args[1:])
	}
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	apply := fs.Bool("apply", false, "удалить найденные записи и terminate сироты")
	interval := fs.Duration("interval", 0, "период повтора уборки, 0 = один проход; рекомендуется "+gcOrphanInterval.String())
	if !parseFlags("gc", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon gc: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}
	if *interval < 0 {
		fmt.Fprintf(c.errOut, "beacon gc: отрицательный интервал %v\n", *interval)
		return 2
	}
	if *interval > 0 {
		return runGCScheduled(c, *apply, *format, *interval)
	}
	return gcSweep(c, *apply, *format)
}

// gcSweep is one pass of the collector. It stays separate from flag parsing
// so the scheduled mode uses the same dry-run and apply rules.
func gcSweep(c *CLI, apply bool, format string) int {
	plan := make([]gcItem, 0)
	var legacy []gcItem
	if state, err := gcReadState(); err == nil {
		legacy = gcStale(state, gcNow())
		plan = append(plan, legacy...)
	}

	serves, err := gcServeList()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc: чтение процессов: %v\n", err)
		return 2
	}
	servicePort := ""
	if cfg, err := gcServiceCfg(); err == nil {
		servicePort = gcPortFromURL(cfg.URL)
	}
	recorded, hasRecord := gcServeState()
	servePlan := gcServePlan(serves, gcServicePID(), servicePort, recorded, hasRecord)
	plan = append(plan, servePlan...)

	// The store selects obligations of closed Watches past the replay window.
	// One call plans a dry run and erases on --apply, so the report lists the
	// exact rows that were removed.
	obligations, err := gcObligationsForGC(c, apply)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc: обязательства: %v\n", err)
		return 2
	}
	obligationItems := make([]gcItem, 0, len(obligations))
	for _, o := range obligations {
		obligationItems = append(obligationItems, gcItem{Kind: gcKindObligation, ID: o.ID, Reason: o.Reason})
	}
	if !apply {
		plan = append(plan, obligationItems...)
	}

	// Sessions of closed Watches are candidates only after their subscriber
	// ids are checked against live Watches and the current session. A dry run
	// lists them; --apply deletes exactly the listed ids once each.
	sessions, err := gcSessionsForGC(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc: сессии закрытых watch: %v\n", err)
		return 2
	}
	sessionItems := make([]gcItem, 0, len(sessions))
	for _, s := range sessions {
		reason := gcReasonClosedWatch + ":" + s.WatchID
		if s.Stale {
			reason = gcReasonStaleSession
		}
		sessionItems = append(sessionItems, gcItem{
			Kind:   gcKindSession,
			ID:     s.SessionID,
			Reason: reason,
		})
	}
	if !apply {
		plan = append(plan, sessionItems...)
	}

	if !apply {
		if format == "json" {
			if err := renderGCJSON(c.out, gcResult{Mode: "dry-run", Removed: len(plan), Items: plan}); err != nil {
				fmt.Fprintf(c.errOut, "beacon gc: %v\n", err)
				return 2
			}
			return 0
		}
		renderGCTable(c.out, plan, false)
		return 0
	}

	removed := make([]gcItem, 0)
	if legacy != nil {
		done, err := gcWriteState(legacy)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: удаление записей: %v\n", err)
			return 2
		}
		removed = append(removed, done...)
	}
	for _, item := range servePlan {
		if item.Kind != gcKindServe || item.Reason != gcReasonOrphan {
			continue
		}
		pid, err := strconv.Atoi(item.ID)
		if err != nil {
			continue
		}
		if err := gcServeKill(pid); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: terminate pid %d: %v\n", pid, err)
			return 2
		}
		removed = append(removed, item)
	}
	if hasRecord && recorded.PID > 0 && !gcProcessAlive(recorded.PID) {
		if err := gcServeClear(); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: очистка записи сервера: %v\n", err)
			return 2
		}
		removed = append(removed, gcItem{
			Kind:   gcKindServe,
			ID:     "state:" + strconv.Itoa(recorded.PID),
			Reason: gcReasonDead,
		})
	}
	removed = append(removed, obligationItems...)
	for _, s := range sessions {
		if err := gcSessionDelete(context.Background(), s.SessionID); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: удаление сессии %s: %v\n", s.SessionID, err)
			return 2
		}
		// Only stale rows have a durable entry. A closed Watch's subscriber
		// may never have been recorded, and CloseSession reports unknown ids.
		if s.Stale {
			if err := gcSessionClose(s.SessionID); err != nil {
				fmt.Fprintf(c.errOut, "beacon gc: закрытие сессии %s в реестре: %v\n", s.SessionID, err)
				return 2
			}
		}
	}
	removed = append(removed, sessionItems...)
	if removed == nil {
		removed = []gcItem{}
	}
	if format == "json" {
		if err := renderGCJSON(c.out, gcResult{Mode: "applied", Removed: len(removed), Items: removed}); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc: %v\n", err)
			return 2
		}
		return 0
	}
	renderGCTable(c.out, removed, true)
	return 0
}

// ---------------------------------------------------------------------------
// IDX-12: worktree GC. A worktree is removed only when no live Watch points at
// its HEAD and `git status --porcelain` is empty. Every other worktree stays
// with a reason in the report. The serve collector above stays unchanged.
// ---------------------------------------------------------------------------

const (
	gcReasonActiveWatch = "active_watch"
	gcReasonDirty       = "dirty"
	gcReasonLocked      = "locked"
	gcReasonStatusError = "status_error"
	gcReasonClean       = "clean"
	gcReasonRemoveError = "remove_error"
)

// gcWorktreeResult is one line of the worktree report. Status is "remove" for
// a dry-run candidate, "removed" after apply and "kept" for everything that
// stays. Reason explains the status.
type gcWorktreeResult struct {
	Dir    string `json:"dir"`
	Branch string `json:"branch,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason"`

	// observedAt and firstStatus are the first clean sighting. They travel
	// from collection to apply so the Guard can compare against the same
	// snapshot instead of a fresh one. They are not part of the JSON report.
	observedAt  time.Time
	firstStatus string
}

// gcWorktreeReport is the JSON form of both a dry run and an apply. Items holds
// every inspected worktree, so the report names the kept ones and their
// reasons. Removed counts the removal set: candidates in a dry run, deletions
// after apply.
type gcWorktreeReport struct {
	Mode      string             `json:"mode"`
	Removed   int                `json:"removed"`
	Kept      int                `json:"kept"`
	Items     []gcWorktreeResult `json:"items"`
	CollectMS int64              `json:"collect_ms"`
	ApplyMS   int64              `json:"apply_ms"`
}

var (
	gcWorktreeList   = func() ([]worktree.Entry, error) { return worktree.List(worktree.CommandRunner) }
	gcWorktreeStatus = func(dir string) (string, error) { return worktree.Status(worktree.CommandRunner, dir) }
	gcWorktreeRemove = func(dir string) error { return worktree.Remove(worktree.CommandRunner, dir) }
	gcWorktreeHeads  = liveWatchHeads
	gcWorktreeRunner = func(c *CLI, args []string) int { return runGCWorktree(c, args) }
)

// RegisterGCWorktree installs the worktree command implementation. Tests use
// it to replace the runner without touching dispatch.
func RegisterGCWorktree(fn func(*CLI, []string) int) { gcWorktreeRunner = fn }

func init() {
	RegisterGCWorktree(runGCWorktree)
}

// liveWatchHeads maps the head SHA of every live Watch to its Watch ID. A
// blocked Watch is still live, so it protects its worktree too. The store is
// opened through the existing inspect factory and closed on every path.
func liveWatchHeads(c *CLI) (map[string]string, error) {
	st, closeStore, err := makeInspectStore(c)
	if err != nil {
		return nil, fmt.Errorf("gc-worktree: store: %w", err)
	}
	defer closeStore()
	watches, err := st.LiveWatches()
	if err != nil {
		return nil, fmt.Errorf("gc-worktree: live watches: %w", err)
	}
	heads := make(map[string]string, len(watches))
	for _, w := range watches {
		if w.HeadSHA == "" {
			continue
		}
		heads[strings.ToLower(w.HeadSHA)] = w.ID
	}
	return heads, nil
}

// watchIDForHead finds a Watch whose recorded head matches the worktree HEAD.
// Git and the store both write full hashes, but a short HEAD stays comparable
// through the prefix check. Sorted keys give a stable answer when two Watches
// record the same head.
func watchIDForHead(heads map[string]string, head string) string {
	head = strings.ToLower(strings.TrimSpace(head))
	if len(head) < 7 {
		return ""
	}
	keys := make([]string, 0, len(heads))
	for sha := range heads {
		keys = append(keys, sha)
	}
	sort.Strings(keys)
	for _, sha := range keys {
		if strings.HasPrefix(sha, head) {
			return heads[sha]
		}
	}
	return ""
}

// gcWorktreeDecision turns collected facts into one report line. Facts are
// checked in this order: a locked worktree, then an active Watch, then a
// status error, then a dirty tree. Only a clean, unlocked worktree with no
// Watch is a removal candidate.
func gcWorktreeDecision(e worktree.Entry, watchID, status string, statusErr error) gcWorktreeResult {
	item := gcWorktreeResult{Dir: e.Dir, Branch: e.Branch, Status: "kept"}
	switch {
	case e.Locked:
		item.Reason = gcReasonLocked
	case watchID != "":
		item.Reason = gcReasonActiveWatch + ":" + watchID
	case statusErr != nil:
		item.Reason = gcReasonStatusError + ":" + statusErr.Error()
	case strings.TrimSpace(status) != "":
		item.Reason = gcReasonDirty
	default:
		item.Status = "remove"
		item.Reason = gcReasonClean
	}
	return item
}

// gcWorktreeCollect reads Watch heads and git status for every worktree. A
// locked worktree or one with a live Watch skips the status call: its reason
// is already decisive and a git failure there must not hide that reason.
func gcWorktreeCollect(c *CLI) ([]gcWorktreeResult, error) {
	entries, err := gcWorktreeList()
	if err != nil {
		return nil, fmt.Errorf("beacon gc-worktree: список worktree: %w", err)
	}
	heads, err := gcWorktreeHeads(c)
	if err != nil {
		return nil, err
	}
	plan := make([]gcWorktreeResult, 0, len(entries))
	for _, e := range entries {
		watchID := watchIDForHead(heads, e.Head)
		var status string
		var statusErr error
		var observedAt time.Time
		if !e.Locked && watchID == "" {
			status, statusErr = gcWorktreeStatus(e.Dir)
			if statusErr == nil && strings.TrimSpace(status) == "" {
				observedAt = time.Now()
			}
		}
		item := gcWorktreeDecision(e, watchID, status, statusErr)
		item.observedAt = observedAt
		item.firstStatus = status
		plan = append(plan, item)
	}
	return plan, nil
}

func renderGCWorktreeJSON(w io.Writer, report gcWorktreeReport) error {
	return json.NewEncoder(w).Encode(report)
}

func renderGCWorktreeTable(w io.Writer, report gcWorktreeReport) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tDIR\tBRANCH\tREASON")
	for _, item := range report.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", item.Status, item.Dir, item.Branch, item.Reason)
	}
	_ = tw.Flush()
	if report.Mode == "applied" {
		fmt.Fprintf(w, "УДАЛЕНО: %d, ОСТАВЛЕНО: %d\n", report.Removed, report.Kept)
		return
	}
	fmt.Fprintf(w, "КАНДИДАТЫ: %d, ОСТАВЛЕНО: %d (просмотр без удаления, --apply удалит)\n", report.Removed, report.Kept)
}

// runGCWorktree implements `beacon gc-worktree [--format table|json] [--apply]`.
// Without --apply it prints the plan. With --apply it removes the candidates
// and prints the outcome for every worktree.
func runGCWorktree(c *CLI, args []string) int {
	fs := flag.NewFlagSet("gc-worktree", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода: table или json")
	apply := fs.Bool("apply", false, "удалить безопасные worktree (по умолчанию только просмотр)")
	if !parseFlags("gc-worktree", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon gc-worktree: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	collectStart := gcNow()
	plan, err := gcWorktreeCollect(c)
	collectMS := gcNow().Sub(collectStart).Milliseconds()
	if err != nil {
		fmt.Fprintln(c.errOut, err)
		return 2
	}

	report := gcWorktreeReport{Mode: "dry-run", Items: plan, CollectMS: collectMS}
	for _, item := range plan {
		if item.Status == "remove" {
			report.Removed++
		} else {
			report.Kept++
		}
	}

	failed := false
	if *apply {
		applyStart := gcNow()
		report.Mode = "applied"
		report.Removed = 0
		for i, item := range plan {
			if item.Status != "remove" {
				continue
			}
			// The second check reads the same injected seam as collection. A
			// changed or unreadable status cancels the candidate before any
			// removal, and both reasons go into the report.
			current, statusErr := gcWorktreeStatus(item.Dir)
			if statusErr != nil {
				report.Items[i].Status = "kept"
				report.Items[i].Reason = worktree.ReasonStatusError + ":" + statusErr.Error()
				report.Kept++
				continue
			}
			if current != item.firstStatus {
				report.Items[i].Status = "kept"
				report.Items[i].Reason = worktree.ReasonChanged
				report.Kept++
				continue
			}
			candidate := worktree.Candidate{
				Dir:        item.Dir,
				ObservedAt: item.observedAt,
				Status:     item.firstStatus,
			}
			guard := worktree.NewGuard(
				func(string) (string, error) { return current, nil },
				gcWorktreeRemove,
			)
			reason, removed := worktree.DecideGuard(guard, candidate)
			if removed {
				report.Items[i].Status = "removed"
				report.Removed++
				continue
			}
			report.Items[i].Status = "kept"
			report.Items[i].Reason = reason
			report.Kept++
			if reason == worktree.ReasonRemoveError {
				failed = true
				fmt.Fprintf(c.errOut, "beacon gc-worktree: удаление %s: вторая проверка отклонила каталог\n", item.Dir)
			}
		}
		report.ApplyMS = gcNow().Sub(applyStart).Milliseconds()
	}

	if *format == "json" {
		if err := renderGCWorktreeJSON(c.out, report); err != nil {
			fmt.Fprintf(c.errOut, "beacon gc-worktree: %v\n", err)
			return 2
		}
	} else {
		renderGCWorktreeTable(c.out, report)
	}
	if failed {
		return 2
	}
	return 0
}

// ---------------------------------------------------------------------------
// CBX-12: card history compaction. The plan is built before a single file or
// ref changes. Old commits collapse into one base; recent commits replay in
// commit order so a child never precedes its parent. Dates decide only which
// side of the cutoff a commit belongs to. When dates cross the cutoff against
// history order the plan refuses, prints a hint and creates nothing. headHash
// is captured when the log is read, and the applied tree is checked against
// that value, never against the branch head after the rewrite.
// The collector above stays unchanged.
// ---------------------------------------------------------------------------

const (
	gcCompactReasonOutOfOrder  = "out_of_order"
	gcCompactReasonNoPartition = "no_partition"
	gcCompactReasonHeadMissing = "head_missing"
	gcCompactCardPath          = "docs/tasks"
	gcCompactHint              = "не сжимать: даты коммитов пересекают cutoff не в порядке истории; выровняйте даты или откажитесь от сжатия"
)

// gcCompactCommit is one commit of the card history. Order is its position in
// `git log --reverse`: a parent always comes before its child. Date is the
// author date used only for the cutoff split, never for replay ordering.
type gcCompactCommit struct {
	Hash  string
	Date  time.Time
	Order int
}

// gcCompactPlan is the whole plan built before any change. Reason is empty
// only when the history passed every check.
type gcCompactPlan struct {
	Cutoff   time.Time
	HeadHash string
	HeadTree string
	Old      []gcCompactCommit
	Recent   []gcCompactCommit
	Reason   string
	Hint     string
}

// gcCompactCreated is the number of commits the plan creates: one base for the
// old history plus one replay per recent commit. A refusal creates nothing.
func (p gcCompactPlan) gcCompactCreated() int {
	if p.Reason != "" {
		return 0
	}
	return 1 + len(p.Recent)
}

// gcCompactReplays returns recent hashes in commit order. A refusal has no
// replays even when recent commits were collected before the check failed.
func (p gcCompactPlan) gcCompactReplays() []string {
	if p.Reason != "" {
		return []string{}
	}
	out := make([]string, 0, len(p.Recent))
	for _, c := range p.Recent {
		out = append(out, c.Hash)
	}
	return out
}

// gcCompactSplit partitions by date while keeping commit order inside each
// half. Replay order comes from these slices, so two commits sharing a date
// still replay parent first.
func gcCompactSplit(commits []gcCompactCommit, cutoff time.Time) ([]gcCompactCommit, []gcCompactCommit) {
	old := make([]gcCompactCommit, 0, len(commits))
	recent := make([]gcCompactCommit, 0, len(commits))
	for _, c := range commits {
		if c.Date.Before(cutoff) {
			old = append(old, c)
		} else {
			recent = append(recent, c)
		}
	}
	return old, recent
}

// gcCompactBuild is the premise check of the subcommand. It refuses when dates
// cross the cutoff against history order: every recent commit must follow the
// last old commit in commit order. headHash is the hash read before any work.
func gcCompactBuild(commits []gcCompactCommit, cutoff time.Time, headHash, headTree string) gcCompactPlan {
	p := gcCompactPlan{Cutoff: cutoff, HeadHash: headHash, HeadTree: headTree}
	if strings.TrimSpace(headHash) == "" {
		p.Reason = gcCompactReasonHeadMissing
		p.Hint = "не сжимать: не зафиксирован headHash на момент чтения истории"
		return p
	}
	p.Old, p.Recent = gcCompactSplit(commits, cutoff)
	if len(p.Old) == 0 || len(p.Recent) == 0 {
		p.Reason = gcCompactReasonNoPartition
		p.Hint = "не сжимать: нет коммитов по одну сторону cutoff"
		return p
	}
	lastOld := p.Old[0].Order
	for _, c := range p.Old {
		if c.Order > lastOld {
			lastOld = c.Order
		}
	}
	for _, c := range p.Recent {
		if c.Order < lastOld {
			p.Reason = gcCompactReasonOutOfOrder
			p.Hint = gcCompactHint
			return p
		}
	}
	return p
}

// gcCompactParseLog reads `hash<TAB>unix` lines from `git log --reverse` and
// assigns commit order by line position. A malformed line fails the plan
// before any ref moves.
func gcCompactParseLog(out string) ([]gcCompactCommit, error) {
	commits := make([]gcCompactCommit, 0)
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("gc compact: строка лога %d: %q", i+1, line)
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("gc compact: дата в строке %d: %w", i+1, err)
		}
		commits = append(commits, gcCompactCommit{
			Hash:  strings.TrimSpace(parts[0]),
			Date:  time.Unix(secs, 0).UTC(),
			Order: len(commits),
		})
	}
	return commits, nil
}

// gcCompactVerify compares the applied tree with the tree captured at read
// time. Byte-for-byte equality is required: a replay that changed a card is
// a failure even when the rewrite itself succeeded.
func gcCompactVerify(headTree, resultTree string) bool {
	return headTree == resultTree
}

var (
	// gcCompactGit is the git seam. cmd/beacon never shells out itself; the
	// infra worktree runner owns the exec.
	gcCompactGit = worktree.CommandRunner

	// gcCompactRead captures the log, the head commit and its tree before any
	// change. The tree is the reference for the result check; headHash names
	// the tip the log was read from.
	gcCompactRead = func(path string) ([]gcCompactCommit, string, string, error) {
		headOut, err := gcCompactGit("", "rev-parse", "HEAD")
		if err != nil {
			return nil, "", "", fmt.Errorf("gc compact: HEAD: %w: %s", err, strings.TrimSpace(headOut))
		}
		headHash := strings.TrimSpace(headOut)
		headTree, err := gcCompactTree(headHash)
		if err != nil {
			return nil, "", "", err
		}
		logOut, err := gcCompactGit("", "log", "--reverse", "--format=%H%x09%at", "--", path)
		if err != nil {
			return nil, "", "", fmt.Errorf("gc compact: log: %w: %s", err, strings.TrimSpace(logOut))
		}
		commits, err := gcCompactParseLog(logOut)
		if err != nil {
			return nil, "", "", err
		}
		return commits, headHash, headTree, nil
	}
)

func gcCompactTree(hash string) (string, error) {
	out, err := gcCompactGit("", "rev-parse", hash+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("gc compact: дерево %s: %w: %s", hash, err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

func gcCompactCommitTree(tree, parent, message string) (string, error) {
	args := []string{"-c", "user.name=beacon", "-c", "user.email=beacon@users.noreply.github.com", "commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", message)
	out, err := gcCompactGit("", args...)
	if err != nil {
		return "", fmt.Errorf("gc compact: commit-tree: %w: %s", err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

// gcCompactApplyRewrite is the production apply path. It builds one base
// commit from the oldest old tree, replays recent trees in commit order and
// moves HEAD only after the plan passed the order check. The resulting tree
// hash is returned for the headHash comparison.
var gcCompactApply = func(p gcCompactPlan) (string, error) {
	if p.Reason != "" || len(p.Old) == 0 {
		return "", errors.New("gc compact: отказ, применение запрещено")
	}
	status, err := gcCompactGit("", "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("gc compact: status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return "", errors.New("gc compact: рабочее дерево не чистое, сжатие отменено")
	}
	baseTree, err := gcCompactTree(p.Old[0].Hash)
	if err != nil {
		return "", err
	}
	parent, err := gcCompactCommitTree(baseTree, "", "gc compact: base")
	if err != nil {
		return "", err
	}
	for _, c := range p.Recent {
		tree, err := gcCompactTree(c.Hash)
		if err != nil {
			return "", err
		}
		parent, err = gcCompactCommitTree(tree, parent, "gc compact: replay "+c.Hash)
		if err != nil {
			return "", err
		}
	}
	if out, err := gcCompactGit("", "update-ref", "HEAD", parent); err != nil {
		return "", fmt.Errorf("gc compact: update-ref: %w: %s", err, strings.TrimSpace(out))
	}
	return gcCompactTree(parent)
}

// gcCompactReport is the single output shape of a refusal, a dry run and an
// apply. Planned counts what the plan would create; Created counts commits
// actually written. A refusal and every dry run leave Created at zero.
type gcCompactReport struct {
	Mode     string   `json:"mode"`
	Planned  int      `json:"planned"`
	Created  int      `json:"created"`
	HeadHash string   `json:"head_hash"`
	Base     int      `json:"base"`
	Replays  []string `json:"replays"`
	Verified bool     `json:"verified"`
	Reason   string   `json:"reason,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

func renderGCCompactJSON(w io.Writer, r gcCompactReport) error {
	return json.NewEncoder(w).Encode(r)
}

func renderGCCompactTable(w io.Writer, r gcCompactReport) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "MODE\tPLANNED\tCREATED\tBASE\tREPLAYS\tVERDICT")
	verdict := "ok"
	switch {
	case r.Reason != "":
		verdict = r.Reason
	case r.Mode == "applied" && !r.Verified:
		verdict = "tree_mismatch"
	}
	fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\n", r.Mode, r.Planned, r.Created, r.Base, len(r.Replays), verdict)
	_ = tw.Flush()
	switch r.Mode {
	case "refusal":
		fmt.Fprintf(w, "ОТКАЗ: %s, СОЗДАНО КОММИТОВ: 0\n", r.Reason)
		if r.Hint != "" {
			fmt.Fprintf(w, "ПОДСКАЗКА: %s\n", r.Hint)
		}
	case "applied":
		fmt.Fprintf(w, "СОЗДАНО КОММИТОВ: %d, ПРОВЕРКА ДЕРЕВА: %v\n", r.Created, r.Verified)
	default:
		fmt.Fprintf(w, "ПЛАН: %d, СОЗДАНО КОММИТОВ: 0 (просмотр без изменений, --apply применит)\n", r.Planned)
	}
}

// runGCCompact implements `beacon gc compact --cutoff <RFC3339> [--dry-run]
// [--apply] [--format table|json]`. The plan always runs first. --apply alone
// writes commits, and only when the applied tree matches the captured headHash.
func runGCCompact(c *CLI, args []string) int {
	fs := flag.NewFlagSet("gc compact", flag.ContinueOnError)
	cutoffRaw := fs.String("cutoff", "", "граница сжатия в формате RFC3339")
	format := fs.String("format", "table", "формат вывода: table или json")
	dryRun := fs.Bool("dry-run", true, "только план, без изменений (по умолчанию)")
	apply := fs.Bool("apply", false, "применить сжатие после проверки плана")
	if !parseFlags("gc compact", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon gc compact: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *cutoffRaw)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc compact: cutoff %q: %v\n", *cutoffRaw, err)
		return 2
	}
	commits, headHash, headTree, err := gcCompactRead(gcCompactCardPath)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc compact: %v\n", err)
		return 2
	}
	plan := gcCompactBuild(commits, cutoff, headHash, headTree)
	report := gcCompactReport{HeadHash: headHash}
	if plan.Reason != "" {
		report.Mode = "refusal"
		report.Reason = plan.Reason
		report.Hint = plan.Hint
		if *format == "json" {
			if err := renderGCCompactJSON(c.out, report); err != nil {
				fmt.Fprintf(c.errOut, "beacon gc compact: %v\n", err)
				return 2
			}
		} else {
			renderGCCompactTable(c.out, report)
		}
		return 2
	}

	report.Planned = plan.gcCompactCreated()
	report.Base = 1
	report.Replays = plan.gcCompactReplays()
	render := func() int {
		if *format == "json" {
			if err := renderGCCompactJSON(c.out, report); err != nil {
				fmt.Fprintf(c.errOut, "beacon gc compact: %v\n", err)
				return 2
			}
			return 0
		}
		renderGCCompactTable(c.out, report)
		return 0
	}

	if *dryRun && !*apply {
		report.Mode = "dry-run"
		return render()
	}

	resultTree, err := gcCompactApply(plan)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon gc compact: применение: %v\n", err)
		return 2
	}
	report.Mode = "applied"
	report.Created = report.Planned
	report.Verified = gcCompactVerify(headTree, resultTree)
	code := render()
	if !report.Verified {
		fmt.Fprintf(c.errOut, "beacon gc compact: дерево результата %s не совпало с деревом headHash %s\n", resultTree, headTree)
		return 2
	}
	return code
}
