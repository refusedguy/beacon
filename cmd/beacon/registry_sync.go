package main

// T5.30: detecting stale registry entries after a restart (ADR 0095).
//
// Registry rows survive a Beacon restart, but the OpenCode sessions they point
// at live in the server and can disappear with it. A row is stale when the
// server has no live session with that session_id. This file owns the
// comparison and the status write. It deliberately has no prompt or session
// creation port: reconciling the registry reads the session list and writes
// registry status only, so it never starts a model (ADR 0095, ADR 0092).
//
// The two narrow ports below are the seam for the later cards. T5.55 wires the
// CLI command and the live session source; T5.49 reuses registryWakeAllowed at
// the wake boundary so a heartbeat tick cannot replace a successful sync.

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// Registry statuses from ADR 0090. The strings match store.RegistryStatus*
// because the two layers persist and read the same column. They are local so
// the pure comparison stays free of a store dependency.
const (
	registryStatusActive  = "active"
	registryStatusStale   = "stale"
	registryStatusStopped = "stopped"
)

// registrySessionSource is the read-only source of live session ids. It has no
// Prompt or CreateSession method on purpose: a reconciliation pass cannot wake
// a session or start a model through this port (ADR 0095, ADR 0092).
type registrySessionSource interface {
	Sessions(context.Context) ([]opencode.Session, error)
}

// registrySessionSourceFunc adapts a function to registrySessionSource for
// tests and for wiring a live client without a new type.
type registrySessionSourceFunc func(context.Context) ([]opencode.Session, error)

func (f registrySessionSourceFunc) Sessions(ctx context.Context) ([]opencode.Session, error) {
	return f(ctx)
}

// registryEntry is the part of a stored executor the comparison reads.
// AgentID is the registry key; SessionID is the opaque address a wake would
// use. Status is the status currently persisted for the row.
type registryEntry struct {
	AgentID   string
	SessionID string
	Status    string
}

// registryStore is the narrow persistence port of a reconciliation pass. It
// reads the project's entries and writes back only the status of rows whose
// desired status changed.
type registryStore interface {
	Entries(projectKey string) ([]registryEntry, error)
	SetStatus(projectKey, agentID, status string) error
}

// registryStoreFuncs adapts two functions to registryStore. Tests use it as a
// stand-in and record the status writes through setStatus.
type registryStoreFuncs struct {
	entries   func(string) ([]registryEntry, error)
	setStatus func(string, string, string) error
}

func (s registryStoreFuncs) Entries(projectKey string) ([]registryEntry, error) {
	return s.entries(projectKey)
}

func (s registryStoreFuncs) SetStatus(projectKey, agentID, status string) error {
	return s.setStatus(projectKey, agentID, status)
}

// registrySyncWriter is the optional persistence capability for the live
// observation fields. registryDBStore implements it. A stand-in that only
// implements registryStore keeps its status-only contract and is never asked
// for a write it cannot answer (ADR 0095).
type registrySyncWriter interface {
	SyncExecutor(projectKey, agentID, status, worktree string, lastSeen int64) error
}

// registryStatusUpdate is one requested status change for an executor.
type registryStatusUpdate struct {
	AgentID string
	Status  string
}

// registrySyncResult is the outcome of one reconciliation pass. Stale and
// Active are agent ids in their desired status; Updated lists only the rows
// whose status actually changed, so a repeat pass reports no updates.
type registrySyncResult struct {
	ProjectKey string
	Stale      []string
	Active     []string
	Updated    []registryStatusUpdate
}

// liveSessionIDs collects the opaque ids from a session.list response. A blank
// id is dropped: it cannot match a registry row and would otherwise mark a row
// with an empty session_id active.
func liveSessionIDs(sessions []opencode.Session) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if id := strings.TrimSpace(s.ID); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// reconcileStale is the pure comparison. For each entry it computes the desired
// status from the live session ids and the current status:
//
//   - a stopped executor keeps its status; an explicit stop is not undone;
//   - a row whose session_id is in live becomes active, which clears stale;
//   - every other row, including one with an empty session_id, becomes stale.
//
// Only changed statuses are returned, so applying the result twice is
// idempotent. The function performs no I/O and sends no wake.
func reconcileStale(entries []registryEntry, liveIDs []string) []registryStatusUpdate {
	live := make(map[string]struct{}, len(liveIDs))
	for _, id := range liveIDs {
		live[id] = struct{}{}
	}

	updates := make([]registryStatusUpdate, 0)
	for _, e := range entries {
		if e.Status == registryStatusStopped {
			continue
		}
		want := registryStatusStale
		if _, ok := live[strings.TrimSpace(e.SessionID)]; ok && strings.TrimSpace(e.SessionID) != "" {
			want = registryStatusActive
		}
		if e.Status != want {
			updates = append(updates, registryStatusUpdate{AgentID: e.AgentID, Status: want})
		}
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].AgentID < updates[j].AgentID })
	return updates
}

