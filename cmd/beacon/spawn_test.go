package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/opencode"
)

// t3142CreateCall and t3142PromptCall record the two halves of the port so a
// test can see which call carried the agent.
type t3142CreateCall struct {
	parentID string
	agent    string
	title    string
	metadata map[string]string
}

type t3142PromptCall struct {
	sessionID string
	text      string
}

type t3142PromptOptionsCall struct {
	sessionID string
	text      string
	opts      opencode.PromptOptions
}

// t3142Spawner is a stand-in for the one OpenCode client. It never touches a
// network or a process; it only records calls and returns configured results.
type t3142Spawner struct {
	id            string
	createErr     error
	promptErr     error
	creates       []t3142CreateCall
	prompts       []t3142PromptCall
	promptOptions []t3142PromptOptionsCall
}

func (s *t3142Spawner) CreateSession(parentID, agent, title string, metadata map[string]string) (string, error) {
	s.creates = append(s.creates, t3142CreateCall{parentID, agent, title, metadata})
	if s.createErr != nil {
		return "", s.createErr
	}
	return s.id, nil
}

func (s *t3142Spawner) Prompt(sessionID, text string) error {
	s.prompts = append(s.prompts, t3142PromptCall{sessionID, text})
	return s.promptErr
}

func (s *t3142Spawner) PromptWithOptions(_ context.Context, sessionID, text string, opts opencode.PromptOptions) error {
	s.promptOptions = append(s.promptOptions, t3142PromptOptionsCall{sessionID, text, opts})
	return s.promptErr
}

// t3142Install saves the previous port and installs s for one test. A nil s
// clears the port, which is how the missing-port case is exercised.
func t3142Install(t *testing.T, s sessionSpawner) {
	t.Helper()
	previous := spawnSessionSpawner
	SetSessionSpawner(s)
	t.Cleanup(func() { SetSessionSpawner(previous) })
}

// t3142CLI builds a CLI with in-memory writers.
func t3142CLI() (*CLI, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	return &CLI{out: out, errOut: errOut}, out, errOut
}

// TestT3142SpawnSendsAgentAtCreation is the core contract from ADR 0060 п3:
// the agent is fixed when the session is created, never in the prompt body.
func TestT3142SpawnSendsAgentAtCreation(t *testing.T) {
	spawner := &t3142Spawner{id: "sess-42"}
	t3142Install(t, spawner)
	t.Setenv(spawnParentEnv, " parent-orchestrator ")
	c, out, errOut := t3142CLI()

	code := c.runSpawn([]string{"T3.142", "--agent", "builder", "--title", " Card title "})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}

	if len(spawner.creates) != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", len(spawner.creates))
	}
	create := spawner.creates[0]
	if create.parentID != "parent-orchestrator" {
		t.Errorf("parentID = %q, want trimmed orchestrator id", create.parentID)
	}
	if create.agent != "builder" {
		t.Errorf("agent = %q, want builder", create.agent)
	}
	if create.title != "Card title" {
		t.Errorf("title = %q, want trimmed card title", create.title)
	}
	wantMeta := map[string]string{
		spawnMetaTaskID: "T3.142",
		spawnMetaTitle:  "Card title",
	}
	if !reflect.DeepEqual(create.metadata, wantMeta) {
		t.Errorf("metadata = %#v, want %#v", create.metadata, wantMeta)
	}

	if len(spawner.promptOptions) != 1 {
		t.Fatalf("PromptWithOptions calls = %d, want 1", len(spawner.promptOptions))
	}
	prompt := spawner.promptOptions[0]
	if prompt.sessionID != "sess-42" {
		t.Errorf("prompt sessionID = %q, want sess-42", prompt.sessionID)
	}
	if !strings.Contains(prompt.text, "T3.142") {
		t.Errorf("prompt %q does not name the task", prompt.text)
	}
	if strings.Contains(prompt.text, "builder") || strings.Contains(prompt.text, `"agent"`) {
		t.Errorf("prompt %q must not carry the agent", prompt.text)
	}
	if want := opencode.StartPromptOptions("T3.142"); prompt.opts != want {
		t.Errorf("prompt options = %+v, want %+v", prompt.opts, want)
	}

	if got := out.String(); got != "sess-42\n" {
		t.Errorf("stdout = %q, want session id line", got)
	}
}

