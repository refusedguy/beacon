package tasks

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

var _ Backlog = (*fakeBacklog)(nil)
var _ Delegator = (*fakeDelegator)(nil)
var _ WatchRegistry = (*fakeWatches)(nil)
var _ AuditWriter = (*fakeAudit)(nil)

type fakeBacklog struct {
	candidates []Issue
	err        error
	calls      int
}

func (f *fakeBacklog) Candidates(context.Context) ([]Issue, error) {
	f.calls++
	return f.candidates, f.err
}

type fakeDelegator struct {
	calls []Issue
	byID  map[string]Delegation
	err   error
}

func (f *fakeDelegator) Delegate(_ context.Context, issue Issue) (Delegation, error) {
	f.calls = append(f.calls, issue)
	if f.err != nil {
		return Delegation{}, f.err
	}
	if d, ok := f.byID[issue.ID]; ok {
		return d, nil
	}
	d := Delegation{
		IssueID: issue.ID,
		TaskID:  "task-" + issue.ID,
		PR: PR{
			Repo:    "refusedguy/beacon",
			Number:  100 + len(f.byID),
			HeadSHA: "sha-" + issue.ID,
		},
	}
	if f.byID == nil {
		f.byID = make(map[string]Delegation)
	}
	f.byID[issue.ID] = d
	return d, nil
}

type fakeWatches struct {
	existing      map[string]WatchRef
	findCalls     int
	registerCalls int
	seq           int
	findErr       error
	registerErr   error
}

func watchKey(pr PR) string {
	return fmt.Sprintf("%s#%d#%s", pr.Repo, pr.Number, pr.HeadSHA)
}

func (f *fakeWatches) Find(_ context.Context, pr PR) (WatchRef, bool, error) {
	f.findCalls++
	if f.findErr != nil {
		return WatchRef{}, false, f.findErr
	}
	ref, ok := f.existing[watchKey(pr)]
	return ref, ok, nil
}

func (f *fakeWatches) Register(_ context.Context, pr PR, role string) (WatchRef, error) {
	f.registerCalls++
	if f.registerErr != nil {
		return WatchRef{}, f.registerErr
	}
	f.seq++
	ref := WatchRef{
		ID:      fmt.Sprintf("watch-%d", f.seq),
		Repo:    pr.Repo,
		PR:      pr.Number,
		HeadSHA: pr.HeadSHA,
		Role:    role,
	}
	if f.existing == nil {
		f.existing = make(map[string]WatchRef)
	}
	f.existing[watchKey(pr)] = ref
	return ref, nil
}

type fakeAudit struct {
	events []AuditEvent
	err    error
}

func (f *fakeAudit) Record(_ context.Context, event AuditEvent) error {
	f.events = append(f.events, event)
	return f.err
}

func newWorker(backlog *fakeBacklog, del *fakeDelegator, watches *fakeWatches, audit *fakeAudit) *Worker {
	return New(backlog, del, watches, audit)
}

func TestPlanTriageSelection(t *testing.T) {
	state := State{Handled: map[string]bool{"BS-1": true}}
	candidates := []Issue{
		{ID: "BS-1", Title: "already done"},
		{ID: "BS-2", Title: "next"},
		{ID: "  BS-2  ", Title: "duplicate with spaces"},
		{ID: "   ", Title: "blank id"},
		{ID: "BS-3", Title: "third"},
	}

	plan := PlanTriage(state, candidates)

	var got []string
	for _, issue := range plan.ToDelegate {
		got = append(got, issue.ID)
	}
	want := []string{"BS-2", "BS-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("to delegate = %v, want %v", got, want)
	}
	if len(plan.Skipped) != 3 {
		t.Fatalf("skipped = %d, want 3", len(plan.Skipped))
	}
}

func TestPlanTriageIsPure(t *testing.T) {
	state := State{Handled: map[string]bool{"BS-1": true}}
	candidates := []Issue{{ID: "BS-1"}, {ID: "BS-2"}}
	wantHandled := map[string]bool{"BS-1": true}
	wantCandidates := append([]Issue(nil), candidates...)

	PlanTriage(state, candidates)

	if !reflect.DeepEqual(state.Handled, wantHandled) {
		t.Fatalf("state mutated: got %v, want %v", state.Handled, wantHandled)
	}
	if !reflect.DeepEqual(candidates, wantCandidates) {
		t.Fatalf("candidates mutated: got %v, want %v", candidates, wantCandidates)
	}
}

