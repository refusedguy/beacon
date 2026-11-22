package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/app/session"
)

// attachIdentitySetup writes a saved session with the given identity and
// installs a registry that returns rowID. Both runAttach paths are covered by
// the caller, so this helper only fixes the state and the service address.
func attachIdentitySetup(t *testing.T, taskID, savedID, identity, rowID string) {
	t.Helper()
	t313Service(t, "http://127.0.0.1:4096")
	t395WriteSessions(t, attachSessionState{
		Sessions: []attachSession{{TaskID: taskID, SessionID: savedID, Identity: identity}},
		Last:     taskID,
	})
	st := &attachResumeStore{
		want:  session.Session{ID: rowID, Status: session.SessionStatusActive},
		found: true,
	}
	attachResumeInstallRegistry(t, st)
}

// A saved record whose identity differs from its session id cannot be trusted.
// Resume must stop before opening opencode.
func TestAttachResumeIdentityMismatchInSavedRecord(t *testing.T) {
	attachIdentitySetup(t, "TX1", "sess-saved", "sess-original", "sess-saved")
	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })

	if code := c.runAttach([]string{"--resume"}); code == 0 {
		t.Fatalf("exit = 0, want non-zero on identity mismatch; stderr = %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("opened %d sessions on identity mismatch, want 0: %+v", len(*calls), *calls)
	}
	if !strings.Contains(errOut.String(), "identity mismatch") {
		t.Fatalf("stderr = %q, want identity mismatch", errOut.String())
	}
}

// A saved id that points at a foreign registry row is a substituted session.
// Resume must not open the row's id.
func TestAttachResumeIdentityMismatchInRegistryRow(t *testing.T) {
	attachIdentitySetup(t, "TX2", "sess-saved", "sess-saved", "sess-foreign")
	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })

	if code := c.runAttach([]string{"--resume"}); code == 0 {
		t.Fatalf("exit = 0, want non-zero on identity mismatch; stderr = %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("opened %d sessions on identity mismatch, want 0: %+v", len(*calls), *calls)
	}
	if !strings.Contains(errOut.String(), "identity mismatch") {
		t.Fatalf("stderr = %q, want identity mismatch", errOut.String())
	}
}

// A matching identity and registry row must still open exactly one session.
func TestAttachResumeIdentityMatchOpensSession(t *testing.T) {
	attachIdentitySetup(t, "TX3", "sess-saved", "sess-saved", "sess-saved")
	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })

	if code := c.runAttach([]string{"--resume"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", "sess-saved"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, want one attach to %q", *calls, wantArgs)
	}
}

func TestRegistryResumeIdentifiedRejectsForeignRow(t *testing.T) {
	st := &attachResumeStore{
		want:  session.Session{ID: "sess-foreign"},
		found: true,
	}
	r := session.NewSessionRegistry(st)

	if _, _, err := r.ResumeIdentified("sess-saved"); !errors.Is(err, session.ErrSessionIdentityMismatch) {
		t.Fatalf("ResumeIdentified error = %v, want ErrSessionIdentityMismatch", err)
	}
}
