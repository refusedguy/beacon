package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

const taskCloseFixedAt = "2026-11-22T12:00:00Z"

func taskCloseClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) }
}

// fakeTaskCloseStore records the normalized input and the stamped time so the
// handler tests can assert the boundary contract without touching the disk.
type fakeTaskCloseStore struct {
	out      TaskCloseOutput
	err      error
	gotInput TaskCloseInput
	gotAt    string
	calls    int
}

func (f *fakeTaskCloseStore) Close(_ context.Context, in TaskCloseInput, closedAt string) (TaskCloseOutput, error) {
	f.calls++
	f.gotInput = in
	f.gotAt = closedAt
	return f.out, f.err
}

func taskCloseAppError(t *testing.T, err error) *domain.AppError {
	t.Helper()
	var appErr *domain.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *domain.AppError, got %v", err)
	}
	return appErr
}

func taskCloseErrorCode(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	return taskCloseAppError(t, err).Code
}

func taskCloseTestCardText(id, status, agent string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s. Test card\n", id)
	fmt.Fprintf(&b, "Статус: %s\n", status)
	fmt.Fprintln(&b, "Тип: task")
	fmt.Fprintf(&b, "Агент: %s\n", agent)
	fmt.Fprintln(&b, "Цель: close boundary test")
	fmt.Fprintln(&b, "## Details")
	fmt.Fprintln(&b, "body line")
	return b.String()
}

func writeTaskCloseTestCard(t *testing.T, dir, id, status, agent string) string {
	t.Helper()
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(taskCloseTestCardText(id, status, agent)), 0644); err != nil {
		t.Fatalf("write card %s: %v", id, err)
	}
	return path
}

func taskCloseTestStore(t *testing.T) (*FileTaskCloseStore, string) {
	t.Helper()
	dir := t.TempDir()
	return &FileTaskCloseStore{TasksDir: dir}, dir
}

func readTaskCloseCard(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read card %s: %v", path, err)
	}
	return string(data)
}

func assertCardHas(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, line := range want {
		if !strings.Contains(text, line) {
			t.Errorf("card is missing %q:\n%s", line, text)
		}
	}
}

func TestTaskCloseHandlerRequiresCardIDAndReason(t *testing.T) {
	tests := []struct {
		name string
		in   TaskCloseInput
	}{
		{"empty input", TaskCloseInput{}},
		{"missing reason", TaskCloseInput{CardID: "IDX-05", Status: "done"}},
		{"missing cardID", TaskCloseInput{Status: "done", Reason: "fixed"}},
		{"blank cardID", TaskCloseInput{CardID: " \t ", Status: "done", Reason: "fixed"}},
		{"blank reason", TaskCloseInput{CardID: "IDX-05", Status: "done", Reason: " \t "}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeTaskCloseStore{}
			h := newTaskCloseHandler(store, taskCloseClock())

			out, err := h.Close(context.Background(), tc.in)
			if code := taskCloseErrorCode(t, err); code != domain.CodeInvalidInput {
				t.Fatalf("code = %s, want INVALID_INPUT", code)
			}
			if out.OK {
				t.Fatal("ok = true for an invalid input")
			}
			if store.calls != 0 {
				t.Fatalf("store called %d times for an invalid input", store.calls)
			}
		})
	}
}

func TestTaskCloseHandlerAcceptsFourStatuses(t *testing.T) {
	for _, status := range []string{"done", "dropped", "blocked", "duplicate"} {
		t.Run(status, func(t *testing.T) {
			store := &fakeTaskCloseStore{out: TaskCloseOutput{OK: true, CardID: "IDX-05", Status: status}}
			h := newTaskCloseHandler(store, taskCloseClock())
			in := TaskCloseInput{
				CardID:      "IDX-05.md",
				Status:      " " + strings.ToUpper(status) + " ",
				Reason:      "  fixed  ",
				DuplicateOf: "IDX-99.md",
				AgentID:     " ses_worker ",
			}

			out, err := h.Close(context.Background(), in)
			if err != nil {
				t.Fatalf("Close: %v", err)
			}
			if !out.OK || out.CardID != "IDX-05" || out.Status != status {
				t.Fatalf("output = %+v", out)
			}
			if store.gotInput.CardID != "IDX-05" || store.gotInput.Status != status ||
				store.gotInput.Reason != "fixed" || store.gotInput.DuplicateOf != "IDX-99" ||
				store.gotInput.AgentID != "ses_worker" {
				t.Fatalf("store input was not normalized: %+v", store.gotInput)
			}
			if store.gotAt != taskCloseFixedAt {
				t.Fatalf("closedAt = %q, want %q", store.gotAt, taskCloseFixedAt)
			}
		})
	}
}

