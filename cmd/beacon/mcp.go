package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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
	"sync"
	"syscall"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/app/taskcli"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/cibridge"
	"github.com/refusedguy/beacon/internal/infra/github"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/ladder"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/registry"
	"github.com/refusedguy/beacon/internal/infra/server"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/taskstore"
	"github.com/refusedguy/beacon/internal/infra/wake"
	"github.com/refusedguy/beacon/internal/infra/webhook"
	"github.com/refusedguy/beacon/internal/infra/worktree"
)

// The app service writes precheck reasons under the same state directory as
// the MCP store, so it satisfies mcp.PrecheckJournal without the mcp package
// importing app (ADR 0005).
var _ mcp.PrecheckJournal = (*taskcli.Service)(nil)

// deferredCardLinks binds the cibridge CardLinks port to the deferred wiring
// queue in the composition root. A merged Watch marks its deferred card done;
// that row is the durable record the wiring acceptance check reads. The store
// transition is idempotent by (watchID, reason), so a repeated merged
// observation cannot write the card twice.
type deferredCardLinks struct {
	db   *store.DB
	root string
}

var _ cibridge.CardLinks = (*deferredCardLinks)(nil)

func (l *deferredCardLinks) CloseMerged(ctx context.Context, watchID string, pr int, at time.Time) error {
	if l == nil || l.db == nil {
		return nil
	}
	rows, err := l.db.ListDeferred("")
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.WatchID != watchID {
			continue
		}
		if _, err := l.db.MarkDeferredDone(row.WatchID, row.Reason); err != nil {
			return err
		}
		if err := closeWiringCard(l.root, pr, at); err != nil {
			return err
		}
	}

	// A watch may also carry an ordinary card link written by beacon_watch_pr.
	// Closing it here makes MERGED move any linked card to done, not only the
	// WIRING-* cards that come from the deferred queue.
	cardID, err := linkedCardID(ctx, l.db, watchID)
	if err != nil {
		observe.ActorError("card-link-lookup", watchID, err)
		return err
	}
	if cardID == "" {
		return nil
	}
	if err := closeLinkedCardFile(l.root, cardID, pr, at); err != nil {
		return err
	}
	_, _ = l.db.Unlink(cardID)
	return nil
}

// linkedCardID reads the card bound to a watch from card_links. The table is
// created on first use by store.Link, so the CREATE here only matters when a
// watch registers without a cardID. A missing row is not an error.
func linkedCardID(ctx context.Context, db *store.DB, watchID string) (string, error) {
	if db == nil || strings.TrimSpace(watchID) == "" {
		return "", nil
	}
	cardID := ""
	err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS card_links (
  card_id  TEXT NOT NULL PRIMARY KEY,
  watch_id TEXT NOT NULL,
  pr       INTEGER NOT NULL
)`); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT card_id FROM card_links WHERE watch_id = ? LIMIT 1`, watchID).Scan(&cardID)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cardID, nil
}

// wiringCardWriteFile is the capabilities-gate seam for writing a card. The
// call goes through the variable, so cmd/beacon never names os.WriteFile
// directly (ADR 0005), as in init.go.
var wiringCardWriteFile = os.WriteFile

// mergedCloseOptions builds the canonical close fields shared with the CLI and
// MCP close paths. An explicit timestamp keeps repeated merged observations
// byte for byte identical.
func mergedCloseOptions(at time.Time) taskstore.CloseCardOptions {
	return taskstore.CloseCardOptions{
		Status:   "done",
		Reason:   "already_landed",
		ClosedBy: "beacon",
		ClosedAt: at.UTC().Format(time.RFC3339),
	}
}

// closeWiringCard flips the deferred card to done and records the merged PR.
// The confirmation line makes the write idempotent: a repeated merged
// observation leaves the file unchanged.
func closeWiringCard(root string, pr int, at time.Time) error {
	if root == "" || pr <= 0 {
		return nil
	}
	lock, err := taskstore.AcquireLock(taskstore.TasksLockPath(filepath.Join(root, "docs", "tasks")), root)
	if err != nil {
		return err
	}
	defer lock.Release()
	path := filepath.Join(root, "docs", "tasks", fmt.Sprintf("WIRING-%d.md", pr))
	// os.DirFS + fs.ReadFile keeps the read out of cmd's forbidden os.ReadFile
	// capability (ADR 0005), as in readMCPConnection above.
	raw, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	text := string(raw)
	if strings.Contains(text, fmt.Sprintf("PR #%d MERGED", pr)) {
		return nil
	}
	text, err = taskstore.ApplyCloseFields(text, mergedCloseOptions(at))
	if err != nil {
		return err
	}
	text += fmt.Sprintf("\n## Подтверждение\n- PR #%d MERGED: %s (squash).\n",
		pr, at.UTC().Format("2006-01-02"))
	return wiringCardWriteFile(path, []byte(text), 0o644)
}

// closeLinkedCardFile flips the card linked to a merged Watch to done. The PR
// marker makes the write idempotent: a repeated merged observation leaves the
// card unchanged. taskstore.ApplyCloseFields owns the close fields, so the
// result matches the CLI and MCP close paths. Only Состояние and Вопрос stay
// outside that contract and are cleared separately.
func closeLinkedCardFile(root, cardID string, pr int, at time.Time) error {
	if root == "" || strings.TrimSpace(cardID) == "" || pr <= 0 {
		return nil
	}
	lock, err := taskstore.AcquireLock(taskstore.TasksLockPath(filepath.Join(root, "docs", "tasks")), root)
	if err != nil {
		return err
	}
	defer lock.Release()
	path := filepath.Join(root, "docs", "tasks", strings.TrimSpace(cardID)+".md")
	raw, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	text := string(raw)
	marker := fmt.Sprintf("PR #%d MERGED", pr)
	if strings.Contains(text, marker) {
		return nil
	}
	text, err = taskstore.ApplyCloseFields(text, mergedCloseOptions(at))
	if err != nil {
		return err
	}
	text = setCardField(text, "Состояние", "")
	text = setCardField(text, "Вопрос", "")
	text += fmt.Sprintf("\n## Подтверждение\n- %s: %s (squash).\n",
		marker, at.UTC().Format("2006-01-02"))
	return wiringCardWriteFile(path, []byte(text), 0o644)
}

// mcpStateDBName is the SQLite file inside the Beacon state directory
// (ADR 0011, ADR 0015).
const mcpStateDBName = "beacon.db"

// The single reconciler uses github.Interval: 10s while a Watch changed
// recently, 30s when quiet, stretched when the point budget is spent.

// mcpDaemonPorts and mcpDaemonRun are compatibility seams kept for callers
// that still assemble the older daemon cycle. The production MCP server and
// daemon command both use ReconcileLoop instead, so no competing bridge runs.
var (
	mcpDaemonPorts = makeDaemonPorts
	mcpDaemonRun   = orchestrator.DaemonRun
)

// mcpDaemonInterval is the pass interval of the in-process daemon. The default
// is the same three minutes as `beacon daemon`; BEACON_DAEMON_INTERVAL overrides
// it. A broken override falls back to the default so a bad setting cannot stop
// the loop, but the ignored value reaches the log.
func mcpDaemonInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(daemonIntervalEnv))
	if raw == "" {
		return orchestrator.DaemonInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		observe.WarnConfig(daemonIntervalEnv, raw, "ожидается положительная длительность, используется значение по умолчанию")
		return orchestrator.DaemonInterval
	}
	return d
}

// defaultVerdictPollInterval is the cadence of the single DB-backed verdict
// poller that feeds beacon_wait_result. It matches the reconciler's quiet 30s
// cycle: a verdict written by the bridge becomes visible on the next tick, and
// the poller does not re-read every live Watch several times per second. The
// source also skips the read entirely while no wait_result subscriber is
// registered, so an idle MCP server does no polling work at all.
const defaultVerdictPollInterval = 30 * time.Second

// verdictPollIntervalEnv overrides the poller cadence at the CLI boundary, the
// same way BEACON_DAEMON_INTERVAL overrides the daemon cycle. A broken value
// falls back to the default so a bad setting cannot stall waiting callers, and
// the ignored value reaches the log.
const verdictPollIntervalEnv = "BEACON_VERDICT_POLL_INTERVAL"

func verdictPollInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(verdictPollIntervalEnv))
	if raw == "" {
		return defaultVerdictPollInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		observe.WarnConfig(verdictPollIntervalEnv, raw, "ожидается положительная длительность, используется значение по умолчанию")
		return defaultVerdictPollInterval
	}
	return d
}

// stopVerdictSource releases the one poller created by buildMCPDependencies
// before its store handle closes. The source is optional so a dependencies
// value assembled by a test still closes cleanly.
func stopVerdictSource(source mcp.VerdictSource) {
	if s, ok := source.(interface{ Stop() }); ok {
		s.Stop()
	}
}

// startMCPDaemon builds the full daemon cycle in this process and starts it as
// a goroutine. The returned stop cancels the loop, waits for it to exit and
// closes the ports it used. Callers must invoke stop before closing any store
// the daemon might still touch.
func startMCPDaemon(ctx context.Context, c *CLI) (func(), error) {
	ports, closePorts, err := mcpDaemonPorts(c)
	if err != nil {
		return nil, err
	}
	daemonCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mcpDaemonRun(daemonCtx, ports, mcpDaemonInterval(), false)
	}()
	return func() {
		cancel()
		<-done
		_ = closePorts()
	}, nil
}

// mcpConnectionFileName is the connection file read on every `beacon mcp`
// startup. It sits next to beacon.db in the state directory (ADR 0036 §1).
// The endpoint lives in this file, never in hardcoded process settings, so a
// daemon that moves to a new address or port is picked up on the next start.
const mcpConnectionFileName = "connection.json"

// initEventTimeoutDefault bounds the startup precheck (ADR 0041 §6). A missing
// bypass is a configuration error, so the precheck never waits for the full
// timeout in that case. It fails immediately with a clear reason.
const initEventTimeoutDefault = 10 * time.Second

// mcpPrecheckFailure is the run outcome from ADR 0041 §6. A run whose bypass is
// not configured ends with this outcome before it starts waiting.
const mcpPrecheckFailure = "precheck_failure"

// precheckFailureError carries the reason of a failed startup precheck. The
// reason reaches the caller (and, in turn, wait_result and the journal)
// instead of a silent wait.
type precheckFailureError struct {
	reason string
}

func (e *precheckFailureError) Error() string {
	return fmt.Sprintf("mcp: %s: %s", mcpPrecheckFailure, e.reason)
}

// isPrecheckFailure reports whether err is a startup precheck failure. Callers
// branch on the error type, not on the rendered message.
func isPrecheckFailure(err error) bool {
	var failure *precheckFailureError
	return errors.As(err, &failure)
}

// initEventConfig is the startup precheck configuration from ADR 0041 §6.
// source is empty when Beacon does not expect an init event; there is then
// nothing to precheck and the run proceeds. bypass skips the check when an
// init event is expected.
type initEventConfig struct {
	source  string
	bypass  bool
	timeout time.Duration
}

