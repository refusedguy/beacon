package orchestrator

import (
	"context"
	"reflect"
	"testing"
)

// TestApprovalActionsFirstAttemptVsRetry is the measurable check for CCx-04:
// the first attempt keeps narrow permissions, the retry after a denial is one
// escalation and zero extra permissions.
func TestApprovalActionsFirstAttemptVsRetry(t *testing.T) {
	w := Watch{ID: "w1", PR: 42, Revision: 7, Verdict: VerdictRetry}
	narrow := []string{"fs:workspace", "net:deny"}

	first, retry := ApprovalActions(w, narrow)

	if first.Action != ActionApprovalFirst {
		t.Fatalf("first action = %q, want %q", first.Action, ActionApprovalFirst)
	}
	if first.Permissions.Escalations != 0 {
		t.Fatalf("first escalations = %d, want 0", first.Permissions.Escalations)
	}
	if !reflect.DeepEqual(first.Permissions.Extra, narrow) {
		t.Fatalf("first extra = %v, want narrow %v", first.Permissions.Extra, narrow)
	}

	if retry.Action != ActionApprovalRetry {
		t.Fatalf("retry action = %q, want %q", retry.Action, ActionApprovalRetry)
	}
	if retry.Permissions.Escalations != 1 {
		t.Fatalf("retry escalations = %d, want 1", retry.Permissions.Escalations)
	}
	if len(retry.Permissions.Extra) != 0 {
		t.Fatalf("retry extra = %v, want 0 extra permissions", retry.Permissions.Extra)
	}
	if retry.WatchID != w.ID || retry.PR != w.PR || retry.Revision != w.Revision {
		t.Fatalf("retry identity = %s/%d/%d, want %s/%d/%d",
			retry.WatchID, retry.PR, retry.Revision, w.ID, w.PR, w.Revision)
	}
}

// TestApprovalActionsCopyNarrowPermissions proves the first-attempt slice is
// copied: clearing the caller's slice must not change the recorded approval.
func TestApprovalActionsCopyNarrowPermissions(t *testing.T) {
	narrow := []string{"fs:workspace"}
	first, _ := ApprovalActions(Watch{ID: "w1"}, narrow)
	narrow[0] = "changed"

	if first.Permissions.Extra[0] != "fs:workspace" {
		t.Fatalf("first extra = %v, want the original narrow permissions", first.Permissions.Extra)
	}
}

// TestRunEmitsEscalatedRetryApproval checks the wiring: a retry Watch gets a
// separate escalated-retry approval, and a fail Watch does not.
func TestRunEmitsEscalatedRetryApproval(t *testing.T) {
	st := newFakeStore(
		Watch{ID: "w1", PR: 11, Verdict: VerdictRetry, Revision: 1},
		Watch{ID: "w2", PR: 12, Verdict: VerdictFail, Revision: 2},
	)

	result, err := Run(context.Background(), st, &fakeMerger{}, &fakeWaker{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Approvals) != 1 {
		t.Fatalf("approvals = %+v, want one retry approval", result.Approvals)
	}
	approval := result.Approvals[0]
	if approval.Action != ActionApprovalRetry || approval.WatchID != "w1" {
		t.Fatalf("approval = %+v, want escalated retry for w1", approval)
	}
	if approval.Permissions.Escalations != 1 || len(approval.Permissions.Extra) != 0 {
		t.Fatalf("retry permissions = %+v, want 1 escalation and 0 extra", approval.Permissions)
	}
}
