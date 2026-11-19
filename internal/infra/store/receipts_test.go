package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const testReceiptAt = "2026-11-19T12:00:00Z"

func newReceiptDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func writeReceiptsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "receipts.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write receipts: %v", err)
	}
	return path
}

func seedReceiptObligation(t *testing.T, d *DB, id string) {
	t.Helper()
	err := d.SaveObligation(domain.Obligation{
		ID:             id,
		WatchID:        "watch-" + id,
		Revision:       1,
		SubscriberID:   "sub-" + id,
		VerdictPointer: domain.VerdictReady,
	})
	if err != nil {
		t.Fatalf("SaveObligation(%s): %v", id, err)
	}
}

func receiptLine(obligationID string, attempt int, outcome string) string {
	return fmt.Sprintf(
		`{"obligationID":%q,"attempt":%d,"outcome":%q,"at":%q}`,
		obligationID, attempt, outcome, testReceiptAt)
}

func TestImportReceiptsIdempotent(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "obl-1")

	path := writeReceiptsFile(t, strings.Join([]string{
		receiptLine("obl-1", 1, "posted"),
		receiptLine("obl-1", 2, "transport-failed"),
	}, "\n")+"\n")

	first, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Imported != 2 || first.Duplicates != 0 || first.Malformed != 0 {
		t.Fatalf("first import = %+v, want 2 imported, no duplicates or malformed", first)
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 2 {
		t.Fatalf("receipt count = %d, want 2", count)
	}
	before, err := d.Obligation("obl-1")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if before.DeliveredAt.IsZero() {
		t.Fatal("posted receipt did not set deliveredAt")
	}
	if len(before.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(before.Attempts))
	}

	second, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 0 || second.Duplicates != 2 {
		t.Fatalf("second import = %+v, want 0 imported, 2 duplicates", second)
	}
	count, err = d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount after repeat: %v", err)
	}
	if count != 2 {
		t.Errorf("receipt count after repeat = %d, want 2", count)
	}
	after, err := d.Obligation("obl-1")
	if err != nil {
		t.Fatalf("Obligation after repeat: %v", err)
	}
	if !after.DeliveredAt.Equal(before.DeliveredAt) {
		t.Errorf("deliveredAt changed: %v -> %v", before.DeliveredAt, after.DeliveredAt)
	}
	if len(after.Attempts) != len(before.Attempts) {
		t.Errorf("attempts grew: %d -> %d", len(before.Attempts), len(after.Attempts))
	}
}

func TestImportReceiptsIgnoresPartialTail(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "obl-tail")

	whole := receiptLine("obl-tail", 1, "posted") + "\n"
	cut := `{"obligationID":"obl-tail","attempt":2,"outcome":"posted","at":"2026-11-19T12`
	path := writeReceiptsFile(t, whole+cut)

	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !result.PartialTail {
		t.Error("PartialTail = false, want true")
	}
	if result.Lines != 1 || result.Imported != 1 || result.Malformed != 0 {
		t.Fatalf("result = %+v, want 1 line imported, no malformed", result)
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 1 {
		t.Errorf("receipt count = %d, want 1", count)
	}
	receipts, err := d.Receipts("obl-tail")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(receipts) != 1 || receipts[0].Attempt != 1 {
		t.Fatalf("stored receipts = %+v, want only attempt 1", receipts)
	}
	o, err := d.Obligation("obl-tail")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if o.DeliveredAt.IsZero() {
		t.Error("complete line did not set deliveredAt")
	}
}

