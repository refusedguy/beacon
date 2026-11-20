package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openOutboxTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// committedOutboxCount reads through the separate read pool, so it sees only
// committed rows and never the writer's uncommitted transaction.
func committedOutboxCount(t *testing.T, d *DB) int {
	t.Helper()
	ok, err := tableExists(context.Background(), d.readDB, "command_outbox")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if !ok {
		return 0
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM command_outbox`).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

func TestOutboxCommitWritesIntentAndEntry(t *testing.T) {
	d := openOutboxTestDB(t)
	s := NewOutboxStore(d)

	created, err := s.Commit(context.Background(), "cmd-1", "notify", []byte("hello"))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !created {
		t.Fatal("first Commit did not report created")
	}

	intent, ok, err := s.Intent("cmd-1")
	if err != nil || !ok {
		t.Fatalf("Intent: ok=%v err=%v", ok, err)
	}
	entry, ok, err := s.OutboxEntry("cmd-1")
	if err != nil || !ok {
		t.Fatalf("OutboxEntry: ok=%v err=%v", ok, err)
	}
	if intent.Kind != "notify" || string(intent.Payload) != "hello" {
		t.Fatalf("intent = %+v, want kind=notify payload=hello", intent)
	}
	if intent.Key != entry.Key || intent.Kind != entry.Kind || string(entry.Payload) != "hello" {
		t.Fatalf("entry = %+v, want it to mirror intent %+v", entry, intent)
	}
	if entry.Seq == 0 {
		t.Fatal("entry seq was not assigned")
	}
	intents, outbox, err := s.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if intents != 1 || outbox != 1 {
		t.Fatalf("counts = (%d,%d), want (1,1)", intents, outbox)
	}
}

func TestOutboxRollbackLeavesNoRow(t *testing.T) {
	d := openOutboxTestDB(t)
	s := NewOutboxStore(d)
	boom := errors.New("force rollback")
	s.probe = func() error { return boom }

	created, err := s.Commit(context.Background(), "cmd-rollback", "notify", []byte("hello"))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if created {
		t.Fatal("rolled-back Commit reported created")
	}
	if n := committedOutboxCount(t, d); n != 0 {
		t.Fatalf("outbox rows after rollback = %d, want 0", n)
	}
	if _, ok, err := s.Intent("cmd-rollback"); err != nil || ok {
		t.Fatalf("intent after rollback: ok=%v err=%v, want absent", ok, err)
	}
}

func TestOutboxRepeatIsIdempotent(t *testing.T) {
	d := openOutboxTestDB(t)
	s := NewOutboxStore(d)

	first, err := s.Commit(context.Background(), "cmd-1", "notify", []byte("hello"))
	if err != nil || !first {
		t.Fatalf("first Commit = %v, %v; want true", first, err)
	}
	second, err := s.Commit(context.Background(), "cmd-1", "notify", []byte("different"))
	if err != nil {
		t.Fatalf("repeat Commit: %v", err)
	}
	if second {
		t.Fatal("repeat Commit reported created")
	}
	intents, outbox, err := s.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if intents != 1 || outbox != 1 {
		t.Fatalf("counts after repeat = (%d,%d), want (1,1)", intents, outbox)
	}
	entry, _, err := s.OutboxEntry("cmd-1")
	if err != nil {
		t.Fatalf("OutboxEntry: %v", err)
	}
	if string(entry.Payload) != "hello" {
		t.Fatalf("payload after repeat = %q, want first value %q", entry.Payload, "hello")
	}
}

func TestOutboxEffectNotVisibleBeforeCommit(t *testing.T) {
	d := openOutboxTestDB(t)
	s := NewOutboxStore(d)

	seenBefore, probeErr := -1, error(nil)
	s.probe = func() error {
		ok, err := tableExists(context.Background(), d.readDB, "command_outbox")
		if err != nil {
			probeErr = err
			return nil
		}
		if ok {
			if err := d.readDB.QueryRow(`SELECT count(*) FROM command_outbox`).Scan(&seenBefore); err != nil {
				probeErr = err
			}
		} else {
			seenBefore = 0
		}
		return nil
	}
	created, err := s.Commit(context.Background(), "cmd-1", "notify", []byte("hello"))
	if err != nil || !created {
		t.Fatalf("Commit = %v, %v; want true", created, err)
	}
	if probeErr != nil {
		t.Fatalf("probe: %v", probeErr)
	}
	if seenBefore != 0 {
		t.Fatalf("outbox rows visible inside transaction = %d, want 0", seenBefore)
	}
	if n := committedOutboxCount(t, d); n != 1 {
		t.Fatalf("outbox rows after commit = %d, want 1", n)
	}
}

func TestOutboxRejectsEmptyIdentity(t *testing.T) {
	d := openOutboxTestDB(t)
	s := NewOutboxStore(d)

	for _, args := range [][2]string{{"", "notify"}, {"cmd-1", ""}} {
		if _, err := s.Commit(context.Background(), args[0], args[1], nil); err == nil {
			t.Fatalf("Commit(%q, %q) accepted empty identity", args[0], args[1])
		}
	}
	if n := committedOutboxCount(t, d); n != 0 {
		t.Fatalf("outbox rows after invalid commits = %d, want 0", n)
	}
}
