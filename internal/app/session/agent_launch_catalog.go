package session

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Permission modes select the CLI flags that guard file writes. A mode has a
// stable name across agents. The flags each agent uses for that mode live in
// the agent's catalog entry.
const (
	PermissionModeDefault     = "default"
	PermissionModeAcceptEdits = "acceptEdits"
	PermissionModeReadOnly    = "readOnly"
)

// Prompt modes name how a CLI receives its prompt argument.
const (
	PromptModeArg   = "arg"
	PromptModeStdin = "stdin"
)

// Template placeholders. A template is a shell command string. BuildCommand
// fills these before it returns the command.
const (
	templateCommand    = "{command}"
	templateSessionID  = "{sessionId}"
	templatePermission = "{permissions}"
)

// AgentLaunchEntry describes one agent CLI. It holds the install command, the
// search paths, the prompt mode, the permission flags and both resume
// templates for a single agent. Adding an agent needs only one new entry.
type AgentLaunchEntry struct {
	ID               string
	Command          string
	InstallCommand   string
	SearchPaths      []string
	PromptMode       string
	PermissionFlags  map[string]string
	ResumeTemplate   string
	ContinueTemplate string
}

var (
	// ErrUnknownAgent means the catalog has no entry for the given agent id.
	ErrUnknownAgent = errors.New("session: unknown agent")
	// ErrUnknownPermissionMode means the entry has no flags for the given
	// permission mode.
	ErrUnknownPermissionMode = errors.New("session: unknown permission mode")
	// ErrIncompleteAgentEntry means an entry misses a command or a continue
	// template.
	ErrIncompleteAgentEntry = errors.New("session: incomplete agent entry")
	// ErrDuplicateAgent means two entries share one id.
	ErrDuplicateAgent = errors.New("session: duplicate agent id")
)

// AgentLaunchCatalog is an immutable lookup of agent CLI descriptions. Build
// one with NewAgentLaunchCatalog or DefaultAgentLaunchCatalog. Do not mutate
// entries after the catalog is built.
type AgentLaunchCatalog struct {
	byID map[string]AgentLaunchEntry
}

// NewAgentLaunchCatalog indexes entries by id. It rejects an empty id, an
// empty command, a missing continue template and duplicate ids.
func NewAgentLaunchCatalog(entries []AgentLaunchEntry) (*AgentLaunchCatalog, error) {
	byID := make(map[string]AgentLaunchEntry, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			return nil, fmt.Errorf("%w: empty id", ErrIncompleteAgentEntry)
		}
		if strings.TrimSpace(entry.Command) == "" {
			return nil, fmt.Errorf("%w: %q has no command", ErrIncompleteAgentEntry, entry.ID)
		}
		if strings.TrimSpace(entry.ContinueTemplate) == "" {
			return nil, fmt.Errorf("%w: %q has no continue template", ErrIncompleteAgentEntry, entry.ID)
		}
		if _, exists := byID[entry.ID]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateAgent, entry.ID)
		}
		byID[entry.ID] = entry
	}
	return &AgentLaunchCatalog{byID: byID}, nil
}

// defaultAgentLaunchEntries is the single place where agent CLI commands and
// permission flags live. To add an agent, append one entry here.
var defaultAgentLaunchEntries = []AgentLaunchEntry{
	{
		ID:             "claude",
		Command:        "claude",
		InstallCommand: "npm install -g @anthropic-ai/claude-code",
		SearchPaths:    []string{".opencode/bin", "/usr/local/bin", "/usr/bin"},
		PromptMode:     PromptModeArg,
		PermissionFlags: map[string]string{
			PermissionModeDefault:     "",
			PermissionModeAcceptEdits: "--permission-mode acceptEdits",
			PermissionModeReadOnly:    "--permission-mode readOnly",
		},
		ResumeTemplate:   "{command} --resume {sessionId} {permissions}",
		ContinueTemplate: "{command} --continue {permissions}",
	},
	{
		ID:             "codex",
		Command:        "codex",
		InstallCommand: "npm install -g @openai/codex",
		SearchPaths:    []string{".opencode/bin", "/usr/local/bin", "/usr/bin"},
		PromptMode:     PromptModeArg,
		PermissionFlags: map[string]string{
			PermissionModeDefault:     "",
			PermissionModeAcceptEdits: "--approval-mode auto-edit",
			PermissionModeReadOnly:    "--sandbox read-only",
		},
		ResumeTemplate:   "{command} resume {sessionId} {permissions}",
		ContinueTemplate: "{command} resume --last {permissions}",
	},
}

// DefaultAgentLaunchCatalog builds the catalog from the shipped entries. The
// entries are static and validated by NewAgentLaunchCatalog, so a failure
// here means a programmer error in the entry list.
func DefaultAgentLaunchCatalog() *AgentLaunchCatalog {
	catalog, err := NewAgentLaunchCatalog(defaultAgentLaunchEntries)
	if err != nil {
		panic(err)
	}
	return catalog
}

// Entry returns the description for id. found is false for an unknown id.
func (c *AgentLaunchCatalog) Entry(id string) (AgentLaunchEntry, bool) {
	if c == nil {
		return AgentLaunchEntry{}, false
	}
	entry, ok := c.byID[id]
	return entry, ok
}

// AgentIDs returns the sorted ids in the catalog. The order is stable so the
// list can be shown to a user or compared in a test.
func (c *AgentLaunchCatalog) AgentIDs() []string {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.byID))
	for id := range c.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// BuildCommand renders the launch command for an agent. A non-empty sessionID
// selects the resume template so the same session opens again. An empty
// sessionID selects the continue template. Unknown agent ids and unknown
// permission modes return an error. The session id is escaped before it is
// substituted.
func (c *AgentLaunchCatalog) BuildCommand(agentID, sessionID, permissionMode string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("%w: %q", ErrUnknownAgent, agentID)
	}
	entry, ok := c.byID[agentID]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownAgent, agentID)
	}
	flags, ok := entry.PermissionFlags[permissionMode]
	if !ok {
		return "", fmt.Errorf("%w: %q for agent %q", ErrUnknownPermissionMode, permissionMode, agentID)
	}

	template := entry.ContinueTemplate
	if strings.TrimSpace(sessionID) != "" && entry.ResumeTemplate != "" {
		template = entry.ResumeTemplate
	}

	rendered := strings.NewReplacer(
		templateCommand, entry.Command,
		templateSessionID, escapeSessionID(sessionID),
		templatePermission, flags,
	).Replace(template)
	return strings.Join(strings.Fields(rendered), " "), nil
}

// escapeSessionID quotes a session id for use in a shell command. Plain ids
// stay readable. Anything with a space, a quote or another shell character is
// wrapped in single quotes, and embedded single quotes are escaped.
func escapeSessionID(id string) string {
	if id == "" {
		return "''"
	}
	for _, r := range id {
		safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '/'
		if !safe {
			return "'" + strings.ReplaceAll(id, "'", `'\''`) + "'"
		}
	}
	return id
}
