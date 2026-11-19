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
