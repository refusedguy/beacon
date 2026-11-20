package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeInspectStore is the read-only slice of the state store inspect uses. It
// returns a fixed binding so the tests cover both the bound and unbound paths
// without opening a real SQLite handle.
type fakeInspectStore struct {
	binding     domain.OrchestratorBinding
	bound       bool
	projectKeys []string
	watches     []domain.Watch
	blockers    []domain.BlockerIncident
	stats       []byte
	statsOK     bool
}

func (s *fakeInspectStore) LiveWatches() ([]domain.Watch, error) { return s.watches, nil }

func (s *fakeInspectStore) ActiveBlockers() ([]domain.BlockerIncident, error) {
	return s.blockers, nil
}

func (s *fakeInspectStore) BoundOrchestrator(projectKey string) (domain.OrchestratorBinding, bool, error) {
	s.projectKeys = append(s.projectKeys, projectKey)
	return s.binding, s.bound, nil
}

func (s *fakeInspectStore) LoadRuntimeStats() ([]byte, bool, error) {
	return s.stats, s.statsOK, nil
}

func installFakeInspectStore(t *testing.T, s *fakeInspectStore) {
	t.Helper()
	old := makeInspectStore
	t.Cleanup(func() { makeInspectStore = old })
	SetInspectStore(func(*CLI) (inspectStore, func() error, error) {
		return s, func() error { return nil }, nil
	})
	t.Setenv(beaconStateDirEnv, t.TempDir())
}

func boundInspectStore() *fakeInspectStore {
	return &fakeInspectStore{
		binding: domain.OrchestratorBinding{
			ProjectKey: "project-key",
			SessionID:  "ses_bound",
			Adapter:    "opencode",
			BoundAt:    time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
		bound: true,
	}
}

func TestInspectTableShowsBoundOrchestrator(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	s := boundInspectStore()
	installFakeInspectStore(t, s)

	if code := c.Run([]string{"inspect"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	want := "Orchestrator: ses_bound (opencode, 2026-01-02T03:04:05Z)"
	if !strings.Contains(out.String(), want) {
		t.Errorf("stdout missing %q: %q", want, out.String())
	}
	if strings.Contains(out.String(), "WARNING: orchestrator not bound") {
		t.Errorf("bound orchestrator still warned: %q", out.String())
	}
	key := orchestratorProjectKey()
	if len(s.projectKeys) != 1 || s.projectKeys[0] != key {
		t.Errorf("BoundOrchestrator keys = %v, want [%s]", s.projectKeys, key)
	}
}

func TestInspectTableWarnsWhenOrchestratorUnbound(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	installFakeInspectStore(t, &fakeInspectStore{})

	if code := c.Run([]string{"inspect"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	want := "WARNING: orchestrator not bound; run beacon orchestrator bind --session <ses_>"
	if !strings.Contains(out.String(), want) {
		t.Errorf("stdout missing %q: %q", want, out.String())
	}
	if strings.Contains(out.String(), "Orchestrator:") {
		t.Errorf("unbound orchestrator printed a binding: %q", out.String())
	}
}

func TestInspectJSONIncludesBoundOrchestrator(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	s := boundInspectStore()
	installFakeInspectStore(t, s)

	if code := c.Run([]string{"inspect", "--json"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	var view inspectView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatalf("decode inspect JSON: %v; output: %q", err, out.String())
	}
	if view.Orchestrator == nil {
		t.Fatalf("orchestrator is empty: %q", out.String())
	}
	if view.Orchestrator.SessionID != "ses_bound" || view.Orchestrator.Adapter != "opencode" {
		t.Errorf("orchestrator = %+v, want ses_bound/opencode", view.Orchestrator)
	}
	if !view.Orchestrator.BoundAt.Equal(s.binding.BoundAt) {
		t.Errorf("boundAt = %s, want %s", view.Orchestrator.BoundAt, s.binding.BoundAt)
	}
}
