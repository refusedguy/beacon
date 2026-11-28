package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

type waitlistKey struct {
	path   core.ScopePath
	taskID core.TaskID
}

type waitlistCall struct {
	taskID core.TaskID
	paths  []core.ScopePath
	since  int64
}

type fakeWaitlist struct {
	entries map[waitlistKey]int64
	calls   []waitlistCall
	fail    error
}

func newFakeWaitlist() *fakeWaitlist {
	return &fakeWaitlist{entries: make(map[waitlistKey]int64)}
}

func (f *fakeWaitlist) AddWaitEntry(_ context.Context, taskID core.TaskID, paths []core.ScopePath, since int64) error {
	f.calls = append(f.calls, waitlistCall{
		taskID: taskID,
		paths:  append([]core.ScopePath(nil), paths...),
		since:  since,
	})
	if f.fail != nil {
		return f.fail
	}
	for _, p := range paths {
		f.entries[waitlistKey{path: p, taskID: taskID}] = since
	}
	return nil
}

// longestWaiter follows the sqlite store contract: the smallest timestamp
// waited longest, and ties break on task ID then path.
func (f *fakeWaitlist) longestWaiter(paths []core.ScopePath) *waitlistCall {
	var oldest *waitlistCall
	for key, since := range f.entries {
		if !slices.Contains(paths, key.path) {
			continue
		}
		if oldest != nil && !(since < oldest.since ||
			(since == oldest.since && key.taskID < oldest.taskID) ||
			(since == oldest.since && key.taskID == oldest.taskID && key.path < oldest.paths[0])) {
			continue
		}
		oldest = &waitlistCall{taskID: key.taskID, paths: []core.ScopePath{key.path}, since: since}
	}
	return oldest
}

func TestPlannerQueuesBothScopeConflictKinds(t *testing.T) {
	first := core.ScopeFile("internal/app/first.go")
	second := core.ScopeFile("internal/app/second.go")
	tasks := []core.Task{
		{ID: "T-first", Status: core.TaskAvailable, Scope: []core.ScopePath{first}},
		{ID: "T-active", Status: core.TaskAvailable, Scope: []core.ScopePath{second}},
		{ID: "T-selected", Status: core.TaskAvailable, Scope: []core.ScopePath{first}},
	}
	active := []*core.Run{{
		ID:     "r-holder",
		TaskID: "T-holder",
		State:  core.RunRunning,
		Scope:  []core.ScopePath{second},
	}}

	wantChosen, wantSkipped, err := NewPlanner(nil).Select(tasks, active, 5, false)
	if err != nil {
		t.Fatalf("baseline Select: %v", err)
	}
	waitlist := newFakeWaitlist()
	p := GreedyPlanner{
		Overlap:  core.PathsAndDependencies,
		Waitlist: waitlist,
		now:      func() int64 { return 100 },
	}
	chosen, skipped, err := p.Select(tasks, active, 5, false)
	if err != nil {
		t.Fatalf("Select with waitlist: %v", err)
	}
	if !reflect.DeepEqual(chosen, wantChosen) || !reflect.DeepEqual(skipped, wantSkipped) {
		t.Fatalf("Select changed output:\n got chosen %+v, skipped %+v\nwant chosen %+v, skipped %+v",
			chosen, skipped, wantChosen, wantSkipped)
	}

	wantEntries := map[waitlistKey]int64{
		{path: second, taskID: "T-active"}:  100,
		{path: first, taskID: "T-selected"}: 100,
	}
	if !reflect.DeepEqual(waitlist.entries, wantEntries) {
		t.Fatalf("wait entries = %+v, want %+v", waitlist.entries, wantEntries)
	}
}

func TestPlannerRepeatConflictRefreshesOneWaitEntry(t *testing.T) {
	path := core.ScopeFile("internal/app/shared.go")
	tasks := []core.Task{
		{ID: "T-holder", Status: core.TaskAvailable, Scope: []core.ScopePath{path}},
		{ID: "T-waiter", Status: core.TaskAvailable, Scope: []core.ScopePath{path}},
	}
	waitlist := newFakeWaitlist()
	now := int64(100)
	p := GreedyPlanner{
		Overlap:  core.PathsAndDependencies,
		Waitlist: waitlist,
		now:      func() int64 { return now },
	}

	if _, _, err := p.Select(tasks, nil, 5, false); err != nil {
		t.Fatalf("first Select: %v", err)
	}
	now = 200
	if _, _, err := p.Select(tasks, nil, 5, false); err != nil {
		t.Fatalf("second Select: %v", err)
	}

	key := waitlistKey{path: path, taskID: "T-waiter"}
	if len(waitlist.entries) != 1 {
		t.Fatalf("wait entries = %+v, want one row", waitlist.entries)
	}
	if got := waitlist.entries[key]; got != 200 {
		t.Fatalf("waiting_since = %d, want 200", got)
	}
}

func TestPlannerWaitAgingFollowsWaitingSince(t *testing.T) {
	path := core.ScopeFile("internal/app/shared.go")
	holder := core.Task{ID: "T-holder", Status: core.TaskAvailable, Scope: []core.ScopePath{path}}
	waitlist := newFakeWaitlist()
	now := int64(100)
	p := GreedyPlanner{
		Overlap:  core.PathsAndDependencies,
		Waitlist: waitlist,
		now:      func() int64 { return now },
	}

	// Both waiters have the same priority, so ID order would put T-a first.
	// Their wait order must come from waiting_since instead.
	if _, _, err := p.Select([]core.Task{holder, {
		ID: "T-z", Status: core.TaskAvailable, Scope: []core.ScopePath{path},
	}}, nil, 5, false); err != nil {
		t.Fatalf("first Select: %v", err)
	}
	now = 200
	if _, _, err := p.Select([]core.Task{holder, {
		ID: "T-a", Status: core.TaskAvailable, Scope: []core.ScopePath{path},
	}}, nil, 5, false); err != nil {
		t.Fatalf("second Select: %v", err)
	}

	oldest := waitlist.longestWaiter([]core.ScopePath{path})
	if oldest == nil || oldest.taskID != "T-z" || oldest.since != 100 {
		t.Fatalf("longest waiter = %+v, want T-z at 100", oldest)
	}
}

func TestPlannerWaitWriteFailureKeepsSkipSet(t *testing.T) {
	path := core.ScopeFile("internal/app/shared.go")
	tasks := []core.Task{
		{ID: "T-holder", Status: core.TaskAvailable, Scope: []core.ScopePath{path}},
		{ID: "T-waiter", Status: core.TaskAvailable, Scope: []core.ScopePath{path}},
	}
	waitlist := newFakeWaitlist()
	waitlist.fail = errors.New("store unavailable")

	wantChosen, wantSkipped, err := NewPlanner(nil).Select(tasks, nil, 5, false)
	if err != nil {
		t.Fatalf("baseline Select: %v", err)
	}
	chosen, skipped, err := NewPlanner(nil, waitlist).Select(tasks, nil, 5, false)
	if err != nil {
		t.Fatalf("Select with failing waitlist: %v", err)
	}
	if !reflect.DeepEqual(chosen, wantChosen) || !reflect.DeepEqual(skipped, wantSkipped) {
		t.Fatalf("failed write changed output:\n got chosen %+v, skipped %+v\nwant chosen %+v, skipped %+v",
			chosen, skipped, wantChosen, wantSkipped)
	}
}
