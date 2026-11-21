package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
)

// w488Store is a mock SessionStore. It records SaveSession so a test can see
// the session that beacon spawn registered, without opening a database.
type w488Store struct {
	saved   []session.Session
	saveErr error
}

func (s *w488Store) SaveSession(v session.Session) error {
	s.saved = append(s.saved, v)
	return s.saveErr
}

func (s *w488Store) GetSession(id string) (session.Session, bool, error) {
	return session.Session{}, false, nil
}

func (s *w488Store) Touch(id string, at time.Time) error { return nil }

// w488InstallRegistry installs a registry backed by st and restores the
// previous registry after the test. A non-nil registry keeps spawn from
// opening the state database.
func w488InstallRegistry(t *testing.T, st session.SessionStore) {
	t.Helper()
	previous := spawnSessionRegistry
	SetSessionRegistry(session.NewSessionRegistry(st))
	t.Cleanup(func() { SetSessionRegistry(previous) })
}

func TestW488SpawnSavesSessionAtStart(t *testing.T) {
	st := &w488Store{}
	w488InstallRegistry(t, st)
	spawner := &t3142Spawner{id: "sess-w488"}
	t3142Install(t, spawner)
	t.Setenv(spawnParentEnv, "parent-1")

	c, out, errOut := t3142CLI()
	if code := c.runSpawn([]string{"T9.9", "--title", "Wiring card"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got := out.String(); got != "sess-w488\n" {
		t.Errorf("stdout = %q, want session id line", got)
	}
	if len(st.saved) != 1 {
		t.Fatalf("SaveSession calls = %d, want 1", len(st.saved))
	}
	got := st.saved[0]
	if got.ID != "sess-w488" || got.Status != session.SessionStatusActive {
		t.Errorf("saved session = %+v", got)
	}
	if got.Metadata[spawnMetaTaskID] != "T9.9" || got.Metadata[spawnMetaTitle] != "Wiring card" {
		t.Errorf("saved metadata = %#v", got.Metadata)
	}
	if got.CreatedAt.IsZero() || got.LastSeen.IsZero() {
		t.Errorf("saved times are zero: %+v", got)
	}
}

func TestW488SpawnACPSavesSessionAtStart(t *testing.T) {
	st := &w488Store{}
	w488InstallRegistry(t, st)

	rt := newW10aMockRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := &w10aEvents{notify: make(chan struct{}, 8)}
	w10aInstallACP(t, rt, nil, &w10aStats{}, events, ctx, cancel, nil)

	go func() {
		<-events.notify
		<-events.notify
		cancel()
	}()

	c, out, errOut := t3142CLI()
	if code := c.runSpawn([]string{"T9.9", "--runtime", "acp"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got := out.String(); got != "ses_w10a\n" {
		t.Errorf("stdout = %q, want ACP session id line", got)
	}
	if len(st.saved) != 1 {
		t.Fatalf("SaveSession calls = %d, want 1", len(st.saved))
	}
	if got := st.saved[0]; got.ID != "ses_w10a" || got.Metadata[spawnMetaTaskID] != "T9.9" {
		t.Errorf("saved session = %+v", got)
	}
}

func TestW488SpawnSaveErrorDoesNotFailCommand(t *testing.T) {
	st := &w488Store{saveErr: errors.New("registry down")}
	w488InstallRegistry(t, st)
	spawner := &t3142Spawner{id: "sess-err"}
	t3142Install(t, spawner)
	t.Setenv(spawnParentEnv, "parent-1")

	c, out, errOut := t3142CLI()
	if code := c.runSpawn([]string{"T9.9"}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "sess-err") {
		t.Errorf("stdout = %q, want session id line", out.String())
	}
	if !strings.Contains(errOut.String(), "не записана в реестр") {
		t.Errorf("stderr = %q, want registry warning", errOut.String())
	}
}
