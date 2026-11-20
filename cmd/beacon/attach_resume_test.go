package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// attachResumeStore is a mock SessionStore. It records GetSession ids so a
// test can see that `attach --resume` consulted the durable registry.
type attachResumeStore struct {
	looked []string

	want   store.Session
	found  bool
	getErr error
}

func (s *attachResumeStore) SaveSession(store.Session) error { return nil }

func (s *attachResumeStore) GetSession(id string) (store.Session, bool, error) {
	s.looked = append(s.looked, id)
	return s.want, s.found, s.getErr
}

func (s *attachResumeStore) Touch(string, time.Time) error { return nil }

// attachResumeInstallRegistry installs a registry backed by st and restores
// the previous registry after the test. A non-nil registry keeps the lazy
// default from opening the state database.
func attachResumeInstallRegistry(t *testing.T, st *attachResumeStore) {
	t.Helper()
	previous := spawnSessionRegistry
	SetSessionRegistry(session.NewSessionRegistry(st))
	t.Cleanup(func() { SetSessionRegistry(previous) })
}

// attachResumeSetup writes the service file and the saved session registry,
// then returns the session id recorded for task T9.9.
func attachResumeSetup(t *testing.T) string {
	t.Helper()
	t313Service(t, "http://127.0.0.1:4096")
	t395WriteSessions(t, attachSessionState{
		Sessions: []attachSession{{TaskID: "T9.9", SessionID: "sess-saved"}},
		Last:     "T9.9",
	})
	return "sess-saved"
}

func TestAttachResumeCallsGetSession(t *testing.T) {
	saved := attachResumeSetup(t)
	st := &attachResumeStore{
		want:  store.Session{ID: saved, Status: store.SessionStatusActive},
		found: true,
	}
	attachResumeInstallRegistry(t, st)

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume", "T9.9"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if want := []string{saved}; !reflect.DeepEqual(st.looked, want) {
		t.Fatalf("GetSession ids = %q, want %q", st.looked, want)
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", saved}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, want one attach to %q", *calls, wantArgs)
	}
}

func TestAttachResumeRegistryUsesStoredID(t *testing.T) {
	attachResumeSetup(t)
	const canonical = "sess-canonical"
	st := &attachResumeStore{
		want:  store.Session{ID: canonical, Status: store.SessionStatusActive},
		found: true,
	}
	attachResumeInstallRegistry(t, st)

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if want := []string{"sess-saved"}; !reflect.DeepEqual(st.looked, want) {
		t.Fatalf("GetSession ids = %q, want %q", st.looked, want)
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", canonical}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, want one attach to %q", *calls, wantArgs)
	}
}

func TestAttachResumeRegistryNotFoundKeepsSavedID(t *testing.T) {
	saved := attachResumeSetup(t)
	st := &attachResumeStore{found: false}
	attachResumeInstallRegistry(t, st)

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if want := []string{saved}; !reflect.DeepEqual(st.looked, want) {
		t.Fatalf("GetSession ids = %q, want %q", st.looked, want)
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", saved}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, want one attach to %q", *calls, wantArgs)
	}
}

func TestAttachResumeRegistryErrorKeepsSavedID(t *testing.T) {
	saved := attachResumeSetup(t)
	st := &attachResumeStore{getErr: errors.New("store unavailable")}
	attachResumeInstallRegistry(t, st)

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if want := []string{saved}; !reflect.DeepEqual(st.looked, want) {
		t.Fatalf("GetSession ids = %q, want %q", st.looked, want)
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", saved}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, want one attach to %q", *calls, wantArgs)
	}
}
