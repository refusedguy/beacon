package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeTaskClaimStore lets a handler test assert the port contract without
// touching the filesystem.
type fakeTaskClaimStore struct {
	out      TaskClaimOutput
	err      error
	gotInput TaskClaimInput
	gotAt    string
}

func (f *fakeTaskClaimStore) Claim(_ context.Context, in TaskClaimInput, claimedAt string) (TaskClaimOutput, error) {
	f.gotInput = in
	f.gotAt = claimedAt
	return f.out, f.err
}

// claimGraphReaderFake is a fake GraphIndexReader for the claim graphReady test.
type claimGraphReaderFake struct {
	status domain.GraphStatus
	err    error
}

func (f *claimGraphReaderFake) GraphStatus(context.Context) (domain.GraphStatus, error) {
	return f.status, f.err
}

// fakeTaskClaimGraph implements ClaimGraph with a fixed dependency and cycle
// table, so selection tests do not depend on the graph index on disk.
type fakeTaskClaimGraph struct {
	deps   map[string][]string
	cycles map[string]bool
}

func (f *fakeTaskClaimGraph) Dependencies(id string) []string { return f.deps[id] }
func (f *fakeTaskClaimGraph) HasCycle(id string) bool         { return f.cycles[id] }

func claimTestCard(id, phase string, extra ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s. Test card\n", id)
	fmt.Fprintln(&b, "Статус: todo")
	fmt.Fprintf(&b, "Фаза: %s\n", phase)
	fmt.Fprintln(&b, "Тип: task")
	fmt.Fprintln(&b, "Владелец файлов: internal/a.go, internal/b.go")
	fmt.Fprintln(&b, "Зависимости: T0.1, T0.2")
	fmt.Fprintln(&b, "Ссылка: нет")
	fmt.Fprintln(&b, "Агент: нет")
	fmt.Fprintln(&b, "Время: нет")
	fmt.Fprintln(&b, "Цель: цель задачи")
	fmt.Fprintln(&b, "Контекст: контекст задачи")
	fmt.Fprintln(&b, "Шаги: шаги задачи")
	fmt.Fprintln(&b, "Проверка: проверка задачи")
	fmt.Fprintln(&b, "Приёмка: приёмка задачи")
	for _, line := range extra {
		fmt.Fprintln(&b, line)
	}
	return b.String()
}

func writeClaimTestCard(t *testing.T, dir, id, phase string, extra ...string) string {
	t.Helper()
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(claimTestCard(id, phase, extra...)), 0644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	return path
}

func writeClaimTestCardWithStatus(t *testing.T, dir, id, phase, status string) string {
	t.Helper()
	text := strings.Replace(claimTestCard(id, phase), "Статус: todo", "Статус: "+status, 1)
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	return path
}

func claimErrorCode(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	var appErr *domain.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected domain.AppError, got %v", err)
	}
	return appErr.Code
}

func TestTaskClaimHandlerRequiresAgentID(t *testing.T) {
	store := &fakeTaskClaimStore{}
	h := newTaskClaimHandler(store, func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) })

	_, err := h.Claim(context.Background(), TaskClaimInput{})
	if claimErrorCode(t, err) != domain.CodeInvalidInput {
		t.Fatalf("empty agentID gave %v, want INVALID_INPUT", err)
	}
	if store.gotAt != "" {
		t.Fatalf("store was called for an invalid input at %q", store.gotAt)
	}
}