// readInitEventConfig parses the precheck configuration from the environment
// at the CLI boundary. init_event_timeout defaults to ten seconds and bypass
// is opt-in.
func readInitEventConfig() initEventConfig {
	cfg := initEventConfig{timeout: initEventTimeoutDefault}
	cfg.source = os.Getenv("BEACON_INIT_EVENT")
	cfg.bypass = observe.ParseBoolEnv(os.Getenv("BEACON_INIT_BYPASS"))
	if raw := strings.TrimSpace(os.Getenv("BEACON_INIT_EVENT_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.timeout = d
		} else {
			observe.WarnConfig("BEACON_INIT_EVENT_TIMEOUT", raw, "ожидается положительная длительность, используется значение по умолчанию")
		}
	}
	return cfg
}

// syncCallTimeoutDefault is the default time a long synchronous MCP call may
// run before it is converted into a ticket carrying a job id (ADR 0045 §2).
const syncCallTimeoutDefault = 30 * time.Second

// syncCallTimeoutMax is the hard ceiling for the threshold. A larger configured
// value is clamped to this, so no call hangs in the connection for more than
// two minutes (ADR 0045 §2).
const syncCallTimeoutMax = 120 * time.Second

// syncCallConfig is the single threshold shared by the long synchronous MCP
// calls. The value flows into Dependencies.After, the one MCP-boundary knob
// that switches a slow response into the client's task form. Past the threshold
// the call returns a ticket with a job id instead of hanging in the connection,
// and further waiting proceeds by that ticket (ADR 0045 §2, ADR 0031 §2).
//
// This is a time threshold, not the delivery field of ADR 0026. ADR 0026 says
// honestly how a follow-up was delivered (steer, queue or unsupported); it does
// not say when a call must become a ticket. This config does not touch delivery.
type syncCallConfig struct {
	after time.Duration
}

// readSyncCallConfig parses the unified sync-call threshold at the CLI
// boundary. The default is 30 seconds, a configured value is capped at 120
// seconds, and an invalid or non-positive value falls back to the default so a
// broken setting can never disable the ticket conversion.
func readSyncCallConfig() syncCallConfig {
	cfg := syncCallConfig{after: syncCallTimeoutDefault}
	raw := strings.TrimSpace(os.Getenv("BEACON_SYNC_CALL_TIMEOUT"))
	if raw == "" {
		return cfg
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		observe.WarnConfig("BEACON_SYNC_CALL_TIMEOUT", raw, "ожидается положительная длительность, используется значение по умолчанию")
		return cfg
	}
	if d > syncCallTimeoutMax {
		observe.WarnConfig("BEACON_SYNC_CALL_TIMEOUT", raw, "значение больше максимума, используется "+syncCallTimeoutMax.String())
		d = syncCallTimeoutMax
	}
	cfg.after = d
	return cfg
}

// mcpConnection is the server endpoint Beacon connects to. Host, Port and
// Token come from the connection file (ADR 0036 §1).
type mcpConnection struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// target is the host:port pair that identifies the current endpoint. It is
// empty only when no connection file is present (local stdio mode).
func (c mcpConnection) target() string {
	if c.Host == "" && c.Port == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// remoteEndpoint converts the connection file record into the port the thin
// MCP client takes. No field is interpreted here beyond the endpoint itself.
func (c mcpConnection) remoteEndpoint() mcp.RemoteEndpoint {
	return mcp.RemoteEndpoint{Host: c.Host, Port: c.Port, Token: c.Token}
}

// readMCPConnection reads and validates the connection file in dir. A missing
// file is not an error: local stdio mode has no master to dial and keeps using
// the persisted store. A present but invalid file is an error so a corrupted
// endpoint is never silently used as the reconnect target.
func readMCPConnection(dir string) (mcpConnection, error) {
	path := filepath.Join(dir, mcpConnectionFileName)
	// os.DirFS + fs.ReadFile keeps file access out of cmd's forbidden
	// os.ReadFile capability (ADR 0005 capabilities gate, как в attach.go).
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		if os.IsNotExist(err) {
			return mcpConnection{}, nil
		}
		return mcpConnection{}, fmt.Errorf("mcp: read connection file %s: %w", path, err)
	}
	var conn mcpConnection
	if err := json.Unmarshal(data, &conn); err != nil {
		return mcpConnection{}, fmt.Errorf("mcp: decode connection file %s: %w", path, err)
	}
	if err := conn.validate(); err != nil {
		return mcpConnection{}, fmt.Errorf("mcp: connection file %s: %w", path, err)
	}
	return conn, nil
}

func (c mcpConnection) validate() error {
	if c.Host == "" {
		return fmt.Errorf("пустой host")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("порт %d вне диапазона 1..65535", c.Port)
	}
	return nil
}

// activeMCPConnection is the endpoint from the most recent startup. Reading
// the connection file on every `beacon mcp` run replaces it, so a moved daemon
// is picked up without recompilation. The watch store lives in the state
// directory and is independent of the endpoint, so registered tasks keep
// waiting while the client reconnects.
var activeMCPConnection mcpConnection

// mcpConnector applies the endpoint read from the connection file. The stdio
// MCP transport is local, so the default records the endpoint instead of
// dialing a network master. It is a seam tests replace to observe reconnects
// without a live server.
var mcpConnector = func(_ context.Context, _ mcpConnection) error { return nil }

// mcpPrecheck runs the startup precheck before the server begins waiting.
// Tests replace it to observe the precheck without a live init event source.
var mcpPrecheck = runMCPPrecheck

// runMCPPrecheck enforces the ADR 0041 §6 contract. When an init event is
// expected and bypass is not configured, the run fails immediately with a
// clear reason instead of waiting silently until the timeout. It never blocks
// on the init event channel: a missing bypass is a configuration error.
func runMCPPrecheck(ctx context.Context, cfg initEventConfig) error {
	if cfg.source == "" || cfg.bypass {
		return nil
	}
	reason := fmt.Sprintf(
		"bypass mode не настроен для init event %q: run завершён до входа в ожидание (init_event_timeout=%s)",
		cfg.source, cfg.timeout,
	)
	select {
	case <-ctx.Done():
		return &precheckFailureError{reason: reason + "; " + ctx.Err().Error()}
	default:
		return &precheckFailureError{reason: reason}
	}
}

// mcpRunner serves MCP over stdio. RegisterMCP replaces it, for example in tests.
var mcpRunner = func(errOut io.Writer) int {
	if err := runMCP(context.Background(), newDefaultCLI()); err != nil {
		fmt.Fprintf(errOut, "beacon mcp: %v\n", err)
		return 1
	}
	return 0
}

// precheckFailureReason is the reason of the startup precheck failure from
// ADR 0041 §6. runMCP stores it here when the precheck refuses the run; the
// production server installs it on its wait_result service so a tools/call
// reports precheck_failure with the reason instead of waiting silently.
var (
	precheckFailureMu     sync.Mutex
	precheckFailureReason string
)

func setPrecheckFailureReason(reason string) {
	precheckFailureMu.Lock()
	precheckFailureReason = reason
	precheckFailureMu.Unlock()
}

// newMCPWaitResult builds the wait_result service used by the production
// server. It reuses the mcp constructor so the tool replacement at the MCP
// boundary applies the same setters as NewServerWithDeps (T3.91). Only the
// startup precheck reason is CLI state: an empty reason clears a previous
// failure, so a run with a configured bypass waits normally.
func newMCPWaitResult(deps mcp.Dependencies) *mcp.WaitResultService {
	precheckFailureMu.Lock()
	deps.PrecheckReason = precheckFailureReason
	precheckFailureMu.Unlock()
	return mcp.NewWaitResultServiceFromDeps(deps)
}

// newMCPServer composes the real server and replaces the wait_result tool with
// the service that carries the startup precheck reason. AttachWaitResult is the
// hook the mcp package exposes for this integration (T3.84, ADR 0041 §6).
func newMCPServer(deps mcp.Dependencies) *mcpsdk.Server {
	s := mcp.NewServerWithDeps(deps)
	mcp.AttachWaitResult(s, newMCPWaitResult(deps))
	// The generic server knows no project root. Bind watch_pr here so every
	// Watch inherits this project's flaky set and layered limits. The local
	// adapter also records the optional cardID link for merged-card closing.
	attachWatchPRWithCardLink(s, deps, projectRootForConfig())
	mcp.AttachWake(s, newMCPWakeHandler(deps))
	return s
}

// watchPRInputWithCard is the tool input at the CLI boundary. It embeds the
// mcp-owned input so every existing argument keeps its schema and validation,
// and adds the optional cardID that binds a Watch to the card a merged PR must
// close. The mcp package stays unaware of the card link.
type watchPRInputWithCard struct {
	mcp.WatchPRInput
	CardID string `json:"cardID,omitempty" jsonschema:"card id to close when the PR merges, for example T3.329"`
}

// claimedCardForNotify resolves the card a Watch should close from the agent
// that registered it. beacon_task_claim writes the agent id into the card's
// Агент field, so CardIDForAgent finds the card when notify.Target carries
// that same id. The CLI boundary meets that condition. A headless session
// registers with its own sessionID, so the lookup finds no card and the caller
// logs the missing link instead of guessing one.
func claimedCardForNotify(store mcp.TaskClaimStore, notify *mcp.WatchPRNotify) string {
	if notify == nil {
		return ""
	}
	agentID := strings.TrimSpace(notify.Target)
	if agentID == "" {
		return ""
	}
	lookup, ok := store.(mcp.AgentCardLookup)
	if !ok {
		return ""
	}
	return strings.TrimSpace(lookup.CardIDForAgent(agentID))
}

// attachWatchPRWithCardLink installs the real watch_pr handler and records the
// optional cardID link after a successful registration. A failed link write is
// logged but does not fail a Watch that is already registered.
func attachWatchPRWithCardLink(s *mcpsdk.Server, deps mcp.Dependencies, projectRoot string) {
	if s == nil {
		return
	}
	handler := mcp.NewWatchPRHandler(deps.Store, deps.CI, deps.Clock)
	handler.SetBlockerIncidents(deps.Blockers)
	handler.SetFixerBinding(deps.FixerBinding)
	if registrar, ok := deps.Store.(mcp.WatchPRRegistrar); ok {
		handler.SetRegistry(registrar)
	}
	handler.SetProjectRoot(projectRoot)
	db, _ := deps.Store.(*store.DB)

	no := false
	s.RemoveTools("beacon_watch_pr")
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "beacon_watch_pr",
		Description: "Register or renew a Watch on a pull request head and subscribe the caller.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  true,
		},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in watchPRInputWithCard) (*mcpsdk.CallToolResult, mcp.WatchPROutput, error) {
		result, out, err := handler.Handle(ctx, req, in.WatchPRInput)
		if err != nil || out.WatchID == "" || db == nil {
			return result, out, err
		}
		cardID := strings.TrimSpace(in.CardID)
		if cardID == "" {
			cardID = claimedCardForNotify(deps.TaskClaimStore, in.Notify)
		}
		if cardID == "" {
			observe.Event(observe.LevelWarn, "watch_pr_card_link_missing", observe.Fields{
				observe.FieldWatchID: out.WatchID,
				observe.FieldReason:  "no cardID in watch_pr input and no claimed card matches notify.Target",
			})
			return result, out, err
		}
		if !taskstore.IsID(cardID) {
			observe.ActorError("watch_pr-card-link", out.WatchID,
				fmt.Errorf("invalid cardID %q", cardID))
			return result, out, err
		}
		linked, linkErr := db.LinkCardWatch(cardID, out.WatchID, in.PR)
		if linkErr == nil && linked.Duplicate &&
			(linked.Link.WatchID != out.WatchID || linked.Link.PR != in.PR) {
			// A rebase creates a successor Watch. The first row still points at
			// the closed Watch, so move the single link to the live one before
			// its merged verdict can close the card.
			if _, unlinkErr := db.Unlink(cardID); unlinkErr != nil {
				linkErr = unlinkErr
			} else {
				_, linkErr = db.LinkCardWatch(cardID, out.WatchID, in.PR)
			}
		}
		if linkErr != nil {
			observe.ActorError("watch_pr-card-link", out.WatchID, linkErr)
		}
		return result, out, err
	})
}

