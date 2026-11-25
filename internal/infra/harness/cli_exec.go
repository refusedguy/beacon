package harness

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

type CLIExec struct {
	Command  string
	Template string
	Timeout  time.Duration
	events   chan core.HarnessEvent
}

func NewCLIExec(command, tmpl string) *CLIExec {
	return &CLIExec{Command: command, Template: tmpl, Timeout: 30 * time.Minute, events: make(chan core.HarnessEvent, 16)}
}

func (c *CLIExec) render(tmpl, prompt string) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		return prompt, nil
	}
	t, err := template.New("prompt").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, map[string]string{"Prompt": prompt, "Task": prompt}); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (c *CLIExec) run(ctx context.Context, prompt string, req core.SpawnRequest) (string, error) {
	text, err := c.render(c.Template, prompt)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", c.Command)
	if req.Workspace.Path != "" {
		cmd.Dir = req.Workspace.Path
	}
	cmd.Stdin = strings.NewReader(shared.Redact(text))
	cmd.Env = append(cmd.Environ(),
		"BEACON_RUN_ID="+string(req.RunID),
		"BEACON_TASK_ID="+string(req.Task.ID),
		"BEACON_WORKSPACE="+req.Workspace.Path,
		"BEACON_BRANCH="+req.Workspace.Branch,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("agent: %w: %s", err, shared.Redact(string(out)))
	}
	return string(out), nil
}

func (c *CLIExec) Spawn(ctx context.Context, req core.SpawnRequest) (core.Session, error) {
	out, err := c.run(ctx, req.Prompt, req)
	if err != nil {
		return core.Session{}, err
	}
	c.emit(req.RunID)
	return core.Session{ID: string(req.RunID), PR: core.PRFromText(out)}, nil
}

func (c *CLIExec) Wake(ctx context.Context, sessionID string, w core.WakeContext) error {
	req := core.SpawnRequest{RunID: w.RunID, Workspace: core.Workspace{Path: w.Workspace, Branch: w.Branch}}
	_, err := c.run(ctx, core.WakeText(w), req)
	if err == nil {
		c.emit(w.RunID)
	}
	return err
}

func (c *CLIExec) Stop(context.Context, string) error { return nil }

// IsAlive reports true because a CLI command runs to completion and leaves no
// session to inspect. Orphan cleanup closes a run only when its liveness check
// returns false, so an unverifiable result must never kill a CLI worker.
func (c *CLIExec) IsAlive(context.Context, string) (bool, error)      { return true, nil }
func (c *CLIExec) NotifyParent(context.Context, string, string) error { return nil }
func (c *CLIExec) Events() <-chan core.HarnessEvent                   { return c.events }

func (c *CLIExec) emit(id core.RunID) {
	if c.events == nil {
		return
	}
	select {
	case c.events <- core.HarnessEvent{RunID: id, Kind: "idle"}:
	default:
	}
}

var _ core.AgentHarness = (*CLIExec)(nil)
