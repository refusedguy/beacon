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
	"unicode"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// sessionSpawner is the narrow port used by beacon spawn. CreateSession creates
// an OpenCode session with the orchestrator's parentID, the agent id, a title
// and the metadata that links the new session to beacon watch. PromptWithOptions
// sends the first text message with a deterministic id, delivery steer and
// resume true, so the new session continues its turn (ADR 0306, ADR 0309).
//
// The agent belongs to CreateSession, not to the prompt: in the v2 prompt route
// the body has no agent field (ADR 0060 п3, docs/design/opencode-cli-manager.md:28).
// Keeping the two calls on one port makes that boundary testable without a live
// server. The production implementation is the single OpenCode client installed
// by SetOpenCodePort; tests install a stand-in with SetSessionSpawner.
type sessionSpawner interface {
	CreateSession(parentID, agent, title string, metadata map[string]string) (id string, err error)
	Prompt(sessionID, text string) error
	PromptWithOptions(ctx context.Context, sessionID, text string, opts opencode.PromptOptions) error
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
		// Session metadata can carry tokens. Only OpenProduction installs the
		// redacting driver, so this handle must not fall back to plain Open (SE-03).
		db, err := store.OpenProduction(filepath.Join(dir, mcpStateDBName))
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

// spawnClaimInstruction pins a spawned child to one card. It names the claim
// tool and the exact cardID, so the child takes its own card instead of the
// first todo card in the store (SL-04).
func spawnClaimInstruction(cardID string) string {
	return fmt.Sprintf("Сначала вызови %s с cardID %s и работай только с этой карточкой.", mcp.TaskClaimToolName, cardID)
}

// spawnPrompt builds the first message of a new session. It names the task and
// adds an explicit claim instruction, so the exact cardID reaches the child in
// text. The agent itself is fixed at session creation and is deliberately absent
// here.
func spawnPrompt(taskID, title string) string {
	var prompt string
	if title != "" {
		prompt = fmt.Sprintf("Выполни задачу Beacon %s: %s", taskID, title)
	} else {
		prompt = fmt.Sprintf("Выполни задачу Beacon %s", taskID)
	}
	return prompt + "\n" + spawnClaimInstruction(taskID)
}

// spawnPromptMatchesMeta reports whether the prompt pins the same card as the
// session metadata. Metadata without spawnMetaTaskID is left unconstrained.
func spawnPromptMatchesMeta(prompt string, metadata map[string]string) bool {
	cardID, ok := metadata[spawnMetaTaskID]
	if !ok {
		return true
	}
	return strings.Contains(prompt, spawnClaimInstruction(cardID))
}

// AgentDefinition is the single typed description of a subagent handed to the
// runtime when a session is initialized. Role, model, tool set and step budget
// live in one structure, so two parallel workers built from the same
// definition are told apart only by their agent_id. The definition travels in
// session metadata, the initialization channel used by CreateSession, and
// never in a command argument. The agent_id is assigned from the card id at
// spawn time, so attribution does not depend on the order tool events arrive
// (CS-03).
type AgentDefinition struct {
	AgentID   string   `json:"agent_id"`
	Role      string   `json:"role"`
	Model     string   `json:"model,omitempty"`
	Tools     []string `json:"tools,omitempty"`
	StepLimit int      `json:"step_limit,omitempty"`
}

const (
	// agentDefinitionMetaKey carries the serialized AgentDefinition in the
	// session metadata passed to CreateSession.
	agentDefinitionMetaKey = "ai.beacon/agent.definition"
	// agentIDMetaKey carries the opaque agent_id on every spawned session so
	// tool events and step messages can attribute parallel workers.
	agentIDMetaKey = "ai.beacon/agent_id"
	// defaultAgentModel is used when a definition does not pin a model.
	defaultAgentModel = "inherit"
)

// agentIDForCard derives a stable, opaque agent id from a card id. Two cards
// always produce different ids and a repeat of the same card reuses its id, so
// parallel agents stay distinguishable regardless of message order.
func agentIDForCard(cardID string) string {
	return "agent:" + strings.TrimSpace(cardID)
}

// newAgentDefinition builds one definition with defaults filled in. Callers
// set the optional model, tools and step budget on the returned value.
func newAgentDefinition(agentID, role string) AgentDefinition {
	return AgentDefinition{AgentID: agentID, Role: role, Model: defaultAgentModel}
}

// agentDefinitions is the process-local registry of named definitions. The
// composition root adds entries through RegisterAgentDefinition; an unknown
// --agent keeps the legacy metadata shape and attaches no definition.
var agentDefinitions = struct {
	sync.RWMutex
	defs map[string]AgentDefinition
}{defs: make(map[string]AgentDefinition)}

// RegisterAgentDefinition adds or replaces one named definition. It is the
// only writer of the registry, so two callers cannot race to define a role.
func RegisterAgentDefinition(def AgentDefinition) {
	role := strings.TrimSpace(def.Role)
	if role == "" {
		return
	}
	agentDefinitions.Lock()
	agentDefinitions.defs[role] = def
	agentDefinitions.Unlock()
}

// resolveAgentDefinition returns the registered definition for a role, or nil.
func resolveAgentDefinition(role string) *AgentDefinition {
	agentDefinitions.RLock()
	def, ok := agentDefinitions.defs[strings.TrimSpace(role)]
	agentDefinitions.RUnlock()
	if !ok {
		return nil
	}
	return &def
}

// attachAgentDefinition writes a definition into session metadata, the
// initialization channel used by CreateSession. Both the serialized structure
// and the agent_id are added, so a consumer never parses tool arguments to
// learn which parallel worker produced an event.
func attachAgentDefinition(metadata map[string]string, def *AgentDefinition) map[string]string {
	if def == nil {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	payload, err := json.Marshal(def)
	if err != nil {
		return metadata
	}
	metadata[agentDefinitionMetaKey] = string(payload)
	metadata[agentIDMetaKey] = def.AgentID
	return metadata
}

// verbatimPromptMeta marks a spawned session's first prompt as client-composed
// when --verbatim is set. The marker goes through the session metadata port,
// not the prompt text. A card title with @/etc/passwd stays the literal string
// the caller supplied, and building the prompt reads no file. The flag is off
// by default, so existing spawns keep their current metadata.
func verbatimPromptMeta(metadata map[string]string, verbatim bool) map[string]string {
	if !verbatim {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	for key, value := range mcp.VerbatimPromptMeta() {
		metadata[key] = value
	}
	return metadata
}

// runSpawn is the CLI wrapper for `beacon spawn`. It reads the parent session
// from BEACON_PARENT_SESSION when a shell sets it and delegates to
// runSpawnWithParent (ADR 0309). The variable is no longer required: an
// orchestrator that calls the command in-process passes the parent directly.
func (c *CLI) runSpawn(args []string) int {
	return c.runSpawnWithParent(args, strings.TrimSpace(os.Getenv(spawnParentEnv)))
}

// runSpawnWithParent implements `beacon spawn <taskID> [--agent id] [--title ...]`
// and `beacon spawn --runtime acp [taskID] [--runner name]` with an explicit
// parentID. A daemon calls this seam without preparing process environment.
// The default path creates a child session through the injected port; --runtime
// acp starts a long-lived ACP runner instead and stays attached to its
// session/update stream. An empty parentID creates a root session. No HTTP
// server or TUI is opened here (ADR 0060 п3 and п5, ADR 0309).
func (c *CLI) runSpawnWithParent(args []string, parentID string) int {
	if hasHelpFlag(args) {
		writeCommandHelp(c.out, "spawn")
		return 0
	}
	// parseFlags rejects positional arguments and the flag package stops at the
	// first one, while spawn accepts flags both before and after the taskID.
	// parseTaskArgs is the existing parser with that shape (cmd/beacon/task.go).
	p, err := parseTaskArgs(args, map[string]bool{
		"agent": true, "title": true, "runtime": true, "runner": true, "verbatim": true,
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
		// The ACP runtime has no session metadata port, so it cannot carry the
		// client_composed marker. Fail instead of accepting a flag with no effect.
		if p.bools["verbatim"] {
			fmt.Fprintln(c.errOut, "beacon spawn: --verbatim не поддерживается runtime acp")
			return 2
		}
		return c.runSpawnACP(parentID, taskID, strings.TrimSpace(p.value("title")), strings.TrimSpace(p.value("runner")))
	default:
		fmt.Fprintf(c.errOut, "beacon spawn: неподдерживаемый runtime %q (доступен: acp)\n", runtime)
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
	metadata = verbatimPromptMeta(metadata, p.bools["verbatim"])
	// A registered definition travels through the initialization channel, not
	// the command line. An unknown --agent keeps the legacy metadata shape.
	metadata = attachAgentDefinition(metadata, resolveAgentDefinition(agentID))

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

	// The first prompt carries a deterministic id derived from the task, so a
	// repeat of the same spawn does not start a second turn. delivery steer with
	// resume true continues the created session instead of restarting its
	// context (ADR 0060 п3, ADR 0306, ADR 0309).
	prompt := spawnPrompt(taskID, sessionTitle)
	if !spawnPromptMatchesMeta(prompt, metadata) {
		fmt.Fprintln(c.errOut, "beacon spawn: cardID в промпте и метаданных расходятся")
		return 2
	}
	if err := spawner.PromptWithOptions(context.Background(), sessionID, prompt, opencode.StartPromptOptions(taskID)); err != nil {
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
		// ACP results are JSON with free-form text. OpenProduction redacts
		// every bound value before it reaches SQLite (SE-03).
		db, err := store.OpenProduction(filepath.Join(dir, mcpStateDBName))
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
// can route ACP through a wrapper. BEACON_ACP_COMMAND accepts an absolute path
// or one of the supported runner names: a bare executable found on PATH would
// let the environment silently pick a different binary (BCX-74).
const (
	spawnACPRunnerEnv  = "BEACON_ACP_RUNNER"
	spawnACPCommandEnv = "BEACON_ACP_COMMAND"
	spawnACPArgsEnv    = "BEACON_ACP_ARGS"
)

// validACPCommand reports whether an environment supplied executable may
// replace the runner. An absolute path is an explicit choice by the operator;
// a bare name is only safe when it is a runner beacon already knows.
func validACPCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return true
	}
	if filepath.IsAbs(command) {
		return true
	}
	for _, name := range wake.SupportedACPRuntimes() {
		if command == name {
			return true
		}
	}
	return false
}

// splitACPArgs splits an ACP argument string on whitespace. Single and double
// quotes group text into one argument and are removed from the value, so an
// argument that contains spaces can be passed without a shell (BCX-74).
func splitACPArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var quote rune
	inQuote, started := false, false
	for _, r := range s {
		switch {
		case inQuote:
			if r == quote {
				inQuote = false
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			inQuote, quote, started = true, r, true
		case unicode.IsSpace(r):
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		return nil, fmt.Errorf("acp args: unclosed %c quote", quote)
	}
	if started {
		args = append(args, cur.String())
	}
	return args, nil
}

// validateACPEnv checks the ACP environment overrides before the runner
// starts. Invalid values are reported instead of silently changing which
// executable beacon launches (BCX-74).
func validateACPEnv() error {
	command := strings.TrimSpace(os.Getenv(spawnACPCommandEnv))
	if command != "" && !validACPCommand(command) {
		return fmt.Errorf("%s must be an absolute path or a supported runner name, got %q", spawnACPCommandEnv, command)
	}
	if args := strings.TrimSpace(os.Getenv(spawnACPArgsEnv)); args != "" {
		if _, err := splitACPArgs(args); err != nil {
			return fmt.Errorf("%s: %w", spawnACPArgsEnv, err)
		}
	}
	return nil
}

func spawnACPConfig(runner string) wake.ACPRuntimeConfig {
	runner = strings.TrimSpace(runner)
	if runner == "" {
		runner = strings.TrimSpace(os.Getenv(spawnACPRunnerEnv))
	}
	if runner == "" {
		runner = "opencode"
	}
	cfg := wake.ACPRuntimeConfig{Runtime: runner}
	if command := strings.TrimSpace(os.Getenv(spawnACPCommandEnv)); validACPCommand(command) {
		cfg.Command = command
	}
	if args := strings.TrimSpace(os.Getenv(spawnACPArgsEnv)); args != "" {
		if parsed, err := splitACPArgs(args); err == nil {
			cfg.Args = parsed
		}
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
	AgentID   string            `json:"agent_id,omitempty"`
	Title     string            `json:"title,omitempty"`
	Updates   []json.RawMessage `json:"updates"`
}

// spawnStatsSink lazily opens the state DB so runtime_stats is written even
// when the composition root has not installed a store yet.
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
		// runtime_stats stores payload text from the spawned runner. The
		// redacting driver on OpenProduction keeps tokens out of the file (SE-03).
		db, err := store.OpenProduction(filepath.Join(dir, mcpStateDBName))
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
	stats := &acpSessionStats{TaskID: taskID, SessionID: sessionID, Runner: runner, AgentID: agentIDForCard(taskID), Title: title}
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
					"agent_id": stats.AgentID,
					"runner":   runner, "update": update.Update,
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
// stream while the worker is still running. parentID is accepted explicitly so
// every spawn entry point carries the same seam; the ACP path itself is
// unchanged (ADR 0309).
func (c *CLI) runSpawnACP(parentID, taskID, title, runner string) int {
	_ = parentID // The ACP runner owns its session; it needs no OpenCode parent.
	factory := spawnACPRuntimeFactory
	if factory == nil {
		fmt.Fprintln(c.errOut, "beacon spawn: ACP-раннер не подключён")
		return 2
	}
	if err := validateACPEnv(); err != nil {
		fmt.Fprintf(c.errOut, "beacon spawn: %v\n", err)
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