// newMCPWakeHandler builds the production wake handler. The registry guard is
// installed when the composition root supplied the SQLite store, so the CLI
// rule from registryWakeAllowed decides whether a stale executor may be woken
// (T5.49). A fake store in a test keeps the pre-feature path. No delivery
// sender is wired here, so a wake that passes the guard still reports an
// unsupported delivery until the environment adapter is attached (ADR 0026).
func newMCPWakeHandler(deps mcp.Dependencies) *mcp.WakeHandler {
	handler := mcp.NewWakeHandler(nil)
	if db, ok := deps.Store.(*store.DB); ok {
		handler.WithRegistryGuard(newWakeRegistryGuard(db, orchestratorProjectKey()))
	}
	return handler
}

// serveMCP starts the stdio transport. Tests replace it to inspect the
// composed dependencies without blocking on stdin.
var serveMCP = func(ctx context.Context, deps mcp.Dependencies) error {
	return newMCPServer(deps).Run(ctx, &mcpsdk.StdioTransport{})
}

// mcpRemoteEnv opts `beacon mcp` into the thin HTTP client of BS.42. The server
// endpoint /mcp arrives with BS.43, so remote mode is explicit until then: the
// default keeps the seven local tools and their cycle instructions unchanged.
// The address still comes from connection.json, never from this setting.
const mcpRemoteEnv = "BEACON_MCP_REMOTE"

func remoteToolsEnabled() bool {
	return observe.ParseBoolEnv(os.Getenv(mcpRemoteEnv))
}

// serveMCPRemote serves the same stdio transport with every contracted tool
// forwarded to the server. This branch opens no store, starts no reconciler and
// owns no verdict poller, so the MCP process holds no Beacon state. Tests
// replace the seam to inspect the client built from connection.json.
var serveMCPRemote = func(ctx context.Context, client *mcp.RemoteClient) error {
	s := mcp.NewServer()
	if err := mcp.AttachRemoteTools(s, client); err != nil {
		return err
	}
	return s.Run(ctx, &mcpsdk.StdioTransport{})
}

// sharedCIWaiter adapts the store gate to the port declared in mcp. The store
// names its callback type SharedGateWait, which is not assignable to the
// unnamed callback in mcp.SharedCIWaiter, so the conversion lives here in the
// composition root that owns both packages (ADR 0005).
func sharedCIWaiter(ctx context.Context, key string, fn func(context.Context) error) error {
	return store.SharedCIWait(ctx, key, store.SharedGateWait(fn))
}

// openMCPDependencies opens the real state store and builds the production
// ports for the seven MCP tools. The returned closer belongs to the caller.
//
// Dependencies.Store is the port that used to stay nil, which made watch_pr
// answer PERSIST_FAILED: store is nil. The SQL stamper, writer and event log all
// use the same single-writer connection, so a tool's write is visible to the
// next tool immediately.
//
// CI still has no production adapter in this tree, so a watch_pr call must
// supply headSHA. Source is now backed by the single DB verdict poller, so a
// wait on a watching Watch can observe the verdict the reconciler persists.
func openMCPDependencies() (mcp.Dependencies, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return mcp.Dependencies{}, nil, err
	}
	return openMCPDependenciesInDir(dir)
}

// openMCPDependenciesInDir is the directory-bound form of openMCPDependencies.
// runMCP already resolved the state directory to read the connection file, so
// it reuses that path here instead of resolving it a second time.
func openMCPDependenciesInDir(dir string) (mcp.Dependencies, func() error, error) {
	db, err := openProcessStore(dir)
	if err != nil {
		return mcp.Dependencies{}, nil, err
	}
	deps := buildMCPDependencies(db, dir, nil)
	return deps, func() error {
		stopVerdictSource(deps.Source)
		return db.Close()
	}, nil
}

// openProcessStore opens the single SQLite handle a long-lived process shares
// between the MCP tools, the reconciler and the stats snapshot.
func openProcessStore(dir string) (*store.DB, error) {
	if err := mcp.EnsureStateDir(dir); err != nil {
		return nil, err
	}
	// The long-lived process writes secrets (CI payloads, wake tokens) through
	// this handle. Only OpenProduction installs the redacting driver and the
	// observed writer, so persist.seq and sqlite-redact stay honest (AUD-04).
	return store.OpenProduction(filepath.Join(dir, mcpStateDBName))
}

// fileGraphIndex is the production GraphIndexReader. It keeps one index handle
// so the source directory bound by the first rebuild stays available to every
// later read. The reader opens lazily, so a process that never asks for graph
// readiness never opens the index. A failed open leaves a zero Index in place
// and GraphStatus reports a read error instead of the no-reader fallback.
type fileGraphIndex struct {
	dir  string
	once sync.Once
	idx  *taskstore.Index
}

func newFileGraphIndex(dir string) *fileGraphIndex {
	return &fileGraphIndex{dir: dir}
}

func (g *fileGraphIndex) index() *taskstore.Index {
	g.once.Do(func() {
		root := projectRootForConfig()
		idx, err := taskstore.OpenIndex(taskstore.IndexPath(g.dir, root))
		if err != nil {
			g.idx = &taskstore.Index{}
			return
		}
		_, _ = idx.RebuildDir(filepath.Join(root, "docs", "tasks"))
		g.idx = idx
	})
	return g.idx
}

// GraphStatus compares the stored card digest with the live docs/tasks
// directory. A card edited after the rebuild changes the source digest, so the
// marker turns stale until the index is rebuilt again.
func (g *fileGraphIndex) GraphStatus(ctx context.Context) (domain.GraphStatus, error) {
	return g.index().GraphStatus(ctx)
}

// buildMCPDependencies wires the real MCP tool ports around one DB handle. The
// optional points reader comes from the process' GitHub batch provider, so
// beacon_stats reports the real hourly budget instead of a process-local zero.
// The snapshot reads LiveWatches, which includes blocked Watches, and surfaces
// the last saved runtime-stats payload as diagnostics. The three ADR 0314 ports
// are bound here: the task index proves graphReady, the registry adapter reads
// the same snapshot, and one generation ledger serves claim and ack.
func buildMCPDependencies(db *store.DB, dir string, points func() (int, int)) mcp.Dependencies {
	// One stateless Ladder over the one store handle. The same instance backs
	// the read/claim decorators, the bridge wake path and the stats projection.
	lad := ladder.New(db)
	graph := newFileGraphIndex(dir)
	snapshot := mcp.SnapshotFunc(func() (mcp.StateSnapshot, error) {
		live, err := db.LiveWatches()
		if err != nil {
			return mcp.StateSnapshot{}, err
		}
		pointsUsed, pointBudget := pointsValue(points)
		pending, escalated := ladderCounts(db)
		snap := mcp.StateSnapshot{
			ActiveWatches:   live,
			LiveWatches:     live,
			PointsThisHour:  pointsUsed,
			Budget:          pointBudget,
			LadderPending:   pending,
			LadderEscalated: escalated,
			Version:         mcp.ServerVersion,
		}
		if raw, ok, err := db.LoadRuntimeStats(); err != nil {
			snap.LoadErrors = append(snap.LoadErrors, "runtime_stats: "+err.Error())
		} else if ok && len(raw) > 0 {
			snap.LoadErrors = append(snap.LoadErrors, fmt.Sprintf("runtime_stats: %d bytes", len(raw)))
		}
		return snap, nil
	})
	return mcp.Dependencies{
		Store:   db,
		Stamper: ladderStamper{ObligationStamper: mcp.NewSQLObligationStamper(db), ladder: lad},
		Writer:  ladderWaitWriter{WaitResultWriter: mcp.NewSQLWaitResultWriter(db), ladder: lad},
		Events:  mcp.NewSQLCancelEventLog(db),
		// One source for the whole process. It polls the same DB handle the
		// reconciler writes, so a verdict written by the bridge reaches every
		// caller waiting on that Watch (ADR 0005: the port stays in mcp).
		Source:          mcp.NewDBVerdictSource(db, verdictPollInterval()),
		SharedCIWaiter:  sharedCIWaiter,
		PrecheckJournal: taskcli.NewService(taskcli.Config{StateDir: dir}),
		CallScopes:      mcp.NewCapabilityScopeRegistry(nil, nil),
		// ADR 0045 §2: one threshold for all long synchronous MCP calls. Past
		// it the server answers with a ticket carrying a job id rather than
		// holding the connection open. This is a time bound, not ADR 0026's
		// delivery field.
		After:        readSyncCallConfig().after,
		Blockers:     db,
		FixerBinding: db,
		// The deferred-wiring port was left nil here, so beacon_defer_wiring
		// answered PERSIST_FAILED. The durable adapter shares this DB handle.
		DeferWiring: newDurableDeferredStore(db),
		// IDX-05 requires a card store port for beacon_task_claim. Without it
		// the handler answers PERSIST_FAILED and the executor gets no card.
		// The adapter reads cards from the project root and keeps its lock in
		// the state directory next to the SQLite file.
		TaskClaimStore: &mcp.FileTaskClaimStore{
			TasksDir: filepath.Join(projectRootForConfig(), "docs", "tasks"),
			StateDir: dir,
		},
		// beacon_task_close needs the same card store port. Without it the
		// handler sees nil and answers PERSIST_FAILED, so agents fall back to
		// the CLI to close their cards.
		TaskCloseStore: &mcp.FileTaskCloseStore{
			TasksDir: filepath.Join(projectRootForConfig(), "docs", "tasks"),
			StateDir: dir,
		},
		Snapshot:            snapshot,
		GraphIndexReader:    graph,
		RegistryStatsReader: mcp.NewSnapshotRegistryStats(snapshot),
		GenerationLedger:    orchestrator.NewClaimGeneration(),
	}
}

// pointsValue is a nil-safe read of the GitHub point counters. A missing
// reader reports zero used and the shared default budget.
func pointsValue(points func() (int, int)) (int, int) {
	if points == nil {
		return 0, github.BudgetPerHour
	}
	used, budget := points()
	if budget <= 0 {
		budget = github.BudgetPerHour
	}
	return used, budget
}

// ---------------------------------------------------------------------------
// Loopback HTTP and SSE (W8-A).
//
// The transport lives in infra/httpapi. This file only assembles its ports
// around the same store handle and wake registry the MCP tools use, so both
// transports observe the same state and the same effects.
// ---------------------------------------------------------------------------

// Both adapters satisfy the transport-owned ports. The assertions keep the
// wiring honest if either interface changes.
var (
	_ httpapi.StateProvider = httpStateProvider{}
	_ httpapi.HistorySource = httpStateProvider{}
	_ httpapi.DecisionSink  = humanDecisionSink{}
)

