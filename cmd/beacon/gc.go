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

	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/worktree"
)

// Q-A-02: the lease/ticket collector in watch.go stays intact. This file adds
// the other half of `beacon gc`: dead and orphaned opencode serve processes.
// Only the user's live registration and Beacon's own recorded server are
// protected; everything else with `opencode serve --hostname` is an orphan.

const (
	gcKindServe      gcKind = "serve"
	gcKindObligation gcKind = "obligation"
	gcReasonOrphan          = "orphan"
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
}

// gcWorktreeReport is the JSON form of both a dry run and an apply. Items holds
// every inspected worktree, so the report names the kept ones and their
// reasons. Removed counts the removal set: candidates in a dry run, deletions
// after apply.
type gcWorktreeReport struct {
	Mode    string             `json:"mode"`
	Removed int                `json:"removed"`
	Kept    int                `json:"kept"`
	Items   []gcWorktreeResult `json:"items"`
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
		if !e.Locked && watchID == "" {
			status, statusErr = gcWorktreeStatus(e.Dir)
		}
		plan = append(plan, gcWorktreeDecision(e, watchID, status, statusErr))
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

	plan, err := gcWorktreeCollect(c)
	if err != nil {
		fmt.Fprintln(c.errOut, err)
		return 2
	}

	report := gcWorktreeReport{Mode: "dry-run", Items: plan}
	for _, item := range plan {
		if item.Status == "remove" {
			report.Removed++
		} else {
			report.Kept++
		}
	}

	failed := false
	if *apply {
		report.Mode = "applied"
		report.Removed = 0
		for i, item := range plan {
			if item.Status != "remove" {
				continue
			}
			if err := gcWorktreeRemove(item.Dir); err != nil {
				report.Items[i].Status = "kept"
				report.Items[i].Reason = gcReasonRemoveError + ":" + err.Error()
				report.Kept++
				failed = true
				fmt.Fprintf(c.errOut, "beacon gc-worktree: удаление %s: %v\n", item.Dir, err)
				continue
			}
			report.Items[i].Status = "removed"
			report.Removed++
		}
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
