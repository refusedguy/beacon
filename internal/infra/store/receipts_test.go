package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/github"
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
	return receiptLineWithSource(obligationID, attempt, outcome, worktreeSourceJSON("result.json"))
}

func receiptLineWithSource(obligationID string, attempt int, outcome, source string) string {
	return fmt.Sprintf(
		`{"obligationID":%q,"attempt":%d,"outcome":%q,"at":%q,"source":%s}`,
		obligationID, attempt, outcome, testReceiptAt, source)
}

func worktreeSourceJSON(file string) string {
	return fmt.Sprintf(`{"kind":"worktree","worktreeFile":%q}`, file)
}

func receiptLineWithVersion(obligationID string, attempt int, outcome, version string) string {
	return fmt.Sprintf(
		`{"obligationID":%q,"attempt":%d,"outcome":%q,"at":%q,"source":%s,"version":%s}`,
		obligationID, attempt, outcome, testReceiptAt, worktreeSourceJSON("result.json"), version)
}

func receiptLineWithVerified(obligationID string, attempt int, outcome string, verified bool) string {
	return fmt.Sprintf(
		`{"obligationID":%q,"attempt":%d,"outcome":%q,"at":%q,"source":%s,"verified":%t}`,
		obligationID, attempt, outcome, testReceiptAt, worktreeSourceJSON("result.json"), verified)
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
	if result.Imported != 2 {
		t.Errorf("imported = %d, want 2 (valid + missing obligation)", result.Imported)
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

func TestArtifactSourceKindsPersisted(t *testing.T) {
	d := newReceiptDB(t)
	for _, id := range []string{"src-branch", "src-pr", "src-worktree"} {
		seedReceiptObligation(t, d, id)
	}
	lines := []string{
		receiptLineWithSource("src-branch", 1, "posted", `{"kind":"branch","branch":"feat/w48"}`),
		receiptLineWithSource("src-pr", 1, "posted", `{"kind":"pr","repo":"refusedguy/beacon","pr":48,"state":"OPEN"}`),
		receiptLineWithSource("src-worktree", 1, "posted", worktreeSourceJSON("worktrees/w48/result.json")),
	}
	path := writeReceiptsFile(t, strings.Join(lines, "\n")+"\n")

	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 3 || result.Malformed != 0 {
		t.Fatalf("result = %+v, want 3 imported, no malformed", result)
	}

	wantKind := map[string]ArtifactKind{
		"src-branch":   ArtifactBranch,
		"src-pr":       ArtifactPR,
		"src-worktree": ArtifactWorktree,
	}
	for id, kind := range wantKind {
		got, err := d.Receipts(id)
		if err != nil {
			t.Fatalf("Receipts(%s): %v", id, err)
		}
		if len(got) != 1 || got[0].Source.Kind != kind {
			t.Errorf("Receipts(%s) = %+v, want one receipt with kind %q", id, got, kind)
		}
	}

	branch, err := d.Receipts("src-branch")
	if err != nil {
		t.Fatalf("Receipts(branch): %v", err)
	}
	if len(branch) == 1 && branch[0].Source.Branch != "feat/w48" {
		t.Errorf("branch source = %+v, want feat/w48", branch[0].Source)
	}
	pr, err := d.Receipts("src-pr")
	if err != nil {
		t.Fatalf("Receipts(pr): %v", err)
	}
	if len(pr) == 1 && (pr[0].Source.Repo != "refusedguy/beacon" || pr[0].Source.PR != 48) {
		t.Errorf("pr source = %+v, want refusedguy/beacon#48", pr[0].Source)
	}
	worktree, err := d.Receipts("src-worktree")
	if err != nil {
		t.Fatalf("Receipts(worktree): %v", err)
	}
	if len(worktree) == 1 && worktree[0].Source.WorktreeFile != "worktrees/w48/result.json" {
		t.Errorf("worktree source = %+v, want worktrees/w48/result.json", worktree[0].Source)
	}
}

func TestArtifactSourceRejectsPrivateStatePaths(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "src-reject")

	// A free-form path is not a source at all. The typed forms below try to
	// smuggle the same private state through each of the three public kinds.
	lines := []string{
		receiptLineWithSource("src-reject", 1, "posted", `"~/.claude/jobs/123/session.jsonl"`),
		receiptLineWithSource("src-reject", 2, "posted", worktreeSourceJSON("~/.claude/jobs/123/session.jsonl")),
		receiptLineWithSource("src-reject", 3, "posted", worktreeSourceJSON("/home/agent/.claude/jobs/123")),
		receiptLineWithSource("src-reject", 4, "posted", worktreeSourceJSON("../.claude/jobs/123")),
		receiptLineWithSource("src-reject", 5, "posted", `{"kind":"branch","branch":"~/.claude/jobs/123"}`),
		receiptLineWithSource("src-reject", 6, "posted", `{"kind":"pr","repo":"refusedguy/beacon","pr":48,"worktreeFile":"~/.claude/jobs/123"}`),
	}
	path := writeReceiptsFile(t, strings.Join(lines, "\n")+"\n")

	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 0 || result.Malformed != len(lines) {
		t.Fatalf("result = %+v, want 0 imported and %d malformed", result, len(lines))
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 0 {
		t.Errorf("receipt count = %d, want 0", count)
	}
	o, err := d.Obligation("src-reject")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !o.DeliveredAt.IsZero() {
		t.Errorf("deliveredAt = %v, want zero for rejected sources", o.DeliveredAt)
	}
}

func TestClosedPRSourceTerminalVerdict(t *testing.T) {
	var closed ArtifactSource
	if err := json.Unmarshal([]byte(`{"kind":"pr","repo":"refusedguy/beacon","pr":48,"state":"CLOSED"}`), &closed); err != nil {
		t.Fatalf("unmarshal closed PR source: %v", err)
	}
	if v := github.ClosedPRVerdict(closed.State); v != domain.VerdictClosed || !github.IsTerminalArtifactVerdict(v) {
		t.Errorf("closed PR verdict = %q, want terminal %q", v, domain.VerdictClosed)
	}
	if v := closed.Verdict(); v != github.ClosedPRVerdict(closed.State) {
		t.Errorf("source verdict = %q, want %q", v, github.ClosedPRVerdict(closed.State))
	}

	var merged ArtifactSource
	if err := json.Unmarshal([]byte(`{"kind":"pr","repo":"refusedguy/beacon","pr":48,"state":"MERGED"}`), &merged); err != nil {
		t.Fatalf("unmarshal merged PR source: %v", err)
	}
	if v := merged.Verdict(); v != domain.VerdictMerged || !github.IsTerminalArtifactVerdict(v) {
		t.Errorf("merged PR verdict = %q, want terminal %q", v, domain.VerdictMerged)
	}

	var open ArtifactSource
	if err := json.Unmarshal([]byte(`{"kind":"pr","repo":"refusedguy/beacon","pr":48,"state":"OPEN"}`), &open); err != nil {
		t.Fatalf("unmarshal open PR source: %v", err)
	}
	if v := open.Verdict(); github.IsTerminalArtifactVerdict(v) {
		t.Errorf("open PR verdict = %q, want non-terminal", v)
	}
}

func TestClosedPRReceiptTerminalWithoutAgentLogs(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "closed-pr")

	line := receiptLineWithSource("closed-pr", 1, "posted",
		`{"kind":"pr","repo":"refusedguy/beacon","pr":48,"state":"CLOSED"}`)
	path := writeReceiptsFile(t, line+"\n")
	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("imported = %d, want 1", result.Imported)
	}
	got, err := d.Receipts("closed-pr")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 1 || !github.IsTerminalArtifactVerdict(got[0].Source.Verdict()) {
		t.Fatalf("stored receipts = %+v, want a terminal PR verdict", got)
	}

	// The receipt carries only the PR locator and state. No agent log path
	// reaches the store, so the verdict needs no log read.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read receipt file: %v", err)
	}
	if strings.Contains(string(data), ".claude") || strings.Contains(string(data), "/jobs/") {
		t.Errorf("receipt carries a private agent log path: %s", data)
	}
}

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

