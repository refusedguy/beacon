package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

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

// ciFakeGHReady answers with one green check so an existing Watch gets a
// terminal verdict.
func ciFakeGHReady(_, name string, args, _ []string) runResult {
	if name != "gh" {
		return runResult{code: 2, stderr: []byte("unexpected command " + name)}
	}
	if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
		return runResult{stdout: []byte(`[{"number":7,"headRefOid":"sha7"}]`)}
	}
	if len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
		return runResult{stdout: []byte(`{"data":{"p0":{"pullRequest":{"statusCheckRollup":{"contexts":{"nodes":[{"__typename":"CheckRun","name":"build","conclusion":"SUCCESS"}]}}}}}}`)}
	}
	return runResult{code: 2, stderr: []byte("unexpected gh call: " + strings.Join(args, " "))}
}

// ciSessionRequest is the shape an MCP caller sends: the sessionID lives in
// _meta, so watch_pr derives the subscriber from the caller itself.
func ciSessionRequest(session string) *mcpsdk.CallToolRequest {
	return &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Meta: mcpsdk.Meta{"ai.opencode/sessionID": session},
	}}
}

// ciRegisterWatch models the agent or orchestrator creating a Watch through
// beacon_watch_pr. The daemon and ci check --all never do this.
func ciRegisterWatch(t *testing.T, db *store.DB, role domain.Role, session string) mcp.WatchPROutput {
	t.Helper()
	out, err := mcp.NewWatchPRHandler(db, nil, nil).Register(context.Background(),
		ciSessionRequest(session), mcp.WatchPRInput{
			Repo: "refusedguy/beacon", PR: 7, HeadSHA: "sha7", Role: role,
		})
	if err != nil {
		t.Fatalf("watch_pr register: %v", err)
	}
	return out
}

func ciOpenState(t *testing.T, stateDir string) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, mcpStateDBName))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDaemonWithoutWakeTargetDoesNotRegisterWatch(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BEACON_STATE_DIR", stateDir)
	t.Setenv("BEACON_DAEMON_INTERVAL", "")
	// No wake-target env exists. A leftover BEACON_PARENT_SESSION must be
	// ignored: the daemon has no wake target and no orchestrator subscriber.
	t.Setenv("BEACON_PARENT_SESSION", "ses_ignored_parent")

	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: ciFakeGH, root: t.TempDir()}
	if code := runDaemon(cli, []string{"--once"}); code != 0 {
		t.Fatalf("runDaemon code = %d, stderr = %s", code, errOut.String())
	}

	db := ciOpenState(t, stateDir)
	defer db.Close()
	watches, err := db.ActiveWatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 0 {
		t.Fatalf("daemon created %d watches, want 0", len(watches))
	}
}

func TestDaemonRejectsWakeTargetFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: ciFakeGH, root: t.TempDir()}
	code := runDaemon(cli, []string{"--once", "--wake-target", "ses_x"})
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "flag provided but not defined") {
		t.Fatalf("stderr must reject the removed flag: %s", errOut.String())
	}
}

func TestCICheckAllWithoutWakeTargetDoesNotRegisterWatch(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BEACON_STATE_DIR", stateDir)
	// No wake-target env exists. A leftover BEACON_PARENT_SESSION must be ignored.
	t.Setenv("BEACON_PARENT_SESSION", "ses_ignored_parent")

	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: ciFakeGH, root: t.TempDir()}
	if code := runCI(cli, []string{"check", "--all"}); code != 0 {
		t.Fatalf("runCI code = %d, stderr = %s", code, errOut.String())
	}

	db := ciOpenState(t, stateDir)
	defer db.Close()
	watches, err := db.ActiveWatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 0 {
		t.Fatalf("ci check created %d watches, want 0", len(watches))
	}
	if !strings.Contains(out.String(), "зарегистрировано 0") || !strings.Contains(out.String(), "beacon_watch_pr") {
		t.Fatalf("summary must report the refused registration: %q", out.String())
	}
}

func TestCICheckAllRejectsWakeTargetFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: ciFakeGH, root: t.TempDir()}
	code := runCI(cli, []string{"check", "--all", "--wake-target", "ses_x"})
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "flag provided but not defined") {
		t.Fatalf("stderr must reject the removed flag: %s", errOut.String())
	}
}