// classifyStale splits entries into their desired stale/active sets. Stopped
// entries are reported in neither set: sync does not own their lifecycle.
func classifyStale(entries []registryEntry, liveIDs []string) (stale, active []string) {
	live := make(map[string]struct{}, len(liveIDs))
	for _, id := range liveIDs {
		live[id] = struct{}{}
	}
	stale, active = make([]string, 0), make([]string, 0)
	for _, e := range entries {
		if e.Status == registryStatusStopped {
			continue
		}
		id := strings.TrimSpace(e.SessionID)
		if id != "" {
			if _, ok := live[id]; ok {
				active = append(active, e.AgentID)
				continue
			}
		}
		stale = append(stale, e.AgentID)
	}
	sort.Strings(stale)
	sort.Strings(active)
	return stale, active
}

// syncRegistry runs one reconciliation pass: read the live session ids, read
// the project's registry entries, compute the desired statuses and write only
// the changes. It never prompts a session and never creates one.
func syncRegistry(ctx context.Context, source registrySessionSource, storePort registryStore, projectKey string) (registrySyncResult, error) {
	if source == nil {
		return registrySyncResult{}, fmt.Errorf("registry sync: session source not connected")
	}
	if storePort == nil {
		return registrySyncResult{}, fmt.Errorf("registry sync: store not connected")
	}
	if strings.TrimSpace(projectKey) == "" {
		return registrySyncResult{}, fmt.Errorf("registry sync: empty project key")
	}

	sessions, err := source.Sessions(ctx)
	if err != nil {
		return registrySyncResult{}, fmt.Errorf("registry sync: sessions: %w", err)
	}
	liveIDs := liveSessionIDs(sessions)

	// A live session carries its worktree in Location.Directory. Map it by
	// session id before reading the entries, so the write loop needs no second
	// session list call (ADR 0095).
	sessionsByID := make(map[string]opencode.Session, len(sessions))
	for _, s := range sessions {
		if id := strings.TrimSpace(s.ID); id != "" {
			sessionsByID[id] = s
		}
	}

	entries, err := storePort.Entries(projectKey)
	if err != nil {
		return registrySyncResult{}, fmt.Errorf("registry sync: entries: %w", err)
	}

	worktreeByAgent := make(map[string]string, len(entries))
	for _, e := range entries {
		if s, ok := sessionsByID[strings.TrimSpace(e.SessionID)]; ok {
			worktreeByAgent[e.AgentID] = s.Location.Directory
		}
	}

	observedAt := time.Now().UTC().UnixNano()
	writer, hasWriter := storePort.(registrySyncWriter)
	updates := reconcileStale(entries, liveIDs)
	changed := make(map[string]bool, len(updates))
	for _, u := range updates {
		changed[u.AgentID] = true
		var err error
		if hasWriter {
			err = writer.SyncExecutor(projectKey, u.AgentID, u.Status, worktreeByAgent[u.AgentID], observedAt)
		} else {
			err = storePort.SetStatus(projectKey, u.AgentID, u.Status)
		}
		if err != nil {
			return registrySyncResult{}, fmt.Errorf("registry sync: set %s to %s: %w", u.AgentID, u.Status, err)
		}
	}

	// A live observation is fresh even when the status did not change. The
	// status loop above already wrote worktree and last_seen for changed rows,
	// so refresh only the remaining rows that have a live session. This is
	// scoped to one project: entries come from storePort.Entries(projectKey),
	// so a pass for project A never touches project B's observation columns.
	if hasWriter {
		for _, e := range entries {
			if changed[e.AgentID] {
				continue
			}
			worktree, live := worktreeByAgent[e.AgentID]
			if !live {
				continue
			}
			if err := writer.SyncExecutor(projectKey, e.AgentID, e.Status, worktree, observedAt); err != nil {
				return registrySyncResult{}, fmt.Errorf("registry sync: observe %s: %w", e.AgentID, err)
			}
		}
	}

	stale, active := classifyStale(entries, liveIDs)
	return registrySyncResult{ProjectKey: projectKey, Stale: stale, Active: active, Updated: updates}, nil
}

// registryWakeAllowed is the wake guard shared with T5.49. Only an active
// executor may be woken. Stale, stopped, empty and unknown statuses all deny
// wake, so a heartbeat tick by itself never proves a live session.
func registryWakeAllowed(status string) bool {
	return status == registryStatusActive
}

// registryDBStore adapts the project-scoped store to registryStore. It is the
// persistence half T5.55 installs; SetStatus is an idempotent column update
// that leaves verdicts and the rest of the row untouched (ADR 0090).
type registryDBStore struct {
	db *store.DB
}

func (s registryDBStore) Entries(projectKey string) ([]registryEntry, error) {
	// A registry written by an older binary may still be on the migration 0005
	// shape. Bring it up to date before the read below selects the sync columns.
	if err := s.db.EnsureRegistrySchema(); err != nil {
		return nil, err
	}
	records, err := s.db.Executors(projectKey)
	if err != nil {
		return nil, err
	}
	out := make([]registryEntry, 0, len(records))
	for _, r := range records {
		out = append(out, registryEntry{AgentID: r.AgentID, SessionID: r.SessionID, Status: r.Status})
	}
	return out, nil
}