func TestTaskCloseHandlerRejectsUnknownStatus(t *testing.T) {
	store := &fakeTaskCloseStore{}
	h := newTaskCloseHandler(store, taskCloseClock())

	_, err := h.Close(context.Background(), TaskCloseInput{CardID: "IDX-05", Status: "closed", Reason: "fixed"})
	if code := taskCloseErrorCode(t, err); code != domain.CodeInvalidInput {
		t.Fatalf("code = %s, want INVALID_INPUT", code)
	}
	if store.calls != 0 {
		t.Fatalf("store called %d times for an unknown status", store.calls)
	}
}

func TestTaskCloseHandlerDuplicateNeedsDifferentTarget(t *testing.T) {
	tests := []struct {
		name        string
		duplicateOf string
	}{
		{"missing duplicateOf", ""},
		{"self with suffix", "IDX-05.md"},
		{"self without suffix", "IDX-05"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeTaskCloseStore{}
			h := newTaskCloseHandler(store, taskCloseClock())
			in := TaskCloseInput{CardID: "IDX-05", Status: "duplicate", Reason: "fixed", DuplicateOf: tc.duplicateOf}

			_, err := h.Close(context.Background(), in)
			if code := taskCloseErrorCode(t, err); code != domain.CodeInvalidInput {
				t.Fatalf("code = %s, want INVALID_INPUT", code)
			}
			if store.calls != 0 {
				t.Fatalf("store called %d times for an invalid duplicate target", store.calls)
			}
		})
	}
}

func TestTaskCloseHandlerNilStore(t *testing.T) {
	h := newTaskCloseHandler(nil, taskCloseClock())
	_, err := h.Close(context.Background(), TaskCloseInput{CardID: "IDX-05", Status: "done", Reason: "fixed"})
	if code := taskCloseErrorCode(t, err); code != domain.CodePersistFailed {
		t.Fatalf("code = %s, want PERSIST_FAILED", code)
	}
}

func TestTaskCloseHandlerNilClockDefaultsToNow(t *testing.T) {
	store := &fakeTaskCloseStore{}
	h := newTaskCloseHandler(store, nil)

	if _, err := h.Close(context.Background(), TaskCloseInput{CardID: "IDX-05", Status: "done", Reason: "fixed"}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, store.gotAt); err != nil {
		t.Fatalf("closedAt %q is not RFC3339: %v", store.gotAt, err)
	}
}

