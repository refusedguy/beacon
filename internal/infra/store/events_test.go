package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// openEventStore opens a fresh store whose event log was prepared at init.
func openEventStore(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestEventLogPreparedAtInit checks that opening a store already gives the
// events table its replay columns and index. No Append call is needed first.
func TestEventLogPreparedAtInit(t *testing.T) {
	d := openEventStore(t)
	cols, err := eventLogColumns(context.Background(), d.readDB)
	if err != nil {
		t.Fatalf("eventLogColumns: %v", err)
	}
	for _, name := range []string{"seq", "scope", "type", "schema_version", "payload"} {
		if _, ok := cols[name]; !ok {
			t.Fatalf("events table is missing column %q; got %v", name, cols)
		}
	}
	var n int
	if err := d.readDB.QueryRow(`
		SELECT count(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'events_by_scope'`).Scan(&n); err != nil {
		t.Fatalf("check replay index: %v", err)
	}
	if n != 1 {
		t.Fatalf("replay index count = %d, want 1", n)
	}
}

// TestAppendSeqMonotonic pins the core promise of the log: two appends in a
// row receive consecutive, increasing seq values.
func TestAppendSeqMonotonic(t *testing.T) {
	d := openEventStore(t)
	var seqs [3]int64
	for i := range seqs {
		seq, err := d.Append("scope-a", "created", []byte(`{"kind":"created","message":"x"}`))
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		seqs[i] = seq
	}
	if seqs[0] <= 0 {
		t.Fatalf("first seq = %d, want > 0", seqs[0])
	}
	if seqs[1] != seqs[0]+1 || seqs[2] != seqs[1]+1 {
		t.Fatalf("seqs = %v, want consecutive values", seqs)
	}
}

// TestReadAfterScopeFilter proves events from one scope never leak into the
// replay of another, even though seq is global across the whole table.
func TestReadAfterScopeFilter(t *testing.T) {
	d := openEventStore(t)
	payload := []byte(`{"kind":"created","message":"x"}`)
	if _, err := d.Append("alpha", "created", payload); err != nil {
		t.Fatalf("Append alpha 1: %v", err)
	}
	if _, err := d.Append("beta", "created", payload); err != nil {
		t.Fatalf("Append beta: %v", err)
	}
	if _, err := d.Append("alpha", "updated", payload); err != nil {
		t.Fatalf("Append alpha 2: %v", err)
	}

	alpha, err := d.ReadAfter("alpha", 0)
	if err != nil {
		t.Fatalf("ReadAfter alpha: %v", err)
	}
	if len(alpha) != 2 {
		t.Fatalf("alpha events = %d, want 2", len(alpha))
	}
	for _, e := range alpha {
		if e.Scope != "alpha" {
			t.Fatalf("event scope = %q, want alpha", e.Scope)
		}
	}
	wantTypes := []string{"created", "updated"}
	for i, want := range wantTypes {
		if alpha[i].Type != want {
			t.Fatalf("alpha[%d].Type = %q, want %q", i, alpha[i].Type, want)
		}
	}

	beta, err := d.ReadAfter("beta", 0)
	if err != nil {
		t.Fatalf("ReadAfter beta: %v", err)
	}
	if len(beta) != 1 || beta[0].Scope != "beta" {
		t.Fatalf("beta events = %+v, want one beta event", beta)
	}
}

// TestReadAfterCursorReplay models a consumer that resumes from the last seq
// it saw: only strictly later events are returned, in order.
func TestReadAfterCursorReplay(t *testing.T) {
	d := openEventStore(t)
	payload := []byte(`{"kind":"created","message":"x"}`)
	var seqs [3]int64
	for i := range seqs {
		seq, err := d.Append("replay", "created", payload)
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		seqs[i] = seq
	}

	got, err := d.ReadAfter("replay", seqs[0])
	if err != nil {
		t.Fatalf("ReadAfter: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("events after cursor = %d, want 2", len(got))
	}
	if got[0].Seq != seqs[1] || got[1].Seq != seqs[2] {
		t.Fatalf("replay seqs = %d, %d; want %d, %d", got[0].Seq, got[1].Seq, seqs[1], seqs[2])
	}

	tail, err := d.ReadAfter("replay", seqs[2])
	if err != nil {
		t.Fatalf("ReadAfter tail: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("events past last seq = %+v, want empty", tail)
	}
}

// TestReadAfterEmptyDB checks there are no phantom events on a fresh file and
// that a missing scope behaves like an empty replay.
func TestReadAfterEmptyDB(t *testing.T) {
	d := openEventStore(t)
	got, err := d.ReadAfter("never-used", 0)
	if err != nil {
		t.Fatalf("ReadAfter on empty DB: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadAfter on empty DB = %+v, want empty", got)
	}
}

// TestAppendCarriesSchemaVersion pins the replay contract: every row returned
// by ReadAfter has the current schema version and the exact stored payload.
func TestAppendCarriesSchemaVersion(t *testing.T) {
	d := openEventStore(t)
	payload := []byte(`{"kind":"created","message":"carried"}`)
	seq, err := d.Append("scoped", "created", payload)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := d.ReadAfter("scoped", seq-1)
	if err != nil {
		t.Fatalf("ReadAfter: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	if got[0].SchemaVersion != currentEventSchemaVersion {
		t.Fatalf("schema version = %d, want %d", got[0].SchemaVersion, currentEventSchemaVersion)
	}
	if !bytes.Equal(got[0].Payload, payload) {
		t.Fatalf("payload = %q, want %q", got[0].Payload, payload)
	}
	if got[0].CreatedAt == 0 {
		t.Fatal("created at = 0, want a stored timestamp")
	}
}

// contractResult is the outcome of one named conformance contract. A skipped
// contract carries no error; skipped is set when the caller opts out through
// skipOptional.
type contractResult struct {
	name    string
	err     error
	skipped bool
}

// storeContract pairs a stable contract name with the behavior it checks.
// optional names the method the contract needs; the runner skips the contract
// when that name appears in the caller's skipOptional list.
type storeContract struct {
	name     string
	optional string
	run      func(sessionStore) error
}

// storeContracts is the shared acceptance set for a sessionStore adapter. It
// covers ordering, idempotency by key, read, delete and enumeration.
var storeContracts = []storeContract{
	{
		name: "save_get_roundtrip",
		run: func(s sessionStore) error {
			created := time.Unix(0, 1000).UTC()
			seen := time.Unix(0, 2000).UTC()
			want := Session{ID: "a", Status: SessionStatusActive, CreatedAt: created, LastSeen: seen, Metadata: map[string]string{"k": "v"}}
			if err := s.SaveSession(want); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			got, ok, err := s.GetSession("a")
			if err != nil {
				return fmt.Errorf("get: %w", err)
			}
			if !ok {
				return errors.New("get: not found")
			}
			if got.ID != want.ID || got.Status != want.Status || got.Metadata["k"] != "v" {
				return fmt.Errorf("roundtrip mismatch: %+v", got)
			}
			if !got.CreatedAt.Equal(created) {
				return fmt.Errorf("created_at = %v, want %v", got.CreatedAt, created)
			}
			if !got.LastSeen.Equal(seen) {
				return fmt.Errorf("last_seen = %v, want %v", got.LastSeen, seen)
			}
			return nil
		},
	},
	{
		name: "get_missing_not_found",
		run: func(s sessionStore) error {
			_, ok, err := s.GetSession("absent")
			if err != nil {
				return fmt.Errorf("get missing: %w", err)
			}
			if ok {
				return errors.New("get missing: found=true")
			}
			return nil
		},
	},
	{
		name: "save_idempotent_by_key",
		run: func(s sessionStore) error {
			t0 := time.Unix(0, 1000).UTC()
			if err := s.SaveSession(Session{ID: "dup", Status: SessionStatusActive, CreatedAt: t0, LastSeen: t0}); err != nil {
				return err
			}
			if err := s.SaveSession(Session{ID: "dup", Status: SessionStatusIdle, CreatedAt: t0, LastSeen: time.Unix(0, 3000).UTC()}); err != nil {
				return err
			}
			all, err := s.ListSessions()
			if err != nil {
				return err
			}
			if len(all) != 1 {
				return fmt.Errorf("rows = %d, want 1", len(all))
			}
			if all[0].Status != SessionStatusIdle {
				return fmt.Errorf("status = %q, want idle", all[0].Status)
			}
			return nil
		},
	},
	{
		name: "created_at_preserved_on_update",
		run: func(s sessionStore) error {
			created := time.Unix(0, 1000).UTC()
			if err := s.SaveSession(Session{ID: "keep", Status: SessionStatusActive, CreatedAt: created, LastSeen: created}); err != nil {
				return err
			}
			if err := s.SaveSession(Session{ID: "keep", Status: SessionStatusActive, CreatedAt: time.Unix(0, 5000).UTC(), LastSeen: time.Unix(0, 5000).UTC()}); err != nil {
				return err
			}
			got, _, err := s.GetSession("keep")
			if err != nil {
				return err
			}
			if !got.CreatedAt.Equal(created) {
				return fmt.Errorf("created_at = %v, want %v", got.CreatedAt, created)
			}
			return nil
		},
	},
	{
		name: "list_empty_on_fresh",
		run: func(s sessionStore) error {
			all, err := s.ListSessions()
			if err != nil {
				return err
			}
			if len(all) != 0 {
				return fmt.Errorf("fresh list = %d rows, want 0", len(all))
			}
			return nil
		},
	},
	{
		name: "list_enumerates_all",
		run: func(s sessionStore) error {
			for _, id := range []string{"a", "b", "c"} {
				if err := s.SaveSession(Session{ID: id, Status: SessionStatusActive, CreatedAt: time.Unix(0, 1000).UTC(), LastSeen: time.Unix(0, 1000).UTC()}); err != nil {
					return err
				}
			}
			all, err := s.ListSessions()
			if err != nil {
				return err
			}
			if len(all) != 3 {
				return fmt.Errorf("list = %d rows, want 3", len(all))
			}
			return nil
		},
	},
	{
		name: "list_ordered_by_created_then_id",
		run: func(s sessionStore) error {
			t0 := time.Unix(0, 1000).UTC()
			rows := []struct {
				id      string
				created time.Time
			}{
				{"c", t0},
				{"a", time.Unix(0, 2000).UTC()},
				{"b", t0},
			}
			for _, row := range rows {
				if err := s.SaveSession(Session{ID: row.id, Status: SessionStatusActive, CreatedAt: row.created, LastSeen: row.created}); err != nil {
					return err
				}
			}
			all, err := s.ListSessions()
			if err != nil {
				return err
			}
			want := []string{"b", "c", "a"}
			if len(all) != len(want) {
				return fmt.Errorf("list = %d rows, want %d", len(all), len(want))
			}
			for i := range want {
				if all[i].ID != want[i] {
					return fmt.Errorf("list[%d] = %q, want %q", i, all[i].ID, want[i])
				}
			}
			return nil
		},
	},
	{
		name: "touch_updates_last_seen",
		run: func(s sessionStore) error {
			t0 := time.Unix(0, 1000).UTC()
			if err := s.SaveSession(Session{ID: "touch", Status: SessionStatusActive, CreatedAt: t0, LastSeen: t0}); err != nil {
				return err
			}
			at := time.Unix(0, 9000).UTC()
			if err := s.Touch("touch", at); err != nil {
				return err
			}
			got, _, err := s.GetSession("touch")
			if err != nil {
				return err
			}
			if !got.LastSeen.Equal(at) {
				return fmt.Errorf("last_seen = %v, want %v", got.LastSeen, at)
			}
			return nil
		},
	},
	{
		name: "touch_preserves_other_fields",
		run: func(s sessionStore) error {
			t0 := time.Unix(0, 1000).UTC()
			if err := s.SaveSession(Session{ID: "tp", Status: SessionStatusActive, CreatedAt: t0, LastSeen: t0, Metadata: map[string]string{"k": "v"}}); err != nil {
				return err
			}
			if err := s.Touch("tp", time.Unix(0, 9000).UTC()); err != nil {
				return err
			}
			got, _, err := s.GetSession("tp")
			if err != nil {
				return err
			}
			if got.Status != SessionStatusActive || got.Metadata["k"] != "v" || !got.CreatedAt.Equal(t0) {
				return fmt.Errorf("touch changed other fields: %+v", got)
			}
			return nil
		},
	},
	{
		name: "touch_unknown_id_errors",
		run: func(s sessionStore) error {
			if err := s.Touch("absent", time.Unix(0, 9000).UTC()); err == nil {
				return errors.New("touch unknown id: want error")
			}
			return nil
		},
	},
	{
		name: "close_marks_closed",
		run: func(s sessionStore) error {
			t0 := time.Unix(0, 1000).UTC()
			if err := s.SaveSession(Session{ID: "cl", Status: SessionStatusActive, CreatedAt: t0, LastSeen: t0}); err != nil {
				return err
			}
			at := time.Unix(0, 9000).UTC()
			if err := s.CloseSession("cl", at); err != nil {
				return err
			}
			got, _, err := s.GetSession("cl")
			if err != nil {
				return err
			}
			if got.Status != SessionStatusClosed {
				return fmt.Errorf("status = %q, want closed", got.Status)
			}
			if !got.LastSeen.Equal(at) {
				return fmt.Errorf("last_seen = %v, want %v", got.LastSeen, at)
			}
			return nil
		},
	},
	{
		name: "close_idempotent",
		run: func(s sessionStore) error {
			t0 := time.Unix(0, 1000).UTC()
			if err := s.SaveSession(Session{ID: "ci", Status: SessionStatusActive, CreatedAt: t0, LastSeen: t0}); err != nil {
				return err
			}
			first := time.Unix(0, 9000).UTC()
			if err := s.CloseSession("ci", first); err != nil {
				return err
			}
			if err := s.CloseSession("ci", time.Unix(0, 9999).UTC()); err != nil {
				return err
			}
			got, _, err := s.GetSession("ci")
			if err != nil {
				return err
			}
			if !got.LastSeen.Equal(first) {
				return fmt.Errorf("last_seen = %v after second close, want %v", got.LastSeen, first)
			}
			return nil
		},
	},
	{
		name: "close_unknown_id_errors",
		run: func(s sessionStore) error {
			if err := s.CloseSession("absent", time.Unix(0, 9000).UTC()); err == nil {
				return errors.New("close unknown id: want error")
			}
			return nil
		},
	},
	{
		name: "find_by_task_returns_oldest_open",
		run: func(s sessionStore) error {
			older := time.Unix(0, 1000).UTC()
			newer := time.Unix(0, 2000).UTC()
			md := map[string]string{SessionMetaTaskID: "T-1"}
			if err := s.SaveSession(Session{ID: "new", Status: SessionStatusActive, CreatedAt: newer, LastSeen: newer, Metadata: md}); err != nil {
				return err
			}
			if err := s.SaveSession(Session{ID: "old", Status: SessionStatusActive, CreatedAt: older, LastSeen: older, Metadata: md}); err != nil {
				return err
			}
			got, ok, err := s.FindSessionByTask("T-1")
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("find by task: not found")
			}
			if got.ID != "old" {
				return fmt.Errorf("find by task = %q, want old", got.ID)
			}
			return nil
		},
	},
	{
		name: "find_by_task_skips_closed",
		run: func(s sessionStore) error {
			older := time.Unix(0, 1000).UTC()
			newer := time.Unix(0, 2000).UTC()
			md := map[string]string{SessionMetaTaskID: "T-2"}
			if err := s.SaveSession(Session{ID: "old", Status: SessionStatusActive, CreatedAt: older, LastSeen: older, Metadata: md}); err != nil {
				return err
			}
			if err := s.SaveSession(Session{ID: "new", Status: SessionStatusActive, CreatedAt: newer, LastSeen: newer, Metadata: md}); err != nil {
				return err
			}
			if err := s.CloseSession("old", newer); err != nil {
				return err
			}
			got, ok, err := s.FindSessionByTask("T-2")
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("find by task: not found")
			}
			if got.ID != "new" {
				return fmt.Errorf("find by task = %q, want new", got.ID)
			}
			return nil
		},
	},
	{
		name:     "delete_removes_session",
		optional: "DeleteSession",
		run: func(s sessionStore) error {
			t0 := time.Unix(0, 1000).UTC()
			if err := s.SaveSession(Session{ID: "del", Status: SessionStatusActive, CreatedAt: t0, LastSeen: t0}); err != nil {
				return err
			}
			if err := s.DeleteSession("del"); err != nil {
				return err
			}
			_, ok, err := s.GetSession("del")
			if err != nil {
				return err
			}
			if ok {
				return errors.New("delete: row still present")
			}
			return nil
		},
	},
	{
		name:     "delete_missing_is_noop",
		optional: "DeleteSession",
		run: func(s sessionStore) error {
			if err := s.DeleteSession("absent"); err != nil {
				return fmt.Errorf("delete missing: %w", err)
			}
			return nil
		},
	},
	{
		name: "invalid_input_rejected",
		run: func(s sessionStore) error {
			if err := s.SaveSession(Session{ID: "   ", Status: SessionStatusActive}); err == nil {
				return errors.New("save empty id: want error")
			}
			if err := s.SaveSession(Session{ID: "x", Status: "   "}); err == nil {
				return errors.New("save empty status: want error")
			}
			return nil
		},
	},
}

// runStoreConformance executes every named contract against a fresh store
// from newStore. Contracts whose optional method is listed in skipOptional are
// recorded as skipped, mirroring the skip_optional list of the SDK runner.
func runStoreConformance(newStore func() sessionStore, skipOptional []string) []contractResult {
	results := make([]contractResult, 0, len(storeContracts))
	for _, c := range storeContracts {
		if c.optional != "" && containsString(skipOptional, c.optional) {
			results = append(results, contractResult{name: c.name, skipped: true})
			continue
		}
		results = append(results, contractResult{name: c.name, err: c.run(newStore())})
	}
	return results
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func assertConformance(t *testing.T, results []contractResult) {
	t.Helper()
	for _, r := range results {
		if r.skipped {
			continue
		}
		if r.err != nil {
			t.Errorf("contract %s: %v", r.name, r.err)
		}
	}
}

// memStore is a second, non-SQLite adapter. It implements sessionStore with a
// plain map so the conformance suite certifies two independent backends.
type memStore struct {
	rows map[string]Session
}

func newMemStore() sessionStore { return &memStore{rows: map[string]Session{}} }

func (m *memStore) SaveSession(s Session) error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Status) == "" {
		return errors.New("invalid session")
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.LastSeen.IsZero() {
		s.LastSeen = now
	}
	if old, ok := m.rows[s.ID]; ok {
		s.CreatedAt = old.CreatedAt
	}
	m.rows[s.ID] = s
	return nil
}

func (m *memStore) GetSession(id string) (Session, bool, error) {
	s, ok := m.rows[id]
	return s, ok, nil
}

func (m *memStore) ListSessions() ([]Session, error) {
	out := make([]Session, 0, len(m.rows))
	for _, s := range m.rows {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *memStore) FindSessionByTask(task string) (Session, bool, error) {
	all, _ := m.ListSessions()
	for _, s := range all {
		if s.Status == SessionStatusClosed {
			continue
		}
		if s.Metadata[SessionMetaTaskID] == task {
			return s, true, nil
		}
	}
	return Session{}, false, nil
}

func (m *memStore) Touch(id string, at time.Time) error {
	s, ok := m.rows[id]
	if !ok {
		return fmt.Errorf("unknown id %q", id)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	s.LastSeen = at
	m.rows[id] = s
	return nil
}

func (m *memStore) CloseSession(id string, at time.Time) error {
	s, ok := m.rows[id]
	if !ok {
		return fmt.Errorf("unknown id %q", id)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if s.Status != SessionStatusClosed {
		s.LastSeen = at
	}
	s.Status = SessionStatusClosed
	m.rows[id] = s
	return nil
}

func (m *memStore) DeleteSession(id string) error {
	delete(m.rows, id)
	return nil
}

// swallowingTouch breaks exactly one behavior: a Touch on an unknown id
// returns nil. The conformance suite must still pass every other contract.
type swallowingTouch struct{ sessionStore }

func (s swallowingTouch) Touch(id string, at time.Time) error {
	_ = s.sessionStore.Touch(id, at)
	return nil
}

func TestStoreConformanceTwoAdapters(t *testing.T) {
	adapters := []struct {
		name     string
		newStore func(*testing.T) sessionStore
	}{
		{"sqlite", func(t *testing.T) sessionStore { return openEventStore(t) }},
		{"memory", func(*testing.T) sessionStore { return newMemStore() }},
	}
	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			results := runStoreConformance(func() sessionStore { return a.newStore(t) }, nil)
			if len(results) < 14 {
				t.Fatalf("named contracts = %d, want at least 14", len(results))
			}
			assertConformance(t, results)
		})
	}
}

func TestStoreConformanceSkipOptional(t *testing.T) {
	results := runStoreConformance(newMemStore, []string{"DeleteSession"})
	skipped := 0
	for _, r := range results {
		if !r.skipped {
			continue
		}
		skipped++
		if r.err != nil {
			t.Errorf("skipped contract %s carries error: %v", r.name, r.err)
		}
	}
	if skipped != 2 {
		t.Fatalf("skipped contracts = %d, want 2 (the DeleteSession pair), results: %+v", skipped, results)
	}
	assertConformance(t, results)
}

func TestStoreConformanceCatchesOneBrokenContract(t *testing.T) {
	factory := func() sessionStore { return swallowingTouch{newMemStore()} }
	results := runStoreConformance(factory, nil)
	var failed []string
	for _, r := range results {
		if r.err != nil {
			failed = append(failed, r.name)
		}
	}
	if len(failed) != 1 || failed[0] != "touch_unknown_id_errors" {
		t.Fatalf("failed contracts = %v, want exactly [touch_unknown_id_errors]", failed)
	}
}