// snapshotHistoryWindow is the time range published in recentWatches.
const snapshotHistoryWindow = 24 * time.Hour

// snapshotHistoryLimit mirrors httpapi.maxRecentWatches. The transport also
// truncates, but the store read stays bounded before it reaches the wire.
const snapshotHistoryLimit = 30

// httpStateProvider projects the MCP snapshot plus the control-plane rows the
// HTTP view needs. It reads only; every write goes through humanDecisionSink.
type httpStateProvider struct {
	db         *store.DB
	dir        string
	projectKey string
	deps       mcp.Dependencies
}

func (p httpStateProvider) Snapshot(ctx context.Context) (httpapi.Snapshot, error) {
	snap := httpapi.Snapshot{
		LiveWatches:      []domain.Watch{},
		BlockerIncidents: []domain.BlockerIncident{},
		Escalations:      []string{},
		LoadErrors:       []string{},
	}
	if p.deps.Snapshot != nil {
		base, err := p.deps.Snapshot.Snapshot()
		if err != nil {
			snap.LoadErrors = append(snap.LoadErrors, "snapshot: "+err.Error())
		} else {
			snap.LiveWatches = base.RadarWatches()
			snap.PointStats = httpapi.PointStats{Used: base.PointsThisHour, Budget: base.Budget}
			snap.LoadErrors = append(snap.LoadErrors, cleanLoadErrors(base.LoadErrors)...)
		}
	}
	if p.db != nil {
		blockers, err := p.db.ActiveBlockers()
		if err != nil {
			snap.LoadErrors = append(snap.LoadErrors, "blockers: "+err.Error())
		} else {
			snap.BlockerIncidents = blockers
		}
		if binding, bound, err := p.db.BoundOrchestrator(p.projectKey); err != nil {
			snap.LoadErrors = append(snap.LoadErrors, "orchestrator: "+err.Error())
		} else if bound {
			snap.BoundOrchestrator = &binding
		}
		if raw, ok, err := p.db.LoadRuntimeStats(); err != nil {
			snap.LoadErrors = append(snap.LoadErrors, "runtime_stats: "+err.Error())
		} else if ok && json.Valid(raw) {
			snap.Metrics = json.RawMessage(raw)
		}
	}
	// The escalation journal is read through the same filesystem seam the
	// `inspect` command uses, so both views show the same rung 2 entries.
	if p.dir != "" {
		lines, err := readEscalations(p.dir)
		if err != nil {
			snap.LoadErrors = append(snap.LoadErrors, "escalations: "+err.Error())
		} else {
			snap.Escalations = lines
		}
	}
	snap.LastCycle = lastCycleInfo()
	return snap, nil
}

// SnapshotHistory implements httpapi.HistorySource. It publishes the newest
// Watches closed in the last day. A store that is not wired in is not an
// error; the caller then receives an empty history block.
func (p httpStateProvider) SnapshotHistory(ctx context.Context) (httpapi.SnapshotHistory, error) {
	h := httpapi.SnapshotHistory{RecentWatches: []httpapi.WatchHistory{}}
	if p.db == nil {
		return h, nil
	}
	since := time.Now().Add(-snapshotHistoryWindow)
	watches, err := p.db.RecentClosedWatches(ctx, since, snapshotHistoryLimit)
	if err != nil {
		return h, err
	}
	for _, w := range watches {
		outcome := historyOutcome(w)
		h.RecentWatches = append(h.RecentWatches, httpapi.WatchHistory{
			ID:          w.ID,
			Repo:        w.Repo,
			PR:          w.PR,
			HeadSHA:     w.HeadSHA,
			Outcome:     outcome,
			ClosedAt:    w.ClosedAt,
			CloseReason: w.CloseReason,
		})
		if outcome == httpapi.OutcomeMerged {
			h.HourlyStats.Merges++
		}
	}
	if blockers, err := p.db.ActiveBlockers(); err == nil {
		h.HourlyStats.Blockers = len(blockers)
	}
	if p.deps.Snapshot != nil {
		if base, err := p.deps.Snapshot.Snapshot(); err == nil {
			h.HourlyStats.Points = base.PointsThisHour
		}
	}
	return h, nil
}

// historyOutcome maps a closed Watch verdict to the transport's stable enum.
// Cancelled and head-changed closes keep their earlier verdict, so their
// outcome is "closed" and the reason carries the cause.
func historyOutcome(w domain.Watch) httpapi.WatchOutcome {
	switch w.Verdict {
	case domain.VerdictMerged:
		return httpapi.OutcomeMerged
	case domain.VerdictTimeout, domain.VerdictBlocked:
		return httpapi.OutcomeEscalated
	default:
		return httpapi.OutcomeClosed
	}
}

// cleanLoadErrors drops the successful runtime_stats byte-count note the MCP
// snapshot adds. It is a size, not a load failure.
func cleanLoadErrors(errs []string) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		if strings.HasPrefix(e, "runtime_stats: ") && strings.HasSuffix(e, " bytes") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// humanDecisionSink journals each command and applies its effect. The HTTP
// layer handles idempotency, so one accepted request produces one journal row.
type humanDecisionSink struct {
	db       *store.DB
	registry *wake.Registry
	events   *httpapi.EventBus
	// acks is shared across requests. The decision and continuation registries
	// inside it keep a repeat answer a no-op and refuse a conflicting second
	// answer, the same way the local `beacon human` path does.
	acks *mcp.AckHandler
}

// newHumanDecisionSink builds a sink with one ack handler for the process. The
// handler carries the in-memory decision registries, so it must outlive a
// single request.
func newHumanDecisionSink(db *store.DB, registry *wake.Registry, events *httpapi.EventBus) humanDecisionSink {
	return humanDecisionSink{
		db: db, registry: registry, events: events,
		acks: mcp.NewAckHandler(db, mcp.NewSQLObligationStamper(db), nil),
	}
}

func (s humanDecisionSink) Apply(ctx context.Context, d httpapi.Decision) (httpapi.DecisionResult, error) {
	result := httpapi.DecisionResult{Command: d.Command, WatchID: d.WatchID}
	if s.db == nil {
		return result, errors.New("httpapi: store недоступен")
	}
	// The transport rejects unknown verbs before they reach the sink. Keep the
	// guard here as well so a direct caller cannot journal a command that has
	// no effect.
	switch d.Command {
	case "approve", "reject", "context", "rework", "pause", "resume", "unblock", "cancel", "force_retry_merge":
	default:
		return result, fmt.Errorf("httpapi: неизвестная команда %s", d.Command)
	}
	w, err := s.db.Watch(d.WatchID)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC()
	seq, err := s.db.AppendEvent(ctx, d.WatchID, "human_decide", fmt.Sprintf(
		"command=%s obligation=%s actor=%s note=%s", d.Command, d.ObligationID, d.Actor, d.Note,
	))
	if err != nil {
		return result, err
	}
	result.DecisionID = strconv.FormatInt(seq, 10)

	switch d.Command {
	case "cancel":
		w.Status = domain.StatusClosed
		w.Verdict = domain.VerdictClosed
		w.CloseReason = strings.TrimSpace("human: " + d.Command + " " + d.Note)
		w.ClosedAt = now
		if err := s.db.SaveWatch(w); err != nil {
			return result, err
		}
	case "unblock":
		if w.BlockerID != "" {
			if _, err := s.db.ResolveBlocker(w.BlockerID, now); err != nil {
				return result, err
			}
			w, err = s.db.Watch(d.WatchID)
			if err != nil {
				return result, err
			}
		} else if w.Blocked != nil {
			w.Status = domain.StatusWatching
			w.Verdict = domain.VerdictWatching
			w.Blocked = nil
			w.Revision++
			if err := s.db.SaveWatch(w); err != nil {
				return result, err
			}
		}
	case "pause", "resume":
		phase := domain.WorkerPhaseWaiting
		if d.Command == "pause" {
			phase = domain.WorkerPhaseBlocked
		}
		for i := range w.Subscribers {
			w.Subscribers[i].Phase = phase
		}
		if err := s.db.SaveWatch(w); err != nil {
			return result, err
		}
	case "approve", "reject", "rework", "context":
		if err := s.applyAckDecision(ctx, d, w); err != nil {
			return result, err
		}
		// The ack resumes the same Watch and stamps its obligation. Read it
		// again so the reported status and the wake carry the new revision.
		w, err = s.db.Watch(d.WatchID)
		if err != nil {
			return result, err
		}
	}
	result.Status = string(w.Status) + "/" + string(w.Verdict)

	if d.Command != "cancel" {
		s.wake(ctx, w, d)
	}
	payload, _ := json.Marshal(result)
	if s.events != nil {
		s.events.Publish(httpapi.Event{
			Type: "human.decide", WatchID: w.ID, Payload: payload,
		})
	}
	return result, nil
}

// applyAckDecision resumes a parked Watch through the same ack port the local
// `beacon human` path uses. approve and reject carry a decision, rework and
// context continue the parked task on the same Watch. The note correlates the
// answer with the request, and continueOf always names the Watch read from the
// store so no successor Watch is created (ADR 0061 §4).
func (s humanDecisionSink) applyAckDecision(ctx context.Context, d httpapi.Decision, w domain.Watch) error {
	obligations, err := s.db.WatchObligations(w.ID)
	if err != nil {
		return err
	}
	if len(obligations) == 0 {
		return fmt.Errorf("httpapi: watch %s has no obligation", w.ID)
	}
	obligationID := d.ObligationID
	if obligationID == "" {
		obligationID = obligations[0].ID
	}
	in := mcp.AckInput{
		WatchID:      w.ID,
		ObligationID: obligationID,
		DecisionID:   d.Note,
	}
	handler := s.acks
	if handler == nil {
		handler = mcp.NewAckHandler(s.db, mcp.NewSQLObligationStamper(s.db), nil)
	}
	switch d.Command {
	case "approve":
		_, err = handler.Decide(ctx, in, mcp.DecisionApprove, d.Note)
	case "reject":
		_, err = handler.Decide(ctx, in, mcp.DecisionReject, d.Note)
	case "rework", "context":
		in.ContinueOf = w.ID
		err = handler.Ack(ctx, in)
	}
	return err
}

// wake steers the executor and orchestrator subscribers of the affected Watch.
// The obligation id comes from the request when present, otherwise from the
// first pending obligation of that Watch.
func (s humanDecisionSink) wake(ctx context.Context, w domain.Watch, d httpapi.Decision) {
	if s.registry == nil {
		return
	}
	obligationID := d.ObligationID
	if obligationID == "" && s.db != nil {
		if pending, err := s.db.PendingObligations(); err == nil {
			for _, o := range pending {
				if o.WatchID == w.ID {
					obligationID = o.ID
					break
				}
			}
		}
	}
	p := domain.WakePayload{
		WatchID: w.ID, ObligationID: obligationID, Revision: w.Revision,
		Verdict: "human:" + d.Command,
	}
	for _, sub := range w.Subscribers {
		if sub.Role != domain.RoleExecutor && sub.Role != domain.RoleOrchestrator {
			continue
		}
		target := sub.Target
		if target == "" {
			target = sub.ID
		}
		if target == "" {
			continue
		}
		if err := s.registry.Deliver(ctx, sub.Adapter, target, p); err != nil {
			observe.ActorError("human-decide-wake", w.ID, err)
		}
	}
}

