package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// sessionSpawner is the narrow port used by beacon spawn. CreateSession creates
// an OpenCode session with the orchestrator's parentID, the agent id, a title
// and the metadata that links the new session to beacon watch. Prompt sends one
// text message to that session after it exists.
//
// The agent belongs to CreateSession, not to Prompt: in the v2 prompt route the
// body has no agent field (ADR 0060 п3, docs/design/opencode-cli-manager.md:28).
// Keeping the two calls on one port makes that boundary testable without a live
// server. The production implementation is the thin HTTP client of T3.144 and
// is installed by T3.145; tests install a stand-in with SetSessionSpawner.
type sessionSpawner interface {
	CreateSession(parentID, agent, title string, metadata map[string]string) (id string, err error)
	Prompt(sessionID, text string) error
}

// spawnSessionSpawner is the installed port. It stays nil until production
// wiring or a test installs an implementation.
var spawnSessionSpawner sessionSpawner

// SetSessionSpawner installs the implementation used by beacon spawn. Passing
// nil clears it, so a command without a port reports an error instead of
// dereferencing nil. T3.145 calls this with the T3.144 HTTP client.
func SetSessionSpawner(s sessionSpawner) {
	spawnSessionSpawner = s
}

// spawnSessionRegistry is the durable session registry used by beacon spawn.
// It stays nil until the composition root or a test installs one. A session is
// recorded when it starts, through the app layer's SessionStore port, so cmd
// never opens a database connection for sessions on its own.
var spawnSessionRegistry *session.SessionRegistry

// SetSessionRegistry installs the session registry used by beacon spawn. A nil
// value clears it. Tests pass a registry backed by a mock store to assert the
// SaveSession call at session start.
func SetSessionRegistry(r *session.SessionRegistry) { spawnSessionRegistry = r }

var (
	spawnSessionOnce sync.Once
	spawnSessionDB   *session.SessionRegistry
)

// defaultSessionRegistry opens the same state file the runtime_stats sink
// uses and adapts the store to the app port. The lazy open keeps a plain
// `beacon spawn` connected without a change to main.go, and a failed open
// leaves the registry nil so the command still starts its session.
func defaultSessionRegistry() *session.SessionRegistry {
	spawnSessionOnce.Do(func() {
		dir, err := stateDir()
		if err != nil {
			return
		}
		db, err := store.Open(filepath.Join(dir, mcpStateDBName))
		if err == nil {
			spawnSessionDB = session.NewSessionRegistry(newSessionDBAdapter(db))
		}
	})
	return spawnSessionDB
}

// sessionRegistryForSpawn returns the installed registry, or the lazy default
// when none was installed.
func sessionRegistryForSpawn() *session.SessionRegistry {
	if spawnSessionRegistry != nil {
		return spawnSessionRegistry
	}
	return defaultSessionRegistry()
}

// recordSpawnSession writes the new session to the durable registry. A failed
// write is reported on stderr and does not fail the command: the session is
// already alive, so returning an error here would suggest the spawn did not
// happen. The next spawn can repair the registry entry.
func recordSpawnSession(c *CLI, id, taskID, title string) {
	reg := sessionRegistryForSpawn()
	if reg == nil {
		return
	}
	// A spawn that reattaches to a run from an earlier invocation already has
	// a session row for the task. Keep that row instead of writing a second
	// one. Stores without task lookup report ErrNoTaskLookup and take the
	// normal path.
	if _, found, err := reg.ResumeByTask(taskID); err == nil && found {
		return
	}
	metadata := map[string]string{spawnMetaTaskID: taskID}
	if title != "" {
		metadata[spawnMetaTitle] = title
	}
	if err := reg.Start(id, store.SessionStatusActive, metadata); err != nil {
		fmt.Fprintf(c.errOut, "beacon spawn: сессия %s не записана в реестр: %v\n", id, err)
	}
}

// Metadata keys on the created session. beacon watch already joins a session
// by its opaque sessionID; the task id in metadata is what makes the link
// readable back to Beacon. The title is stored only when the caller set one.
const (
	spawnMetaTaskID = store.SessionMetaTaskID
	spawnMetaTitle  = "ai.beacon/title"
)