func TestImportReceiptsOutcomeStates(t *testing.T) {
	d := newReceiptDB(t)
	for _, id := range []string{"posted", "failed", "none"} {
		seedReceiptObligation(t, d, id)
	}
	path := writeReceiptsFile(t, strings.Join([]string{
		receiptLine("posted", 1, "posted"),
		receiptLine("failed", 1, "transport-failed"),
	}, "\n")+"\n")

	if _, err := d.ImportReceipts(path); err != nil {
		t.Fatalf("import: %v", err)
	}
	posted, err := d.Obligation("posted")
	if err != nil {
		t.Fatalf("Obligation(posted): %v", err)
	}
	failed, err := d.Obligation("failed")
	if err != nil {
		t.Fatalf("Obligation(failed): %v", err)
	}
	none, err := d.Obligation("none")
	if err != nil {
		t.Fatalf("Obligation(none): %v", err)
	}
	if posted.DeliveredAt.IsZero() {
		t.Error("posted obligation has zero deliveredAt")
	}
	if !failed.DeliveredAt.IsZero() {
		t.Errorf("transport-failed obligation has deliveredAt = %v, want zero", failed.DeliveredAt)
	}
	if !none.DeliveredAt.IsZero() {
		t.Errorf("obligation without receipt has deliveredAt = %v, want zero", none.DeliveredAt)
	}

	pending, err := d.PendingObligations()
	if err != nil {
		t.Fatalf("PendingObligations: %v", err)
	}
	seen := map[string]bool{}
	for _, o := range pending {
		seen[o.ID] = true
	}
	if seen["posted"] {
		t.Error("posted obligation is still pending")
	}
	if !seen["failed"] || !seen["none"] {
		t.Errorf("pending = %v, want failed and none", seen)
	}
}

func TestImportReceiptsIndeterminateNeverPosted(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "indet")
	path := writeReceiptsFile(t, receiptLine("indet", 1, "indeterminate")+"\n")

	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("imported = %d, want 1", result.Imported)
	}
	o, err := d.Obligation("indet")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !o.DeliveredAt.IsZero() {
		t.Errorf("indeterminate receipt set deliveredAt = %v", o.DeliveredAt)
	}
	if len(o.Attempts) != 1 || o.Attempts[0].Outcome != domain.OutcomeIndeterminate {
		t.Errorf("attempts = %+v, want one indeterminate attempt", o.Attempts)
	}
}

func TestImportReceiptsRejectsBadLines(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "ok")

	path := writeReceiptsFile(t, strings.Join([]string{
		`not json`,
		`{"obligationID":"","attempt":1,"outcome":"posted","at":"` + testReceiptAt + `"}`,
		receiptLine("ghost", 1, "posted"),
		receiptLine("ok", 1, "unknown-outcome"),
		``,
		receiptLine("ok", 2, "posted"),
	}, "\n")+"\n")

	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Lines != 5 {
		t.Errorf("lines = %d, want 5 (blank ignored)", result.Lines)
	}
	if result.Malformed != 3 {
		t.Errorf("malformed = %d, want 3", result.Malformed)
	}
	if result.Missing != 1 {
		t.Errorf("missing = %d, want 1", result.Missing)
	}
	if result.Imported != 1 {
		t.Errorf("imported = %d, want 1", result.Imported)
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 2 {
		t.Errorf("receipt count = %d, want 2 (valid + missing obligation)", count)
	}
}

func TestImportReceiptsMissingFileIsNoop(t *testing.T) {
	d := newReceiptDB(t)
	result, err := d.ImportReceipts(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("import missing file: %v", err)
	}
	if result != (ImportReceiptsResult{}) {
		t.Errorf("result = %+v, want zero", result)
	}
}

func TestParseReceiptTime(t *testing.T) {
	want := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		raw  string
		want time.Time
	}{
		{name: "rfc3339", raw: `"2026-11-19T12:00:00Z"`, want: want},
		{name: "seconds", raw: "1795089600", want: want},
		{name: "millis", raw: "1795089600000", want: want},
		{name: "nanos", raw: "1795089600000000000", want: want},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReceiptTime(jsonRaw(tc.raw))
			if err != nil {
				t.Fatalf("parseReceiptTime: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseReceiptTime(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
	if _, err := parseReceiptTime(jsonRaw(`"not-a-time"`)); err == nil {
		t.Error("bad timestamp was accepted")
	}
	if _, err := parseReceiptTime(jsonRaw(`null`)); err == nil {
		t.Error("null timestamp was accepted")
	}
}

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }
