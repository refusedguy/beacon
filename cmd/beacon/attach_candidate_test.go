package main

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
)

// attachCandidateSetup writes a saved session for task T9.9 and returns the
// record, so a test can build a registry row for the same task and time.
func attachCandidateSetup(t *testing.T) attachSession {
	t.Helper()
	t313Service(t, "http://127.0.0.1:4096")
	saved := attachSession{
		TaskID:    "T9.9",
		SessionID: "sess-saved",
		SavedAt:   time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
	}
	t395WriteSessions(t, attachSessionState{
		Sessions: []attachSession{saved},
		Last:     saved.TaskID,
	})
	return saved
}

// attachCandidateAssertOpen checks that attach opened exactly one session and
// used the expected id.
func attachCandidateAssertOpen(t *testing.T, calls *[]t313Call, errOut *bytes.Buffer, wantID string) {
	t.Helper()
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", wantID}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, stderr = %q; want one attach to %q", *calls, errOut.String(), wantID)
	}
}

// An eligible row is accepted even when its id differs from the saved one.
// The registry id becomes the session id passed to opencode.
func TestAttachResumeAcceptsEligibleCandidate(t *testing.T) {
	saved := attachCandidateSetup(t)
	st := &attachResumeStore{
		want: session.Session{
			ID:       "sess-canonical",
			Status:   session.SessionStatusActive,
			LastSeen: saved.SavedAt.Add(time.Minute),
			Metadata: map[string]string{spawnMetaTaskID: saved.TaskID},
		},
		found: true,
	}
	attachResumeInstallRegistry(t, st)

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume", saved.TaskID}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	attachCandidateAssertOpen(t, calls, errOut, "sess-canonical")
}

// A row naming another task cannot open the requested session, so attach keeps
// the id from attach-sessions.json.
func TestAttachResumeSkipsMismatchedCandidate(t *testing.T) {
	saved := attachCandidateSetup(t)
	st := &attachResumeStore{
		want: session.Session{
			ID:       "sess-canonical",
			Status:   session.SessionStatusActive,
			LastSeen: saved.SavedAt.Add(time.Minute),
			Metadata: map[string]string{spawnMetaTaskID: "T-OTHER"},
		},
		found: true,
	}
	attachResumeInstallRegistry(t, st)

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume", saved.TaskID}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	attachCandidateAssertOpen(t, calls, errOut, saved.SessionID)
}

// A row last seen before the id was saved describes a different session.
func TestAttachResumeSkipsStaleCandidate(t *testing.T) {
	saved := attachCandidateSetup(t)
	st := &attachResumeStore{
		want: session.Session{
			ID:       "sess-canonical",
			Status:   session.SessionStatusActive,
			LastSeen: saved.SavedAt.Add(-time.Minute),
			Metadata: map[string]string{spawnMetaTaskID: saved.TaskID},
		},
		found: true,
	}
	attachResumeInstallRegistry(t, st)

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume", saved.TaskID}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	attachCandidateAssertOpen(t, calls, errOut, saved.SessionID)
}