func TestTaskCloseHandlerHandleAdapter(t *testing.T) {
	want := TaskCloseOutput{OK: true, CardID: "IDX-05", Status: "done"}
	store := &fakeTaskCloseStore{out: want}
	h := newTaskCloseHandler(store, taskCloseClock())

	result, out, err := h.Handle(context.Background(), &mcpsdk.CallToolRequest{}, TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "fixed", AgentID: "ses_worker",
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	if out != want {
		t.Fatalf("output = %+v, want %+v", out, want)
	}
}

func TestFileTaskCloseWritesAllCloseFields(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")

	out, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "fixed the bug", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !out.OK || out.CardID != "IDX-05" || out.Status != "done" {
		t.Fatalf("output = %+v", out)
	}

	text := readTaskCloseCard(t, path)
	assertCardHas(t, text,
		"Статус: done",
		"Причина: fixed the bug",
		"Закрыл: ses_worker",
		"Время закрытия: "+taskCloseFixedAt,
		"body line",
		"## Details",
	)
	if strings.Contains(text, "Статус: todo") {
		t.Fatalf("old status survived the close:\n%s", text)
	}
}

func TestFileTaskCloseWritesEveryTerminalStatus(t *testing.T) {
	for _, status := range []string{"done", "dropped", "blocked", "duplicate"} {
		t.Run(status, func(t *testing.T) {
			store, dir := taskCloseTestStore(t)
			path := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")
			writeTaskCloseTestCard(t, dir, "IDX-99", "todo", "нет")

			out, err := store.Close(context.Background(), TaskCloseInput{
				CardID: "IDX-05", Status: status, Reason: "fixed", AgentID: "ses_worker", DuplicateOf: "IDX-99",
			}, taskCloseFixedAt)
			if err != nil {
				t.Fatalf("Close: %v", err)
			}
			if out.Status != status {
				t.Fatalf("status = %q, want %q", out.Status, status)
			}
			assertCardHas(t, readTaskCloseCard(t, path), "Статус: "+status)
		})
	}
}

func TestFileTaskCloseRejectsForeignInProgressCard(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "in_progress", "ses_owner")

	_, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "fixed", AgentID: "ses_other",
	}, taskCloseFixedAt)
	appErr := taskCloseAppError(t, err)
	if appErr.Code != domain.CodeInvalidInput {
		t.Fatalf("code = %s, want INVALID_INPUT", appErr.Code)
	}
	if !strings.Contains(appErr.Message, "in_progress by ses_owner, not ses_other") {
		t.Fatalf("message = %q, want both agents named", appErr.Message)
	}

	text := readTaskCloseCard(t, path)
	assertCardHas(t, text, "Статус: in_progress", "Агент: ses_owner")
}

func TestFileTaskCloseAllowsOwnerToCloseInProgressCard(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "in_progress", "ses_owner")

	out, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "fixed", AgentID: "ses_owner",
	}, taskCloseFixedAt)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !out.OK {
		t.Fatal("ok = false for the owning agent")
	}
	assertCardHas(t, readTaskCloseCard(t, path), "Статус: done", "Закрыл: ses_owner")
}

func TestFileTaskCloseRepeatWithSameValuesIsIdempotent(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")
	in := TaskCloseInput{CardID: "IDX-05", Status: "done", Reason: "fixed", AgentID: "ses_worker"}

	for i := 1; i <= 2; i++ {
		out, err := store.Close(context.Background(), in, taskCloseFixedAt)
		if err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
		if !out.OK || out.Status != "done" {
			t.Fatalf("close %d output = %+v", i, out)
		}
	}

	text := readTaskCloseCard(t, path)
	if n := strings.Count(text, "Причина: fixed"); n != 1 {
		t.Fatalf("Причина written %d times, want 1:\n%s", n, text)
	}
	if n := strings.Count(text, "Время закрытия: "+taskCloseFixedAt); n != 1 {
		t.Fatalf("close time written %d times, want 1:\n%s", n, text)
	}
}

func TestFileTaskCloseConflictsWhenReasonChanges(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")

	if _, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "first reason", AgentID: "ses_worker",
	}, taskCloseFixedAt); err != nil {
		t.Fatalf("first close: %v", err)
	}

	_, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "second reason", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	appErr := taskCloseAppError(t, err)
	if appErr.Code != domain.CodeRejectDuplicate || appErr.Status != 409 {
		t.Fatalf("code/status = %s/%d, want REJECT_DUPLICATE/409", appErr.Code, appErr.Status)
	}

	text := readTaskCloseCard(t, path)
	assertCardHas(t, text, "Причина: first reason")
	if strings.Contains(text, "second reason") {
		t.Fatalf("conflicting reason was written:\n%s", text)
	}
}

func TestFileTaskCloseConflictsWhenStatusChanges(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")

	if _, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "fixed", AgentID: "ses_worker",
	}, taskCloseFixedAt); err != nil {
		t.Fatalf("first close: %v", err)
	}

	_, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "dropped", Reason: "fixed", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	if code := taskCloseErrorCode(t, err); code != domain.CodeRejectDuplicate {
		t.Fatalf("code = %s, want REJECT_DUPLICATE", code)
	}
	assertCardHas(t, readTaskCloseCard(t, path), "Статус: done")
}

