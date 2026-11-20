// Package recovery restores the server replay position after a restart.
//
// The last durable snapshot carries the position the server resumes from. A
// fresh store has no snapshot, so the server starts at the beginning of the
// event log. The restore logic here is pure: a port supplies the bytes, and
// this package decodes them and decides the starting cursor. Persistence stays
// in the store; this package never writes.
package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
)

// SchemaVersion is the payload layout this reader understands. A snapshot
// carrying another version cannot be decoded safely, so Boot reports it as a
// broken snapshot instead of guessing at the bytes.
const SchemaVersion = 1

// ErrBrokenSnapshot marks a snapshot that is present but cannot be restored.
// Callers can use errors.Is to tell a decode problem from a port failure.
var ErrBrokenSnapshot = errors.New("recovery: broken snapshot")

// Cursor is the position the server resumes from. Seq is the last event
// sequence already applied; replay continues at Seq + 1. A zero Seq means no
// event has been applied, so replay covers the whole log.
type Cursor struct {
	Seq int64 `json:"seq"`
}

// State is the outcome of a restore. Cursor holds the position to resume from.
// Found reports whether the store had a snapshot. Reset is true only when no
// snapshot existed, in which case the cursor is reset to zero.
type State struct {
	Cursor Cursor
	Found  bool
	Reset  bool
}

// Snapshot is the part of a durable snapshot the restore needs: the schema
// version stamped at save time and the opaque payload bytes.
type Snapshot struct {
	SchemaVersion int
	Payload       []byte
}

// Port reads the latest snapshot from durable state. found is false when the
// store has never saved a snapshot. An error means the read itself failed and
// no conclusion can be drawn about the state.
type Port interface {
	LatestSnapshot() (Snapshot, bool, error)
}

// payload is the on-disk shape of a snapshot this reader understands. Keeping
// it separate from State lets State grow without changing the stored format.
type payload struct {
	Cursor Cursor `json:"cursor"`
}

// Boot reads the latest snapshot through p and returns the cursor to resume
// from. A missing snapshot resets the cursor to zero. A broken snapshot or a
// failed read returns an error and no state, so the caller never starts from a
// position it did not verify.
func Boot(p Port) (State, error) {
	if p == nil {
		return State{}, errors.New("recovery: nil port")
	}
	snap, found, err := p.LatestSnapshot()
	if err != nil {
		return State{}, fmt.Errorf("recovery: read latest snapshot: %w", err)
	}
	if !found {
		return State{Reset: true}, nil
	}
	if snap.SchemaVersion != SchemaVersion {
		return State{}, fmt.Errorf("%w: schema version %d, want %d",
			ErrBrokenSnapshot, snap.SchemaVersion, SchemaVersion)
	}
	var stored payload
	if err := json.Unmarshal(snap.Payload, &stored); err != nil {
		return State{}, fmt.Errorf("%w: decode payload: %v", ErrBrokenSnapshot, err)
	}
	if stored.Cursor.Seq < 0 {
		return State{}, fmt.Errorf("%w: negative cursor seq %d",
			ErrBrokenSnapshot, stored.Cursor.Seq)
	}
	return State{Cursor: stored.Cursor, Found: true}, nil
}