// spawnParentEnv carries the orchestrator's opaque OpenCode sessionID into the
// child process. It is the same kind of opaque identifier as the _meta
// "ai.opencode/sessionID" used by watch (internal/infra/mcp/meta.go:15).
const spawnParentEnv = "BEACON_PARENT_SESSION"

// spawnPrompt builds the first message of a new session. It names the task so
// the agent knows which Beacon card to take; the agent itself is fixed at
// session creation and is deliberately absent here.
func spawnPrompt(taskID, title string) string {
	if title != "" {
		return fmt.Sprintf("Выполни задачу Beacon %s: %s", taskID, title)
	}
	return fmt.Sprintf("Выполни задачу Beacon %s", taskID)
}

// runSpawn implements `beacon spawn <taskID> [--agent id] [--title ...]` and
// `beacon spawn --runtime acp [taskID] [--runner name]`. The default path
// creates a child session through the injected port; --runtime acp starts a
// long-lived ACP runner instead and stays attached to its session/update
// stream. No HTTP server or TUI is opened here (ADR 0060 п3 and п5).
func (c *CLI) runSpawn(args []string) int {
	// parseFlags rejects positional arguments and the flag package stops at the
	// first one, while spawn accepts flags both before and after the taskID.
	// parseTaskArgs is the existing parser with that shape (cmd/beacon/task.go).
	p, err := parseTaskArgs(args, map[string]bool{
		"agent": true, "title": true, "runtime": true, "runner": true,
	}, nil)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon spawn: %v\n", err)
		return 2
	}
	if len(p.pos) != 1 || strings.TrimSpace(p.pos[0]) == "" {
		fmt.Fprintln(c.errOut, "beacon spawn: нужна одна команда: beacon spawn <taskID> [--agent id] [--title ...]")
		return 2
	}

	taskID := strings.TrimSpace(p.pos[0])

	switch runtime := strings.ToLower(strings.TrimSpace(p.value("runtime"))); runtime {
	case "", "default", "session":
		// Legacy path: create an OpenCode session through the injected port.
	case "acp":
		return c.runSpawnACP(taskID, strings.TrimSpace(p.value("title")), strings.TrimSpace(p.value("runner")))
	default:
		fmt.Fprintf(c.errOut, "beacon spawn: неподдерживаемый runtime %q (доступен: acp)\n", runtime)
		return 2
	}

	parentID := strings.TrimSpace(os.Getenv(spawnParentEnv))
	if parentID == "" {
		fmt.Fprintf(c.errOut, "beacon spawn: не задан parentID оркестратора (%s)\n", spawnParentEnv)
		return 2
	}
	spawner := spawnSessionSpawner
	if spawner == nil {
		fmt.Fprintln(c.errOut, "beacon spawn: порт создания сессий не подключён")
		return 2
	}

	agentID := strings.TrimSpace(p.value("agent"))
	sessionTitle := strings.TrimSpace(p.value("title"))
	metadata := map[string]string{spawnMetaTaskID: taskID}
	if sessionTitle != "" {
		metadata[spawnMetaTitle] = sessionTitle
	}

	sessionID, err := spawner.CreateSession(parentID, agentID, sessionTitle, metadata)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon spawn: не удалось создать сессию: %v\n", err)
		return 2
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		fmt.Fprintln(c.errOut, "beacon spawn: создание вернуло пустой sessionID")
		return 2
	}

	if err := spawner.Prompt(sessionID, spawnPrompt(taskID, sessionTitle)); err != nil {
		fmt.Fprintf(c.errOut, "beacon spawn: сессия %s создана, но промпт не отправлен: %v\n", sessionID, err)
		return 2
	}

	recordSpawnSession(c, sessionID, taskID, sessionTitle)

	fmt.Fprintln(c.out, sessionID)
	return 0
}

// ===== ACP-раннер (W10-A) =====
// acpRuntimeFactory builds a long-lived WorkerRuntime. wake.NewWorkerRuntime
// is the production implementation; tests install a stand-in so no runner
// binary is ever needed.
type acpRuntimeFactory func(wake.ACPRuntimeConfig) (wake.WorkerRuntime, error)

var spawnACPRuntimeFactory acpRuntimeFactory = func(cfg wake.ACPRuntimeConfig) (wake.WorkerRuntime, error) {
	return wake.NewWorkerRuntime(cfg)
}

// SetACPRuntimeFactory installs the ACP runtime factory used by beacon spawn.
func SetACPRuntimeFactory(f acpRuntimeFactory) { spawnACPRuntimeFactory = f }

// spawnStatsStore is the runtime_stats port, satisfied by *store.DB.
type spawnStatsStore interface {
	SaveRuntimeStats(payload []byte, now time.Time) error
}

// spawnEventSink is the SSE port, satisfied by *httpapi.EventBus. cmd only
// names the httpapi Event type and never imports net/http itself.
type spawnEventSink interface {
	Publish(httpapi.Event)
}

var (
	spawnRuntimeStats spawnStatsStore
	spawnEvents       spawnEventSink
)

// SetSpawnRuntimeStats installs the runtime_stats sink for ACP updates.
func SetSpawnRuntimeStats(s spawnStatsStore) { spawnRuntimeStats = s }

// SetSpawnEvents installs the SSE sink (/api/v1/events) for ACP updates.
func SetSpawnEvents(s spawnEventSink) { spawnEvents = s }

// ACPRunStore is the durable run port used by beacon spawn. *store.DB
// satisfies it. Tests install a fake so they never touch beacon.db.
type ACPRunStore interface {
	ClaimACPRun(store.ACPRun) (store.ACPRun, bool, error)
	GetACPRun(task string) (store.ACPRun, bool, error)
	TransitionACPRun(task string, to store.ACPRunState, sessionID string, result json.RawMessage) (bool, error)
}

var (
	spawnACPRunStore ACPRunStore
	spawnACPRunOnce  sync.Once
	spawnACPRunDB    ACPRunStore
)

// SetACPRunStore installs the durable run store used by beacon spawn. A nil
// value falls back to the lazy store in the project state directory.
func SetACPRunStore(s ACPRunStore) { spawnACPRunStore = s }

// acpRunStoreForSpawn returns the installed run store, or one on the same
// beacon.db the session registry and runtime_stats use. Nothing here writes
// outside the state directory of the current project (ADR 0086).
func acpRunStoreForSpawn() ACPRunStore {
	if spawnACPRunStore != nil {
		return spawnACPRunStore
	}
	spawnACPRunOnce.Do(func() {
		dir, err := stateDir()
		if err != nil {
			return
		}
		db, err := store.Open(filepath.Join(dir, mcpStateDBName))
		if err == nil {
			spawnACPRunDB = db
		}
	})
	return spawnACPRunDB
}

// transitionSpawnACPRun records a state change when a run store is present.
// A failed write is reported and does not change the command exit code: the
// runner is already in that state, so a later spawn can repair the row.
func transitionSpawnACPRun(c *CLI, storePort ACPRunStore, task string, to store.ACPRunState, sessionID string, result json.RawMessage) {
	if storePort == nil {
		return
	}
	if _, err := storePort.TransitionACPRun(task, to, sessionID, result); err != nil {
		fmt.Fprintf(c.errOut, "beacon spawn: run %s не переведён в %s: %v\n", task, to, err)
	}
}

// spawnACPLifetime is the lifetime of the attached worker. Production stops
// on a signal; tests replace it with a cancellable context.
var spawnACPLifetime = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// spawnACPRunnerEnv overrides the --runner flag. BEACON_ACP_COMMAND and
// BEACON_ACP_ARGS replace the runner command line entirely, so any harness
// can route ACP through a wrapper.
const (
	spawnACPRunnerEnv  = "BEACON_ACP_RUNNER"
	spawnACPCommandEnv = "BEACON_ACP_COMMAND"
	spawnACPArgsEnv    = "BEACON_ACP_ARGS"
)

