package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
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