func (s registryDBStore) SetStatus(projectKey, agentID, status string) error {
	return s.SyncExecutor(projectKey, agentID, status, "", time.Now().UTC().UnixNano())
}

// SyncExecutor persists the status and the live observation of one executor.
// The store writes all three columns in one transaction, so a sync pass cannot
// leave a refreshed status with a stale last_seen or worktree.
func (s registryDBStore) SyncExecutor(projectKey, agentID, status, worktree string, lastSeen int64) error {
	return s.db.SyncExecutor(projectKey, agentID, status, worktree, lastSeen)
}

// ---------------------------------------------------------------------------
// T5.55: the sync subcommand (ADR 0095).
//
// This file owns the comparison and now the CLI that runs it. The command reads
// session.list through a read-only source and writes registry rows. It has no
// prompt or session creation port, so it cannot wake a session or start a model
// while reconciling.
// ---------------------------------------------------------------------------

// registrySyncOpenSource resolves the live session.list source. The seam keeps
// the command testable without an opencode server.
var registrySyncOpenSource = func() (registrySessionSource, error) {
	return newOpencodePort()
}

// registrySyncOpenStore opens the process store that owns agent_registry. The
// same store backs the stop command, so both subcommands read one registry.
var registrySyncOpenStore = func() (*store.DB, func() error, error) {
	return openStateDB(func(db *store.DB) *store.DB { return db })
}

// runRegistrySync reconciles the project registry with the live session list.
// The project key defaults to the current project and can be overridden when a
// caller needs to sync a different scope.
func runRegistrySync(c *CLI, args []string) int {
	fs := flag.NewFlagSet("registry sync", flag.ContinueOnError)
	project := fs.String("project", "", "project key (default: current project)")
	if !parseFlags("registry sync", args, c.errOut, fs) {
		return 2
	}

	key := strings.TrimSpace(*project)
	if key == "" {
		key = orchestratorProjectKey()
	}
	if key == "" {
		fmt.Fprintf(c.errOut, "beacon registry sync: empty project key, pass --project\n")
		return 2
	}

	source, err := registrySyncOpenSource()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry sync: session source: %v\n", err)
		return 2
	}
	db, closeStore, err := registrySyncOpenStore()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry sync: store: %v\n", err)
		return 2
	}
	defer func() { _ = closeStore() }()

	result, err := syncRegistry(context.Background(), source, registryDBStore{db: db}, key)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry sync: %v\n", err)
		return 2
	}

	fmt.Fprintf(c.out, "project=%s stale=%d active=%d updated=%d\n",
		result.ProjectKey, len(result.Stale), len(result.Active), len(result.Updated))
	if len(result.Stale) > 0 {
		fmt.Fprintf(c.out, "stale: %s\n", strings.Join(result.Stale, " "))
	}
	if len(result.Active) > 0 {
		fmt.Fprintf(c.out, "active: %s\n", strings.Join(result.Active, " "))
	}
	if len(result.Updated) > 0 {
		updates := make([]string, 0, len(result.Updated))
		for _, u := range result.Updated {
			updates = append(updates, u.AgentID+"="+u.Status)
		}
		fmt.Fprintf(c.out, "updated: %s\n", strings.Join(updates, " "))
	}
	return 0
}

// wakeRegistryGuard adapts the project registry to mcp.RegistryWakeGuard. It
// reads one stored row and applies registryWakeAllowed, so the CLI owns the
// wake rule while the MCP wake boundary only consumes the answer (T5.49).
// An absent row, a read error or an unknown id yields an empty status and
// denies wake: a heartbeat tick never stands in for a successful sync.
type wakeRegistryGuard struct {
	db         *store.DB
	projectKey string
}

// newWakeRegistryGuard binds the guard to one project. It is the production
// adapter installed on the wake handler by newMCPServer.
func newWakeRegistryGuard(db *store.DB, projectKey string) mcp.RegistryWakeGuard {
	return wakeRegistryGuard{db: db, projectKey: projectKey}
}

// WakeStatus looks up one executor and applies the registry wake rule. The id
// is first read as an agent id, then as a session id, because an explicit wake
// carries the opaque session target while a routed wake carries the executor.
func (g wakeRegistryGuard) WakeStatus(executorID string) (string, bool) {
	status := g.status(executorID)
	return status, registryWakeAllowed(status)
}

// status reads the stored status of one executor in the bound project.
func (g wakeRegistryGuard) status(executorID string) string {
	if g.db == nil || strings.TrimSpace(executorID) == "" || strings.TrimSpace(g.projectKey) == "" {
		return ""
	}
	record, found, err := g.db.Executor(g.projectKey, executorID)
	if err == nil && found {
		return record.Status
	}
	entries, err := g.db.Executors(g.projectKey)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.SessionID == executorID {
			return entry.Status
		}
	}
	return ""
}