func TestRejectDuplicateReceiptRecordedOnRepeat(t *testing.T) {
	d := newReceiptDB(t)

	first, err := d.StartTask("req-abc", "task-1", []byte("state"))
	if err != nil {
		t.Fatalf("first StartTask: %v", err)
	}
	if !first.Created || first.Conflict {
		t.Fatalf("first start = %+v, want created with no conflict", first)
	}
	if n, err := d.RejectDuplicateCount(); err != nil || n != 0 {
		t.Fatalf("RejectDuplicateCount after first start = %d, %v; want 0", n, err)
	}

	second, err := d.StartTask("req-abc", "task-2", []byte("other"))
	if err != nil {
		t.Fatalf("second StartTask: %v", err)
	}
	if !second.Conflict {
		t.Fatalf("second start = %+v, want RejectDuplicate conflict", second)
	}
	if n, err := d.RejectDuplicateCount(); err != nil || n != 1 {
		t.Fatalf("RejectDuplicateCount after repeat = %d, %v; want 1", n, err)
	}

	namespaced := domain.IdempotencyKey(domain.StartNamespace, "req-abc")
	got, err := d.Receipts(namespaced)
	if err != nil {
		t.Fatalf("Receipts(%q): %v", namespaced, err)
	}
	if len(got) != 1 {
		t.Fatalf("conflict receipts = %+v, want 1", got)
	}
	if got[0].Outcome != domain.AttemptOutcome(rejectDuplicateOutcome) {
		t.Errorf("conflict outcome = %q, want %q", got[0].Outcome, rejectDuplicateOutcome)
	}

	// A further repeat is still idempotent: no second task and no second row.
	if _, err := d.StartTask("req-abc", "task-3", []byte("third")); err != nil {
		t.Fatalf("third StartTask: %v", err)
	}
	if n, err := d.RejectDuplicateCount(); err != nil || n != 1 {
		t.Fatalf("RejectDuplicateCount after third start = %d, %v; want 1", n, err)
	}
	if count, err := d.TaskStartCount(); err != nil || count != 1 {
		t.Fatalf("TaskStartCount = %d, %v; want 1", count, err)
	}
}