func TestCICheckAllUpdatesExistingWatchWithoutWakeTarget(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BEACON_STATE_DIR", stateDir)
	// No wake-target env exists. A leftover BEACON_PARENT_SESSION must be ignored.
	t.Setenv("BEACON_PARENT_SESSION", "ses_ignored_parent")

	db := ciOpenState(t, stateDir)
	registered := ciRegisterWatch(t, db, domain.RoleExecutor, "ses_exec_1")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: ciFakeGHReady, root: t.TempDir()}
	if code := runCI(cli, []string{"check", "--all"}); code != 0 {
		t.Fatalf("runCI code = %d, stderr = %s", code, errOut.String())
	}

	db = ciOpenState(t, stateDir)
	defer db.Close()
	w, err := db.Watch(registered.WatchID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Verdict != domain.VerdictReady || w.Status != domain.StatusNotifying || w.Revision != 1 {
		t.Fatalf("watch = %+v, want ready/notifying/revision 1", w)
	}
	o, err := db.Obligation(domain.ObligationKey(registered.WatchID, 1, "ses_exec_1"))
	if err != nil {
		t.Fatal(err)
	}
	if o.VerdictPointer != domain.VerdictReady || o.SubscriberID != "ses_exec_1" {
		t.Fatalf("obligation = %+v", o)
	}
	watches, err := db.ActiveWatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 {
		t.Fatalf("active watches = %d, want 1 (no duplicate from ci check)", len(watches))
	}
}

type ciRecordingDeliverer struct {
	sessionID string
	payload   domain.WakePayload
}

func (d *ciRecordingDeliverer) Deliver(_ context.Context, sessionID string, p domain.WakePayload) error {
	d.sessionID = sessionID
	d.payload = p
	return nil
}

func TestBlockedWakeStillFindsAgentCreatedOrchestratorSubscriber(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BEACON_STATE_DIR", stateDir)

	db := ciOpenState(t, stateDir)
	registered := ciRegisterWatch(t, db, domain.RoleOrchestrator, "ses_orch_agent")
	w, err := db.Watch(registered.WatchID)
	if err != nil {
		t.Fatal(err)
	}
	if got := wake.OrchestratorSession(w); got != "ses_orch_agent" {
		t.Fatalf("orchestrator subscriber = %q, want ses_orch_agent", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// ci check must not replace the agent-created Watch with one of its own.
	var out, errOut bytes.Buffer
	cli := &CLI{out: &out, errOut: &errOut, run: ciFakeGH, root: t.TempDir()}
	if code := runCI(cli, []string{"check", "--all"}); code != 0 {
		t.Fatalf("runCI code = %d, stderr = %s", code, errOut.String())
	}

	db = ciOpenState(t, stateDir)
	defer db.Close()
	watches, err := db.ActiveWatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 {
		t.Fatalf("active watches = %d, want 1 (ci check must not register another)", len(watches))
	}
	w, err = db.Watch(registered.WatchID)
	if err != nil {
		t.Fatal(err)
	}
	if got := wake.OrchestratorSession(w); got != "ses_orch_agent" {
		t.Fatalf("orchestrator subscriber after ci check = %q, want ses_orch_agent", got)
	}

	// The blocked path resolves the orchestrator from this Watch, not from a
	// daemon registration, and still steers to its session.
	deliverer := &ciRecordingDeliverer{}
	ctx := wake.WithOrchestratorNotifier(context.Background(), &wake.OrchestratorNotifier{
		Deliverer: deliverer,
		Watches:   db,
	})
	if err := wake.NotifyOrchestrator(ctx, wake.ReasonMyFault, domain.WakePayload{WatchID: registered.WatchID}); err != nil {
		t.Fatalf("NotifyOrchestrator: %v", err)
	}
	if deliverer.sessionID != "ses_orch_agent" {
		t.Fatalf("wake target = %q, want ses_orch_agent", deliverer.sessionID)
	}
	if deliverer.payload.Verdict != wake.ReasonMyFault {
		t.Fatalf("reason = %q, want %q", deliverer.payload.Verdict, wake.ReasonMyFault)
	}
}
