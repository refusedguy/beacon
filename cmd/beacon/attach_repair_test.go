package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// attachRepairStore is a SessionStore that starts with no rows and accepts a
// rebuilt one. It records reads and writes so a test can count one restore and
// one successful resume.
type attachRepairStore struct {
	rows    map[string]session.Session
	saveErr error
	gets    []string
	saves   []session.Session
}

func newAttachRepairStore() *attachRepairStore {
	return &attachRepairStore{rows: map[string]session.Session{}}
}

func (s *attachRepairStore) SaveSession(v session.Session) error {
	s.saves = append(s.saves, v)
	if s.saveErr != nil {
		return s.saveErr
	}
	s.rows[v.ID] = v
	return nil
}

func (s *attachRepairStore) GetSession(id string) (session.Session, bool, error) {
	s.gets = append(s.gets, id)
	v, ok := s.rows[id]
	return v, ok, nil
}

func (s *attachRepairStore) Touch(string, time.Time) error { return nil }

// attachRepairInstallRegistry installs r and restores the previous registry
// when the test ends.
func attachRepairInstallRegistry(t *testing.T, r *session.SessionRegistry) {
	t.Helper()
	previous := spawnSessionRegistry
	SetSessionRegistry(r)
	t.Cleanup(func() { SetSessionRegistry(previous) })
}

func TestAttachResumeRepairsMissingSessionRow(t *testing.T) {
	saved := attachResumeSetup(t)
	st := newAttachRepairStore()
	attachRepairInstallRegistry(t, session.NewSessionRegistry(st))

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}

	if len(st.saves) != 1 {
		t.Fatalf("SaveSession calls = %d, want 1 restored row", len(st.saves))
	}
	if got := st.saves[0].ID; got != saved {
		t.Fatalf("restored id = %q, want %q", got, saved)
	}
	if got := st.saves[0].Metadata[store.SessionMetaTaskID]; got != "T9.9" {
		t.Fatalf("restored task metadata = %q, want %q", got, "T9.9")
	}
	if want := []string{saved}; !reflect.DeepEqual(st.gets, want) {
		t.Fatalf("GetSession ids = %q, want %q", st.gets, want)
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", saved}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, want one attach to %q", *calls, wantArgs)
	}
}

func TestAttachResumeRestoreWriteErrorKeepsSavedID(t *testing.T) {
	saved := attachResumeSetup(t)
	st := newAttachRepairStore()
	st.saveErr = errors.New("store is read only")
	attachRepairInstallRegistry(t, session.NewSessionRegistry(st))

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}

	if len(st.saves) != 1 {
		t.Fatalf("SaveSession calls = %d, want 1 attempted restore", len(st.saves))
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", saved}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, want one attach to %q", *calls, wantArgs)
	}
}
