package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeToken = "ghp_1234567890abcdefghijklmnopqrstuvwxyz"

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		secrets []string
		keep    []string
	}{
		{
			name:    "classic github token",
			in:      `{"token":"` + fakeToken + `"}`,
			secrets: []string{fakeToken},
			keep:    []string{`"token":"`},
		},
		{
			name:    "fine grained pat",
			in:      "token=github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz0123",
			secrets: []string{"github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz0123"},
		},
		{
			name:    "authorization bearer",
			in:      "Authorization: Bearer abcdef1234567890xyz",
			secrets: []string{"abcdef1234567890xyz", "Bearer"},
			keep:    []string{"Authorization:"},
		},
		{
			name:    "authorization in json",
			in:      `{"authorization":"Bearer shortsecretvalue123"}`,
			secrets: []string{"shortsecretvalue123", "Bearer"},
			keep:    []string{`"authorization":`},
		},
		{
			name:    "url credentials",
			in:      "clone https://alice:hunter2@github.com/org/repo.git",
			secrets: []string{"alice:hunter2", "hunter2"},
			keep:    []string{"https://", "github.com/org/repo.git"},
		},
		{
			name: "mixed",
			in: `msg="` + fakeToken +
				`" auth="Authorization: token plainsecret123" url="https://u:pw@host/x"`,
			secrets: []string{fakeToken, "plainsecret123", "u:pw"},
			keep:    []string{"[REDACTED]", "https://", "host/x"},
		},
		{
			name: "plain text unchanged",
			in:   `{"kind":"verdict","message":"all checks passed"}`,
			keep: []string{"all checks passed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.in)
			got := string(Redact(src))
			if string(src) != tc.in {
				t.Fatal("Redact mutated its input")
			}
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Errorf("secret %q survived redaction: %s", secret, got)
				}
			}
			for _, want := range tc.keep {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q after redaction: %s", want, got)
				}
			}
		})
	}
}

func TestRedactingWriterStoresRedactedVersionedEvents(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	wantVersion := d.Version()
	rw, err := NewRedactingWriter(d, wantVersion)
	if err != nil {
		t.Fatalf("NewRedactingWriter: %v", err)
	}
	if got := rw.Version(); got != wantVersion {
		t.Errorf("Version() = %d, want %d", got, wantVersion)
	}

	payload := []byte(`{"kind":"verdict","message":"token=` + fakeToken +
		` auth=Authorization: Bearer abcdef1234567890xyz url=https://u:pw@host/x"}`)
	ctx := context.Background()
	for _, watchID := range []string{"w1", "w2"} {
		if err := rw.WriteEvent(ctx, watchID, payload); err != nil {
			t.Fatalf("WriteEvent(%s): %v", watchID, err)
		}
	}

	var count int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM events`).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 2 {
		t.Errorf("events = %d, want 2", count)
	}

	rows, err := d.readDB.Query(`SELECT watch_id, schema_version, payload, created_at FROM events ORDER BY seq`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var watchID string
		var version int
		var stored []byte
		var createdAt int64
		if err := rows.Scan(&watchID, &version, &stored, &createdAt); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		if version != wantVersion {
			t.Errorf("%s schema_version = %d, want %d", watchID, version, wantVersion)
		}
		if createdAt <= 0 {
			t.Errorf("%s created_at = %d, want positive", watchID, createdAt)
		}
		for _, secret := range []string{fakeToken, "abcdef1234567890xyz", "u:pw"} {
			if strings.Contains(string(stored), secret) {
				t.Errorf("%s stored payload kept secret %q: %s", watchID, secret, stored)
			}
		}
		if string(stored) != string(Redact(payload)) {
			t.Errorf("%s stored payload is not the redacted payload", watchID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	decoded, err := ReadEvents(ctx, d.readDB, "w1")
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("ReadEvents returned %d rows, want 1", len(decoded))
	}
	if decoded[0].SchemaVersion != wantVersion || decoded[0].Kind != "verdict" {
		t.Errorf("decoded row = %+v", decoded[0])
	}
	for _, secret := range []string{fakeToken, "abcdef1234567890xyz", "u:pw"} {
		if strings.Contains(decoded[0].Message, secret) {
			t.Errorf("decoded message kept secret %q: %s", secret, decoded[0].Message)
		}
	}
}

func TestOldVersionFixtureReadsWithoutError(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	// Corpus fixture: a pre-migration row with version 1 and a bare JSON string.
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO events (watch_id, schema_version, payload, created_at)
			VALUES (?, ?, ?, ?)`,
			"legacy", 1, []byte(`"old corpus line"`), time.Now().UnixNano())
		return err
	}); err != nil {
		t.Fatalf("insert legacy fixture: %v", err)
	}

	decoded, err := ReadEvents(ctx, d.readDB, "legacy")
	if err != nil {
		t.Fatalf("ReadEvents legacy fixture: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("ReadEvents returned %d rows, want 1", len(decoded))
	}
	if decoded[0].Message != "old corpus line" || decoded[0].SchemaVersion != 1 {
		t.Errorf("legacy row = %+v", decoded[0])
	}
}