// cycleMu guards the last completed reconciler pass reported by the HTTP view.
var (
	cycleMu   sync.Mutex
	cycleInfo httpapi.LastCycle
)

func recordCycle(started time.Time, updated int) {
	cycleMu.Lock()
	cycleInfo = httpapi.LastCycle{
		At: started.UTC(), WatchesUpdated: updated,
		DurationMs: time.Since(started).Milliseconds(),
	}
	cycleMu.Unlock()
}

func lastCycleInfo() httpapi.LastCycle {
	cycleMu.Lock()
	defer cycleMu.Unlock()
	return cycleInfo
}

// newMCPBearerToken generates the token the HTTP API gateway and the MCP route
// share. The connection file publishes it, so a remote client sends the same
// bearer value to both checks.
func newMCPBearerToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mcp: bearer token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// startHTTPAPI is called only by the process that owns the poller lock. The
// returned function stops the server and removes its connection file.
func startHTTPAPI(_ context.Context, cfg reconcileConfig, bus *httpapi.EventBus) (func(), error) {
	token, err := newMCPBearerToken()
	if err != nil {
		return nil, err
	}
	mcpServer := newMCPServer(cfg.deps)
	srv, err := httpapi.Start(httpapi.Config{
		Addr:          strings.TrimSpace(os.Getenv(httpapi.AddrEnv)),
		StateDir:      cfg.dir,
		Version:       mcp.ServerVersion,
		ProjectKey:    cfg.projectKey,
		IsPollerOwner: true,
		StartedAt:     time.Now(),
		Token:         token,
		ExtraRoutes:   server.MCPExtraRoutes(mcpServer, token),
		Provider: httpStateProvider{
			db: cfg.db, dir: cfg.dir, projectKey: cfg.projectKey, deps: cfg.deps,
		},
		Sink:   newHumanDecisionSink(cfg.db, reconcileWaker(cfg.db), bus),
		Events: bus,
	})
	if err != nil {
		return nil, err
	}
	return func() {
		if err := srv.Close(); err != nil {
			observe.ActorError("httpapi-close", "", err)
		}
	}, nil
}

// ---------------------------------------------------------------------------
// Signed webhook receiver (T3.376).
//
// The transport lives in infra/webhook and binds 127.0.0.1 only. This file
// supplies the recorder adapter and rebuilds the dedupe index from the durable
// journal at startup, so the receiver depends on a port rather than on store.
// ---------------------------------------------------------------------------

// webhookRestorePageSize bounds one read of the delivery journal at startup.
const webhookRestorePageSize = 256

// restoreWebhookFilter builds the receiver's dedupe filter and rebuilds its
// index from the durable journal one page at a time. A restart then rejects a
// redelivery before any effect is queued. The store keeps its own idempotency;
// this only refills the in-memory index.
func restoreWebhookFilter(db *store.DB) (*webhook.Filter, error) {
	f := webhook.NewFilter(webhook.FilterConfig{})
	if db == nil {
		return f, nil
	}
	cursor := ""
	for {
		page, err := db.ListWebhookDeliveries(context.Background(), cursor, webhookRestorePageSize)
		if err != nil {
			return nil, fmt.Errorf("webhook: restore dedupe index: %w", err)
		}
		for _, row := range page.Rows {
			f.Restore(store.RawWebhookDeliveryID(row.DeliveryID), row.HeadSHA)
		}
		if page.Next == "" {
			return f, nil
		}
		cursor = page.Next
	}
}

// webhookDeliveryRecorder gates a delivery through the restored filter and
// then writes it to the durable journal. A repeat fails the filter before the
// write. RecordDelivery reports Duplicate and Conflict when a row already
// exists, and the receiver treats both as success after its 202.
type webhookDeliveryRecorder struct {
	filter  *webhook.Filter
	journal *store.DB
}

func (r webhookDeliveryRecorder) Record(ctx context.Context, d webhook.Delivery) (webhook.RecordResult, error) {
	if r.filter == nil || r.journal == nil {
		return webhook.RecordResult{}, errors.New("webhook: nil delivery filter or journal")
	}
	if !r.filter.ClaimDelivery(d.ID) {
		return webhook.RecordResult{Duplicate: true}, nil
	}
	result, err := r.journal.RecordDelivery(store.WebhookDelivery{
		DeliveryID: d.ID,
		Event:      d.Event,
		Action:     d.Action,
		ReceivedAt: d.At.UnixNano(),
	})
	if err != nil {
		return webhook.RecordResult{}, err
	}
	return webhook.RecordResult{
		Created:   result.Created,
		Duplicate: result.Duplicate,
		Conflict:  result.Conflict,
	}, nil
}

// startWebhookReceiver loads the 0600 secret from the state directory and
// starts the loopback listener. A missing secret or a bad address is logged and
// skipped: an optional receiver must never take down the stdio MCP transport.
func startWebhookReceiver(cfg reconcileConfig) (func(), error) {
	filter, err := restoreWebhookFilter(cfg.db)
	if err != nil {
		return nil, err
	}
	srv, err := webhook.Start(webhook.Config{
		Addr:       os.Getenv(webhook.WebhookAddrEnv),
		SecretPath: filepath.Join(cfg.dir, webhook.SecretFileName),
		Recorder:   webhookDeliveryRecorder{filter: filter, journal: cfg.db},
	})
	if err != nil {
		return nil, err
	}
	return func() {
		if err := srv.Close(); err != nil {
			observe.ActorError("webhook-close", "", err)
		}
	}, nil
}

// ---------------------------------------------------------------------------
// Wake ladder wiring (ADR 0017).
//
// The ladder core lives in internal/infra/ladder and depends on narrow ports.
// This composition root binds those ports to the single *store.DB handle, the
// wake registry and the project orchestrator source. No file operation lives
// here: rung 2's ESCALATIONS.md append is done by the ladder package.
// ---------------------------------------------------------------------------

// ladderWallClock is the real clock handed to the escalator. The transitions
// themselves take time from the caller and read no clock.
type ladderWallClock struct{}

func (ladderWallClock) Now() time.Time { return time.Now() }

// ladderStamper stops every ladder timer as soon as a read or claim is stamped.
// beacon_get_result and beacon_ack both go through ObligationStamper, so
// decorating it keeps the tools and the bridge read path in sync without the
// mcp package importing the ladder (ADR 0005).
type ladderStamper struct {
	mcp.ObligationStamper
	ladder *ladder.Ladder
}

func (s ladderStamper) StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	o, applied, err := s.ObligationStamper.StampRead(ctx, id, at)
	if err != nil || !applied {
		return o, applied, err
	}
	stopLadder(s.ladder, ctx, id, at, true)
	return o, applied, nil
}

func (s ladderStamper) StampClaimed(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	o, applied, err := s.ObligationStamper.StampClaimed(ctx, id, at)
	if err != nil || !applied {
		return o, applied, err
	}
	stopLadder(s.ladder, ctx, id, at, false)
	return o, applied, nil
}

// ladderWaitWriter decorates beacon_wait_result's writer. A waiting caller that
// receives a verdict has read it, so all ladder timers stop with the stamp.
type ladderWaitWriter struct {
	mcp.WaitResultWriter
	ladder *ladder.Ladder
}

func (w ladderWaitWriter) MarkRead(ctx context.Context, obligationID string, at time.Time) error {
	if err := w.WaitResultWriter.MarkRead(ctx, obligationID, at); err != nil {
		return err
	}
	stopLadder(w.ladder, ctx, obligationID, at, true)
	return nil
}

// stopLadder cancels the timers of an obligation that was just read or claimed.
// An obligation that was never staged has no ladder to stop, which is the
// normal case for legacy obligations, so ErrNotStaged is not a tool failure.
func stopLadder(l *ladder.Ladder, ctx context.Context, id string, at time.Time, read bool) {
	if l == nil {
		return
	}
	var err error
	if read {
		err = l.MarkRead(ctx, id, at)
	} else {
		err = l.MarkClaimed(ctx, id, at)
	}
	if err != nil && !errors.Is(err, ladder.ErrNotStaged) {
		observe.ActorError("ladder-stop", id, err)
	}
}

// bridgeLadder adapts *ladder.Ladder to the domain-shaped port cibridge takes.
// Only the Record signature differs; Stage already matches exactly.
type bridgeLadder struct{ l *ladder.Ladder }

func (b bridgeLadder) Stage(ctx context.Context, o domain.Obligation, verdict domain.Verdict, at time.Time) error {
	return b.l.Stage(ctx, o, verdict, at)
}

func (b bridgeLadder) Record(ctx context.Context, obligationID string, attempt int, outcome domain.AttemptOutcome, at time.Time) error {
	return b.l.Record(ctx, ladder.Receipt{
		ObligationID: obligationID, Attempt: attempt, Outcome: outcome, At: at,
	})
}

// ladderCounts reads the pending and escalated counters of beacon_stats.ladder
// through the single writer the ladder uses. A failed read logs and reports
// zero, so a broken counter never fails the observability tool.
func ladderCounts(db *store.DB) (int, int) {
	if db == nil {
		return 0, 0
	}
	l := ladder.New(db)
	pending, err := l.PendingCount(context.Background())
	if err != nil {
		observe.ActorError("ladder-pending", "", err)
	}
	escalated, err := ladder.NewEscalationStore(db).EscalatedCount(context.Background())
	if err != nil {
		observe.ActorError("ladder-escalated", "", err)
	}
	return pending, escalated
}