func TestTaskClaimHandlerReturnsFullContext(t *testing.T) {
	want := TaskClaimOutput{
		CardID:       "IDX-05",
		Title:        "MCP инструмент beacon_task_claim",
		Status:       "in_progress",
		AgentID:      "ses_worker",
		Goal:         "атомарная выдача задачи",
		Dependencies: []string{"IDX-01"},
		OwnerFiles:   []string{"internal/infra/mcp/tool_task_claim.go"},
		PlanMode:     "full",
		ClaimedAt:    "2026-11-21T12:00:00Z",
		Handoff: domain.Handoff{
			Intent:           "цель",
			Evidence:         "контекст",
			PatchPlan:        "шаги",
			VerificationPlan: "проверка",
			RiskNotes:        "приёмка",
		},
	}
	store := &fakeTaskClaimStore{out: want}
	h := newTaskClaimHandler(store, func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) })

	got, err := h.Claim(context.Background(), TaskClaimInput{AgentID: "ses_worker", Phase: "P1", OrderByPhase: true})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got.CardID != want.CardID || got.Goal != want.Goal || got.Status != "in_progress" {
		t.Fatalf("core fields = %+v, want %+v", got, want)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0] != "IDX-01" {
		t.Fatalf("dependencies = %v", got.Dependencies)
	}
	if len(got.OwnerFiles) != 1 || got.OwnerFiles[0] != "internal/infra/mcp/tool_task_claim.go" {
		t.Fatalf("ownerFiles = %v", got.OwnerFiles)
	}
	if got.PlanMode != "full" || got.ClaimedAt != want.ClaimedAt {
		t.Fatalf("plan mode or time = %q / %q", got.PlanMode, got.ClaimedAt)
	}
	if got.Handoff != want.Handoff {
		t.Fatalf("handoff = %+v, want %+v", got.Handoff, want.Handoff)
	}
	if store.gotInput.AgentID != "ses_worker" || store.gotInput.Phase != "P1" || !store.gotInput.OrderByPhase {
		t.Fatalf("store input = %+v", store.gotInput)
	}
	if store.gotAt != want.ClaimedAt {
		t.Fatalf("claimedAt = %q, want %q", store.gotAt, want.ClaimedAt)
	}
}

func TestTaskClaimHandlerGraphReady(t *testing.T) {
	base := TaskClaimOutput{CardID: "IDX-05", Status: "in_progress"}
	fresh := domain.GraphStatus{Built: true, IndexHash: "h", SourceHash: "h"}
	stale := domain.GraphStatus{Built: true, IndexHash: "old", SourceHash: "new"}

	tests := []struct {
		name   string
		reader GraphIndexReader
		want   bool
	}{
		{"no reader", nil, false},
		{"fresh graph", &claimGraphReaderFake{status: fresh}, true},
		{"stale graph", &claimGraphReaderFake{status: stale}, false},
		{"read error", &claimGraphReaderFake{status: fresh, err: errors.New("index closed")}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeTaskClaimStore{out: base}
			h := newTaskClaimHandler(store, func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) })
			h.SetGraphIndexReader(tc.reader)
			got, err := h.Claim(context.Background(), TaskClaimInput{AgentID: "ses_worker"})
			if err != nil {
				t.Fatalf("Claim: %v", err)
			}
			if got.GraphReady != tc.want {
				t.Fatalf("GraphReady = %t, want %t", got.GraphReady, tc.want)
			}
		})
	}
}

func TestFileTaskClaimMarksCardInProgress(t *testing.T) {
	dir := t.TempDir()
	path := writeClaimTestCard(t, dir, "IDX-05", "P1")
	store := &FileTaskClaimStore{TasksDir: dir, StateDir: dir}
	when := "2026-11-21T12:00:00Z"

	out, err := store.Claim(context.Background(), TaskClaimInput{AgentID: "ses_worker", CardID: "IDX-05"}, when)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if out.CardID != "IDX-05" || out.Status != "in_progress" || out.AgentID != "ses_worker" {
		t.Fatalf("output = %+v", out)
	}
	if out.ClaimedAt != when {
		t.Fatalf("claimedAt = %q, want %q", out.ClaimedAt, when)
	}
	if len(out.OwnerFiles) != 2 || out.OwnerFiles[0] != "internal/a.go" {
		t.Fatalf("ownerFiles = %v", out.OwnerFiles)
	}
	if len(out.Dependencies) != 2 || out.Dependencies[0] != "T0.1" {
		t.Fatalf("dependencies = %v", out.Dependencies)
	}
	if out.PlanMode != "fast" {
		t.Fatalf("planMode = %q, want fast for a missing field", out.PlanMode)
	}
	if err := out.Handoff.Validate(); err != nil {
		t.Fatalf("handoff is incomplete: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read claimed card: %v", err)
	}
	text := string(data)
	for _, want := range []string{"Статус: in_progress", "Агент: ses_worker", "Время: " + when} {
		if !strings.Contains(text, want) {
			t.Fatalf("claimed card is missing %q:\n%s", want, text)
		}
	}
}

