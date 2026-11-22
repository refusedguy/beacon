package orchestrator

import (
	"errors"
	"testing"
)

// A resumed session starts read-only. The first shell call must be refused
// before it writes anything, whatever the earlier run used (CCx-14).
func TestResumeSessionFirstShellCallIsReadOnly(t *testing.T) {
	session := ResumeSession("sess-1")

	if session.Mode != SandboxReadOnly {
		t.Fatalf("mode = %q, want %q", session.Mode, SandboxReadOnly)
	}
	if !session.ReadOnly() {
		t.Fatal("a fresh resumed session must be read-only")
	}
	check := session.Sandbox().CheckWrite("notes.txt")
	if check.Allowed {
		t.Fatalf("first shell call may write: %+v", check)
	}

	err := session.BeforeShellCall("notes.txt")
	if !errors.Is(err, ErrReadOnlyResume) {
		t.Fatalf("err = %v, want ErrReadOnlyResume", err)
	}
	if !errors.Is(err, ErrSandboxDenied) {
		t.Fatalf("err = %v, want it to wrap ErrSandboxDenied", err)
	}
	if session.Denials() != 1 {
		t.Fatalf("denials = %d, want 1", session.Denials())
	}
	if session.Writes() != 0 {
		t.Fatalf("writes = %d, want 0 on the first call", session.Writes())
	}
}

// Approval lifts the window. The second shell call writes one file and the
// denial from the first call stays recorded (CCx-14).
func TestResumeSessionApprovalAllowsSecondShellCall(t *testing.T) {
	session := ResumeSession("sess-2")

	if err := session.BeforeShellCall("first.txt"); !errors.Is(err, ErrReadOnlyResume) {
		t.Fatalf("first call err = %v, want ErrReadOnlyResume", err)
	}
	if session.Denials() != 1 || session.Writes() != 0 {
		t.Fatalf("before approval: denials = %d, writes = %d", session.Denials(), session.Writes())
	}

	session.Approve()
	if !session.Approved {
		t.Fatal("approval was not recorded on the session metadata")
	}
	if session.ReadOnly() {
		t.Fatal("the read-only window must close after approval")
	}

	if err := session.BeforeShellCall("second.txt"); err != nil {
		t.Fatalf("second call err = %v, want nil", err)
	}
	if session.Writes() != 1 {
		t.Fatalf("writes = %d, want 1 after approval", session.Writes())
	}
	if session.Denials() != 1 {
		t.Fatalf("denials = %d, want 1 after approval", session.Denials())
	}
}

// Approval removes the read-only window but not the sandbox protection of
// control files. A write to a protected path still fails (CCx-07, CCx-14).
func TestResumeSessionApprovalKeepsProtectedPathsDenied(t *testing.T) {
	session := ResumeSession("sess-3")
	session.Approve()

	err := session.BeforeShellCall(".git/hooks/pre-commit")
	if !errors.Is(err, ErrSandboxDenied) {
		t.Fatalf("err = %v, want ErrSandboxDenied", err)
	}
	if errors.Is(err, ErrReadOnlyResume) {
		t.Fatalf("err = %v, must not be a resume denial after approval", err)
	}
	if session.Writes() != 0 {
		t.Fatalf("writes = %d, want 0 for a protected path", session.Writes())
	}
}

// An invalid path is refused in both windows. The refusal before approval is
// a resume denial; the one after approval is a plain sandbox denial.
func TestResumeSessionInvalidPathIsRefusedInBothWindows(t *testing.T) {
	session := ResumeSession("sess-4")

	if err := session.BeforeShellCall("../escape.txt"); !errors.Is(err, ErrReadOnlyResume) {
		t.Fatalf("err = %v, want ErrReadOnlyResume", err)
	}

	session.Approve()
	if err := session.BeforeShellCall("../escape.txt"); !errors.Is(err, ErrSandboxDenied) {
		t.Fatalf("err = %v, want ErrSandboxDenied", err)
	}
	if session.Writes() != 0 {
		t.Fatalf("writes = %d, want 0 for an invalid path", session.Writes())
	}
}