func spawnACPConfig(runner string) wake.ACPRuntimeConfig {
	runner = strings.TrimSpace(runner)
	if runner == "" {
		runner = strings.TrimSpace(os.Getenv(spawnACPRunnerEnv))
	}
	if runner == "" {
		runner = "opencode"
	}
	cfg := wake.ACPRuntimeConfig{Runtime: runner}
	if command := strings.TrimSpace(os.Getenv(spawnACPCommandEnv)); command != "" {
		cfg.Command = command
	}
	if args := strings.TrimSpace(os.Getenv(spawnACPArgsEnv)); args != "" {
		cfg.Args = strings.Fields(args)
	}
	return cfg
}

// acpSessionStats is the snapshot written to runtime_stats for one session.
// Every session/update replaces the row, so a fresh reader sees the latest
// stream rather than an unbounded log (ADR 0061 п2: no replay of missed frames).
type acpSessionStats struct {
	TaskID    string            `json:"taskId"`
	SessionID string            `json:"sessionId"`
	Runner    string            `json:"runner"`
	Title     string            `json:"title,omitempty"`
	Updates   []json.RawMessage `json:"updates"`
}

// spawnStatsSink lazily opens the state DB so runtime_stats is written even
// when the composition root has not installed a store yet. A test or the
// composition root can replace it with SetSpawnRuntimeStats.
var (
	spawnStatsOnce sync.Once
	spawnStatsDB   spawnStatsStore
)

func defaultSpawnStats() spawnStatsStore {
	spawnStatsOnce.Do(func() {
		dir, err := stateDir()
		if err != nil {
			return
		}
		db, err := store.Open(filepath.Join(dir, mcpStateDBName))
		if err == nil {
			spawnStatsDB = db
		}
	})
	return spawnStatsDB
}

// streamACPSession drains session/update into runtime_stats and the SSE bus
// until the runtime or the caller's context ends. Both sinks are captured at
// entry so a test can swap the globals without racing this loop. The last
// snapshot becomes the durable run result. finished reports whether the
// worker ended on its own; a cancelled context means the terminal closed and
// the run is only interrupted.
func streamACPSession(ctx context.Context, rt wake.WorkerRuntime, sessionID, taskID, title, runner string) (json.RawMessage, bool) {
	statsSink, eventSink := spawnRuntimeStats, spawnEvents
	if statsSink == nil {
		statsSink = defaultSpawnStats()
	}
	stats := &acpSessionStats{TaskID: taskID, SessionID: sessionID, Runner: runner, Title: title}
	snapshot := func() json.RawMessage {
		payload, err := json.Marshal(stats)
		if err != nil {
			return json.RawMessage(`{}`)
		}
		return payload
	}
	for {
		select {
		case update, ok := <-rt.Updates():
			if !ok {
				return snapshot(), false
			}
			stats.Updates = append(stats.Updates, append(json.RawMessage(nil), update.Update...))
			payload := snapshot()
			if statsSink != nil {
				_ = statsSink.SaveRuntimeStats(payload, time.Now().UTC())
			}
			if eventSink != nil {
				event, err := json.Marshal(map[string]any{
					"taskId": taskID, "sessionId": update.SessionID,
					"runner": runner, "update": update.Update,
				})
				if err == nil {
					eventSink.Publish(httpapi.Event{
						Type: "session.update", WatchID: taskID, Payload: event,
					})
				}
			}
		case <-rt.Done():
			return snapshot(), true
		case <-ctx.Done():
			return snapshot(), false
		}
	}
}

