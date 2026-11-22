package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// wakeIntentGoldenPayload pins the bytes of a version 2 wake-intent row. The
// commit comes from verdictCommitFixture in atomic_write_test.go.
const wakeIntentGoldenPayload = `{"kind":"wake-intent","watchID":"w1","obligationID":"w1:3:s1","revision":3,"stagedAt":"2026-11-19T12:00:00Z"}`

// TestWakeIntentWriterDecoderAndFixtureAgree checks the row a caller can read
// back after CommitVerdict: its version, its exact payload, and the value the
// decoder returns from both the row and the golden fixture.
func TestWakeIntentWriterDecoderAndFixtureAgree(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	c := verdictCommitFixture(t)
	if err := d.CommitVerdict(context.Background(), c); err != nil {
		t.Fatalf("CommitVerdict: %v", err)
	}

	var version int
	var payload []byte
	if err := d.readDB.QueryRow(`
		SELECT schema_version, payload FROM events
		WHERE json_extract(payload, '$.kind') = ?`, wakeIntentKind).Scan(&version, &payload); err != nil {
		t.Fatalf("read wake row: %v", err)
	}
	if version != 2 || version != int(defaultWakeSchemaVersion) {
		t.Fatalf("row schema_version = %d, want %d", version, defaultWakeSchemaVersion)
	}
	if string(payload) != wakeIntentGoldenPayload {
		t.Fatalf("row payload = %s, want %s", payload, wakeIntentGoldenPayload)
	}

	fromWriter, err := DecodeEvent(Event{SchemaVersion: version, Payload: payload})
	if err != nil {
		t.Fatalf("decode writer payload: %v", err)
	}
	fromFixture, err := DecodeEvent(Event{SchemaVersion: version, Payload: []byte(wakeIntentGoldenPayload)})
	if err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	if fromWriter != fromFixture {
		t.Fatalf("decoded row = %+v, want %+v", fromWriter, fromFixture)
	}
	if fromWriter.Kind != wakeIntentKind || fromWriter.Message != "" || fromWriter.SchemaVersion != 2 {
		t.Fatalf("decoded row = %+v, want kind %q, empty message and version 2", fromWriter, wakeIntentKind)
	}

	writerVersion, err := wakeSchemaVersionForPayload(payload)
	if err != nil {
		t.Fatalf("version from writer payload: %v", err)
	}
	fixtureVersion, err := wakeSchemaVersionForPayload([]byte(wakeIntentGoldenPayload))
	if err != nil {
		t.Fatalf("version from golden fixture: %v", err)
	}
	if writerVersion != defaultWakeSchemaVersion || fixtureVersion != defaultWakeSchemaVersion {
		t.Fatalf("derived versions = %d and %d, want %d", writerVersion, fixtureVersion, defaultWakeSchemaVersion)
	}
}

// TestWakeVersionNeedsLedgerEntryForReaderField covers a reader-visible change.
// A payload whose message is consumed has no version until the bump ledger and
// a new golden fixture are added, so the writer must refuse to stamp it.
func TestWakeVersionNeedsLedgerEntryForReaderField(t *testing.T) {
	for _, fields := range []wakeReaderFields{
		{Kind: wakeIntentKind, Message: "reader now consumes message"},
		{Kind: "other-kind"},
	} {
		payload, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("marshal reader fields: %v", err)
		}
		if version, err := wakeSchemaVersionForPayload(payload); err == nil {
			t.Errorf("reader fields %+v got version %d without a ledger entry", fields, version)
		}
	}
}

// TestRawOnlyPayloadChangeKeepsVersion covers a field that no reader consumes.
// The version stays at 2 and the decoder returns the same event as before.
func TestRawOnlyPayloadChangeKeepsVersion(t *testing.T) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(wakeIntentGoldenPayload), &raw); err != nil {
		t.Fatalf("unmarshal golden fixture: %v", err)
	}
	raw["trace"] = json.RawMessage(`"diag"`)
	changed, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal raw field: %v", err)
	}

	version, err := wakeSchemaVersionForPayload(changed)
	if err != nil {
		t.Fatalf("version from raw change: %v", err)
	}
	if version != defaultWakeSchemaVersion {
		t.Fatalf("version after raw change = %d, want %d", version, defaultWakeSchemaVersion)
	}

	want, err := DecodeEvent(Event{SchemaVersion: int(version), Payload: []byte(wakeIntentGoldenPayload)})
	if err != nil {
		t.Fatalf("decode golden fixture: %v", err)
	}
	got, err := DecodeEvent(Event{SchemaVersion: int(version), Payload: changed})
	if err != nil {
		t.Fatalf("decode raw change: %v", err)
	}
	if got != want {
		t.Fatalf("decoded raw change = %+v, want %+v", got, want)
	}
}
