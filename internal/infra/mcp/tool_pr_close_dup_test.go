package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

type prCloseDupCleanup struct {
	mu          sync.Mutex
	closeCalls  []prCloseDupCloseCall
	headCalls   int
	branchCalls []string
	headBranch  string
	headErr     error
	closeErr    error
	branchErr   error
}

type prCloseDupCloseCall struct {
	repo   string
	pr     int
	reason string
}

func newPRCloseDupCleanup() *prCloseDupCleanup {
	return &prCloseDupCleanup{headBranch: "fix/dup"}
}

func (f *prCloseDupCleanup) ClosePR(_ context.Context, repo string, pr int, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls = append(f.closeCalls, prCloseDupCloseCall{repo: repo, pr: pr, reason: reason})
	return f.closeErr
}

func (f *prCloseDupCleanup) HeadBranch(_ context.Context, _ string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headCalls++
	return f.headBranch, f.headErr
}

func (f *prCloseDupCleanup) DeleteBranch(_ context.Context, _, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branchCalls = append(f.branchCalls, branch)
	return f.branchErr
}

func (f *prCloseDupCleanup) counts() (closes, heads, branches int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.closeCalls), f.headCalls, len(f.branchCalls)
}

type prCloseDupCI struct {
	state string
	err   error
}

func (f prCloseDupCI) PRState(_ context.Context, _ string, _ int) (domain.PRState, error) {
	return domain.PRState{State: f.state}, f.err
}

type prCloseDupSnapshot struct {
	watches []domain.Watch
	err     error
}

func (f prCloseDupSnapshot) Snapshot() (StateSnapshot, error) {
	return StateSnapshot{LiveWatches: f.watches}, f.err
}

type prCloseDupEvent struct {
	name   string
	fields observe.Fields
}

type prCloseDupSink struct {
	mu     sync.Mutex
	events []prCloseDupEvent
}

func (s *prCloseDupSink) Log(_ observe.Level, event string, fields observe.Fields) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, prCloseDupEvent{name: event, fields: fields})
}

func (s *prCloseDupSink) Inc(_, _ string) {}

func (s *prCloseDupSink) find(name string) (observe.Fields, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.name == name {
			return e.fields, true
		}
	}
	return nil, false
}

func prCloseDupTestHandler(cleanup PRCleanup, ci domain.CIProvider, snap SnapshotReader) *PRCloseDupHandler {
	return NewPRCloseDupHandler(cleanup, ci, snap, "executor")
}

func TestPRCloseDupDryRunMakesNoWrites(t *testing.T) {
	cleanup := newPRCloseDupCleanup()
	h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: "OPEN"}, prCloseDupSnapshot{})

	out, err := h.Close(context.Background(), PRCloseDupInput{
		Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11,
	})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.Action != PRCloseDupActionDryRun {
		t.Errorf("action = %q, want %q", out.Action, PRCloseDupActionDryRun)
	}
	if out.Reason != PRCloseDupReasonConfirmRequired {
		t.Errorf("reason = %q, want %q", out.Reason, PRCloseDupReasonConfirmRequired)
	}
	if closes, heads, branches := cleanup.counts(); closes != 0 || heads != 0 || branches != 0 {
		t.Errorf("port calls = closes %d, heads %d, branches %d; want all zero", closes, heads, branches)
	}
}

func TestPRCloseDupLiveWatchRefusesBeforeClose(t *testing.T) {
	cleanup := newPRCloseDupCleanup()
	snap := prCloseDupSnapshot{watches: []domain.Watch{
		{ID: "watch:live", Repo: "refusedguy/beacon", PR: 12, Status: domain.StatusWatching},
	}}
	h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: "OPEN"}, snap)

	out, err := h.Close(context.Background(), PRCloseDupInput{
		Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11, Confirm: true, DeleteBranch: true,
	})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.Action != PRCloseDupActionNoop || out.Reason != PRCloseDupReasonActiveWatch {
		t.Errorf("got action %q reason %q; want noop/active_watch", out.Action, out.Reason)
	}
	if out.WatchID != "watch:live" {
		t.Errorf("watchID = %q, want watch:live", out.WatchID)
	}
	if closes, heads, branches := cleanup.counts(); closes != 0 || heads != 0 || branches != 0 {
		t.Errorf("port calls = closes %d, heads %d, branches %d; want all zero", closes, heads, branches)
	}
}

func TestPRCloseDupSnapshotFailureRefusesClose(t *testing.T) {
	cleanup := newPRCloseDupCleanup()
	snap := prCloseDupSnapshot{err: errors.New("store offline")}
	h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: "OPEN"}, snap)

	_, err := h.Close(context.Background(), PRCloseDupInput{
		Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11, Confirm: true, DeleteBranch: true,
	})
	if err == nil {
		t.Fatal("want snapshot error")
	}
	var appErr *domain.AppError
	if !errors.As(err, &appErr) || appErr.Code != domain.CodePersistFailed {
		t.Errorf("error = %v, want PERSIST_FAILED", err)
	}
	if closes, heads, branches := cleanup.counts(); closes != 0 || heads != 0 || branches != 0 {
		t.Errorf("port calls = closes %d, heads %d, branches %d; want all zero", closes, heads, branches)
	}
}

func TestPRCloseDupClosedWatchDoesNotBlock(t *testing.T) {
	cleanup := newPRCloseDupCleanup()
	snap := prCloseDupSnapshot{watches: []domain.Watch{
		{ID: "watch:old", Repo: "refusedguy/beacon", PR: 12, Status: domain.StatusClosed},
	}}
	h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: "OPEN"}, snap)

	out, err := h.Close(context.Background(), PRCloseDupInput{
		Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11, Confirm: true,
	})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.Action != PRCloseDupActionClosed {
		t.Errorf("action = %q, want %q", out.Action, PRCloseDupActionClosed)
	}
	if closes, _, _ := cleanup.counts(); closes != 1 {
		t.Errorf("ClosePR calls = %d, want 1", closes)
	}
}

func TestPRCloseDupClosedAndMergedAreNoop(t *testing.T) {
	for _, tt := range []struct {
		name   string
		state  string
		reason string
	}{
		{"closed", "CLOSED", PRCloseDupReasonAlreadyClosed},
		{"merged", "MERGED", PRCloseDupReasonMerged},
		{"lowercase merged", "merged", PRCloseDupReasonMerged},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := newPRCloseDupCleanup()
			h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: tt.state}, prCloseDupSnapshot{})

			out, err := h.Close(context.Background(), PRCloseDupInput{
				Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11, Confirm: true, DeleteBranch: true,
			})
			if err != nil {
				t.Fatalf("Close: %v", err)
			}
			if out.Action != PRCloseDupActionNoop || out.Reason != tt.reason {
				t.Errorf("got action %q reason %q; want noop/%s", out.Action, out.Reason, tt.reason)
			}
			if closes, heads, branches := cleanup.counts(); closes != 0 || heads != 0 || branches != 0 {
				t.Errorf("port calls = closes %d, heads %d, branches %d; want all zero", closes, heads, branches)
			}
		})
	}
}

func TestPRCloseDupConfirmedWritesOnceAndRepeatsNoop(t *testing.T) {
	cleanup := newPRCloseDupCleanup()
	sink := &prCloseDupSink{}
	observe.SetDefault(sink)
	defer observe.SetDefault(nil)

	h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: "OPEN"}, prCloseDupSnapshot{})
	in := PRCloseDupInput{
		Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11, Confirm: true, DeleteBranch: true,
	}

	out, err := h.Close(context.Background(), in)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.Action != PRCloseDupActionClosed || !out.BranchDeleted {
		t.Errorf("got action %q branchDeleted %v; want closed/true", out.Action, out.BranchDeleted)
	}
	if closes, heads, branches := cleanup.counts(); closes != 1 || heads != 1 || branches != 1 {
		t.Errorf("port calls = closes %d, heads %d, branches %d; want 1/1/1", closes, heads, branches)
	}
	if cleanup.closeCalls[0].reason != "duplicate of #11" {
		t.Errorf("close reason = %q, want duplicate of #11", cleanup.closeCalls[0].reason)
	}
	if cleanup.branchCalls[0] != "fix/dup" {
		t.Errorf("deleted branch = %q, want fix/dup", cleanup.branchCalls[0])
	}

	// The CIProvider still reports OPEN on a repeat. The in-process key must
	// stop the second close without relying on the provider catching up.
	repeat, err := h.Close(context.Background(), in)
	if err != nil {
		t.Fatalf("repeat Close: %v", err)
	}
	if repeat.Action != PRCloseDupActionNoop || repeat.Reason != PRCloseDupReasonAlreadyClosed {
		t.Errorf("repeat got action %q reason %q; want noop/already_closed", repeat.Action, repeat.Reason)
	}
	if closes, heads, branches := cleanup.counts(); closes != 1 || heads != 1 || branches != 1 {
		t.Errorf("after repeat port calls = closes %d, heads %d, branches %d; want 1/1/1", closes, heads, branches)
	}

	fields, ok := sink.find("pr_close_dup")
	if !ok {
		t.Fatal("no pr_close_dup observe event")
	}
	if fields["repo"] != "refusedguy/beacon" || fields[observe.FieldPR] != 12 || fields["duplicateOf"] != 11 {
		t.Errorf("observe fields = %v, want repo/pr/duplicateOf", fields)
	}
	if fields[observe.FieldActor] != "executor" {
		t.Errorf("observe actor = %v, want executor", fields[observe.FieldActor])
	}
}

func TestPRCloseDupConfirmWithoutDeleteBranch(t *testing.T) {
	cleanup := newPRCloseDupCleanup()
	h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: "OPEN"}, prCloseDupSnapshot{})

	out, err := h.Close(context.Background(), PRCloseDupInput{
		Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11, Confirm: true,
	})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.Action != PRCloseDupActionClosed || out.BranchDeleted {
		t.Errorf("got action %q branchDeleted %v; want closed/false", out.Action, out.BranchDeleted)
	}
	if closes, heads, branches := cleanup.counts(); closes != 1 || heads != 0 || branches != 0 {
		t.Errorf("port calls = closes %d, heads %d, branches %d; want 1/0/0", closes, heads, branches)
	}
}

func TestPRCloseDupBranchDeleteFailureKeepsCloseRecorded(t *testing.T) {
	cleanup := newPRCloseDupCleanup()
	cleanup.branchErr = errors.New("branch in use")
	h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: "OPEN"}, prCloseDupSnapshot{})

	out, err := h.Close(context.Background(), PRCloseDupInput{
		Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11, Confirm: true, DeleteBranch: true,
	})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.Action != PRCloseDupActionClosed || out.Reason != PRCloseDupReasonBranchDeleteFailed || out.BranchDeleted {
		t.Errorf("got action %q reason %q branchDeleted %v", out.Action, out.Reason, out.BranchDeleted)
	}

	repeat, err := h.Close(context.Background(), PRCloseDupInput{
		Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 11, Confirm: true, DeleteBranch: true,
	})
	if err != nil {
		t.Fatalf("repeat Close: %v", err)
	}
	if repeat.Action != PRCloseDupActionNoop {
		t.Errorf("repeat action = %q, want noop", repeat.Action)
	}
	if closes, _, _ := cleanup.counts(); closes != 1 {
		t.Errorf("ClosePR calls = %d, want 1", closes)
	}
}

func TestPRCloseDupInputValidation(t *testing.T) {
	cleanup := newPRCloseDupCleanup()
	h := prCloseDupTestHandler(cleanup, prCloseDupCI{state: "OPEN"}, prCloseDupSnapshot{})

	for _, in := range []PRCloseDupInput{
		{Repo: "bad", PR: 12, DuplicateOf: 11},
		{Repo: "a/b/c", PR: 12, DuplicateOf: 11},
		{Repo: "refusedguy/beacon", PR: 0, DuplicateOf: 11},
		{Repo: "refusedguy/beacon", PR: 12, DuplicateOf: -1},
		{Repo: "refusedguy/beacon", PR: 12, DuplicateOf: 12},
	} {
		_, err := h.Close(context.Background(), in)
		if err == nil {
			t.Errorf("in %+v: want invalid input error", in)
			continue
		}
		var appErr *domain.AppError
		if !errors.As(err, &appErr) || appErr.Code != domain.CodeInvalidInput {
			t.Errorf("in %+v: error %v, want INVALID_INPUT", in, err)
		}
	}
	if closes, _, _ := cleanup.counts(); closes != 0 {
		t.Errorf("ClosePR calls = %d, want 0", closes)
	}
}

func TestPRCloseDupKeySeparatesDuplicates(t *testing.T) {
	a := prCloseDupKey("o/r", 1, 23)
	b := prCloseDupKey("o/r", 12, 3)
	if a == b {
		t.Errorf("keys collide: %q", a)
	}
}

func TestAttachPRCloseDupMarksToolDestructive(t *testing.T) {
	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "v1"}, nil)
	AttachPRCloseDup(server, prCloseDupTestHandler(newPRCloseDupCleanup(), prCloseDupCI{state: "OPEN"}, prCloseDupSnapshot{}))

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "v1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != PRCloseDupToolName {
			continue
		}
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("tool %s is not marked destructive", PRCloseDupToolName)
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %s has no output schema", PRCloseDupToolName)
		}
		return
	}
	t.Errorf("tool %s not attached", PRCloseDupToolName)
}
