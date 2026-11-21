// Package store exposes obligation erasure to callers above the storage
// layer. The SQLite implementation lives in internal/infra/store.
package store

import (
	"context"
	"errors"
	"time"

	infrastore "github.com/refusedguy/beacon/internal/infra/store"
)

// EraseRequest names one obligation and the audit context for removing it.
type EraseRequest struct {
	ObligationID string
	Actor        string
	Reason       string
	At           time.Time
}

// EraseResult is the ledger row written for the removal. Erased is true only
// for the call that wrote the row.
type EraseResult = infrastore.ObligationEraseRecord

// Erase removes one obligation and returns the ledger row for the removal.
// The ledger is written before the delete in one transaction. A repeat of the
// same erase returns the stored row without adding a second ledger entry.
func Erase(ctx context.Context, db *infrastore.DB, req EraseRequest) (EraseResult, error) {
	if db == nil {
		return EraseResult{}, errors.New("store: erase: nil database")
	}
	if req.ObligationID == "" {
		return EraseResult{}, errors.New("store: erase: empty obligation id")
	}
	if req.Actor == "" {
		return EraseResult{}, errors.New("store: erase: empty actor")
	}
	if req.Reason == "" {
		return EraseResult{}, errors.New("store: erase: empty reason")
	}
	if req.At.IsZero() {
		return EraseResult{}, errors.New("store: erase: zero time")
	}
	return db.EraseObligation(ctx, req.ObligationID, req.Actor, req.Reason, req.At)
}
