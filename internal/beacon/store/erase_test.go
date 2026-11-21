package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	infrastore "github.com/refusedguy/beacon/internal/infra/store"
)

func openEraseDB(t *testing.T) *infrastore.DB {
	t.Helper()
	db, err := infrastore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func seedObligation(t *testing.T, db *infrastore.DB) domain.Obligation {
	t.Helper()
	o := domain.Obligation{
		ID:             "w1:3:s1",
		WatchID:        "w1",
		Revision:       3,
		SubscriberID:   "s1",
		VerdictPointer: domain.VerdictRebase,
		StagedAt:       time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
	if err := db.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation: %v", err)
	}
	return o
}

func TestEraseRemovesObligationAndKeepsLedger(t *testing.T) {
	db := openEraseDB(t)
	o := seedObligation(t, db)
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	res, err := Erase(context.Background(), db, EraseRequest{
		ObligationID: o.ID,
		Actor:        "worker-1",
		Reason:       "closed watch",
		At:           at,
	})
	if err != nil {
		t.Fatalf("Erase: %v", err)
	}
	if !res.Erased {
		t.Error("first erase: Erased = false, want true")
	}
	if _, err := db.Obligation(o.ID); err == nil {
		t.Error("obligation still present after erase")
	}

	ledger, err := db.ObligationErase(o.ID)
	if err != nil {
		t.Fatalf("ObligationErase: %v", err)
	}
	if ledger.Actor != "worker-1" || ledger.Reason != "closed watch" {
		t.Errorf("ledger actor/reason = %q/%q, want worker-1/closed watch", ledger.Actor, ledger.Reason)
	}
	if !ledger.ErasedAt.Equal(at) {
		t.Errorf("ledger erasedAt = %v, want %v", ledger.ErasedAt, at)
	}
	if ledger.Erased {
		t.Error("read ledger row: Erased = true, want false")
	}
	var restored domain.Obligation
	if err := json.Unmarshal(ledger.Payload, &restored); err != nil {
		t.Fatalf("decode ledger payload: %v", err)
	}
	if restored.ID != o.ID {
		t.Errorf("ledger payload id = %q, want %q", restored.ID, o.ID)
	}
}

func TestEraseIsIdempotent(t *testing.T) {
	db := openEraseDB(t)
	o := seedObligation(t, db)
	first := EraseRequest{
		ObligationID: o.ID,
		Actor:        "worker-1",
		Reason:       "closed watch",
		At:           time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
	}
	if _, err := Erase(context.Background(), db, first); err != nil {
		t.Fatalf("first Erase: %v", err)
	}

	second := EraseRequest{
		ObligationID: o.ID,
		Actor:        "worker-2",
		Reason:       "repeat",
		At:           first.At.Add(time.Hour),
	}
	again, err := Erase(context.Background(), db, second)
	if err != nil {
		t.Fatalf("second Erase: %v", err)
	}
	if again.Erased {
		t.Error("repeat erase: Erased = true, want false")
	}
	if again.Actor != first.Actor || again.Reason != first.Reason || !again.ErasedAt.Equal(first.At) {
		t.Errorf("repeat erase changed ledger = %+v", again)
	}

	var count int
	err = db.Write(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM obligation_erase_ledger WHERE id = ?`, o.ID).Scan(&count)
	})
	if err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if count != 1 {
		t.Errorf("ledger rows = %d, want 1", count)
	}
}

func TestEraseRejectsInvalidRequest(t *testing.T) {
	db := openEraseDB(t)
	cases := []struct {
		name string
		req  EraseRequest
	}{
		{"empty id", EraseRequest{Actor: "a", Reason: "r", At: time.Now()}},
		{"empty actor", EraseRequest{ObligationID: "o", Reason: "r", At: time.Now()}},
		{"empty reason", EraseRequest{ObligationID: "o", Actor: "a", At: time.Now()}},
		{"zero time", EraseRequest{ObligationID: "o", Actor: "a", Reason: "r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Erase(context.Background(), db, tc.req); err == nil {
				t.Error("Erase with invalid request: error = nil, want validation error")
			}
		})
	}
}
