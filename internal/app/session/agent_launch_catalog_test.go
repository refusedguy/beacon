package session

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAgentLaunchCatalogClaudeExactResume(t *testing.T) {
	got, err := DefaultAgentLaunchCatalog().BuildCommand("claude", "sess-123", PermissionModeAcceptEdits)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	want := "claude --resume sess-123 --permission-mode acceptEdits"
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestAgentLaunchCatalogContinueWithoutSessionID(t *testing.T) {
	got, err := DefaultAgentLaunchCatalog().BuildCommand("claude", "", PermissionModeAcceptEdits)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	want := "claude --continue --permission-mode acceptEdits"
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestAgentLaunchCatalogDefaultModeHasNoFlag(t *testing.T) {
	got, err := DefaultAgentLaunchCatalog().BuildCommand("claude", "sess-9", PermissionModeDefault)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if want := "claude --resume sess-9"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestAgentLaunchCatalogCodexResume(t *testing.T) {
	got, err := DefaultAgentLaunchCatalog().BuildCommand("codex", "sess-7", PermissionModeReadOnly)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	want := "codex resume sess-7 --sandbox read-only"
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestAgentLaunchCatalogEscapesSessionID(t *testing.T) {
	const supplied = "sess 1'bad"
	got, err := DefaultAgentLaunchCatalog().BuildCommand("claude", supplied, PermissionModeDefault)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if want := "claude --resume 'sess 1'\\''bad'"; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if strings.Contains(got, supplied) {
		t.Errorf("command kept the unescaped session id %q: %q", supplied, got)
	}
}

func TestAgentLaunchCatalogRejectsUnknownAgent(t *testing.T) {
	_, err := DefaultAgentLaunchCatalog().BuildCommand("mystery", "sess-1", PermissionModeDefault)
	if !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("error = %v, want ErrUnknownAgent", err)
	}
}

func TestAgentLaunchCatalogRejectsUnknownPermissionMode(t *testing.T) {
	_, err := DefaultAgentLaunchCatalog().BuildCommand("claude", "sess-1", "admin")
	if !errors.Is(err, ErrUnknownPermissionMode) {
		t.Fatalf("error = %v, want ErrUnknownPermissionMode", err)
	}
}

func TestAgentLaunchCatalogEntryAndIDs(t *testing.T) {
	catalog := DefaultAgentLaunchCatalog()
	if got, want := catalog.AgentIDs(), []string{"claude", "codex"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AgentIDs = %#v, want %#v", got, want)
	}
	entry, ok := catalog.Entry("claude")
	if !ok {
		t.Fatal("Entry(claude) not found")
	}
	if entry.ResumeTemplate == "" || entry.ContinueTemplate == "" {
		t.Errorf("claude entry misses a template: %+v", entry)
	}
	if _, ok := catalog.Entry("mystery"); ok {
		t.Error("Entry reported an unknown agent as found")
	}
}

func TestAgentLaunchCatalogAddingAgentNeedsOneEntry(t *testing.T) {
	extra := AgentLaunchEntry{
		ID:               "newcli",
		Command:          "newcli",
		PromptMode:       PromptModeStdin,
		PermissionFlags:  map[string]string{PermissionModeAcceptEdits: "--write"},
		ResumeTemplate:   "{command} attach {sessionId} {permissions}",
		ContinueTemplate: "{command} new {permissions}",
	}
	entries := append(append([]AgentLaunchEntry{}, defaultAgentLaunchEntries...), extra)
	catalog, err := NewAgentLaunchCatalog(entries)
	if err != nil {
		t.Fatalf("NewAgentLaunchCatalog: %v", err)
	}
	got, err := catalog.BuildCommand("newcli", "sess-5", PermissionModeAcceptEdits)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if want := "newcli attach sess-5 --write"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestNewAgentLaunchCatalogRejectsDuplicateIDs(t *testing.T) {
	entry := AgentLaunchEntry{
		ID:               "claude",
		Command:          "claude",
		ContinueTemplate: "{command} --continue",
	}
	_, err := NewAgentLaunchCatalog([]AgentLaunchEntry{entry, entry})
	if !errors.Is(err, ErrDuplicateAgent) {
		t.Fatalf("error = %v, want ErrDuplicateAgent", err)
	}
}

func TestNewAgentLaunchCatalogRejectsIncompleteEntry(t *testing.T) {
	entry := AgentLaunchEntry{ID: "broken"}
	if _, err := NewAgentLaunchCatalog([]AgentLaunchEntry{entry}); !errors.Is(err, ErrIncompleteAgentEntry) {
		t.Fatalf("error = %v, want ErrIncompleteAgentEntry", err)
	}
}