func TestReceiptAgentVersionPersisted(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "ver-1")

	line := receiptLineWithVersion("ver-1", 1, "posted",
		`{"model":"hy4-flash","prompt":"worker-v3","tools":["get_result","ack"]}`)
	path := writeReceiptsFile(t, line+"\n")
	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 1 || result.Malformed != 0 {
		t.Fatalf("result = %+v, want 1 imported, no malformed", result)
	}

	got, err := d.Receipts("ver-1")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("stored receipts = %+v, want 1", got)
	}
	want := AgentVersion{Model: "hy4-flash", Prompt: "worker-v3", Tools: []string{"get_result", "ack"}}
	if !reflect.DeepEqual(got[0].Version, want) {
		t.Errorf("version = %+v, want %+v", got[0].Version, want)
	}
}

func TestReceiptWithoutVersionStillImports(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "ver-old")

	path := writeReceiptsFile(t, receiptLine("ver-old", 1, "posted")+"\n")
	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("result = %+v, want 1 imported", result)
	}
	got, err := d.Receipts("ver-old")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("stored receipts = %+v, want 1", got)
	}
	if !reflect.DeepEqual(got[0].Version, AgentVersion{}) {
		t.Errorf("version = %+v, want zero value for an old line without version", got[0].Version)
	}
}

func TestReceiptMalformedVersionRejected(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "ver-bad")

	lines := []string{
		receiptLineWithVersion("ver-bad", 1, "posted", `{"tools":["ok",7]}`),
		receiptLineWithVersion("ver-bad", 2, "posted", `{"model":"x"} garbage`),
	}
	path := writeReceiptsFile(t, strings.Join(lines, "\n")+"\n")
	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 0 || result.Malformed != len(lines) {
		t.Fatalf("result = %+v, want 0 imported and %d malformed", result, len(lines))
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 0 {
		t.Errorf("receipt count = %d, want 0", count)
	}
}

func TestReceiptVersionStableOnReimport(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "ver-repeat")

	line := receiptLineWithVersion("ver-repeat", 1, "posted",
		`{"model":"hy4-flash","prompt":"worker-v3","tools":["get_result"]}`)
	path := writeReceiptsFile(t, line+"\n")
	if _, err := d.ImportReceipts(path); err != nil {
		t.Fatalf("first import: %v", err)
	}
	second, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 0 || second.Duplicates != 1 {
		t.Fatalf("second import = %+v, want 0 imported, 1 duplicate", second)
	}
	got, err := d.Receipts("ver-repeat")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 1 || got[0].Version.Model != "hy4-flash" || got[0].Version.Prompt != "worker-v3" {
		t.Errorf("stored receipts = %+v, want one with the version unchanged", got)
	}
}