// TestT3142SpawnWithoutAgentStillSeparatesThePorts checks that an omitted
// --agent is passed to CreateSession as empty and never leaks into Prompt.
func TestT3142SpawnWithoutAgentStillSeparatesThePorts(t *testing.T) {
	spawner := &t3142Spawner{id: "sess-7"}
	t3142Install(t, spawner)
	t.Setenv(spawnParentEnv, "parent-1")
	c, _, errOut := t3142CLI()

	if code := c.runSpawn([]string{"T3.142"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if len(spawner.creates) != 1 || spawner.creates[0].agent != "" {
		t.Fatalf("creates = %#v, want one call with empty agent", spawner.creates)
	}
	if len(spawner.promptOptions) != 1 {
		t.Fatalf("prompts = %d, want 1", len(spawner.promptOptions))
	}
	if _, ok := spawner.creates[0].metadata[spawnMetaTitle]; ok {
		t.Error("title metadata present without --title")
	}
}

func TestT3142SpawnRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		parent  string
		spawner sessionSpawner
		stderr  string
	}{
		{"no task", nil, "parent-1", &t3142Spawner{id: "s"}, "beacon spawn: нужна одна команда"},
		{"two tasks", []string{"T1", "T2"}, "parent-1", &t3142Spawner{id: "s"}, "beacon spawn: нужна одна команда"},
		{"no port", []string{"T3.142"}, "parent-1", nil, "порт создания сессий не подключён"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t3142Install(t, tc.spawner)
			t.Setenv(spawnParentEnv, tc.parent)
			c, _, errOut := t3142CLI()
			if code := c.runSpawn(tc.args); code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.stderr) {
				t.Errorf("stderr = %q, want %q", errOut.String(), tc.stderr)
			}
		})
	}
}

// TestAS202SpawnWithExplicitParent proves that a daemon can spawn without
// preparing process environment: the parent travels as a parameter and reaches
// CreateSession, and the first prompt carries a deterministic id with steer
// and resume.
func TestAS202SpawnWithExplicitParent(t *testing.T) {
	spawner := &t3142Spawner{id: "sess-202"}
	t3142Install(t, spawner)
	t.Setenv(spawnParentEnv, "")
	c, out, errOut := t3142CLI()

	code := c.runSpawnWithParent([]string{"T3.142"}, "ses_parent")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if len(spawner.creates) != 1 || spawner.creates[0].parentID != "ses_parent" {
		t.Fatalf("creates = %#v, want one call with parent ses_parent", spawner.creates)
	}
	if len(spawner.promptOptions) != 1 {
		t.Fatalf("prompts = %d, want 1", len(spawner.promptOptions))
	}
	opts := spawner.promptOptions[0].opts
	if opts.MessageID != "msg_T3.142" || opts.Delivery != "steer" || !opts.Resume {
		t.Errorf("prompt options = %+v, want msg_T3.142, delivery steer, resume true", opts)
	}
	if got := out.String(); got != "sess-202\n" {
		t.Errorf("stdout = %q, want session id line", got)
	}
}

// TestAS202RunSpawnWithoutEnvKeepsCLIWorking checks that the CLI wrapper no
// longer requires BEACON_PARENT_SESSION and still prints the session id.
func TestAS202RunSpawnWithoutEnvKeepsCLIWorking(t *testing.T) {
	spawner := &t3142Spawner{id: "sess-cli"}
	t3142Install(t, spawner)
	t.Setenv(spawnParentEnv, "")
	c, out, errOut := t3142CLI()

	if code := c.runSpawn([]string{"T3.142"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if len(spawner.creates) != 1 || spawner.creates[0].parentID != "" {
		t.Fatalf("creates = %#v, want one call with empty parent", spawner.creates)
	}
	if got := out.String(); got != "sess-cli\n" {
		t.Errorf("stdout = %q, want session id line", got)
	}
}

func TestT3142SpawnReportsPortErrors(t *testing.T) {
	cases := []struct {
		name    string
		spawner *t3142Spawner
		stderr  string
	}{
		{"create failed", &t3142Spawner{createErr: errors.New("boom")}, "не удалось создать сессию: boom"},
		{"empty id", &t3142Spawner{}, "пустой sessionID"},
		{"prompt failed", &t3142Spawner{id: "sess-1", promptErr: errors.New("no route")}, "промпт не отправлен: no route"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t3142Install(t, tc.spawner)
			t.Setenv(spawnParentEnv, "parent-1")
			c, out, errOut := t3142CLI()
			if code := c.runSpawn([]string{"T3.142", "--agent", "builder"}); code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %q", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.stderr) {
				t.Errorf("stderr = %q, want %q", errOut.String(), tc.stderr)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want no id on failure", out.String())
			}
		})
	}
}
