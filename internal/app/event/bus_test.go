package event

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// memJournal is an in-memory stand-in for the BS.11 store-backed journal.
type memJournal struct {
	mu      sync.Mutex
	seq     uint64
	fail    error
	appends int
}

func (j *memJournal) Append(_ context.Context, envs []Envelope) ([]uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fail != nil {
		return nil, j.fail
	}
	j.appends++
	seqs := make([]uint64, len(envs))
	for i := range envs {
		j.seq++
		seqs[i] = j.seq
	}
	return seqs, nil
}

func TestEventNotVisibleBeforeCommit(t *testing.T) {
	b := NewBus(nil)
	var seen []string
	b.Subscribe(func(_ context.Context, env Envelope) error {
		seen = append(seen, env.Type)
		return nil
	})

	tx := b.Begin(context.Background())
	if err := tx.Emit(Envelope{Type: "created", Payload: []byte("one")}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("subscriber saw event before commit: %v", seen)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "created" {
		t.Fatalf("after commit seen = %v, want [created]", seen)
	}

	// A rollback discards staged envelopes (discardOnThrow).
	tx2 := b.Begin(context.Background())
	if err := tx2.Emit(Envelope{Type: "rolled-back"}); err != nil {
		t.Fatal(err)
	}
	tx2.Rollback()
	if len(seen) != 1 {
		t.Fatalf("rollback leaked an event: %v", seen)
	}
}

func TestDrainAfterCommitDeliversOnce(t *testing.T) {
	j := &memJournal{}
	b := NewBus(j)
	var got []Envelope
	b.Subscribe(func(_ context.Context, env Envelope) error {
		got = append(got, env)
		return nil
	})

	tx := b.Begin(context.Background())
	for _, typ := range []string{"a", "b", "c"} {
		if err := tx.Emit(Envelope{Scope: "s", Type: typ, SchemaVersion: 1, Payload: []byte(typ)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 0 {
		t.Fatalf("events delivered before commit: %v", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("delivered %d events, want 3: %v", len(got), got)
	}
	want := []string{"a", "b", "c"}
	for i, env := range got {
		if env.Type != want[i] {
			t.Fatalf("event %d type = %q, want %q (out of order)", i, env.Type, want[i])
		}
		if env.Seq != uint64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, env.Seq, i+1)
		}
	}

	// A second commit must not redeliver the first batch.
	if len(got) != 3 || j.appends != 1 {
		t.Fatalf("duplicate delivery: got=%d appends=%d", len(got), j.appends)
	}
	if err := tx.Commit(); !errors.Is(err, ErrTxDone) {
		t.Fatalf("reused tx error = %v, want ErrTxDone", err)
	}
}

func TestWriterErrorDoesNotBlockBus(t *testing.T) {
	b := NewBus(nil)

	badCalls := 0
	b.Subscribe(func(_ context.Context, _ Envelope) error {
		badCalls++
		return errors.New("writer failed")
	})
	panicking := 0
	b.Subscribe(func(_ context.Context, _ Envelope) error {
		panicking++
		panic("writer panic")
	})
	var good []string
	b.Subscribe(func(_ context.Context, env Envelope) error {
		good = append(good, env.Type)
		return nil
	})

	if err := b.WithTx(context.Background(), func(tx *Tx) error {
		if err := tx.Emit(Envelope{Type: "one"}); err != nil {
			return err
		}
		return tx.Emit(Envelope{Type: "two"})
	}); err != nil {
		t.Fatal(err)
	}

	if len(good) != 2 || good[0] != "one" || good[1] != "two" {
		t.Fatalf("healthy subscriber got %v, want [one two]", good)
	}
	if badCalls != 2 || panicking != 2 {
		t.Fatalf("failing subscribers not invoked for every event: bad=%d panic=%d", badCalls, panicking)
	}
}

func TestDeliverOnePanicKeepsErrorCause(t *testing.T) {
	wantErr := errors.New("subscriber crashed")
	sub := func(context.Context, Envelope) error { panic(wantErr) }

	err := deliverOne(context.Background(), sub, Envelope{Type: "one"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("panic error = %v, want the panic cause in the chain", err)
	}
}

func TestPoisonMovesWatermark(t *testing.T) {
	b := NewBus(nil)
	poisonPayload := []byte("undecodable")
	seen := make(map[string]bool)
	b.Subscribe(func(_ context.Context, env Envelope) error {
		seen[env.Type] = true
		if env.Type == "bad" {
			return NewPoisonError("cannot decode payload")
		}
		return nil
	})

	if err := b.WithTx(context.Background(), func(tx *Tx) error {
		for _, env := range []Envelope{
			{Type: "good-1", Payload: []byte("g1")},
			{Type: "bad", Payload: poisonPayload},
			{Type: "good-2", Payload: []byte("g2")},
		} {
			if err := tx.Emit(env); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if !seen["good-1"] || !seen["bad"] || !seen["good-2"] {
		t.Fatalf("a poisoned event stopped delivery: seen = %v", seen)
	}
	if got := b.Watermark(); got != 3 {
		t.Fatalf("watermark = %d, want 3", got)
	}
	p := b.LastPoison()
	if p == nil {
		t.Fatal("lastPoison is nil")
	}
	if p.Seq != 2 {
		t.Fatalf("poison seq = %d, want 2", p.Seq)
	}
	wantSHA := fmt.Sprintf("%x", sha256.Sum256(poisonPayload))
	if p.PayloadSHA != wantSHA {
		t.Fatalf("poison payload_sha = %q, want %q", p.PayloadSHA, wantSHA)
	}
}

func TestSavepointAndFailedWriterDiscard(t *testing.T) {
	b := NewBus(nil)
	var seen []string
	b.Subscribe(func(_ context.Context, env Envelope) error {
		seen = append(seen, env.Type)
		return nil
	})

	// Rollback to a savepoint drops only the work staged after it.
	tx := b.Begin(context.Background())
	_ = tx.Emit(Envelope{Type: "keep"})
	sp := tx.Savepoint()
	_ = tx.Emit(Envelope{Type: "drop-1"})
	_ = tx.Emit(Envelope{Type: "drop-2"})
	if err := tx.RollbackTo(sp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "keep" {
		t.Fatalf("savepoint delivery = %v, want [keep]", seen)
	}

	// An error from WithTx discards everything staged in that transaction.
	errBoom := errors.New("boom")
	err := b.WithTx(context.Background(), func(tx *Tx) error {
		_ = tx.Emit(Envelope{Type: "discard"})
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("WithTx error = %v, want boom", err)
	}
	if len(seen) != 1 {
		t.Fatalf("failed writer leaked an event: %v", seen)
	}
	if b.Watermark() != 1 {
		t.Fatalf("watermark = %d, want 1 after discarded tx", b.Watermark())
	}
}

func TestJournalFailureDoesNotDeliver(t *testing.T) {
	j := &memJournal{fail: errors.New("append failed")}
	b := NewBus(j)
	delivered := false
	b.Subscribe(func(_ context.Context, _ Envelope) error {
		delivered = true
		return nil
	})

	err := b.WithTx(context.Background(), func(tx *Tx) error {
		return tx.Emit(Envelope{Type: "lost"})
	})
	if err == nil {
		t.Fatal("WithTx succeeded despite journal failure")
	}
	if delivered {
		t.Fatal("journal failure still delivered an event")
	}
	if b.Watermark() != 0 {
		t.Fatalf("watermark = %d, want 0 after failed commit", b.Watermark())
	}
}
