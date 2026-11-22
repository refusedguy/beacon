// Package store: external session transcripts with resume materialization.
//
// A transcript is the append-only record of one agent session. Records live in
// the same SQLite file as the rest of Beacon state, so a session survives the
// death of the process that ran it (CS-04). The table is created on first use;
// reads never create it and report an empty result on a fresh store.
//
// Resume follows the boundary established by the Claude Agent SDK comparison:
// the resume id is validated as a UUID before any directory is created, records
// are materialized as JSONL into a private temp dir, subagent transcripts are
// restored through listSubkeys, and the dir is removed on cancel or on any
// error. Credentials are copied only when a caller supplies a source path, and
// the same cleanup removes the copy. The key beacon/survival-rules stays the
// source of resume rules; this file stores transcript data only.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

const createSessionTranscriptsSQL = `
CREATE TABLE IF NOT EXISTS session_transcripts (
  session_id TEXT NOT NULL,
  parent     TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  payload    BLOB NOT NULL,
  PRIMARY KEY (session_id, seq)
)`

const createSessionTranscriptsIndexSQL = `
CREATE INDEX IF NOT EXISTS idx_session_transcripts_parent
ON session_transcripts(parent, session_id, seq)`

// AppendSessionTranscriptRecord stores one transcript line. The write is
// idempotent on (session_id, seq): repeating a record overwrites the same row
// instead of adding a duplicate. A record with an empty session id, with seq
// below one or with an invalid parent id is refused before any I/O.
func (d *DB) AppendSessionTranscriptRecord(rec domain.SessionTranscriptRecord) error {
	if d == nil {
		return errors.New("store: transcript: nil db")
	}
	if strings.TrimSpace(rec.SessionID) == "" {
		return domain.NewAppError(domain.CodeInvalidInput, "store: transcript: empty session id")
	}
	if rec.Seq < 1 {
		return domain.NewAppError(domain.CodeInvalidInput, "store: transcript: sequence below one")
	}
	if rec.Parent != "" {
		if _, err := domain.ValidateResumeID(rec.Parent); err != nil {
			return domain.NewAppError(domain.CodeInvalidInput, "store: transcript: invalid parent id")
		}
	}
	payload := rec.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(createSessionTranscriptsSQL); err != nil {
			return fmt.Errorf("store: transcript: create table: %w", err)
		}
		if _, err := tx.Exec(createSessionTranscriptsIndexSQL); err != nil {
			return fmt.Errorf("store: transcript: create index: %w", err)
		}
		_, err := tx.Exec(`
			INSERT INTO session_transcripts (session_id, parent, seq, kind, payload)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(session_id, seq) DO UPDATE SET
			  parent = excluded.parent,
			  kind = excluded.kind,
			  payload = excluded.payload`,
			rec.SessionID, rec.Parent, int64(rec.Seq), rec.Kind, []byte(payload))
		if err != nil {
			return fmt.Errorf("store: transcript: append: %w", err)
		}
		return nil
	})
}

