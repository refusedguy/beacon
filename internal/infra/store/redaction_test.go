package store

import (
	"bytes"
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

// TestRedactKeepsWatchIDIntact guards the ID format built by cibridge:
// watch:<repo>:<PR>:<40-char SHA>. The PR number followed by the first 35
// characters of the SHA looks like a Telegram token to a bare
// digits-colon-35 pattern. Redacting it corrupts the key that watch lookup
// uses, which makes beacon_get_result fail with WATCH_NOT_FOUND.
func TestRedactKeepsWatchIDIntact(t *testing.T) {
	watchID := "watch:refusedguy/beacon:1515:fc46d0b30798739cc1a8519f896be165da49c77d@1795341801:1800"
	got := string(Redact([]byte(watchID)))
	if got != watchID {
		t.Fatalf("watchID changed by redaction: got %q, want %q", got, watchID)
	}
	if strings.Contains(got, "[REDACTED]") {
		t.Fatalf("watchID was redacted: %q", got)
	}
}

// TestRedactStillCatchesTelegramTokens proves the delimiter requirement does
// not weaken redaction for a token in a quote or after a space.
func TestRedactStillCatchesTelegramTokens(t *testing.T) {
	token := "123456789:" + strings.Repeat("b", 35)
	in := `msg="` + token + `" watch=` +
		"watch:refusedguy/beacon:1515:fc46d0b30798739cc1a8519f896be165da49c77d"
	got := string(Redact([]byte(in)))
	if strings.Contains(got, token) {
		t.Errorf("Telegram token survived redaction: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("no redaction marker: %s", got)
	}
	if !strings.Contains(got, "watch:refusedguy/beacon:1515:") {
		t.Errorf("watchID was redacted with the token: %s", got)
	}
}

func TestRedactingWriterStoresRedactedVersionedEvents(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// Версия строки журнала задаётся форматом события, а не версией всех
	// таблиц базы: migration 0005 поднимает user_version, формат events не менялся.
	wantVersion := currentEventSchemaVersion
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

// TestOpenProductionRedactsEveryStoredTable is the W6-A acceptance core: a raw
// write to any of the five state tables cannot smuggle a secret past Redact,
// and each committed write is counted exactly once in persist_progress.seq.
func TestOpenProductionRedactsEveryStoredTable(t *testing.T) {
	d, err := OpenProduction(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenProduction: %v", err)
	}
	defer d.Close()

	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	payload := []byte(`{"message":"token ` + secret + `"}`)
	ctx := context.Background()
	now := time.Now().UTC().UnixNano()

	writes := []struct {
		name string
		sql  string
		args []any
	}{
		{"events", `INSERT INTO events (watch_id, schema_version, payload, created_at) VALUES (?, ?, ?, ?)`,
			[]any{"w1", 2, payload, now}},
		{"watches", `INSERT INTO watches (id, repo, pr, head_sha, status, revision, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]any{"w1", "org/repo", 1, "sha", "watching", 1, payload}},
		{"obligations", `INSERT INTO obligations (id, watch_id, revision, subscriber_id, verdict_pointer, delivered_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			[]any{"o1", "w1", 1, "s1", "ready", 0, payload}},
		{"blocker_incidents", `INSERT INTO blocker_incidents (id, repo, base_ref, signature, status, fixer_watch_id, fix_attempts, max_fix_attempts, payload, created_at, resolved_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"b1", "org/repo", "main", "check " + secret, "open", "", 0, 3, payload, now, 0}},
		{"runtime_stats", `INSERT INTO runtime_stats (id, payload, updated_at) VALUES (?, ?, ?)`,
			[]any{1, payload, now}},
	}
	for _, w := range writes {
		if err := d.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(w.sql, w.args...)
			return err
		}); err != nil {
			t.Fatalf("write %s: %v", w.name, err)
		}
	}

	checks := []struct {
		name  string
		query string
	}{
		{"events", `SELECT payload FROM events`},
		{"watches", `SELECT payload FROM watches`},
		{"obligations", `SELECT payload FROM obligations`},
		{"blocker_incidents", `SELECT payload FROM blocker_incidents`},
		{"runtime_stats", `SELECT payload FROM runtime_stats`},
	}
	for _, c := range checks {
		var stored []byte
		if err := d.readDB.QueryRow(c.query).Scan(&stored); err != nil {
			t.Fatalf("read %s: %v", c.name, err)
		}
		if bytes.Contains(stored, []byte(secret)) {
			t.Errorf("%s kept the token: %s", c.name, stored)
		}
		if !bytes.Contains(stored, []byte("[REDACTED]")) {
			t.Errorf("%s has no redaction marker: %s", c.name, stored)
		}
	}
	var signature string
	if err := d.readDB.QueryRow(`SELECT signature FROM blocker_incidents`).Scan(&signature); err != nil {
		t.Fatalf("read blocker signature: %v", err)
	}
	if strings.Contains(signature, secret) || !strings.Contains(signature, "[REDACTED]") {
		t.Errorf("blocker signature was not redacted: %q", signature)
	}

	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != uint64(len(writes)) {
		t.Errorf("seq = %d, want %d", state.Seq, len(writes))
	}
	if state.LastError != "" || !state.FailedSince.IsZero() {
		t.Errorf("state = %+v, want a clean progress row after successful writes", state)
	}
}

// TestPlainOpenKeepsRawWrites documents why the filter is opt-in: legacy
// corpus fixtures insert bytes verbatim through Open and must stay unchanged.
func TestPlainOpenKeepsRawWrites(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	const payload = `{"message":"token ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`
	if err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO events (watch_id, schema_version, payload, created_at) VALUES (?, ?, ?, ?)`,
			"legacy", 1, []byte(payload), time.Now().UnixNano())
		return err
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var stored []byte
	if err := d.readDB.QueryRow(`SELECT payload FROM events`).Scan(&stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(stored) != payload {
		t.Errorf("plain Open changed a raw payload: %s", stored)
	}
	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != 0 {
		t.Errorf("plain Open counted a write: seq = %d", state.Seq)
	}
}