func TestDecodeEventRejectsUnknownVersion(t *testing.T) {
	if _, err := DecodeEvent(Event{SchemaVersion: 0, Payload: []byte(`{}`)}); err == nil {
		t.Error("version 0 must have no decoder")
	}
	if _, err := DecodeEvent(Event{SchemaVersion: 99, Payload: []byte(`{}`)}); err == nil {
		t.Error("unknown version must have no decoder")
	}
	if _, err := DecodeEvent(Event{SchemaVersion: currentEventSchemaVersion + 1, Payload: []byte(`{}`)}); err == nil {
		t.Error("a version newer than the store must have no decoder")
	}
	if _, err := DecodeEvent(Event{SchemaVersion: 2, Payload: []byte(`not json`)}); err == nil {
		t.Error("malformed current payload must fail to decode")
	}
	if decoded, err := DecodeEvent(Event{
		SchemaVersion: currentEventSchemaVersion,
		Payload:       []byte(`{"kind":"verdict","message":"all checks passed"}`),
	}); err != nil {
		t.Errorf("current version %d must decode: %v", currentEventSchemaVersion, err)
	} else if decoded.Kind != "verdict" || decoded.Message != "all checks passed" {
		t.Errorf("current version decoded = %+v", decoded)
	}
}

func TestDecodeLegacyV1MapForm(t *testing.T) {
	got, err := DecodeEvent(Event{
		SchemaVersion: 1,
		Payload:       []byte(`{"msg":"legacy map line"}`),
	})
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if got.Message != "legacy map line" || got.Kind != "legacy" {
		t.Errorf("decoded = %+v", got)
	}
}

func TestNewRedactingWriterValidation(t *testing.T) {
	if _, err := NewRedactingWriter(nil, 4); err == nil {
		t.Error("nil writer must be rejected")
	}
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if _, err := NewRedactingWriter(d, 0); err == nil {
		t.Error("zero schema version must be rejected")
	}
}

func TestRedactKeepsJSONValid(t *testing.T) {
	in := []byte(`{"kind":"verdict","message":"token=` + fakeToken + `"}`)
	var before map[string]any
	if err := json.Unmarshal(in, &before); err != nil {
		t.Fatalf("input is not JSON: %v", err)
	}
	out := Redact(in)
	var after map[string]any
	if err := json.Unmarshal(out, &after); err != nil {
		t.Fatalf("redacted payload is not JSON: %v (%s)", err, out)
	}
	if after["kind"] != "verdict" || strings.Contains(outStr(after["message"]), fakeToken) {
		t.Errorf("redacted payload changed structure or kept the secret: %s", out)
	}
}

func outStr(v any) string {
	s, _ := v.(string)
	return s
}
