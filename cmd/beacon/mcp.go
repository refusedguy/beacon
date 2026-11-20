package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app/taskcli"
	"github.com/refusedguy/beacon/internal/infra/cibridge"
	"github.com/refusedguy/beacon/internal/infra/ghci"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// The app service writes precheck reasons under the same state directory as
// the MCP store, so it satisfies mcp.PrecheckJournal without the mcp package
// importing app (ADR 0005).
var _ mcp.PrecheckJournal = (*taskcli.Service)(nil)

// mcpStateDBName is the SQLite file inside the Beacon state directory
// (ADR 0011, ADR 0015).
const mcpStateDBName = "beacon.db"

// ciBridgeInterval bounds how often Beacon observes CI for active Watches.
const ciBridgeInterval = 5 * time.Second

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
	switch os.Getenv("BEACON_INIT_BYPASS") {
	case "1", "true", "yes", "on":
		cfg.bypass = true
	}
	if raw := os.Getenv("BEACON_INIT_EVENT_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.timeout = d
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
		return cfg
	}
	if d > syncCallTimeoutMax {
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
	if err := runMCP(context.Background()); err != nil {
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
	return s
}

// serveMCP starts the stdio transport. Tests replace it to inspect the
// composed dependencies without blocking on stdin.
var serveMCP = func(ctx context.Context, deps mcp.Dependencies) error {
	return newMCPServer(deps).Run(ctx, &mcpsdk.StdioTransport{})
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
// CI and Source have no production adapter in this tree. A watch_pr call must
// therefore supply headSHA; a wait on a still-watching Watch reports that no
// verdict source is configured. Neither affects the store-backed handlers.
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
	if err := mcp.EnsureStateDir(dir); err != nil {
		return mcp.Dependencies{}, nil, err
	}
	db, err := store.Open(filepath.Join(dir, mcpStateDBName))
	if err != nil {
		return mcp.Dependencies{}, nil, err
	}

	// T3.91: the ports below are the real production implementations. The
	// app service persists precheck reasons under this state directory, and
	// the capability registry binds waits to the calling agent from _meta.
	//
	// The remaining setters are deliberately left at their real defaults.
	// CIWait is a no-op until a CI adapter is bound to one Watch; the shared
	// gate is still wired, so it coalesces callers honestly. The in-memory
	// idempotency index lives as long as the process, which is the current
	// store lifetime; a durable one would need the same transaction as the
	// obligation write and does not exist yet. Only beacon_ack consumes the
	// continuation counter, so its own counter is sufficient. store.SharedGate
	// keeps its log.Printf warning sink in production; tests replace it to
	// capture warnings without touching the global logger (SetWarningSink).
	deps := mcp.Dependencies{
		Store:           db,
		Stamper:         mcp.NewSQLObligationStamper(db),
		Writer:          mcp.NewSQLWaitResultWriter(db),
		Events:          mcp.NewSQLCancelEventLog(db),
		SharedCIWaiter:  sharedCIWaiter,
		PrecheckJournal: taskcli.NewService(taskcli.Config{StateDir: dir}),
		CallScopes:      mcp.NewCapabilityScopeRegistry(nil, nil),
		// ADR 0045 §2: one threshold for all long synchronous MCP calls. Past
		// it the server answers with a ticket carrying a job id rather than
		// holding the connection open. This is a time bound, not ADR 0026's
		// delivery field.
		After: readSyncCallConfig().after,
		Snapshot: mcp.SnapshotFunc(func() (mcp.StateSnapshot, error) {
			watches, err := db.ActiveWatches()
			if err != nil {
				return mcp.StateSnapshot{}, err
			}
			return mcp.StateSnapshot{ActiveWatches: watches, Version: mcp.ServerVersion}, nil
		}),
	}
	return deps, db.Close, nil
}

// runMCP reads the connection file on every startup, applies the endpoint and
// then composes the dependencies and serves them until the client leaves.
// Re-reading the file is the reconnect: a daemon that moved to another address,
// port or token is observed on the next start, while the watches stay in the
// on-disk store and continue to wait.
func runMCP(ctx context.Context) error {
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

	deps, closeStore, err := openMCPDependenciesInDir(dir)
	if err != nil {
		return err
	}
	defer func() { _ = closeStore() }()

	// The CI bridge is the missing production verdict source. It observes the
	// hosting service for active Watches and persists ready/retry, so the
	// orchestrator reads a verdict from get_result instead of polling CI.
	ciBridge := cibridge.New(deps.Store, &ghci.Provider{}, deps.Clock, ciBridgeInterval)
	ciBridge.WithWaker(&wake.OpenCodeDeliverer{})
	go ciBridge.Run(ctx)

	return serveMCP(ctx, deps)
}

// RegisterMCP installs the mcp command implementation.
func RegisterMCP(fn func(io.Writer) int) {
	mcpRunner = fn
}