func TestWorkerDelegatesAndWatches(t *testing.T) {
	backlog := &fakeBacklog{candidates: []Issue{{ID: "BS-10", Title: "triage me"}}}
	del := &fakeDelegator{}
	watches := &fakeWatches{}
	audit := &fakeAudit{}
	w := newWorker(backlog, del, watches, audit)

	res, err := w.Run(context.Background(), State{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(del.calls) != 1 || del.calls[0].ID != "BS-10" {
		t.Fatalf("delegate calls = %+v, want one call for BS-10", del.calls)
	}
	if watches.findCalls != 1 || watches.registerCalls != 1 {
		t.Fatalf("watch find=%d register=%d, want 1 and 1", watches.findCalls, watches.registerCalls)
	}
	if len(res.Delegated) != 1 || res.Delegated[0].TaskID != "task-BS-10" {
		t.Fatalf("delegated = %+v, want one task-BS-10", res.Delegated)
	}
	if len(res.Watches) != 1 {
		t.Fatalf("watches = %+v, want one", res.Watches)
	}
	ref := res.Watches[0]
	if ref.ID == "" || ref.Role != RoleExecutor || ref.PR != 100 || ref.HeadSHA != "sha-BS-10" {
		t.Fatalf("watch = %+v, want a registered executor watch on PR 100", ref)
	}
	if !res.State.Handled["BS-10"] {
		t.Fatalf("state = %+v, want BS-10 handled", res.State)
	}
	wantActions := []string{AuditDelegated, AuditWatchRegistered}
	var got []string
	for _, e := range audit.events {
		got = append(got, e.Action)
		if e.IssueID != "BS-10" {
			t.Fatalf("audit event = %+v, want issue BS-10", e)
		}
	}
	if !reflect.DeepEqual(got, wantActions) {
		t.Fatalf("audit actions = %v, want %v", got, wantActions)
	}
}

func TestWorkerRepeatNoDuplicate(t *testing.T) {
	backlog := &fakeBacklog{candidates: []Issue{{ID: "BS-20"}}}
	del := &fakeDelegator{}
	watches := &fakeWatches{}
	audit := &fakeAudit{}
	w := newWorker(backlog, del, watches, audit)

	first, err := w.Run(context.Background(), State{})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if !first.State.Handled["BS-20"] {
		t.Fatal("first pass did not mark BS-20 handled")
	}

	second, err := w.Run(context.Background(), first.State)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(del.calls) != 1 {
		t.Fatalf("delegate calls after repeat = %+v, want one", del.calls)
	}
	if watches.registerCalls != 1 {
		t.Fatalf("register calls after repeat = %d, want 1", watches.registerCalls)
	}
	if len(second.Delegated) != 0 || len(second.Watches) != 0 {
		t.Fatalf("second result = %+v, want no new work", second)
	}
	if second.Skipped != 1 {
		t.Fatalf("second skipped = %d, want 1", second.Skipped)
	}
	if len(audit.events) != 2 {
		t.Fatalf("audit events after repeat = %d, want 2 from the first pass only", len(audit.events))
	}
}

func TestWorkerEmptyBacklog(t *testing.T) {
	backlog := &fakeBacklog{}
	del := &fakeDelegator{}
	watches := &fakeWatches{}
	audit := &fakeAudit{}
	w := newWorker(backlog, del, watches, audit)

	res, err := w.Run(context.Background(), State{})
	if err != nil {
		t.Fatalf("Run on empty backlog: %v", err)
	}
	if backlog.calls != 1 {
		t.Fatalf("backlog calls = %d, want 1", backlog.calls)
	}
	if len(del.calls) != 0 || watches.findCalls != 0 || watches.registerCalls != 0 || len(audit.events) != 0 {
		t.Fatal("empty backlog called a side-effecting port")
	}
	if len(res.Delegated) != 0 || len(res.Watches) != 0 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want no work and no skips", res)
	}
}

func TestWorkerFindsExistingWatch(t *testing.T) {
	backlog := &fakeBacklog{candidates: []Issue{{ID: "BS-30"}}}
	del := &fakeDelegator{}
	watches := &fakeWatches{existing: map[string]WatchRef{
		watchKey(PR{Repo: "refusedguy/beacon", Number: 100, HeadSHA: "sha-BS-30"}): {
			ID: "watch-existing", Repo: "refusedguy/beacon", PR: 100, HeadSHA: "sha-BS-30",
		},
	}}
	audit := &fakeAudit{}
	w := newWorker(backlog, del, watches, audit)

	res, err := w.Run(context.Background(), State{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if watches.registerCalls != 0 {
		t.Fatalf("register calls = %d, want 0 for an existing watch", watches.registerCalls)
	}
	if len(res.Watches) != 1 || res.Watches[0].ID != "watch-existing" {
		t.Fatalf("watches = %+v, want the existing watch", res.Watches)
	}
	if len(audit.events) != 2 || audit.events[1].Action != AuditWatchFound {
		t.Fatalf("audit = %+v, want a watch_found event", audit.events)
	}
}

func TestWorkerNilPorts(t *testing.T) {
	w := New(nil, nil, nil, nil)
	if _, err := w.Run(context.Background(), State{}); !errors.Is(err, ErrNilPorts) {
		t.Fatalf("error = %v, want %v", err, ErrNilPorts)
	}
}

func TestWorkerDelegateError(t *testing.T) {
	boom := errors.New("delegation unavailable")
	backlog := &fakeBacklog{candidates: []Issue{{ID: "BS-40"}}}
	del := &fakeDelegator{err: boom}
	watches := &fakeWatches{}
	audit := &fakeAudit{}
	w := newWorker(backlog, del, watches, audit)

	res, err := w.Run(context.Background(), State{})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if watches.findCalls != 0 || len(audit.events) != 0 {
		t.Fatal("a failed delegation touched the watch or audit ports")
	}
	if res.State.Handled["BS-40"] {
		t.Fatal("state marks an issue handled after a failed delegation")
	}
}

func TestWorkerErrorAfterDelegationStillMarksHandled(t *testing.T) {
	backlog := &fakeBacklog{candidates: []Issue{{ID: "BS-50"}}}
	del := &fakeDelegator{}
	watches := &fakeWatches{}
	audit := &fakeAudit{err: errors.New("audit down")}
	w := newWorker(backlog, del, watches, audit)

	res, err := w.Run(context.Background(), State{})
	if err == nil {
		t.Fatal("Run with a failing audit port returned no error")
	}
	if !res.State.Handled["BS-50"] {
		t.Fatal("state does not mark the delegated issue handled after a later failure")
	}
	if len(res.Delegated) != 1 {
		t.Fatalf("delegated = %+v, want the completed delegation preserved", res.Delegated)
	}
	if len(del.calls) != 1 {
		t.Fatalf("delegate calls = %d, want 1", len(del.calls))
	}
}