// runMCP reads the connection file on every startup, applies the endpoint and
// then composes the dependencies and serves them until the client leaves.
// Re-reading the file is the reconnect: a daemon that moved to another address,
// port or token is observed on the next start, while the watches stay in the
// on-disk store and continue to wait.
//
// The optional CLI argument exists so tests can inject a child runner without
// changing the two-argument mcpRunner seam.
func runMCP(ctx context.Context, cli ...*CLI) error {
	c := newDefaultCLI()
	if len(cli) > 0 && cli[0] != nil {
		c = cli[0]
	}

	// A run starts from a clean precheck state. A failure from an earlier run
	// in the same process must not leak into this server's wait_result tool.
	setPrecheckFailureReason("")

	dir, err := stateDir()
	if err != nil {
		return err
	}
	conn, err := readMCPConnection(dir)
	if err != nil {
		return err
	}

	// ADR 0041 §6: precheck the init-event configuration before the server
	// enters its wait loop. With no bypass for an expected init event this
	// fails fast, so the run reports precheck_failure instead of hanging.
	// T3.84: the reason is also installed on the wait_result service that the
	// production server builds, so a tools/call sees precheck_failure with the
	// same reason instead of only stderr.
	if err := mcpPrecheck(ctx, readInitEventConfig()); err != nil {
		var failure *precheckFailureError
		if errors.As(err, &failure) {
			setPrecheckFailureReason(failure.reason)
		}
		return err
	}

	if err := mcpConnector(ctx, conn); err != nil {
		return fmt.Errorf("mcp: connect to %s: %w", conn.target(), err)
	}
	activeMCPConnection = conn

	// BS.42: in remote mode the stdio process is a thin client. Every tool call
	// goes to the server over HTTP, so a stopped server is reported as an
	// honest error instead of being answered from a stale local store. No DB
	// handle, reconciler or poller is created on this path.
	if remoteToolsEnabled() {
		if conn.target() == "" {
			return fmt.Errorf("mcp: %s требует connection.json", mcpRemoteEnv)
		}
		client, err := mcp.NewRemoteClient(conn.remoteEndpoint(), nil)
		if err != nil {
			return fmt.Errorf("mcp: remote client: %w", err)
		}
		return serveMCPRemote(ctx, client)
	}

	// One DB handle for the whole process. The same pointer backs the MCP
	// tools, the cibridge control plane, the stats snapshot and the metrics
	// writer, so a verdict written by the loop is visible to the next tool.
	db, err := openProcessStore(dir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ghBatch := newGitHubBatch(ctx, c)
	deps := buildMCPDependencies(db, dir, ghBatch.provider.PointStats)
	defer stopVerdictSource(deps.Source)
	_, resolver := storeResolverPorts(c, db)

	events := httpapi.NewEventBus(0)
	cfg := reconcileConfig{
		dir:        dir,
		db:         db,
		deps:       deps,
		batch:      ghBatch.provider,
		counter:    ghBatch.counter,
		projectKey: orchestratorProjectKey(),
		root:       projectRootForConfig(),
		resolver:   resolver,
		events:     events,
		autoSpawn:  &autoSpawner{start: autoSpawnStartSession},
		gate: &autoSpawnGate{
			// A stored blocker comes from a failed required check. Informational
			// checks (required: false) never create one, so they do not close the
			// gate. The live-token source is issued by the daemon slot ledger and
			// is wired by the follow-up WIRING card.
			mainBlocked: func() (bool, error) {
				blockers, err := db.ActiveBlockers()
				if err != nil {
					return false, err
				}
				return len(blockers) > 0, nil
			},
		},
	}

	// Only the owner of the non-blocking poller lock starts the single
	// background cycle and the loopback HTTP API. A second MCP process serves
	// tools but stays silent and owns no connection file.
	var (
		stopHTTP    func()
		httpErr     error
		stopWebhook func()
		webhookErr  error
	)
	cfg.onPollerOwner = func(c context.Context) {
		stopHTTP, httpErr = startHTTPAPI(c, cfg, events)
		stopWebhook, webhookErr = startWebhookReceiver(cfg)
	}
	runCtx, stopLoops := context.WithCancel(ctx)
	defer stopLoops()
	stop, err := runReconciler(runCtx, cfg)
	if err != nil {
		return fmt.Errorf("mcp: poller: %w", err)
	}
	defer stop()
	if err := publishPollerRuntimeStats(db); err != nil {
		observe.ActorError("poller-stats", "", err)
	}
	if httpErr != nil {
		// An optional API must never take down the stdio MCP transport.
		observe.ActorError("httpapi", "", httpErr)
	} else if stopHTTP != nil {
		defer stopHTTP()
	}
	if webhookErr != nil {
		// A receiver without a valid secret cannot serve. Log the config
		// error and keep the MCP transport alive.
		observe.ActorError("webhook", "", webhookErr)
	} else if stopWebhook != nil {
		defer stopWebhook()
	}

	return serveMCP(ctx, deps)
}

// pollerLockName is the non-blocking flock file that elects one background
// poller per state directory. Its text body holds the PID of the poller owner,
// so a restarted MCP can tell a live poller from a stale lock.
const pollerLockName = "beacon-poller.lock"

// pollerLockOpen is the capabilities-gate seam for opening the lock file. The
// call goes through the variable, so cmd/beacon never names os.OpenFile
// directly (ADR 0005).
var pollerLockOpen = os.OpenFile

// pollerProcessAlive checks a PID with signal 0. It is a seam so tests can
// simulate a dead owner without spawning a real process.
var pollerProcessAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// readPollerPID returns the PID stored in the lock file. A missing file has no
// owner. A malformed or non-positive body is a stale lock, so it reports zero
// and no error and the caller replaces it with its own PID.
func readPollerPID(dir string) (int, error) {
	data, err := fs.ReadFile(os.DirFS(dir), pollerLockName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, nil
	}
	return pid, nil
}

// writePollerPID replaces the lock file body with pid. It uses the held file
// handle, so cmd/beacon never names os.WriteFile (ADR 0005).
func writePollerPID(f *os.File, pid int) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := fmt.Fprintf(f, "%d\n", pid)
	return err
}

// pollerLockResult is the outcome of the lock check. owned tells whether this
// process must start the loop. activePID names the process recorded as the
// current poller, either this one or a live predecessor.
type pollerLockResult struct {
	owned     bool
	activePID int
}

// acquirePollerLock takes LOCK_EX|LOCK_NB on <dir>/beacon-poller.lock and
// checks the recorded PID. A live PID from another process means a poller
// already runs, so this process stays silent. A missing or dead PID means the
// lock is stale: this process takes ownership and records its own PID.
func acquirePollerLock(dir string) (func(), pollerLockResult, error) {
	path := filepath.Join(dir, pollerLockName)
	f, err := pollerLockOpen(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, pollerLockResult{}, err
	}
	fail := func(err error) (func(), pollerLockResult, error) {
		_ = f.Close()
		return nil, pollerLockResult{}, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			// Another process holds the flock. The PID is best-effort
			// diagnostic information, not the reason we skip the loop.
			pid, _ := readPollerPID(dir)
			if !pollerProcessAlive(pid) {
				pid = 0
			}
			return nil, pollerLockResult{activePID: pid}, nil
		}
		return nil, pollerLockResult{}, err
	}
	pid, err := readPollerPID(dir)
	if err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return fail(err)
	}
	if pid > 0 && pid != os.Getpid() && pollerProcessAlive(pid) {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, pollerLockResult{activePID: pid}, nil
	}
	self := os.Getpid()
	if err := writePollerPID(f, self); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return fail(err)
	}
	release := func() {
		_ = writePollerPID(f, 0)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, pollerLockResult{owned: true, activePID: self}, nil
}

// pollerMetrics carries the poller gauges published to runtime_stats. The
// server metric set keeps active and PID together and fixes their JSON names.
var pollerMetrics = server.NewMetrics()

// publishPollerStatus records the poller state on both diagnostic paths. mcp
// exposes it in beacon_stats, while runtime_stats carries it to beacon inspect.
func publishPollerStatus(activePID int) {
	status := mcp.PollerStatus{Active: activePID > 0, PID: activePID}
	mcp.SetPollerStatus(status)
	pollerMetrics.SetPoller(status.Active, status.PID)
}

// withPollerStats adds the poller gauges to a runtime_stats snapshot. A broken
// snapshot is replaced by the poller values only, so a corrupt row cannot hide
// the state of the running poller.
func withPollerStats(raw []byte) []byte {
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			fields = map[string]json.RawMessage{}
		}
	}
	encoded, err := json.Marshal(pollerMetrics.Poller())
	if err != nil {
		return raw
	}
	var gauges map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &gauges); err != nil {
		return raw
	}
	for key, value := range gauges {
		fields[key] = value
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return append(out, '\n')
}

// publishPollerRuntimeStats writes the current poller state without waiting
// for the first reconcile pass. A second MCP process with a live poller owner
// still reports the owner's PID in beacon inspect.
func publishPollerRuntimeStats(db *store.DB) error {
	raw, _, _ := db.LoadRuntimeStats()
	return db.SaveRuntimeStats(withPollerStats(raw), time.Now())
}

// reconcileConfig bundles the one store handle and the ports the single
// background cycle needs. An intervalOverride above zero replaces the adaptive
// 10s/30s interval, which the daemon command uses for its explicit flag.
type reconcileConfig struct {
	dir              string
	db               *store.DB
	deps             mcp.Dependencies
	batch            *github.BatchProvider
	counter          *github.Counter
	projectKey       string
	root             string
	resolver         orchestrator.DependencyResolver
	intervalOverride time.Duration
	// events carries live cycle and decision frames to SSE subscribers. A
	// config assembled by `beacon daemon` leaves it nil.
	events *httpapi.EventBus
	// onPollerOwner runs once when this process wins the poller lock. runMCP
	// uses it to start the loopback API only on the owning process.
	onPollerOwner func(context.Context)
	// autoSpawn is the one seam the reconciler calls per pass to start the
	// next queued card. A nil spawner leaves the cycle unchanged. Only the MCP
	// process assembles a config with one; daemon and up leave it nil.
	autoSpawn *autoSpawner
	// gate supplies the two signals that open a spawn slot: a live token from
	// a report_to_orchestrator terminal signal and a green main branch. A nil
	// gate or nil function leaves that check unset.
	gate *autoSpawnGate
}

// autoSpawnGate is the decision input for one auto-spawn. liveTokens counts
// slot tokens opened by report_to_orchestrator that are still live. A spawn
// needs at least one. mainBlocked reports a failed required check on the base
// branch; informational checks (required: false) do not set it.
type autoSpawnGate struct {
	liveTokens  func() int
	mainBlocked func() (bool, error)
}

// autoSpawner starts one agent for one selected card. start returns true when
// the session and its first prompt were created. Splitting the decision from
// the start keeps slot counting and OwnerFiles admission testable without a
// live OpenCode server.
type autoSpawner struct {
	start func(ctx context.Context, cfg reconcileConfig, cardID, title string) bool
}

// githubBatch is the real CI provider plus the shared point counter.
type githubBatch struct {
	provider *github.BatchProvider
	counter  *github.Counter
}

// githubAPIURL is the REST base used to seed the shared point counter.
const githubAPIURL = "https://api.github.com"

// newGitHubBatch builds the batch provider behind a seam so tests never need
// a token or a network. Every merge it performs carries --repo through the
// CLI runner, so the target never depends on the working directory.
var newGitHubBatch = func(ctx context.Context, c *CLI) githubBatch {
	// Token resolution shells out to `gh auth token`. It needs its own budget:
	// the counter GET used to spend the same three seconds, leaving ResolveToken
	// with an expired context, an empty token and a 403 on every CI read.
	tokenCtx, tokenCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer tokenCancel()
	token, err := github.ResolveToken(tokenCtx)
	if err != nil {
		token = ""
	}

	// Only the rate_limit GET keeps the short deadline.
	counterCtx, counterCancel := context.WithTimeout(ctx, 3*time.Second)
	defer counterCancel()
	counter, err := github.NewCounter(counterCtx, nil, githubAPIURL)
	if err != nil {
		counter = nil
	}

	graph := github.NewClient("", token, nil, counter)
	rest, err := github.NewRestClient(tokenCtx, counter)
	if err != nil {
		rest = nil
	}
	var opts []github.BatchOption
	if c != nil && c.run != nil {
		opts = append(opts, github.WithRunner(cliRunner{c: c}))
	}
	return githubBatch{provider: github.NewBatchProvider(graph, rest, counter, opts...), counter: counter}
}

// defaultCIRepo keeps the identifier older callers read, but it no longer
// carries a hardcoded owner/name. It resolves from the project config or the
// git remote once at startup; a caller with an explicit --repo passes its own
// value straight to the source.
var defaultCIRepo = github.DetectRepoSlug(projectRootForConfig())