func TestReceiptVersionColumnAddedToLegacyTable(t *testing.T) {
	d := newReceiptDB(t)

	// A receipts table made by a build before versions were recorded has no
	// version column. Import must add it and still store the label.
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DROP TABLE IF EXISTS receipts`); err != nil {
			return err
		}
		_, err := tx.Exec(`
			CREATE TABLE receipts (
			  seq           INTEGER PRIMARY KEY,
			  obligation_id TEXT NOT NULL,
			  attempt       INTEGER NOT NULL,
			  outcome       TEXT NOT NULL,
			  at            INTEGER NOT NULL,
			  raw           BLOB NOT NULL,
			  imported_at   INTEGER NOT NULL,
			  UNIQUE (obligation_id, attempt)
			)`)
		return err
	})
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}

	seedReceiptObligation(t, d, "ver-legacy")
	line := receiptLineWithVersion("ver-legacy", 1, "posted",
		`{"model":"old-model","prompt":"old-prompt","tools":["ack"]}`)
	path := writeReceiptsFile(t, line+"\n")
	if _, err := d.ImportReceipts(path); err != nil {
		t.Fatalf("import: %v", err)
	}
	got, err := d.Receipts("ver-legacy")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 1 || got[0].Version.Model != "old-model" || got[0].Version.Prompt != "old-prompt" {
		t.Fatalf("stored receipts = %+v, want the version from a legacy table", got)
	}

	rows, err := d.readDB.Query(`PRAGMA table_info(receipts)`)
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	defer rows.Close()
	hasVersion := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if name == "version" {
			hasVersion = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("column rows: %v", err)
	}
	if !hasVersion {
		t.Error("legacy receipts table has no version column")
	}
}

// ADR 0040 §5: only a verified result reaches results. An unverified result
// stays inside its task with verified=false as its reason and publishes no
// deliveredAt, so wait_result keeps waiting.
func TestImportReceiptsUnverifiedNotStored(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "unverified")

	path := writeReceiptsFile(t, receiptLineWithVerified("unverified", 1, "posted", false)+"\n")
	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Lines != 1 || result.Imported != 0 || result.Unverified != 1 || result.Malformed != 0 {
		t.Fatalf("result = %+v, want 1 line, 0 imported, 1 unverified, 0 malformed", result)
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 0 {
		t.Errorf("receipt count = %d, want 0", count)
	}
	got, err := d.Receipts("unverified")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("stored receipts = %+v, want none for an unverified result", got)
	}
	o, err := d.Obligation("unverified")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !o.DeliveredAt.IsZero() {
		t.Errorf("deliveredAt = %v, want zero", o.DeliveredAt)
	}
	if len(o.Attempts) != 0 {
		t.Errorf("attempts = %+v, want none", o.Attempts)
	}
	pending, err := d.PendingObligations()
	if err != nil {
		t.Fatalf("PendingObligations: %v", err)
	}
	found := false
	for _, p := range pending {
		if p.ID == "unverified" {
			found = true
		}
	}
	if !found {
		t.Error("unverified obligation is no longer pending")
	}
}

func TestImportReceiptsUnverifiedKeepsWaitUntilVerified(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "wait")

	unverified := receiptLineWithVerified("wait", 1, "posted", false)
	first, err := d.ImportReceipts(writeReceiptsFile(t, unverified+"\n"))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Imported != 0 || first.Unverified != 1 {
		t.Fatalf("first import = %+v, want 0 imported and 1 unverified", first)
	}
	before, err := d.Obligation("wait")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !before.DeliveredAt.IsZero() || len(before.Attempts) != 0 {
		t.Fatalf("unverified result was published: %+v", before)
	}

	// The same (obligationID, attempt) arrives later with a passing check.
	// The unverified row was never stored, so the verified one is new.
	verified := receiptLineWithVerified("wait", 1, "posted", true)
	second, err := d.ImportReceipts(writeReceiptsFile(t, unverified+"\n"+verified+"\n"))
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 1 || second.Unverified != 1 || second.Malformed != 0 {
		t.Fatalf("second import = %+v, want 1 imported and 1 unverified", second)
	}
	after, err := d.Obligation("wait")
	if err != nil {
		t.Fatalf("Obligation after verification: %v", err)
	}
	if after.DeliveredAt.IsZero() {
		t.Error("verified result did not set deliveredAt")
	}
	if len(after.Attempts) != 1 || after.Attempts[0].Outcome != domain.OutcomePosted {
		t.Errorf("attempts = %+v, want one posted attempt", after.Attempts)
	}
}

func TestImportReceiptsLegacyLineWithoutVerifiedIsVerified(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "legacy")

	// A receipts.jsonl written before verification was recorded has no
	// verified field. It must still import, as older lines still do.
	path := writeReceiptsFile(t, receiptLine("legacy", 1, "posted")+"\n")
	result, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 1 || result.Unverified != 0 {
		t.Fatalf("result = %+v, want 1 imported and 0 unverified", result)
	}
	got, err := d.Receipts("legacy")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 1 || !got[0].Verified {
		t.Errorf("stored receipts = %+v, want one verified receipt", got)
	}
	o, err := d.Obligation("legacy")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if o.DeliveredAt.IsZero() {
		t.Error("legacy posted receipt did not set deliveredAt")
	}
}

func TestReceiptsOnlyContainVerifiedResults(t *testing.T) {
	d := newReceiptDB(t)
	for _, id := range []string{"v-yes", "v-no"} {
		seedReceiptObligation(t, d, id)
	}
	lines := []string{
		receiptLineWithVerified("v-yes", 1, "posted", true),
		receiptLineWithVerified("v-no", 1, "posted", false),
	}
	if _, err := d.ImportReceipts(writeReceiptsFile(t, strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatalf("import: %v", err)
	}

	yes, err := d.Receipts("v-yes")
	if err != nil {
		t.Fatalf("Receipts(v-yes): %v", err)
	}
	if len(yes) != 1 || !yes[0].Verified {
		t.Fatalf("verified receipts = %+v, want one verified result", yes)
	}
	no, err := d.Receipts("v-no")
	if err != nil {
		t.Fatalf("Receipts(v-no): %v", err)
	}
	if len(no) != 0 {
		t.Errorf("unverified receipts = %+v, want none in results", no)
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 1 {
		t.Errorf("receipt count = %d, want 1", count)
	}
}

func TestImportReceiptsRejectsNonBoolVerified(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "v-bad")

	line := `{"obligationID":"v-bad","attempt":1,"outcome":"posted","at":"` + testReceiptAt +
		`","source":` + worktreeSourceJSON("result.json") + `,"verified":"yes"}`
	result, err := d.ImportReceipts(writeReceiptsFile(t, line+"\n"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Malformed != 1 || result.Imported != 0 {
		t.Fatalf("result = %+v, want 1 malformed and 0 imported", result)
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 0 {
		t.Errorf("receipt count = %d, want 0", count)
	}
}

// ADR 0049 §3. The fence token, the enqueued task and the task data change
// commit together. The helpers below build the three parts of one write.

func mustParseReceipt(t *testing.T, line string) (Receipt, []byte) {
	t.Helper()
	raw := []byte(line)
	var r Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("parse receipt: %v", err)
	}
	return r, raw
}

func fencedMessage(peer, suffix string) InboxMessage {
	return InboxMessage{
		WatchID:      "watch-" + suffix,
		Revision:     1,
		SubscriberID: "sub-" + suffix,
		PeerName:     peer,
		Payload:      []byte("work-" + suffix),
	}
}

func corruptReceiptObligation(t *testing.T, d *DB, id string) {
	t.Helper()
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO obligations
			  (id, watch_id, revision, subscriber_id, verdict_pointer, delivered_at, payload)
			VALUES (?, ?, ?, ?, ?, 0, ?)`,
			id, "watch-"+id, 1, "sub-"+id, string(domain.VerdictReady), []byte("not json"))
		return err
	})
	if err != nil {
		t.Fatalf("corrupt obligation: %v", err)
	}
}

func TestEnqueueFencedCommitsTokenDataAndQueueTogether(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "fenced-ok")

	r, raw := mustParseReceipt(t, receiptLine("fenced-ok", 1, "posted"))
	got, err := d.EnqueueFenced(FencedEnqueue{
		TaskID: "task-ok", OwnerID: "worker-a", FencingToken: 5,
		Receipt: r, Raw: raw, Message: fencedMessage("worker-a", "ok"),
	})
	if err != nil {
		t.Fatalf("EnqueueFenced: %v", err)
	}
	if got.FencingToken != 5 || !got.Enqueued || !got.Applied {
		t.Fatalf("result = %+v, want token 5, enqueued and applied", got)
	}
	token, ok, err := d.TaskFence("task-ok")
	if err != nil || !ok || token != 5 {
		t.Fatalf("TaskFence = %d, %v, %v; want 5, true", token, ok, err)
	}
	o, err := d.Obligation("fenced-ok")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if o.DeliveredAt.IsZero() || len(o.Attempts) != 1 {
		t.Fatalf("obligation did not change: %+v", o)
	}
	receipts, err := d.Receipts("fenced-ok")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("stored receipts = %d, want 1", len(receipts))
	}
	queued, err := d.InboxMessages("worker-a")
	if err != nil {
		t.Fatalf("InboxMessages: %v", err)
	}
	if len(queued) != 1 || string(queued[0].Payload) != "work-ok" {
		t.Fatalf("queued = %+v, want one message work-ok", queued)
	}
}

func TestEnqueueFencedStaleTokenRefusesWholeWrite(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "fenced-stale")

	first, raw1 := mustParseReceipt(t, receiptLine("fenced-stale", 1, "posted"))
	if _, err := d.EnqueueFenced(FencedEnqueue{
		TaskID: "task-stale", OwnerID: "worker-old", FencingToken: 10,
		Receipt: first, Raw: raw1, Message: fencedMessage("worker-old", "stale-1"),
	}); err != nil {
		t.Fatalf("first EnqueueFenced: %v", err)
	}

	later, raw2 := mustParseReceipt(t, receiptLine("fenced-stale", 2, "posted"))
	_, err := d.EnqueueFenced(FencedEnqueue{
		TaskID: "task-stale", OwnerID: "worker-late", FencingToken: 4,
		Receipt: later, Raw: raw2, Message: fencedMessage("worker-late", "stale-2"),
	})
	if !IsStaleFenceToken(err) {
		t.Fatalf("EnqueueFenced error = %v, want stale token", err)
	}
	token, ok, err := d.TaskFence("task-stale")
	if err != nil || !ok || token != 10 {
		t.Fatalf("TaskFence after stale = %d, %v, %v; want 10", token, ok, err)
	}
	o, err := d.Obligation("fenced-stale")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if len(o.Attempts) != 1 {
		t.Fatalf("attempts after stale = %d, want 1", len(o.Attempts))
	}
	receipts, err := d.Receipts("fenced-stale")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("receipts after stale = %d, want 1", len(receipts))
	}
	late, err := d.InboxMessages("worker-late")
	if err != nil {
		t.Fatalf("InboxMessages(late): %v", err)
	}
	if len(late) != 0 {
		t.Fatalf("late worker enqueued %d messages, want 0", len(late))
	}
}

func TestEnqueueFencedRollbackLeavesNoPartialState(t *testing.T) {
	d := newReceiptDB(t)
	corruptReceiptObligation(t, d, "fenced-corrupt")

	r, raw := mustParseReceipt(t, receiptLine("fenced-corrupt", 1, "posted"))
	_, err := d.EnqueueFenced(FencedEnqueue{
		TaskID: "task-corrupt", OwnerID: "worker-a", FencingToken: 6,
		Receipt: r, Raw: raw, Message: fencedMessage("worker-a", "corrupt"),
	})
	if err == nil {
		t.Fatal("EnqueueFenced with corrupt obligation succeeded, want failure")
	}
	if _, ok, _ := d.TaskFence("task-corrupt"); ok {
		t.Error("fence row survived a failed enqueue")
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 0 {
		t.Errorf("receipt count = %d after failed enqueue, want 0", count)
	}
	queued, err := d.InboxMessages("worker-a")
	if err != nil {
		t.Fatalf("InboxMessages: %v", err)
	}
	if len(queued) != 0 {
		t.Errorf("queue rows = %d after failed enqueue, want 0", len(queued))
	}
}

func TestEnqueueFencedSameTokenIsIdempotent(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "fenced-same")

	r, raw := mustParseReceipt(t, receiptLine("fenced-same", 1, "posted"))
	in := FencedEnqueue{
		TaskID: "task-same", OwnerID: "worker-a", FencingToken: 7,
		Receipt: r, Raw: raw, Message: fencedMessage("worker-a", "same"),
	}
	first, err := d.EnqueueFenced(in)
	if err != nil {
		t.Fatalf("first EnqueueFenced: %v", err)
	}
	if !first.Enqueued || !first.Applied {
		t.Fatalf("first result = %+v, want enqueued and applied", first)
	}
	second, err := d.EnqueueFenced(in)
	if err != nil {
		t.Fatalf("second EnqueueFenced: %v", err)
	}
	if second.FencingToken != 7 || second.Enqueued || second.Applied {
		t.Fatalf("second result = %+v, want token 7, no new enqueue or apply", second)
	}
	receipts, err := d.Receipts("fenced-same")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("receipts = %d, want 1", len(receipts))
	}
	queued, err := d.InboxMessages("worker-a")
	if err != nil {
		t.Fatalf("InboxMessages: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("queue rows = %d, want 1", len(queued))
	}
}

func TestEnqueueFencedHigherTokenSupersedes(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "fenced-higher")

	r1, raw1 := mustParseReceipt(t, receiptLine("fenced-higher", 1, "posted"))
	if _, err := d.EnqueueFenced(FencedEnqueue{
		TaskID: "task-higher", OwnerID: "worker-a", FencingToken: 3,
		Receipt: r1, Raw: raw1, Message: fencedMessage("worker-a", "higher-1"),
	}); err != nil {
		t.Fatalf("first EnqueueFenced: %v", err)
	}

	r2, raw2 := mustParseReceipt(t, receiptLine("fenced-higher", 2, "transport-failed"))
	got, err := d.EnqueueFenced(FencedEnqueue{
		TaskID: "task-higher", OwnerID: "worker-b", FencingToken: 9,
		Receipt: r2, Raw: raw2, Message: fencedMessage("worker-b", "higher-2"),
	})
	if err != nil {
		t.Fatalf("second EnqueueFenced: %v", err)
	}
	if got.FencingToken != 9 || !got.Enqueued || !got.Applied {
		t.Fatalf("second result = %+v, want token 9, enqueued and applied", got)
	}
	token, ok, err := d.TaskFence("task-higher")
	if err != nil || !ok || token != 9 {
		t.Fatalf("TaskFence = %d, %v, %v; want 9", token, ok, err)
	}
	o, err := d.Obligation("fenced-higher")
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if len(o.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(o.Attempts))
	}
	receipts, err := d.Receipts("fenced-higher")
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(receipts) != 2 {
		t.Fatalf("receipts = %d, want 2", len(receipts))
	}
}

func TestEnqueueFencedRejectsInvalidInput(t *testing.T) {
	d := newReceiptDB(t)
	seedReceiptObligation(t, d, "fenced-input")
	r, raw := mustParseReceipt(t, receiptLine("fenced-input", 1, "posted"))
	msg := fencedMessage("worker-a", "input")

	bad := []FencedEnqueue{
		{TaskID: "", OwnerID: "a", FencingToken: 1, Receipt: r, Raw: raw, Message: msg},
		{TaskID: "t", OwnerID: "", FencingToken: 1, Receipt: r, Raw: raw, Message: msg},
		{TaskID: "t", OwnerID: "a", FencingToken: 0, Receipt: r, Raw: raw, Message: msg},
		{TaskID: "t", OwnerID: "a", FencingToken: 1, Receipt: r, Raw: raw, Message: InboxMessage{}},
	}
	for i, in := range bad {
		if _, err := d.EnqueueFenced(in); err == nil {
			t.Errorf("case %d: EnqueueFenced succeeded, want validation error", i)
		}
	}
	count, err := d.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 0 {
		t.Errorf("receipt count = %d, want 0 after rejected inputs", count)
	}
}

// ADR 0058 §3. Checking the agent's own claims against git, test reports and
// CI. These tests assert what a caller observes: a verdict and the exact
// evidence that decided it, never the agent's words.

func TestCheckClaimConfirmedByEachSource(t *testing.T) {
	sources := []ClaimSourceKind{ClaimSourceTest, ClaimSourceCIRun, ClaimSourceGitFact}
	for _, kind := range sources {
		t.Run(string(kind), func(t *testing.T) {
			evidence := ClaimEvidence{Kind: kind, Ref: "ref-" + string(kind), Observed: true, Passed: true}
			claim := AgentClaim{ID: "c", Text: "I passed the check", Evidence: []ClaimEvidence{evidence}}
			got := CheckClaim(claim)
			if got.ClaimID != "c" || got.Verdict != ClaimConfirmed {
				t.Fatalf("CheckClaim = %+v, want CONFIRMED for c", got)
			}
			if got.Evidence != evidence {
				t.Fatalf("deciding evidence = %+v, want %+v", got.Evidence, evidence)
			}
		})
	}
}

func TestCheckClaimFailedEvidenceRefutesEvenWithPass(t *testing.T) {
	failed := ClaimEvidence{Kind: ClaimSourceCIRun, Ref: "run-1", Observed: true, Passed: false}
	passed := ClaimEvidence{Kind: ClaimSourceTest, Ref: "test-1", Observed: true, Passed: true}
	claim := AgentClaim{ID: "c", Text: "CI is green", Evidence: []ClaimEvidence{passed, failed}}
	got := CheckClaim(claim)
	if got.Verdict != ClaimRefuted {
		t.Fatalf("verdict = %q, want REFUTED", got.Verdict)
	}
	if got.Evidence != failed {
		t.Fatalf("deciding evidence = %+v, want the failed observation %+v", got.Evidence, failed)
	}
}

func TestCheckClaimUnverifiableWithoutObservedEvidence(t *testing.T) {
	cases := []struct {
		name     string
		evidence []ClaimEvidence
	}{
		{"no evidence", nil},
		{"unobserved", []ClaimEvidence{{Kind: ClaimSourceTest, Ref: "t", Observed: false, Passed: true}}},
		{"unknown source", []ClaimEvidence{{Kind: ClaimSourceKind("log"), Ref: "x", Observed: true, Passed: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckClaim(AgentClaim{ID: "c", Text: "anything", Evidence: tc.evidence})
			if got.Verdict != ClaimUnverifiable {
				t.Fatalf("verdict = %q, want UNVERIFIABLE", got.Verdict)
			}
			if got.Evidence != (ClaimEvidence{}) {
				t.Fatalf("evidence = %+v, want zero value", got.Evidence)
			}
		})
	}
}

func TestCheckClaimTextNeverDecidesVerdict(t *testing.T) {
	evidence := []ClaimEvidence{{Kind: ClaimSourceGitFact, Ref: "abc123", Observed: true, Passed: true}}
	modest := CheckClaim(AgentClaim{ID: "c", Text: "maybe", Evidence: evidence})
	bold := CheckClaim(AgentClaim{ID: "c", Text: "absolutely proven, trust me", Evidence: evidence})
	if modest.Verdict != bold.Verdict || modest.Evidence != bold.Evidence {
		t.Fatalf("claim text changed the result: %+v vs %+v", modest, bold)
	}
}

func TestCheckClaimsKeepsInputOrder(t *testing.T) {
	claims := []AgentClaim{
		{ID: "a", Evidence: []ClaimEvidence{{Kind: ClaimSourceTest, Observed: true, Passed: true}}},
		{ID: "b", Evidence: []ClaimEvidence{{Kind: ClaimSourceTest, Observed: true, Passed: false}}},
		{ID: "c"},
	}
	got := CheckClaims(claims)
	want := []ClaimVerdict{ClaimConfirmed, ClaimRefuted, ClaimUnverifiable}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ClaimID != claims[i].ID || got[i].Verdict != want[i] {
			t.Errorf("result %d = %+v, want %s/%s", i, got[i], claims[i].ID, want[i])
		}
	}
}

func TestSaveClaimsAndClaimResults(t *testing.T) {
	d := newReceiptDB(t)
	claims := []AgentClaim{
		{ID: "b", Text: "CI failed", Evidence: []ClaimEvidence{{Kind: ClaimSourceCIRun, Ref: "run-9", Observed: true, Passed: false}}},
		{ID: "a", Text: "tests pass", Evidence: []ClaimEvidence{{Kind: ClaimSourceTest, Ref: "go test", Observed: true, Passed: true}}},
		{ID: "c", Text: "no proof"},
	}
	if err := d.SaveClaims(claims); err != nil {
		t.Fatalf("SaveClaims: %v", err)
	}
	got, err := d.ClaimResults()
	if err != nil {
		t.Fatalf("ClaimResults: %v", err)
	}
	want := []ClaimResult{
		{ClaimID: "a", Verdict: ClaimConfirmed, Evidence: claims[1].Evidence[0]},
		{ClaimID: "b", Verdict: ClaimRefuted, Evidence: claims[0].Evidence[0]},
		{ClaimID: "c", Verdict: ClaimUnverifiable},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaimResults = %+v, want %+v", got, want)
	}
}

func TestSaveClaimsUpsertById(t *testing.T) {
	d := newReceiptDB(t)
	pass := []ClaimEvidence{{Kind: ClaimSourceTest, Ref: "t1", Observed: true, Passed: true}}
	fail := []ClaimEvidence{{Kind: ClaimSourceTest, Ref: "t1", Observed: true, Passed: false}}
	if err := d.SaveClaims([]AgentClaim{{ID: "c", Text: "first", Evidence: pass}}); err != nil {
		t.Fatalf("first SaveClaims: %v", err)
	}
	if err := d.SaveClaims([]AgentClaim{{ID: "c", Text: "second", Evidence: fail}}); err != nil {
		t.Fatalf("second SaveClaims: %v", err)
	}
	got, err := d.ClaimResults()
	if err != nil {
		t.Fatalf("ClaimResults: %v", err)
	}
	if len(got) != 1 || got[0].Verdict != ClaimRefuted || got[0].Evidence != fail[0] {
		t.Fatalf("ClaimResults = %+v, want one REFUTED from the second save", got)
	}
}

func TestSaveClaimsRejectsEmptyID(t *testing.T) {
	d := newReceiptDB(t)
	if err := d.SaveClaims([]AgentClaim{{ID: "  ", Text: "no id"}}); err == nil {
		t.Fatal("SaveClaims succeeded with an empty id, want validation error")
	}
}

func TestClaimResultsEmptyBeforeFirstSave(t *testing.T) {
	d := newReceiptDB(t)
	got, err := d.ClaimResults()
	if err != nil {
		t.Fatalf("ClaimResults on empty table: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ClaimResults = %+v, want empty", got)
	}
}

// W6-C: the plugin appends exactly {obligationID, attempt, outcome, at} to
// receipts.jsonl. No source, no verified flag. Such a line must import, set
// deliveredAt only for posted, and stay a single row on a repeated import.
func TestImportReceiptsPluginWireShape(t *testing.T) {
	d := newReceiptDB(t)
	for _, id := range []string{"wire-posted", "wire-failed"} {
		seedReceiptObligation(t, d, id)
	}
	path := writeReceiptsFile(t, strings.Join([]string{
		`{"obligationID":"wire-posted","attempt":1,"outcome":"posted","at":"` + testReceiptAt + `"}`,
		`{"obligationID":"wire-failed","attempt":2,"outcome":"transport-failed","at":"` + testReceiptAt + `"}`,
	}, "\n")+"\n")

	first, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if first.Imported != 2 || first.Malformed != 0 {
		t.Fatalf("first import = %+v, want 2 imported and no malformed lines", first)
	}
	posted, err := d.Obligation("wire-posted")
	if err != nil {
		t.Fatalf("Obligation(wire-posted): %v", err)
	}
	failed, err := d.Obligation("wire-failed")
	if err != nil {
		t.Fatalf("Obligation(wire-failed): %v", err)
	}
	if posted.DeliveredAt.IsZero() {
		t.Error("posted plugin receipt did not set deliveredAt")
	}
	if !failed.DeliveredAt.IsZero() {
		t.Errorf("transport-failed plugin receipt set deliveredAt = %v", failed.DeliveredAt)
	}
	if len(posted.Attempts) != 1 || len(failed.Attempts) != 1 {
		t.Fatalf("attempts = %d/%d, want one each", len(posted.Attempts), len(failed.Attempts))
	}

	second, err := d.ImportReceipts(path)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 0 || second.Duplicates != 2 {
		t.Fatalf("second import = %+v, want 0 imported and 2 duplicates", second)
	}
	count, err := d.ReceiptCount()
	if err != nil || count != 2 {
		t.Fatalf("ReceiptCount = %d, %v; want 2", count, err)
	}
}