func TestFileTaskCloseSkipsBrokenLedgerCard(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	brokenPath := filepath.Join(dir, "BAD-01.md")
	if err := os.WriteFile(brokenPath, []byte("# BAD-01. no status\nПричина: orphan\n"), 0644); err != nil {
		t.Fatalf("write broken card: %v", err)
	}
	goodPath := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")

	_, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "BAD-01", Status: "done", Reason: "fixed", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	if code := taskCloseErrorCode(t, err); code != domain.CodeInvalidInput {
		t.Fatalf("code = %s, want INVALID_INPUT for a broken card", code)
	}
	if text := readTaskCloseCard(t, brokenPath); !strings.Contains(text, "Причина: orphan") {
		t.Fatalf("broken card was modified:\n%s", text)
	}
	assertCardHas(t, readTaskCloseCard(t, goodPath), "Статус: todo")
}

func TestFileTaskClosePreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "IDX-05.md")
	crlf := strings.ReplaceAll(taskCloseTestCardText("IDX-05", "todo", "нет"), "\n", "\r\n")
	if err := os.WriteFile(path, []byte(crlf), 0644); err != nil {
		t.Fatalf("write CRLF card: %v", err)
	}
	store := &FileTaskCloseStore{TasksDir: dir}

	out, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "fixed", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !out.OK {
		t.Fatal("ok = false for a CRLF card")
	}

	text := readTaskCloseCard(t, path)
	if strings.Count(text, "\n") != strings.Count(text, "\r\n") {
		t.Fatalf("close introduced a bare LF:\n%q", text)
	}
	assertCardHas(t, text, "Статус: done", "Причина: fixed", "Время закрытия: "+taskCloseFixedAt)
}

func TestFileTaskCloseDuplicateNeedsExistingTarget(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")

	_, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "duplicate", Reason: "fixed", DuplicateOf: "IDX-99", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	appErr := taskCloseAppError(t, err)
	if appErr.Code != domain.CodeInvalidInput {
		t.Fatalf("code = %s, want INVALID_INPUT", appErr.Code)
	}
	if !strings.Contains(appErr.Message, "duplicateOf card IDX-99 not found") {
		t.Fatalf("message = %q, want a missing-target error", appErr.Message)
	}
	assertCardHas(t, readTaskCloseCard(t, path), "Статус: todo")
}

func TestFileTaskCloseDuplicateWritesTargetField(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")
	writeTaskCloseTestCard(t, dir, "IDX-99", "todo", "нет")

	out, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "duplicate", Reason: "same as IDX-99",
		DuplicateOf: "IDX-99.md", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.Status != "duplicate" {
		t.Fatalf("status = %q, want duplicate", out.Status)
	}
	assertCardHas(t, readTaskCloseCard(t, path), "Статус: duplicate", "Дубликат: IDX-99")
}

func TestFileTaskCloseNormalizesInput(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	path := writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")

	out, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05.md", Status: " DONE ", Reason: "  fixed  ",
		AgentID: " ses_worker ", DuplicateOf: "  ",
	}, taskCloseFixedAt)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.CardID != "IDX-05" || out.Status != "done" {
		t.Fatalf("output = %+v, want normalized id and status", out)
	}
	assertCardHas(t, readTaskCloseCard(t, path),
		"Статус: done", "Причина: fixed", "Закрыл: ses_worker", "Время закрытия: "+taskCloseFixedAt)
}

func TestFileTaskCloseRejectsEmptyTasksDir(t *testing.T) {
	store := &FileTaskCloseStore{}
	_, err := store.Close(context.Background(), TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "fixed", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	if code := taskCloseErrorCode(t, err); code != domain.CodePersistFailed {
		t.Fatalf("code = %s, want PERSIST_FAILED", code)
	}
}

func TestFileTaskCloseRespectsCancelledContext(t *testing.T) {
	store, dir := taskCloseTestStore(t)
	writeTaskCloseTestCard(t, dir, "IDX-05", "todo", "нет")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Close(ctx, TaskCloseInput{
		CardID: "IDX-05", Status: "done", Reason: "fixed", AgentID: "ses_worker",
	}, taskCloseFixedAt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
