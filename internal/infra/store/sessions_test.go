package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func openSessionStore(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return d
}

func assertSession(t *testing.T, got, want Session) {
	t.Helper()
	if got.ID != want.ID || got.Status != want.Status {
		t.Fatalf("session identity = %+v, want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("created_at = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if !got.LastSeen.Equal(want.LastSeen) {
		t.Fatalf("last_seen = %v, want %v", got.LastSeen, want.LastSeen)
	}
	if !reflect.DeepEqual(got.Metadata, want.Metadata) {
		t.Fatalf("metadata = %v, want %v", got.Metadata, want.Metadata)
	}
}

func TestSessionsEmptyStore(t *testing.T) {
	d := openSessionStore(t)
	all, err := d.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions on empty store: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("empty store returned %d sessions, want 0", len(all))
	}
	_, found, err := d.GetSession("missing")
	if err != nil {
		t.Fatalf("GetSession on empty store: %v", err)
	}
	if found {
		t.Fatal("GetSession on empty store reported found")
	}
	ok, err := tableExists(context.Background(), d.readDB, "sessions")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if ok {
		t.Fatal("read created the sessions table")
	}
}

func TestSaveSessionAndGetSession(t *testing.T) {
	d := openSessionStore(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	seen := created.Add(time.Minute)
	want := Session{
		ID:        "ses_abc",
		Status:    SessionStatusActive,
		CreatedAt: created,
		LastSeen:  seen,
		Metadata:  map[string]string{"kind": "executor", "repo": "refusedguy/beacon"},
	}
	if err := d.SaveSession(want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, found, err := d.GetSession(want.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !found {
		t.Fatal("GetSession did not find the saved session")
	}
	assertSession(t, got, want)
}

func TestSaveSessionUpsertKeepsCreatedAt(t *testing.T) {
	d := openSessionStore(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := Session{
		ID:        "ses_upsert",
		Status:    SessionStatusActive,
		CreatedAt: created,
		LastSeen:  created,
		Metadata:  map[string]string{"rank": "low"},
	}
	if err := d.SaveSession(first); err != nil {
		t.Fatalf("first SaveSession: %v", err)
	}
	updated := first
	updated.Status = SessionStatusIdle
	updated.LastSeen = created.Add(2 * time.Hour)
	updated.Metadata = map[string]string{"rank": "high"}
	if err := d.SaveSession(updated); err != nil {
		t.Fatalf("second SaveSession: %v", err)
	}
	got, found, err := d.GetSession(first.ID)
	if err != nil || !found {
		t.Fatalf("GetSession after upsert: found=%v err=%v", found, err)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v, want %v", got.CreatedAt, created)
	}
	if got.Status != updated.Status || !got.LastSeen.Equal(updated.LastSeen) ||
		!reflect.DeepEqual(got.Metadata, updated.Metadata) {
		t.Fatalf("upsert did not replace the row: %+v", got)
	}
	all, err := d.ListSessions()
	if err != nil || len(all) != 1 {
		t.Fatalf("upsert left %d rows, err=%v", len(all), err)
	}
}

func TestListSessions(t *testing.T) {
	d := openSessionStore(t)
	base := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	wants := []Session{
		{ID: "ses_a", Status: SessionStatusActive, CreatedAt: base, LastSeen: base, Metadata: map[string]string{"n": "1"}},
		{ID: "ses_b", Status: SessionStatusIdle, CreatedAt: base.Add(time.Hour), LastSeen: base.Add(time.Hour), Metadata: map[string]string{"n": "2"}},
		{ID: "ses_c", Status: SessionStatusClosed, CreatedAt: base.Add(2 * time.Hour), LastSeen: base.Add(2 * time.Hour), Metadata: map[string]string{"n": "3"}},
	}
	for _, s := range wants {
		if err := d.SaveSession(s); err != nil {
			t.Fatalf("SaveSession(%s): %v", s.ID, err)
		}
	}
	got, err := d.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != len(wants) {
		t.Fatalf("ListSessions returned %d rows, want %d", len(got), len(wants))
	}
	for i := range wants {
		assertSession(t, got[i], wants[i])
	}
}

func TestTouchUpdatesLastSeen(t *testing.T) {
	d := openSessionStore(t)
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	want := Session{
		ID:        "ses_touch",
		Status:    SessionStatusActive,
		CreatedAt: created,
		LastSeen:  created,
		Metadata:  map[string]string{"worker": "bs66"},
	}
	if err := d.SaveSession(want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	touched := created.Add(30 * time.Minute)
	if err := d.Touch(want.ID, touched); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, found, err := d.GetSession(want.ID)
	if err != nil || !found {
		t.Fatalf("GetSession after Touch: found=%v err=%v", found, err)
	}
	if !got.LastSeen.Equal(touched) {
		t.Fatalf("last_seen = %v, want %v", got.LastSeen, touched)
	}
	if got.Status != want.Status || !got.CreatedAt.Equal(want.CreatedAt) ||
		!reflect.DeepEqual(got.Metadata, want.Metadata) {
		t.Fatalf("Touch changed other fields: %+v", got)
	}
	if err := d.Touch("ses_missing", touched); err == nil {
		t.Fatal("Touch on unknown id returned no error")
	}
}

func TestSessionsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := Session{
		ID:        "ses_persist",
		Status:    SessionStatusActive,
		CreatedAt: time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC),
		LastSeen:  time.Date(2026, 4, 5, 7, 7, 8, 0, time.UTC),
		Metadata:  map[string]string{"branch": "fix/bs66"},
	}
	if err := d.SaveSession(want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, found, err := reopened.GetSession(want.ID)
	if err != nil || !found {
		t.Fatalf("GetSession after reopen: found=%v err=%v", found, err)
	}
	assertSession(t, got, want)
}

func TestSaveSessionValidation(t *testing.T) {
	d := openSessionStore(t)
	tests := []struct {
		name string
		s    Session
	}{
		{"empty id", Session{Status: SessionStatusActive}},
		{"blank id", Session{ID: "  ", Status: SessionStatusActive}},
		{"empty status", Session{ID: "ses_x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := d.SaveSession(tc.s); err == nil {
				t.Fatalf("SaveSession(%+v) returned no error", tc.s)
			}
		})
	}
	all, err := d.ListSessions()
	if err != nil || len(all) != 0 {
		t.Fatalf("invalid saves wrote rows: %+v, err=%v", all, err)
	}
}

func TestCloseSessionIsIdempotent(t *testing.T) {
	d := openSessionStore(t)
	created := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	want := Session{
		ID:        "ses_close",
		Status:    SessionStatusActive,
		CreatedAt: created,
		LastSeen:  created,
		Metadata:  map[string]string{"kind": "executor"},
	}
	if err := d.SaveSession(want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	first := created.Add(time.Hour)
	if err := d.CloseSession(want.ID, first); err != nil {
		t.Fatalf("first CloseSession: %v", err)
	}
	got, found, err := d.GetSession(want.ID)
	if err != nil || !found {
		t.Fatalf("GetSession after close: found=%v err=%v", found, err)
	}
	if got.Status != SessionStatusClosed {
		t.Fatalf("status = %q, want %q", got.Status, SessionStatusClosed)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v, want %v", got.CreatedAt, created)
	}
	if !got.LastSeen.Equal(first) {
		t.Fatalf("last_seen = %v, want %v", got.LastSeen, first)
	}
	if !reflect.DeepEqual(got.Metadata, want.Metadata) {
		t.Fatalf("metadata = %v, want %v", got.Metadata, want.Metadata)
	}

	second := first.Add(time.Hour)
	if err := d.CloseSession(want.ID, second); err != nil {
		t.Fatalf("second CloseSession: %v", err)
	}
	got, found, err = d.GetSession(want.ID)
	if err != nil || !found {
		t.Fatalf("GetSession after second close: found=%v err=%v", found, err)
	}
	if got.Status != SessionStatusClosed || !got.CreatedAt.Equal(created) ||
		!got.LastSeen.Equal(first) || !reflect.DeepEqual(got.Metadata, want.Metadata) {
		t.Fatalf("second close changed the row: %+v", got)
	}
	all, err := d.ListSessions()
	if err != nil || len(all) != 1 {
		t.Fatalf("second close left %d rows, err=%v", len(all), err)
	}
	if err := d.CloseSession("ses_missing", second); err == nil {
		t.Fatal("CloseSession on unknown id returned no error")
	}
}

func TestStaleSessionsFiltersAndLimits(t *testing.T) {
	d := openSessionStore(t)
	before := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
	sessions := []Session{
		{ID: "ses_old1", Status: SessionStatusActive, CreatedAt: before.Add(-3 * time.Hour), LastSeen: before.Add(-3 * time.Hour), Metadata: map[string]string{"n": "1"}},
		{ID: "ses_old2", Status: SessionStatusIdle, CreatedAt: before.Add(-2 * time.Hour), LastSeen: before.Add(-2 * time.Hour), Metadata: map[string]string{"n": "2"}},
		{ID: "ses_fresh", Status: SessionStatusActive, CreatedAt: before.Add(2 * time.Hour), LastSeen: before.Add(time.Hour), Metadata: map[string]string{"n": "3"}},
		{ID: "ses_edge", Status: SessionStatusActive, CreatedAt: before.Add(-time.Hour), LastSeen: before, Metadata: map[string]string{"n": "4"}},
		{ID: "ses_shut", Status: SessionStatusActive, CreatedAt: before.Add(-4 * time.Hour), LastSeen: before.Add(-4 * time.Hour), Metadata: map[string]string{"n": "5"}},
	}
	for _, s := range sessions {
		if err := d.SaveSession(s); err != nil {
			t.Fatalf("SaveSession(%s): %v", s.ID, err)
		}
	}
	if err := d.CloseSession("ses_shut", before.Add(-time.Minute)); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	ids := func(rows []Session) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID)
		}
		return out
	}
	got, err := d.StaleSessions(before, 10)
	if err != nil {
		t.Fatalf("StaleSessions: %v", err)
	}
	if want := []string{"ses_old1", "ses_old2"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("stale ids = %v, want %v", ids(got), want)
	}
	got, err = d.StaleSessions(before, 1)
	if err != nil {
		t.Fatalf("StaleSessions with limit: %v", err)
	}
	if want := []string{"ses_old1"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("limited stale ids = %v, want %v", ids(got), want)
	}
	got, err = d.StaleSessions(before, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("zero limit returned %v, err=%v", ids(got), err)
	}
}

func TestStaleSessionsEmptyStore(t *testing.T) {
	d := openSessionStore(t)
	got, err := d.StaleSessions(time.Now(), 10)
	if err != nil {
		t.Fatalf("StaleSessions on empty store: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty store returned %d sessions", len(got))
	}
	ok, err := tableExists(context.Background(), d.readDB, "sessions")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if ok {
		t.Fatal("read created the sessions table")
	}
}

func TestFindSessionByTaskSkipsClosed(t *testing.T) {
	d := openSessionStore(t)
	base := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	open := Session{
		ID:        "ses_task_open",
		Status:    SessionStatusActive,
		CreatedAt: base,
		LastSeen:  base,
		Metadata:  map[string]string{SessionMetaTaskID: "T-MM01"},
	}
	shut := Session{
		ID:        "ses_task_shut",
		Status:    SessionStatusActive,
		CreatedAt: base.Add(time.Hour),
		LastSeen:  base.Add(time.Hour),
		Metadata:  map[string]string{SessionMetaTaskID: "T-MM01"},
	}
	for _, s := range []Session{open, shut} {
		if err := d.SaveSession(s); err != nil {
			t.Fatalf("SaveSession(%s): %v", s.ID, err)
		}
	}
	if err := d.CloseSession(shut.ID, base.Add(2*time.Hour)); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	got, found, err := d.FindSessionByTask("T-MM01")
	if err != nil || !found {
		t.Fatalf("FindSessionByTask: found=%v err=%v", found, err)
	}
	if got.ID != open.ID {
		t.Fatalf("session = %+v, want open row %s", got, open.ID)
	}
	if err := d.CloseSession(open.ID, base.Add(2*time.Hour)); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	_, found, err = d.FindSessionByTask("T-MM01")
	if err != nil || found {
		t.Fatalf("all rows closed: found=%v err=%v", found, err)
	}
}
