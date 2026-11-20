package session

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// fakeSessionStore records every port call. It never touches a database, so a
// test can assert which lifecycle method the registry used.
type fakeSessionStore struct {
	saved   []store.Session
	looked  []string
	touched []touchCall

	want     store.Session
	found    bool
	getErr   error
	saveErr  error
	touchErr error
}

type touchCall struct {
	id string
	at time.Time
}

func (f *fakeSessionStore) SaveSession(s store.Session) error {
	f.saved = append(f.saved, s)
	return f.saveErr
}

func (f *fakeSessionStore) GetSession(id string) (store.Session, bool, error) {
	f.looked = append(f.looked, id)
	return f.want, f.found, f.getErr
}

func (f *fakeSessionStore) Touch(id string, at time.Time) error {
	f.touched = append(f.touched, touchCall{id: id, at: at})
	return f.touchErr
}

func pinnedRegistry(f *fakeSessionStore, at time.Time) *SessionRegistry {
	r := NewSessionRegistry(f)
	r.now = func() time.Time { return at }
	return r
}

func TestRegistryStartSavesSession(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	f := &fakeSessionStore{}
	r := pinnedRegistry(f, now)
	meta := map[string]string{"taskID": "T9.9"}

	if err := r.Start(" sess-1 ", "", meta); err != nil {
		t.Fatalf("Start: %v", err)
	}
	meta["taskID"] = "changed"

	if len(f.saved) != 1 {
		t.Fatalf("SaveSession calls = %d, want 1", len(f.saved))
	}
	got := f.saved[0]
	if got.ID != "sess-1" || got.Status != store.SessionStatusActive {
		t.Errorf("saved identity = %+v", got)
	}
	if !got.CreatedAt.Equal(now) || !got.LastSeen.Equal(now) {
		t.Errorf("saved times = %v / %v, want %v", got.CreatedAt, got.LastSeen, now)
	}
	if !reflect.DeepEqual(got.Metadata, map[string]string{"taskID": "T9.9"}) {
		t.Errorf("saved metadata = %#v", got.Metadata)
	}
}

func TestRegistryStartPropagatesStoreError(t *testing.T) {
	want := errors.New("disk full")
	f := &fakeSessionStore{saveErr: want}
	if err := NewSessionRegistry(f).Start("sess-1", "active", nil); !errors.Is(err, want) {
		t.Fatalf("Start error = %v, want %v", err, want)
	}
}

func TestRegistryStartRejectsEmptyID(t *testing.T) {
	f := &fakeSessionStore{}
	if err := NewSessionRegistry(f).Start("  ", "active", nil); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("Start error = %v, want ErrEmptyID", err)
	}
	if len(f.saved) != 0 {
		t.Fatalf("SaveSession calls = %d, want 0", len(f.saved))
	}
}

func TestRegistryResumeLoadsSession(t *testing.T) {
	want := store.Session{ID: "sess-2", Status: store.SessionStatusIdle}
	f := &fakeSessionStore{want: want, found: true}

	got, found, err := NewSessionRegistry(f).Resume(" sess-2 ")
	if err != nil || !found {
		t.Fatalf("Resume = (%+v, %v, %v), want found with no error", got, found, err)
	}
	if got.ID != want.ID || got.Status != want.Status {
		t.Errorf("Resume session = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(f.looked, []string{"sess-2"}) {
		t.Errorf("GetSession ids = %#v", f.looked)
	}
}

func TestRegistryResumeReportsMissingSession(t *testing.T) {
	f := &fakeSessionStore{found: false}
	_, found, err := NewSessionRegistry(f).Resume("sess-3")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if found {
		t.Fatal("Resume reported a missing session as found")
	}
}

func TestRegistryResumePropagatesStoreError(t *testing.T) {
	want := errors.New("read failed")
	f := &fakeSessionStore{getErr: want}
	_, _, err := NewSessionRegistry(f).Resume("sess-4")
	if !errors.Is(err, want) {
		t.Fatalf("Resume error = %v, want %v", err, want)
	}
}

func TestRegistryHeartbeatTouchesNow(t *testing.T) {
	now := time.Date(2026, 11, 20, 11, 30, 0, 0, time.UTC)
	f := &fakeSessionStore{}
	r := pinnedRegistry(f, now)

	if err := r.Heartbeat(" sess-5 "); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if len(f.touched) != 1 {
		t.Fatalf("Touch calls = %d, want 1", len(f.touched))
	}
	if f.touched[0].id != "sess-5" || !f.touched[0].at.Equal(now) {
		t.Errorf("Touch call = %+v, want sess-5 at %v", f.touched[0], now)
	}
}

func TestRegistryHeartbeatPropagatesStoreError(t *testing.T) {
	want := errors.New("unknown id")
	f := &fakeSessionStore{touchErr: want}
	if err := NewSessionRegistry(f).Heartbeat("sess-6"); !errors.Is(err, want) {
		t.Fatalf("Heartbeat error = %v, want %v", err, want)
	}
}

func TestRegistryRejectsEmptyIDOnResumeAndHeartbeat(t *testing.T) {
	f := &fakeSessionStore{}
	r := NewSessionRegistry(f)
	if _, _, err := r.Resume(" "); !errors.Is(err, ErrEmptyID) {
		t.Errorf("Resume error = %v, want ErrEmptyID", err)
	}
	if err := r.Heartbeat(""); !errors.Is(err, ErrEmptyID) {
		t.Errorf("Heartbeat error = %v, want ErrEmptyID", err)
	}
	if len(f.looked) != 0 || len(f.touched) != 0 {
		t.Fatalf("store called on empty id: looks=%d touches=%d", len(f.looked), len(f.touched))
	}
}

func TestRegistryNilStoreFailsClosed(t *testing.T) {
	var r *SessionRegistry
	if err := r.Start("sess-7", "active", nil); !errors.Is(err, ErrNilStore) {
		t.Errorf("Start error = %v, want ErrNilStore", err)
	}
	if _, _, err := r.Resume("sess-7"); !errors.Is(err, ErrNilStore) {
		t.Errorf("Resume error = %v, want ErrNilStore", err)
	}
	if err := r.Heartbeat("sess-7"); !errors.Is(err, ErrNilStore) {
		t.Errorf("Heartbeat error = %v, want ErrNilStore", err)
	}
	if err := NewSessionRegistry(nil).Heartbeat("sess-7"); !errors.Is(err, ErrNilStore) {
		t.Errorf("Heartbeat on nil store error = %v, want ErrNilStore", err)
	}
}