// projectRootForConfig resolves the absolute project root used by the config
// fallback of the orchestrator-id source.
func projectRootForConfig() string {
	root := projectRootFromEnv()
	if root != "" {
		return root
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// orchestratorProjectKey returns the stable project key used by the SQLite
// orchestrator binding.
func orchestratorProjectKey() string {
	root := projectRootForConfig()
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	key, err := projectStateKey(abs)
	if err != nil {
		return ""
	}
	return key
}

// orchestratorIDSource reads the bound orchestrator from SQLite first and
// falls back to the project config. An empty result leaves a blocked Watch
// unsent rather than guessing a session.
func orchestratorIDSource(db *store.DB, projectKey, root string) cibridge.OrchestratorIDSource {
	return cibridge.OrchestratorIDSource(func() string {
		if binding, ok, err := db.BoundOrchestrator(projectKey); err == nil && ok {
			if id := strings.TrimSpace(binding.SessionID); id != "" {
				return id
			}
		}
		cfg, err := orchestrator.LoadProjectConfig(root)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(cfg.OrchestratorID)
	})
}

// opencodeWakePorts adapts the live opencode client to the HTTP ports of the
// wake deliverer. A missing service registration yields nil ports, so the
// deliverer falls back to its own CLI path instead of faking delivery.
func opencodeWakePorts() (wake.PromptWithIDFunc, wake.SessionActiveFunc) {
	port, err := newReadOnlyOpencodePort()
	if err != nil || port == nil {
		return nil, nil
	}
	client, ok := port.(*opencode.Client)
	if !ok {
		return nil, nil
	}
	prompt := func(ctx context.Context, sessionID, text, delivery, obligationID string, resume bool) error {
		opts := opencode.StartPromptOptions(obligationID)
		opts.Delivery = delivery
		opts.Resume = resume
		return client.PromptWithOptions(ctx, sessionID, text, opts)
	}
	active := func(ctx context.Context, sessionID string) (bool, bool, error) {
		sessions, err := client.Sessions(ctx)
		if err != nil {
			return false, false, err
		}
		for _, s := range sessions {
			if s.ID != sessionID {
				continue
			}
			return s.Time.Busy > 0 && s.Time.Idle == 0, true, nil
		}
		return false, false, fmt.Errorf("opencode: session %s: %w", sessionID, wake.ErrSessionDead)
	}
	return prompt, active
}

// reconcileWaker installs the HTTP-backed OpenCode deliverer as the opencode
// adapter of one registry. The registry stays the adapter-aware port cibridge
// expects; no subscriber name is interpreted here.
func reconcileWaker(db *store.DB) *wake.Registry {
	registry := wake.NewRegistry()
	prompt, active := opencodeWakePorts()
	registry.Register(wake.AdapterOpenCode, &wake.OpenCodeDeliverer{
		PromptWithIDFunc:  prompt,
		SessionActiveFunc: active,
		Obligations:       db,
	})
	return registry
}

// ladderPointerSender delivers rung 1 pings through the same wake registry the
// bridge uses. The payload carries identifiers only, never result content
// (ADR 0017). An empty registry is a no-op so an unconfigured host cannot fake
// a delivery.
type ladderPointerSender struct {
	registry *wake.Registry
}

func (s ladderPointerSender) Send(ctx context.Context, target string, p domain.Pointer) error {
	if s.registry == nil || target == "" {
		return nil
	}
	return s.registry.Deliver(ctx, wake.AdapterOpenCode, target, domain.WakePayload{
		WatchID:      p.WatchID,
		ObligationID: p.ObligationID,
		Revision:     p.Revision,
		Verdict:      "ladder rung 1: снимите результат через beacon_get_result",
	})
}

// makeEscalator binds the escalation core to the process' single store handle,
// the wake registry and the project orchestrator source. The ESCALATIONS.md
// path stays in the state directory next to the store (ADR 0017, T3.10).
func makeEscalator(cfg reconcileConfig) *ladder.Escalator {
	orchestratorID := orchestratorIDSource(cfg.db, cfg.projectKey, cfg.root)
	return ladder.NewEscalator(
		ladder.NewStagedEpisodeStore(cfg.db, orchestratorID),
		ladder.NewEscalationStore(cfg.db),
		ladderPointerSender{registry: reconcileWaker(cfg.db)},
		ladderWallClock{},
		filepath.Join(cfg.dir, "ESCALATIONS.md"),
	)
}

// resendLadderPointer repeats the same obligationID and revision for a
// transport-failed attempt. It records a posted receipt on success so the
// branch switches to the no-retry evidence wait; a delivery failure leaves the
// transport-failed branch armed for the next retry.
func resendLadderPointer(ctx context.Context, cfg reconcileConfig, waker *wake.Registry, l *ladder.Ladder, action ladder.Action, now time.Time) error {
	o, err := cfg.db.Obligation(action.ObligationID)
	if err != nil {
		return err
	}
	w, err := cfg.db.Watch(o.WatchID)
	if err != nil {
		return err
	}
	var target, adapter string
	for _, sub := range w.Subscribers {
		if sub.ID != o.SubscriberID {
			continue
		}
		target = sub.Target
		if target == "" {
			target = sub.ID
		}
		adapter = sub.Adapter
		break
	}
	if target == "" {
		return nil
	}
	if adapter == "" {
		adapter = wake.AdapterOpenCode
	}
	p := domain.WakePayload{
		WatchID:      o.WatchID,
		ObligationID: action.ObligationID,
		Revision:     o.Revision,
		Verdict:      "ladder retry: снимите результат через beacon_get_result",
	}
	if err := waker.Deliver(ctx, adapter, target, p); err != nil {
		return err
	}
	return l.Record(ctx, ladder.Receipt{
		ObligationID: action.ObligationID,
		Attempt:      action.Attempt,
		Outcome:      domain.OutcomePosted,
		At:           now,
	})
}

// runLadderPass advances the due ADR 0017 timers and runs the escalation rungs
// in the same cycle as the CI bridge. The ladder marker is the only clock, so
// a process restart resumes at the last persisted position.
func runLadderPass(ctx context.Context, cfg reconcileConfig) {
	if cfg.db == nil {
		return
	}
	l := ladder.New(cfg.db)
	now := time.Now()
	due, err := l.Due(ctx, now)
	if err != nil {
		observe.ActorError("ladder-due", "", err)
		return
	}
	var waker *wake.Registry
	for _, d := range due {
		action, err := l.Advance(ctx, d.ObligationID, d.At)
		if err != nil {
			observe.ActorError("ladder-advance", d.ObligationID, err)
			continue
		}
		switch action.Kind {
		case ladder.ActionRetry:
			if waker == nil {
				waker = reconcileWaker(cfg.db)
			}
			if err := resendLadderPointer(ctx, cfg, waker, l, action, now); err != nil {
				observe.ActorError("ladder-retry", action.ObligationID, err)
			}
		case ladder.ActionEvidence:
			// Claimed and read already stopped the marker. A quiet episode is
			// handed to the escalation rungs below.
		}
	}
	if _, err := makeEscalator(cfg).Run(ctx); err != nil {
		observe.ActorError("ladder-escalation", "", err)
	}
}

// bridgeWorktreeGC binds the cibridge WorktreeGC port to the real collector.
// Heads are read from the same DB handle the bridge uses, so a Watch that is
// still watching or blocked protects its worktree at collection time. The
// current project root is protected as well, so a process never removes the
// directory it runs from. The bridge declares the port and this composition
// root owns the adapter, so the layer matrix stays unchanged (ADR 0005).
func bridgeWorktreeGC(db *store.DB, root string) cibridge.WorktreeGC {
	return func(ctx context.Context) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if db == nil {
			return 0, nil
		}
		watches, err := db.LiveWatches()
		if err != nil {
			return 0, fmt.Errorf("worktree gc: live watches: %w", err)
		}
		heads := make(map[string]string, len(watches))
		for _, w := range watches {
			if w.HeadSHA == "" {
				continue
			}
			heads[strings.ToLower(w.HeadSHA)] = w.ID
		}
		protect := ""
		if root != "" {
			if abs, err := filepath.Abs(root); err == nil {
				protect = abs
			}
		}
		result, err := worktree.Collect(worktree.CommandRunner, heads, protect)
		if err != nil {
			return 0, fmt.Errorf("worktree gc: collect: %w", err)
		}
		return len(result.Removed), nil
	}
}

// reconcileMaintenance runs the durable housekeeping of one pass: archive
// closed watches, checkpoint the WAL, publish the footprint gauges and return
// abandoned in_progress cards to the queue (MEM-04). A failure in one piece is
// reported through observe and never stops the cycle. Both daemon modes reach
// this helper because --once calls ReconcilePass as well as the loop.
func reconcileMaintenance(ctx context.Context, cfg reconcileConfig) {
	if cfg.db == nil {
		return
	}
	if _, err := cfg.db.ArchiveRun(ctx, time.Now()); err != nil {
		observe.ActorError("archive", "", err)
	}
	// DB.Checkpoint queries writeDB, whose only connection is held by the
	// dedicated writer for the process lifetime. A direct call waits forever.
	// Run the same PRAGMA on that writer so the checkpoint can complete.
	if err := cfg.db.Write(ctx, func(tx *sql.Tx) error {
		var busy, log, checkpointed int
		return tx.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").
			Scan(&busy, &log, &checkpointed)
	}); err != nil {
		observe.ActorError("checkpoint", "", err)
	}

	watches, watchErr := cfg.db.LiveWatches()
	activeWatches := len(watches)
	if watchErr != nil {
		observe.ActorError("live-watches", "", watchErr)
	}
	warn := func(format string, args ...any) {
		observe.ActorError("footprint", "", fmt.Errorf(format, args...))
	}
	// CheckLaunchFootprint is the AUTOSP-11 launch decision. It publishes the
	// RSS gauge through FootprintCheck and calls warn only when the process is
	// over budget, so an unreadable RSS reading stays informational.
	server.CheckLaunchFootprint(pollerMetrics, activeWatches, server.ProcessRSSBytes, warn)
	if watchErr != nil {
		return
	}

	// A card with a live Watch still has a worker. The reaper needs that fact
	// from the bridge, so map each live Watch back to its linked card before
	// releasing leases. An incomplete map skips the reaper for this pass.
	liveCards := make(map[string]bool, len(watches))
	for _, w := range watches {
		cardID, err := linkedCardID(ctx, cfg.db, w.ID)
		if err != nil {
			observe.ActorError("card-liveness", w.ID, err)
			return
		}
		if cardID != "" {
			liveCards[cardID] = true
		}
	}
	svc := taskcli.NewService(taskcli.Config{
		TasksDir: filepath.Join(cfg.root, "docs", "tasks"),
		StateDir: cfg.dir,
		Root:     cfg.root,
	})
	if _, err := svc.ReapStaleCards(taskcli.ReapOptions{
		WatchAlive: func(cardID string) (bool, error) { return liveCards[cardID], nil },
	}); err != nil {
		observe.ActorError("reaper", "", err)
	}
}

// autoSpawnSlotTotal reads the configured parallel-slot budget (SL-01) from
// the project's agents.yaml. A missing config or a malformed value falls back
// to the documented default. The old hardcoded four never reached this code.
func autoSpawnSlotTotal(root string) int {
	rc, err := findRegistryConfig(root)
	if err != nil {
		return registry.DefaultSlotBudget
	}
	configured, err := slotBudgetFromConfig(rc.Path)
	if err != nil {
		observe.ActorError("auto-spawn-slot-budget", "", err)
		return registry.DefaultSlotBudget
	}
	total, err := registry.ResolveSlotBudget(configured)
	if err != nil {
		observe.ActorError("auto-spawn-slot-budget", "", err)
		return registry.DefaultSlotBudget
	}
	return total
}

// autoSpawnSlotsUsed counts occupied slots from live work. A Watch is a PR
// signal, not an agent, so it is not counted. Live cards and live slot tokens
// both occupy a slot; the larger count wins so one agent is not counted twice.
func autoSpawnSlotsUsed(cards []taskstore.Card, liveTokens int) int {
	liveCards := 0
	for _, c := range cards {
		if c.Status == "in_progress" || c.Status == "reserved" {
			liveCards++
		}
	}
	if liveTokens > liveCards {
		return liveTokens
	}
	return liveCards
}

// autoSpawnSourceRank orders candidate cards by source. Deferred wiring wins,
// then the card index and graph, then owner questions.
func autoSpawnSourceRank(id string) int {
	switch {
	case strings.HasPrefix(id, "WIRING-"):
		return 0
	case strings.HasPrefix(id, "Q-"), strings.HasPrefix(id, "OWNER-"):
		return 2
	default:
		return 1
	}
}

func autoSpawnGateIncident(kind, cardID, detail string) {
	observe.Event(observe.LevelWarn, "auto_spawn_gate", observe.Fields{
		"kind": kind, "card": cardID, "detail": detail,
	})
}

// reconcileAutoSpawn is the minimal slot spawner. One pass starts at most one
// agent: it needs a free slot measured in live cards and slot tokens, a live
// terminal signal, a green main branch and a todo card whose OwnerFiles are not
// held by an in_progress card. Slot and file admission still come from the
// AUTOSP-06 gate. It returns the number of agents started (0 or 1).
func reconcileAutoSpawn(ctx context.Context, cfg reconcileConfig, sp *autoSpawner) int {
	if sp == nil || sp.start == nil || cfg.db == nil || cfg.root == "" {
		return 0
	}
	tasksDir := filepath.Join(cfg.root, "docs", "tasks")
	cards, err := taskstore.LoadCards(tasksDir)
	if err != nil {
		observe.ActorError("auto-spawn-cards", "", err)
		return 0
	}

	liveTokens := 0
	if cfg.gate != nil && cfg.gate.liveTokens != nil {
		liveTokens = cfg.gate.liveTokens()
	}
	slotsUsed := autoSpawnSlotsUsed(cards, liveTokens)
	slotsTotal := autoSpawnSlotTotal(cfg.root)

	// Cards in progress hold their files. A todo candidate that touches one of
	// them cannot take a slot this pass.
	active := make([]taskcli.LaunchAssignment, 0, len(cards))
	for _, c := range cards {
		if c.Status != "in_progress" || len(c.OwnerFiles) == 0 {
			continue
		}
		active = append(active, taskcli.LaunchAssignment{
			Name: c.ID, Files: c.OwnerFiles, Status: c.Status,
		})
	}
	// Collect every eligible card, then order by source and ID. The first card
	// is the one spawn this pass may start.
	candidates := make([]taskstore.Card, 0, len(cards))
	for i := range cards {
		c := cards[i]
		if c.Status != "todo" || len(c.OwnerFiles) == 0 {
			continue
		}
		ca := taskcli.LaunchAssignment{Name: c.ID, Files: c.OwnerFiles, Status: c.Status}
		conflict := false
		for _, a := range active {
			if _, _, ok := taskcli.IntersectingFiles(a, ca); ok {
				conflict = true
				break
			}
		}
		if !conflict {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return 0
	}
	sort.Slice(candidates, func(i, j int) bool {
		ri, rj := autoSpawnSourceRank(candidates[i].ID), autoSpawnSourceRank(candidates[j].ID)
		if ri != rj {
			return ri < rj
		}
		return candidates[i].ID < candidates[j].ID
	})
	candidate := &candidates[0]

	// A slot opens only on a live report_to_orchestrator token and a main
	// branch without a failed required check. A nil gate function leaves that
	// signal unset; a configured function that refuses closes the gate.
	if cfg.gate != nil {
		if cfg.gate.liveTokens != nil && liveTokens <= 0 {
			autoSpawnGateIncident("no_terminal_token", candidate.ID, "no live report_to_orchestrator slot token")
			return 0
		}
		if cfg.gate.mainBlocked != nil {
			blocked, err := cfg.gate.mainBlocked()
			if err != nil {
				observe.ActorError("auto-spawn-main-check", candidate.ID, err)
				return 0
			}
			if blocked {
				autoSpawnGateIncident("main_blocked", candidate.ID, "required check failed on main")
				return 0
			}
		}
	}

	q := github.NewPreflightGuard(cfg.counter).Check(ctx)
	decision := taskcli.NewService(taskcli.Config{
		TasksDir: tasksDir, StateDir: cfg.dir, Root: cfg.root,
	}).AdmitLaunch(
		taskcli.PreflightResult{Allowed: q.Allowed, Reason: q.Reason},
		slotsUsed, slotsTotal,
		append(active, taskcli.LaunchAssignment{
			Name: candidate.ID, Files: candidate.OwnerFiles, Status: candidate.Status,
		})...,
	)
	if !decision.Allowed {
		autoSpawnGateIncident("launch_denied", candidate.ID, decision.Reason)
		observe.ActorError("auto-spawn-denied", candidate.ID, errors.New(decision.Reason))
		return 0
	}
	if !sp.start(ctx, cfg, candidate.ID, candidate.Title) {
		return 0
	}
	return 1
}

// autoSpawnStartSession creates the child session and sends the first prompt.
// It reuses the spawn port and the same deterministic prompt options as the
// beacon spawn command, so an auto-started agent follows the normal path.
func autoSpawnStartSession(ctx context.Context, cfg reconcileConfig, cardID, title string) bool {
	spawner := spawnSessionSpawner
	if spawner == nil {
		return false
	}
	parentID := orchestratorIDSource(cfg.db, cfg.projectKey, cfg.root)()
	metadata := map[string]string{spawnMetaTaskID: cardID}
	if title != "" {
		metadata[spawnMetaTitle] = title
	}
	// The agent_id is derived from the card id, so a parallel worker is
	// attributed without relying on the order in which tool events arrive. The
	// definition goes through the initialization channel, not a CLI argument.
	agentID := agentIDForCard(cardID)
	def := newAgentDefinition(agentID, "executor")
	metadata = attachAgentDefinition(metadata, &def)
	sessionID, err := spawner.CreateSession(parentID, agentID, title, metadata)
	if err != nil || strings.TrimSpace(sessionID) == "" {
		if err == nil {
			err = errors.New("auto-spawn: empty session id")
		}
		observe.ActorError("auto-spawn-create", cardID, err)
		return false
	}
	if err := spawner.PromptWithOptions(ctx, sessionID, spawnPrompt(cardID, title), opencode.StartPromptOptions(cardID)); err != nil {
		observe.ActorError("auto-spawn-prompt", cardID, err)
		return false
	}
	return true
}

// ReconcilePass runs one cycle: cibridge reads LiveWatches, derives verdicts
// with domain.EvaluatePR, merges and wakes through the wired ports, the wake
// ladder advances its due timers and escalation rungs, then the in-process
// counters are saved to runtime_stats. It also records the cycle duration in
// the run_duration_seconds histogram, because the daemon reaches this pass
// instead of orchestrator.RunWithResolver (PF-06). It returns the number of
// watches the bridge updated.
func ReconcilePass(ctx context.Context, cfg reconcileConfig) int {
	started := time.Now()
	runLadderPass(ctx, cfg)
	if cfg.autoSpawn != nil {
		reconcileAutoSpawn(ctx, cfg, cfg.autoSpawn)
	}
	if cfg.db == nil || cfg.batch == nil {
		recordCycle(started, 0)
		return 0
	}
	bridge := cibridge.New(cfg.db, cfg.batch, cfg.deps.Clock, 0)
	bridge.WithWaker(&wake.AdapterDedupDeliverer{
		Inner: reconcileWaker(cfg.db),
		Dedup: wake.SharedDeliveryDedup(),
	}).
		WithReader(cfg.deps.Stamper).
		WithLadder(bridgeLadder{l: ladder.New(cfg.db)}).
		WithOrchestratorIDSource(orchestratorIDSource(cfg.db, cfg.projectKey, cfg.root)).
		WithMerger(cfg.batch).
		WithAutoRebase(cfg.batch).
		WithBlockerIncidents(cfg.db).
		WithDependencyResolver(cfg.resolver).
		WithCardLinks(&deferredCardLinks{db: cfg.db, root: cfg.root}).
		WithWorktreeGC(bridgeWorktreeGC(cfg.db, cfg.root))
	updated := bridge.Refresh(ctx)
	reconcileMaintenance(ctx, cfg)
	// This reconciler cycle is the daemon's real pass. Observe its duration
	// before the snapshot is saved so this cycle's runtime_stats already
	// carries the point (PF-06). RunEnd owns the histogram write.
	observe.RunEnd(observe.CycleStats{Duration: time.Since(started)}, nil)
	if metrics := observe.MetricsOf(observe.Default()); metrics != nil {
		if err := cfg.db.SaveRuntimeStats(withPollerStats(metrics.SnapshotJSON()), time.Now()); err != nil {
			observe.ActorError("runtime_stats", "", err)
		}
	}
	recordCycle(started, updated)
	if cfg.events != nil {
		payload, _ := json.Marshal(httpapi.LastCycle{
			At: started.UTC(), WatchesUpdated: updated,
			DurationMs: time.Since(started).Milliseconds(),
		})
		cfg.events.Publish(httpapi.Event{Type: "cycle", Payload: payload})
	}
	return updated
}

// ReconcileLoop is the single background cycle of the process. The next
// interval comes from github.Interval: 10s while a Watch changed recently,
// 30s once it is quiet, stretched threefold when the point budget is spent.
func ReconcileLoop(ctx context.Context, cfg reconcileConfig) {
	var lastChange time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		if n := ReconcilePass(ctx, cfg); n > 0 {
			lastChange = time.Now()
		}
		limit := cfg.counter != nil && cfg.counter.LimitReached()
		wait := github.Interval(time.Now(), lastChange, limit)
		if cfg.intervalOverride > 0 {
			wait = cfg.intervalOverride
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// runReconciler starts the loop only when this process owns the poller lock.
// The returned function cancels and waits for the loop and releases the lock.
func runReconciler(ctx context.Context, cfg reconcileConfig) (func(), error) {
	release, lock, err := acquirePollerLock(cfg.dir)
	if err != nil {
		return nil, err
	}
	publishPollerStatus(lock.activePID)
	if !lock.owned {
		return func() {}, nil
	}
	if cfg.onPollerOwner != nil {
		cfg.onPollerOwner(ctx)
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ReconcileLoop(loopCtx, cfg)
	}()
	return func() {
		cancel()
		<-done
		release()
	}, nil
}

// RegisterMCP installs the mcp command implementation.
func RegisterMCP(fn func(io.Writer) int) {
	mcpRunner = fn
}
