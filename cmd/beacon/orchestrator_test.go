package main

import (
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// recordingRebindStore captures the store arguments and returns a fixed list
// of affected Watches, so the command tests stay at the CLI boundary.
type recordingRebindStore struct {
	calls   int
	from    string
	to      string
	dryRun  bool
	targets []store.RebindTarget
	err     error
}

func (s *recordingRebindStore) RebindOrchestrator(from, to string, dryRun bool) ([]store.RebindTarget, error) {
	s.calls++
	s.from, s.to, s.dryRun = from, to, dryRun
	return s.targets, s.err
}

type recordedWake struct {
	session string
	text    string
}

func installRebindFakes(t *testing.T, s rebindStore, wakes *[]recordedWake) {
	t.Helper()
	oldFactory, oldWake := makeRebindStore, rebindWake
	t.Cleanup(func() {
		makeRebindStore = oldFactory
		rebindWake = oldWake
	})
	SetRebindStore(func(*CLI) (rebindStore, func() error, error) {
		return s, func() error { return nil }, nil
	})
	rebindWake = func(sessionID, text string) error {
		*wakes = append(*wakes, recordedWake{session: sessionID, text: text})
		return nil
	}
}

func TestOrchestratorRebindUpdatesTargetsAndSendsOneWake(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	s := &recordingRebindStore{targets: []store.RebindTarget{
		{WatchID: "watch-1", PR: 7, Reason: "orchestratorID + orchestrator subscriber"},
		{WatchID: "watch-2", PR: 9, Reason: "orchestratorID"},
	}}
	var wakes []recordedWake
	installRebindFakes(t, s, &wakes)

	code := c.Run([]string{"orchestrator", "rebind", "--from", "ses_old", "--to", "ses_new"})
	if code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	if s.calls != 1 || s.from != "ses_old" || s.to != "ses_new" || s.dryRun {
		t.Fatalf("store call = %+v, want one live rebind ses_old -> ses_new", s)
	}
	if len(wakes) != 1 {
		t.Fatalf("wakes = %d, want exactly one", len(wakes))
	}
	if wakes[0].session != "ses_new" {
		t.Errorf("wake addressee = %q, want ses_new", wakes[0].session)
	}
	for _, want := range []string{"watch=watch-1", "pr=7", "watch=watch-2", "pr=9", "reason=orchestratorID"} {
		if !strings.Contains(wakes[0].text, want) {
			t.Errorf("wake text missing %q: %q", want, wakes[0].text)
		}
	}
	for _, want := range []string{"watch=watch-1", "watch=watch-2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout missing %q: %q", want, out.String())
		}
	}
}

func TestOrchestratorRebindRejectsInvalidNewSession(t *testing.T) {
	c, _, errOut, _ := t06NewCLI(t, nil)
	s := &recordingRebindStore{}
	var wakes []recordedWake
	installRebindFakes(t, s, &wakes)

	code := c.Run([]string{"orchestrator", "rebind", "--from", "ses_old", "--to", "not-a-session"})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "ses_") {
		t.Errorf("stderr does not explain the session prefix: %q", errOut.String())
	}
	if s.calls != 0 {
		t.Errorf("store was called %d times for an invalid target", s.calls)
	}
	if len(wakes) != 0 {
		t.Errorf("invalid target still produced %d wakes", len(wakes))
	}
}

func TestOrchestratorRebindDryRunSkipsWriteAndWake(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	s := &recordingRebindStore{targets: []store.RebindTarget{{WatchID: "watch-1", PR: 7, Reason: "orchestratorID"}}}
	var wakes []recordedWake
	installRebindFakes(t, s, &wakes)

	code := c.Run([]string{"orchestrator", "rebind", "--from", "ses_old", "--to", "ses_new", "--dry-run"})
	if code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	if s.calls != 1 || !s.dryRun {
		t.Fatalf("store call = %+v, want one dry run", s)
	}
	if len(wakes) != 0 {
		t.Errorf("dry run produced %d wakes", len(wakes))
	}
	if !strings.Contains(out.String(), "без записи") {
		t.Errorf("dry run not reported: %q", out.String())
	}
}

func TestOrchestratorRebindWithNoTargetsSendsNoWake(t *testing.T) {
	c, _, errOut, _ := t06NewCLI(t, nil)
	s := &recordingRebindStore{}
	var wakes []recordedWake
	installRebindFakes(t, s, &wakes)

	code := c.Run([]string{"orchestrator", "rebind", "--from", "ses_absent", "--to", "ses_new"})
	if code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	if len(wakes) != 0 {
		t.Errorf("no targets still produced %d wakes", len(wakes))
	}
}

func TestOrchestratorRequiresRebindSubcommand(t *testing.T) {
	c, _, _, _ := t06NewCLI(t, nil)
	if code := c.Run([]string{"orchestrator"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