func TestFileTaskClaimRejectsSecondAgent(t *testing.T) {
	dir := t.TempDir()
	writeClaimTestCard(t, dir, "IDX-05", "P1")
	store := &FileTaskClaimStore{TasksDir: dir, StateDir: dir}
	in := TaskClaimInput{CardID: "IDX-05"}

	in.AgentID = "ses_first"
	if _, err := store.Claim(context.Background(), in, "2026-11-21T12:00:00Z"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	in.AgentID = "ses_second"
	if _, err := store.Claim(context.Background(), in, "2026-11-21T12:01:00Z"); err == nil {
		t.Fatal("second agent received the same card")
	} else if code := claimErrorCode(t, err); code != domain.CodeGone {
		t.Fatalf("second claim code = %s, want GONE", code)
	}

	data, err := os.ReadFile(filepath.Join(dir, "IDX-05.md"))
	if err != nil {
		t.Fatalf("read card: %v", err)
	}
	if !strings.Contains(string(data), "Агент: ses_first") {
		t.Fatalf("second claim overwrote the first agent:\n%s", data)
	}
}

func TestFileTaskClaimConcurrentAgentsGetDifferentCards(t *testing.T) {
	dir := t.TempDir()
	writeClaimTestCard(t, dir, "IDX-05", "P1")
	writeClaimTestCard(t, dir, "IDX-06", "P1")
	store := &FileTaskClaimStore{TasksDir: dir, StateDir: dir}
	when := "2026-11-21T12:00:00Z"

	type result struct {
		out TaskClaimOutput
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, agent := range []string{"ses_a", "ses_b"} {
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			out, err := store.Claim(context.Background(), TaskClaimInput{AgentID: agent}, when)
			results <- result{out: out, err: err}
		}(agent)
	}
	wg.Wait()
	close(results)

	seen := make(map[string]string)
	for r := range results {
		if r.err != nil {
			t.Fatalf("concurrent claim: %v", r.err)
		}
		if r.out.CardID == "" {
			t.Fatal("agent got an empty card id")
		}
		if other, dup := seen[r.out.CardID]; dup {
			t.Fatalf("card %s went to both %s and %s", r.out.CardID, other, r.out.AgentID)
		}
		seen[r.out.CardID] = r.out.AgentID
		data, err := os.ReadFile(filepath.Join(dir, r.out.CardID+".md"))
		if err != nil {
			t.Fatalf("read %s: %v", r.out.CardID, err)
		}
		if !strings.Contains(string(data), "Агент: "+r.out.AgentID) {
			t.Fatalf("card %s did not record %s", r.out.CardID, r.out.AgentID)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("claimed %d cards, want 2", len(seen))
	}
}

func TestFileTaskClaimPlanMode(t *testing.T) {
	dir := t.TempDir()
	writeClaimTestCard(t, dir, "IDX-05", "P1", "Режим плана: full")
	writeClaimTestCard(t, dir, "IDX-06", "P1")
	store := &FileTaskClaimStore{TasksDir: dir, StateDir: dir}

	for _, tc := range []struct {
		id   string
		want string
	}{
		{"IDX-05", "full"},
		{"IDX-06", "fast"},
	} {
		out, err := store.Claim(context.Background(), TaskClaimInput{AgentID: "ses_plan", CardID: tc.id}, "2026-11-21T12:00:00Z")
		if err != nil {
			t.Fatalf("claim %s: %v", tc.id, err)
		}
		if out.PlanMode != tc.want {
			t.Fatalf("%s planMode = %q, want %q", tc.id, out.PlanMode, tc.want)
		}
	}
}

func TestFileTaskClaimPhaseFilter(t *testing.T) {
	dir := t.TempDir()
	writeClaimTestCard(t, dir, "IDX-05", "P1")
	writeClaimTestCard(t, dir, "IDX-06", "P2")
	store := &FileTaskClaimStore{TasksDir: dir, StateDir: dir}

	out, err := store.Claim(context.Background(), TaskClaimInput{AgentID: "ses_phase", Phase: "P2"}, "2026-11-21T12:00:00Z")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if out.CardID != "IDX-06" {
		t.Fatalf("claimed %s, want the P2 card", out.CardID)
	}
	if out.Title != "Test card" {
		t.Fatalf("title = %q, want Test card", out.Title)
	}
	if _, err := store.Claim(context.Background(), TaskClaimInput{AgentID: "ses_phase2", Phase: "P3"}, "2026-11-21T12:00:00Z"); err == nil {
		t.Fatal("claim with no matching card should fail")
	} else if code := claimErrorCode(t, err); code != domain.CodeGone {
		t.Fatalf("no-card code = %s, want GONE", code)
	}
}

func TestFileTaskClaimPhasePriorityOverID(t *testing.T) {
	when := "2026-11-21T12:00:00Z"
	newStore := func() *FileTaskClaimStore {
		dir := t.TempDir()
		writeClaimTestCard(t, dir, "AAA-01", "P2")
		writeClaimTestCard(t, dir, "ZZZ-01", "P1")
		return &FileTaskClaimStore{TasksDir: dir, StateDir: dir, Graph: &fakeTaskClaimGraph{}}
	}

	out, err := newStore().Claim(context.Background(), TaskClaimInput{AgentID: "ses_ids"}, when)
	if err != nil {
		t.Fatalf("claim without OrderByPhase: %v", err)
	}
	if out.CardID != "AAA-01" {
		t.Fatalf("claimed %s, want the conservative id order pick AAA-01", out.CardID)
	}

	out, err = newStore().Claim(context.Background(), TaskClaimInput{AgentID: "ses_phase", OrderByPhase: true}, when)
	if err != nil {
		t.Fatalf("claim with OrderByPhase: %v", err)
	}
	if out.CardID != "ZZZ-01" {
		t.Fatalf("claimed %s, want the earlier P1 card", out.CardID)
	}
}

func TestFileTaskClaimPhasePriorityNeedsGraph(t *testing.T) {
	dir := t.TempDir()
	writeClaimTestCard(t, dir, "AAA-01", "P2")
	writeClaimTestCard(t, dir, "ZZZ-01", "P1")
	store := &FileTaskClaimStore{TasksDir: dir, StateDir: dir}

	out, err := store.Claim(context.Background(), TaskClaimInput{AgentID: "ses_nograph", OrderByPhase: true}, "2026-11-21T12:00:00Z")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if out.CardID != "AAA-01" {
		t.Fatalf("claimed %s, want the conservative id order without a graph port", out.CardID)
	}
}

func TestFileTaskClaimRejectsCyclicCard(t *testing.T) {
	when := "2026-11-21T12:00:00Z"
	newStore := func() *FileTaskClaimStore {
		dir := t.TempDir()
		writeClaimTestCard(t, dir, "AAA-01", "P0")
		return &FileTaskClaimStore{
			TasksDir: dir,
			StateDir: dir,
			Graph:    &fakeTaskClaimGraph{cycles: map[string]bool{"AAA-01": true}},
		}
	}

	store := newStore()
	if _, err := store.Claim(context.Background(), TaskClaimInput{AgentID: "ses_cycle"}, when); err == nil {
		t.Fatal("cyclic candidate was claimed")
	} else if code := claimErrorCode(t, err); code != domain.CodeGone {
		t.Fatalf("cyclic candidate code = %s, want GONE", code)
	}
	data, err := os.ReadFile(filepath.Join(store.TasksDir, "AAA-01.md"))
	if err != nil {
		t.Fatalf("read cyclic card: %v", err)
	}
	if !strings.Contains(string(data), "Статус: todo") {
		t.Fatalf("cyclic card was modified:\n%s", data)
	}

	withAlternative := t.TempDir()
	writeClaimTestCard(t, withAlternative, "AAA-01", "P0")
	writeClaimTestCard(t, withAlternative, "ZZZ-01", "P1")
	second := &FileTaskClaimStore{
		TasksDir: withAlternative,
		StateDir: withAlternative,
		Graph:    &fakeTaskClaimGraph{cycles: map[string]bool{"AAA-01": true}},
	}
	out, err := second.Claim(context.Background(), TaskClaimInput{AgentID: "ses_skip", OrderByPhase: true}, when)
	if err != nil {
		t.Fatalf("claim after skipping cycle: %v", err)
	}
	if out.CardID != "ZZZ-01" {
		t.Fatalf("claimed %s, want the non-cyclic card", out.CardID)
	}
}

func TestFileTaskClaimDependencyReadiness(t *testing.T) {
	when := "2026-11-21T12:00:00Z"
	newStore := func(deps map[string][]string) *FileTaskClaimStore {
		dir := t.TempDir()
		writeClaimTestCardWithStatus(t, dir, "DEP-01", "P0", "done")
		writeClaimTestCard(t, dir, "AAA-01", "P1")
		writeClaimTestCard(t, dir, "ZZZ-01", "P2")
		return &FileTaskClaimStore{TasksDir: dir, StateDir: dir, Graph: &fakeTaskClaimGraph{deps: deps}}
	}

	unresolved := newStore(map[string][]string{"AAA-01": {"MISSING-01"}})
	out, err := unresolved.Claim(context.Background(), TaskClaimInput{AgentID: "ses_unresolved", OrderByPhase: true}, when)
	if err != nil {
		t.Fatalf("claim with unresolved dependency: %v", err)
	}
	if out.CardID != "ZZZ-01" {
		t.Fatalf("claimed %s, want the card without unresolved dependencies", out.CardID)
	}

	resolved := newStore(map[string][]string{"AAA-01": {"DEP-01.md"}})
	out, err = resolved.Claim(context.Background(), TaskClaimInput{AgentID: "ses_resolved", OrderByPhase: true}, when)
	if err != nil {
		t.Fatalf("claim with resolved dependency: %v", err)
	}
	if out.CardID != "AAA-01" {
		t.Fatalf("claimed %s, want the card whose dependency is done", out.CardID)
	}
}

func TestWriteMergedNoteIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := writeClaimTestCard(t, dir, "IDX-05", "P1")
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	if err := WriteMergedNote(dir, path, 1605, at); err != nil {
		t.Fatalf("first merged note: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read card: %v", err)
	}
	line := "- PR #1605 MERGED: 2026-11-23 (squash)."
	if !strings.Contains(string(first), line) {
		t.Fatalf("card is missing the MERGED note:\n%s", first)
	}

	if err := WriteMergedNote(dir, path, 1605, at.Add(time.Hour)); err != nil {
		t.Fatalf("repeated merged note: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read card after repeat: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("repeat changed the card:\n%s", second)
	}
	if n := strings.Count(string(second), "PR #1605 MERGED"); n != 1 {
		t.Fatalf("MERGED marker count = %d, want 1", n)
	}
}

func TestWriteMergedNoteConcurrentWithClaim(t *testing.T) {
	dir := t.TempDir()
	path := writeClaimTestCard(t, dir, "IDX-05", "P1")
	store := &FileTaskClaimStore{TasksDir: dir, StateDir: dir}
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

	errs := make(chan error, 3)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- WriteMergedNote(dir, path, 1605, at)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := store.Claim(context.Background(), TaskClaimInput{AgentID: "ses_a", CardID: "IDX-05"}, "2026-11-23T10:00:00Z")
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read card: %v", err)
	}
	text := string(data)
	if n := strings.Count(text, "PR #1605 MERGED"); n != 1 {
		t.Fatalf("MERGED marker count = %d, want 1:\n%s", n, text)
	}
	for _, want := range []string{"Статус: in_progress", "Агент: ses_a", "## Подтверждение"} {
		if !strings.Contains(text, want) {
			t.Fatalf("card lost %q:\n%s", want, text)
		}
	}
}

func TestFileTaskClaimDroppedCardUnavailable(t *testing.T) {
	dir := t.TempDir()
	writeClaimTestCardWithStatus(t, dir, "AAA-01", "P0", "dropped")
	store := &FileTaskClaimStore{TasksDir: dir, StateDir: dir, Graph: &fakeTaskClaimGraph{}}
	when := "2026-11-21T12:00:00Z"

	for _, in := range []TaskClaimInput{
		{AgentID: "ses_pinned", CardID: "AAA-01"},
		{AgentID: "ses_auto", OrderByPhase: true},
	} {
		if _, err := store.Claim(context.Background(), in, when); err == nil {
			t.Fatalf("dropped card was claimed for %+v", in)
		} else if code := claimErrorCode(t, err); code != domain.CodeGone {
			t.Fatalf("dropped card code = %s, want GONE", code)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "AAA-01.md"))
	if err != nil {
		t.Fatalf("read dropped card: %v", err)
	}
	if !strings.Contains(string(data), "Статус: dropped") {
		t.Fatalf("dropped card was modified:\n%s", data)
	}
}