// runSpawnACP starts or resumes an ACP run for one task line and stays
// attached to session/update. The durable record is claimed before the runner
// starts, so a second invocation returns the existing run instead of creating
// a second piece of work. Closing the terminal cancels the local context but
// never closes the runner: the command records interrupted and leaves the
// worker alive. A later invocation reuses the run record and its stored
// result.
//
// The session id is printed before the prompt so the caller can watch the
// stream while the worker is still running.
func (c *CLI) runSpawnACP(taskID, title, runner string) int {
	factory := spawnACPRuntimeFactory
	if factory == nil {
		fmt.Fprintln(c.errOut, "beacon spawn: ACP-раннер не подключён")
		return 2
	}

	runStore := acpRunStoreForSpawn()
	var run store.ACPRun
	if runStore != nil {
		stored, created, err := runStore.ClaimACPRun(store.ACPRun{
			TaskID: taskID, Runner: runner, Title: title, State: store.ACPRunRequested,
		})
		run = stored
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon spawn: run %s не записан: %v\n", taskID, err)
			return 2
		}
		if !created && run.State == store.ACPRunFinished {
			fmt.Fprintln(c.out, run.SessionID)
			if len(run.Result) != 0 {
				fmt.Fprintln(c.out, string(run.Result))
			}
			return 0
		}
	}

	cfg := spawnACPConfig(runner)
	rt, err := factory(cfg)
	if err != nil {
		transitionSpawnACPRun(c, runStore, taskID, store.ACPRunInterrupted, "", nil)
		fmt.Fprintf(c.errOut, "beacon spawn: не удалось запустить ACP-раннер: %v\n", err)
		return 2
	}
	// There is deliberately no deferred rt.Close here. A terminal closing is
	// a cancelled context, not a reason to kill the worker.

	ctx, cancel := spawnACPLifetime()
	defer cancel()

	if err := rt.Initialize(ctx); err != nil {
		transitionSpawnACPRun(c, runStore, taskID, store.ACPRunInterrupted, "", nil)
		fmt.Fprintf(c.errOut, "beacon spawn: ACP initialize: %v\n", err)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		transitionSpawnACPRun(c, runStore, taskID, store.ACPRunInterrupted, "", nil)
		fmt.Fprintf(c.errOut, "beacon spawn: рабочая директория: %v\n", err)
		return 2
	}

	sessionID := strings.TrimSpace(run.SessionID)
	if run.State == store.ACPRunStarted && sessionID != "" {
		// The worker died while the run was still active. Hand the old session
		// id back to the runner through session/load instead of losing it.
		if err := rt.LoadSession(ctx, sessionID, cwd); err != nil {
			transitionSpawnACPRun(c, runStore, taskID, store.ACPRunInterrupted, sessionID, nil)
			fmt.Fprintf(c.errOut, "beacon spawn: ACP session/load: %v\n", err)
			return 2
		}
	} else {
		// A fresh run, or one interrupted by a closed terminal, gets a new
		// runner session under the same durable run record.
		sessionID, err = rt.NewSession(ctx, cwd)
		if err != nil {
			transitionSpawnACPRun(c, runStore, taskID, store.ACPRunInterrupted, "", nil)
			fmt.Fprintf(c.errOut, "beacon spawn: ACP session/new: %v\n", err)
			return 2
		}
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			transitionSpawnACPRun(c, runStore, taskID, store.ACPRunInterrupted, "", nil)
			fmt.Fprintln(c.errOut, "beacon spawn: ACP session/new вернул пустой sessionID")
			return 2
		}
	}

	transitionSpawnACPRun(c, runStore, taskID, store.ACPRunStarted, sessionID, nil)
	recordSpawnSession(c, sessionID, taskID, title)

	fmt.Fprintln(c.out, sessionID)

	streamDone := make(chan struct{})
	var result json.RawMessage
	var finished bool
	go func() {
		defer close(streamDone)
		result, finished = streamACPSession(ctx, rt, sessionID, taskID, title, cfg.Runtime)
	}()

	if err := rt.Prompt(ctx, sessionID, spawnPrompt(taskID, title)); err != nil {
		fmt.Fprintf(c.errOut, "beacon spawn: сессия %s создана, но промпт не отправлен: %v\n", sessionID, err)
		cancel()
		<-streamDone
		transitionSpawnACPRun(c, runStore, taskID, store.ACPRunInterrupted, sessionID, result)
		return 2
	}
	select {
	case <-streamDone:
	case <-ctx.Done():
		<-streamDone
	}
	to := store.ACPRunInterrupted
	if finished {
		to = store.ACPRunFinished
	}
	transitionSpawnACPRun(c, runStore, taskID, to, sessionID, result)
	return 0
}
