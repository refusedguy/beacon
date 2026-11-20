package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

func TestCIWakeTargetResolution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     string
		target   string
		parent   string
		want     string
		wantErr  bool
		errPart  string
	}{
		{name: "flag wins", flag: "ses_flag", target: "ses_env", parent: "ses_parent", want: "ses_flag"},
		{name: "explicit env", target: "ses_env", parent: "ses_parent", want: "ses_env"},
		{name: "orchestrator parent session", parent: "ses_parent", want: "ses_parent"},
		{name: "service name rejected", flag: "beacon-ci-checkall", wantErr: true, errPart: "ses_"},
		{name: "missing target rejected", wantErr: true, errPart: "BEACON_WAKE_TARGET"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(ciWakeTargetEnv, tc.target)
			t.Setenv(ciParentSessionEnv, tc.parent)
			got, err := ciWakeTarget(tc.flag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ciWakeTarget(%q) accepted an invalid target", tc.flag)
				}
				if !strings.Contains(err.Error(), tc.errPart) {
					t.Fatalf("error %q must contain %q", err, tc.errPart)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ciWakeTarget = %q, want %q", got, tc.want)
			}
		})
	}
}

// ciFakeGH answers the two gh calls the checkall pass makes. An empty GraphQL
// body keeps the Watch watching, so this test isolates registration.
func ciFakeGH(_, name string, args, _ []string) runResult {
	if name != "gh" {
		return runResult{code: 2, stderr: []byte("unexpected command " + name)}
	}
	if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
		return runResult{stdout: []byte(`[{"number":7,"headRefOid":"sha7"}]`)}
	}
	if len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
		return runResult{stdout: []byte(`{"data":{}}`)}
	}
	return runResult{code: 2, stderr: []byte("unexpected gh call: " + strings.Join(args, " "))}
}

func TestCICheckAllRegistersOrchestratorSessionAndWakesIt(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BEACON_STATE_DIR", stateDir)
	t.Setenv(ciWakeTargetEnv, "")
	t.Setenv(ciParentSessionEnv, "")

	const session = "ses_orch_1"
	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: ciFakeGH, root: t.TempDir()}
	if code := runCI(cli, []string{"check", "--all", "--wake-target", session}); code != 0 {
		t.Fatalf("runCI code = %d, stderr = %s", code, errOut.String())
	}

	db, err := store.Open(filepath.Join(stateDir, mcpStateDBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	watches, err := db.ActiveWatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 {
		t.Fatalf("active watches = %d, want 1", len(watches))
	}
	w := watches[0]
	var subscriberID, subscriberTarget, adapter string
	for _, s := range w.Subscribers {
		if string(s.Role) == "orchestrator" {
			subscriberID, subscriberTarget, adapter = s.ID, s.Target, s.Adapter
		}
	}
	if subscriberID != session || subscriberTarget != session || adapter != "opencode" {
		t.Fatalf("orchestrator subscriber = (id %q, target %q, adapter %q), want (%s, %s, opencode)",
			subscriberID, subscriberTarget, adapter, session, session)
	}

	// The wake path resolves the recipient from the persisted Watch. A service
	// name fails here and would otherwise reach the prompt API as a bad path.
	target, err := wake.WatchTarget(w, w.ID)
	if err != nil {
		t.Fatalf("WatchTarget: %v", err)
	}
	if target != session {
		t.Fatalf("wake target = %q, want %q", target, session)
	}

	var promptArgs []string
	deliverer := &wake.OpenCodeDeliverer{Runner: func(_ context.Context, args ...string) ([]byte, error) {
		promptArgs = append([]string(nil), args...)
		return []byte(`{"data":{"id":"msg_1"}}`), nil
	}}
	payload := domain.WakePayload{
		WatchID:      w.ID,
		Revision:     w.Revision,
		ObligationID: domain.ObligationKey(w.ID, w.Revision, target),
		Verdict:      string(domain.VerdictReady),
	}
	if err := deliverer.Deliver(context.Background(), target, payload); err != nil {
		t.Fatalf("wake delivery: %v", err)
	}
	wantPath := "/api/session/" + session + "/prompt"
	if len(promptArgs) < 3 || promptArgs[1] != "POST" || promptArgs[2] != wantPath {
		t.Fatalf("prompt args = %v, want POST %s", promptArgs, wantPath)
	}
}

func TestCICheckAllRejectsInvalidWakeTarget(t *testing.T) {
	t.Setenv(ciWakeTargetEnv, "")
	t.Setenv(ciParentSessionEnv, "")
	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: ciFakeGH, root: t.TempDir()}
	code := runCI(cli, []string{"check", "--all", "--wake-target", "beacon-ci-checkall"})
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "ses_") || !strings.Contains(errOut.String(), "--wake-target") {
		t.Fatalf("error must name the prefix and the flag: %s", errOut.String())
	}
}