// ListSessionTranscriptRecords returns the stored records ordered by session
// and sequence. An empty sessionID lists every session, which is what
// ListSessionTranscriptSubkeys needs to restore the full tree. The read does
// not create the table.
func (d *DB) ListSessionTranscriptRecords(sessionID string) ([]domain.SessionTranscriptRecord, error) {
	if d == nil {
		return nil, errors.New("store: transcript: nil db")
	}
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "session_transcripts")
	if err != nil || !ok {
		return nil, err
	}
	query := `SELECT session_id, parent, seq, kind, payload FROM session_transcripts`
	args := []any{}
	if sessionID != "" {
		query += ` WHERE session_id = ?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY session_id, seq`
	rows, err := d.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: transcript: list: %w", err)
	}
	defer rows.Close()
	out := []domain.SessionTranscriptRecord{}
	for rows.Next() {
		var rec domain.SessionTranscriptRecord
		var seq int64
		var payload []byte
		if err := rows.Scan(&rec.SessionID, &rec.Parent, &seq, &rec.Kind, &payload); err != nil {
			return nil, fmt.Errorf("store: transcript: scan: %w", err)
		}
		rec.Seq = uint64(seq)
		rec.Payload = append(json.RawMessage(nil), payload...)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ListSessionTranscriptSubkeys returns the child transcripts of parent, the
// store equivalent of the SDK's list_subkeys. Grouping is a pure domain
// operation, so the store only loads rows and delegates the shape.
func (d *DB) ListSessionTranscriptSubkeys(parent string) ([]domain.SessionTranscriptSubkey, error) {
	if d == nil {
		return nil, errors.New("store: transcript: nil db")
	}
	if parent != "" {
		if _, err := domain.ValidateResumeID(parent); err != nil {
			return nil, domain.NewAppError(domain.CodeInvalidInput, "store: transcript: invalid parent id")
		}
	}
	all, err := d.ListSessionTranscriptRecords("")
	if err != nil {
		return nil, err
	}
	return domain.ListSessionTranscriptSubkeys(parent, all), nil
}

// LoadSessionTranscript validates the resume id and returns the materialized
// transcript from the store. Validation happens before any data is read, so a
// path or a non-UUID id never reaches the filesystem.
func (d *DB) LoadSessionTranscript(resumeID string) (domain.MaterializedTranscript, error) {
	if d == nil {
		return domain.MaterializedTranscript{}, errors.New("store: transcript: nil db")
	}
	validated, err := domain.ValidateResumeID(resumeID)
	if err != nil {
		return domain.MaterializedTranscript{}, err
	}
	all, err := d.ListSessionTranscriptRecords("")
	if err != nil {
		return domain.MaterializedTranscript{}, err
	}
	return domain.MaterializeSessionTranscript(validated, all)
}

// MaterializeOptions controls one resume materialization. CredentialPath is
// optional: when set, the named file is copied into the temp dir so the resumed
// process can read it. TempDir is optional and passed to os.MkdirTemp; an empty
// value uses the system default.
type MaterializeOptions struct {
	CredentialPath string
	TempDir        string
}

// MaterializedTranscriptDir is the on-disk result of a resume. Root is the
// JSONL file for the resumed session, Subagents are the restored child
// transcripts, and Dir is the private temp root the caller must remove with
// RemoveMaterializedTranscript when the resumed session ends.
type MaterializedTranscriptDir struct {
	Dir       string
	Root      string
	Subagents []string
}

// MaterializeSessionTranscript writes a validated resume into a private temp
// dir as JSONL. Subagent transcripts are restored through listSubkeys into
// numbered files, so a hostile subagent id cannot escape the directory. On any
// error after the directory is created, the directory is removed before the
// error returns; this is the SDK's BaseException cleanup expressed with a Go
// defer. The caller owns a successful directory and must remove it explicitly.
func (d *DB) MaterializeSessionTranscript(resumeID string, opts MaterializeOptions) (result MaterializedTranscriptDir, err error) {
	if d == nil {
		return result, errors.New("store: transcript: nil db")
	}
	// Validate before mkdtemp: an invalid id must leave no directory behind.
	if _, err := domain.ValidateResumeID(resumeID); err != nil {
		return result, err
	}
	mt, err := d.LoadSessionTranscript(resumeID)
	if err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp(opts.TempDir, "beacon-transcript-")
	if err != nil {
		return result, fmt.Errorf("store: transcript: mkdtemp: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(dir)
		}
	}()

	rootPath := filepath.Join(dir, "session.jsonl")
	if err := writeTranscriptJSONL(rootPath, mt.Root); err != nil {
		return result, fmt.Errorf("store: transcript: write root: %w", err)
	}
	result.Root = rootPath

	subDir := filepath.Join(dir, "subagents")
	if len(mt.Subkeys) > 0 {
		if err := os.MkdirAll(subDir, 0o700); err != nil {
			return result, fmt.Errorf("store: transcript: mkdir subagents: %w", err)
		}
	}
	for i, sub := range mt.Subkeys {
		path := filepath.Join(subDir, fmt.Sprintf("subagent-%d.jsonl", i))
		if err := writeTranscriptJSONL(path, sub.Records); err != nil {
			return result, fmt.Errorf("store: transcript: write subagent %d: %w", i, err)
		}
		result.Subagents = append(result.Subagents, path)
	}

	if opts.CredentialPath != "" {
		if err := copyCredential(opts.CredentialPath, filepath.Join(dir, "credentials")); err != nil {
			return result, fmt.Errorf("store: transcript: copy credentials: %w", err)
		}
	}

	result.Dir = dir
	committed = true
	return result, nil
}

// RemoveMaterializedTranscript deletes a temp directory produced by
// MaterializeSessionTranscript. It is idempotent and removes a missing dir
// without error, so a caller can defer it and also call it on cancel.
func RemoveMaterializedTranscript(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("store: transcript: remove materialized dir: %w", err)
	}
	return nil
}

// writeTranscriptJSONL writes one record per line. json.RawMessage payloads are
// embedded as raw JSON, so a structured event keeps its shape in the file.
func writeTranscriptJSONL(path string, records []domain.SessionTranscriptRecord) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	writeErr := func() error {
		for _, rec := range records {
			payload := rec.Payload
			if len(payload) == 0 {
				payload = json.RawMessage("null")
			}
			line := struct {
				SessionID string          `json:"sessionID"`
				Parent    string          `json:"parent"`
				Seq       uint64          `json:"seq"`
				Kind      string          `json:"kind"`
				Payload   json.RawMessage `json:"payload"`
			}{rec.SessionID, rec.Parent, rec.Seq, rec.Kind, payload}
			b, err := json.Marshal(line)
			if err != nil {
				return err
			}
			if _, err := f.Write(append(b, '\n')); err != nil {
				return err
			}
		}
		return nil
	}()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// copyCredential copies a single credential file into the temp dir with owner
// only permissions. The credential lives only as long as the materialized dir,
// which MaterializeSessionTranscript removes on error and the caller removes
// when the resumed session ends.
func copyCredential(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
