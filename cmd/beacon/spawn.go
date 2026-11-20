package main

import (
	"fmt"
	"os"
	"strings"
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

// Metadata keys on the created session. beacon watch already joins a session
// by its opaque sessionID; the task id in metadata is what makes the link
// readable back to Beacon. The title is stored only when the caller set one.
const (
	spawnMetaTaskID = "ai.beacon/taskID"
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

// runSpawn implements `beacon spawn <taskID> [--agent id] [--title ...]`. It
// creates a child session through the injected port, sends it one prompt and
// prints the new sessionID. No server is started and no TUI is opened here:
// both stay the job of the adapter and of beacon attach (ADR 0060 п3 and п5).
func (c *CLI) runSpawn(args []string) int {
	// parseFlags rejects positional arguments and the flag package stops at the
	// first one, while spawn accepts flags both before and after the taskID.
	// parseTaskArgs is the existing parser with that shape (cmd/beacon/task.go).
	p, err := parseTaskArgs(args, map[string]bool{"agent": true, "title": true}, nil)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon spawn: %v\n", err)
		return 2
	}
	if len(p.pos) != 1 || strings.TrimSpace(p.pos[0]) == "" {
		fmt.Fprintln(c.errOut, "beacon spawn: нужна одна команда: beacon spawn <taskID> [--agent id] [--title ...]")
		return 2
	}

	taskID := strings.TrimSpace(p.pos[0])
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

	fmt.Fprintln(c.out, sessionID)
	return 0
}
