package app

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type callLog struct{ items []string }

func (l *callLog) add(entry string) {
	if l != nil {
		l.items = append(l.items, entry)
	}
}

type markCall struct {
	id     core.TaskID
	path   string
	status string
}

type commitCall struct {
	pathspec string
	message  string
}

type fakeSource struct {
	tasks       []core.Task
	claimed     []core.TaskID
	complete    []core.TaskID
	completeErr error
	marked      []markCall
	removed     []core.TaskID
	log         *callLog
}

func (f *fakeSource) ListAvailable(context.Context) ([]core.Task, error) { return f.tasks, nil }
func (f *fakeSource) Claim(_ context.Context, id core.TaskID) error {
	f.claimed = append(f.claimed, id)
	return nil
}
func (f *fakeSource) Complete(_ context.Context, id core.TaskID) error {
	if f.completeErr != nil {
		return f.completeErr
	}
	f.complete = append(f.complete, id)
	return nil
}
func (f *fakeSource) MarkInWorkspace(_ context.Context, id core.TaskID, path, status string) error {
	f.marked = append(f.marked, markCall{id: id, path: path, status: status})
	f.log.add("mark:" + status)
	return nil
}
func (f *fakeSource) Remove(_ context.Context, id core.TaskID) error {
	f.removed = append(f.removed, id)
	f.log.add("remove:" + string(id))
	return nil
}

type fakeStore struct {
	runs       map[core.RunID]*core.Run
	deliveries map[string]bool
	approvals  map[string]bool
	tasks      []core.Task
	taskStatus map[string]string
	incidents  map[string]*core.BaseIncident
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		runs: map[core.RunID]*core.Run{}, deliveries: map[string]bool{},
		approvals: map[string]bool{}, incidents: map[string]*core.BaseIncident{},
		taskStatus: map[string]string{},
	}
}

func (f *fakeStore) Save(_ context.Context, r *core.Run) error {
	copy := *r
	f.runs[r.ID] = &copy
	return nil
}
func (f *fakeStore) Get(_ context.Context, id core.RunID) (*core.Run, error) {
	r, ok := f.runs[id]
	if !ok {
		return nil, nil
	}
	return r, nil
}
func (f *fakeStore) FindByPR(_ context.Context, pr int) (*core.Run, error) {
	for _, r := range f.runs {
		if r.PRNumber == pr && !r.State.Terminal() {
			return r, nil
		}
	}
	return nil, nil
}
func (f *fakeStore) FindActiveByTask(_ context.Context, id core.TaskID) (*core.Run, error) {
	for _, r := range f.runs {
		if r.TaskID == id && !r.State.Terminal() {
			return r, nil
		}
	}
	return nil, nil
}
func (f *fakeStore) ListActive(context.Context) ([]*core.Run, error) {
	var out []*core.Run
	for _, r := range f.runs {
		if !r.State.Terminal() {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeStore) ListRecent(_ context.Context, limit int) ([]*core.Run, error) {
	var out []*core.Run
	for _, r := range f.runs {
		if r.State == core.RunMerged || r.State == core.RunClosed || r.State == core.RunState("dropped") {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeStore) ListByIncident(_ context.Context, id string) ([]*core.Run, error) {
	var out []*core.Run
	for _, r := range f.runs {
		if r.BlockedIncidentID == id {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeStore) ClaimDelivery(_ context.Context, key string) (bool, error) {
	if f.deliveries[key] {
		return false, nil
	}
	f.deliveries[key] = true
	return true, nil
}
func (f *fakeStore) ReleaseDelivery(_ context.Context, key string) error {
	delete(f.deliveries, key)
	return nil
}
func (f *fakeStore) SetApproval(_ context.Context, id core.RunID, sha string) error {
	f.approvals[string(id)+":"+sha] = true
	return nil
}
func (f *fakeStore) Approved(_ context.Context, id core.RunID, sha string) (bool, error) {
	return f.approvals[string(id)+":"+sha], nil
}
func (f *fakeStore) SyncTasks(_ context.Context, tasks []core.Task) error {
	f.tasks = tasks
	return nil
}
func (f *fakeStore) ListReadyTasks(context.Context) ([]core.Task, error) {
	return f.tasks, nil
}
func (f *fakeStore) SetTaskStatus(_ context.Context, taskID core.TaskID, status string) error {
	f.taskStatus[string(taskID)] = status
	return nil
}
func (f *fakeStore) CreateIncident(_ context.Context, in *core.BaseIncident) (*core.BaseIncident, error) {
	if existing := f.incidents[in.Signature]; existing != nil {
		return existing, nil
	}
	copy := *in
	copy.ID = "inc-1"
	if copy.Status == "" {
		copy.Status = "open"
	}
	f.incidents[in.Signature] = &copy
	return &copy, nil
}
func (f *fakeStore) GetIncident(_ context.Context, id string) (*core.BaseIncident, error) {
	for _, in := range f.incidents {
		if in.ID == id {
			return in, nil
		}
	}
	return nil, nil
}
func (f *fakeStore) FindOpenIncident(_ context.Context, _, _, signature string) (*core.BaseIncident, error) {
	return f.incidents[signature], nil
}
func (f *fakeStore) ListOpenIncidents(context.Context) ([]*core.BaseIncident, error) {
	var out []*core.BaseIncident
	for _, in := range f.incidents {
		if in.Status == "open" {
			out = append(out, in)
		}
	}
	return out, nil
}
func (f *fakeStore) UpdateIncident(_ context.Context, in *core.BaseIncident) error {
	f.incidents[in.Signature] = in
	return nil
}

type fakeWorkspace struct {
	removed       int
	commits       []commitCall
	log           *callLog
	pushes        int
	rebases       []string
	rejectFirst   bool
	rebaseBaseErr error
}

func (f *fakeWorkspace) Create(_ context.Context, t core.Task) (core.Workspace, error) {
	return core.Workspace{Path: "/ws/" + string(t.ID), Branch: "beacon/" + string(t.ID)}, nil
}
func (f *fakeWorkspace) Clean(context.Context, core.Workspace) (bool, error) { return true, nil }
func (f *fakeWorkspace) Remove(_ context.Context, _ core.Workspace) error {
	f.removed++
	return nil
}
func (f *fakeWorkspace) HasChanges(context.Context, core.Workspace, string) (bool, error) {
	return false, nil
}
func (f *fakeWorkspace) CommitPath(_ context.Context, _ core.Workspace, pathspec, message string) error {
	f.commits = append(f.commits, commitCall{pathspec: pathspec, message: message})
	f.log.add("commit:" + pathspec)
	f.pushes++
	if f.rejectFirst && f.pushes == 1 {
		return &NonFastForwardError{Err: errors.New("remote ref advanced")}
	}
	return nil
}

func (f *fakeWorkspace) RebaseBase(_ context.Context, _ core.Workspace, base string) error {
	if f.rebaseBaseErr != nil {
		return f.rebaseBaseErr
	}
	f.rebases = append(f.rebases, base)
	f.log.add("rebase:" + base)
	return nil
}

type parentNotification struct {
	sessionID string
	text      string
}

type fakeHarness struct {
	sessions      []string
	prompts       []string
	wakes         []core.WakeContext
	stops         int
	notifications []parentNotification
	log           *callLog
	events        chan core.HarnessEvent
}

func (f *fakeHarness) Spawn(_ context.Context, req core.SpawnRequest) (core.Session, error) {
	f.sessions = append(f.sessions, string(req.RunID))
	f.prompts = append(f.prompts, req.Prompt)
	return core.Session{ID: "sess-" + string(req.RunID)}, nil
}
func (f *fakeHarness) Wake(_ context.Context, _ string, w core.WakeContext) error {
	f.wakes = append(f.wakes, w)
	return nil
}
func (f *fakeHarness) Stop(context.Context, string) error { f.stops++; return nil }
func (f *fakeHarness) IsAlive(context.Context, string) (bool, error) {
	return true, nil
}
func (f *fakeHarness) NotifyParent(_ context.Context, sessionID, text string) error {
	f.notifications = append(f.notifications, parentNotification{sessionID: sessionID, text: text})
	f.log.add("notify_parent")
	return nil
}
func (f *fakeHarness) Events() <-chan core.HarnessEvent {
	if f.events == nil {
		f.events = make(chan core.HarnessEvent, 16)
	}
	return f.events
}

type openedPR struct {
	head  string
	base  string
	title string
}

type fakeSCM struct {
	obs        core.Observation
	obsByPR    map[int]core.Observation
	observeSeq []core.Observation
	observeN   int
	base       core.BaseStatus
	rebases    int
	merges     int
	mergeSHA   string
	foundPR    int
	openedPRs  []openedPR
	updateErr  error
	log        *callLog
}

func (f *fakeSCM) Observe(_ context.Context, pr int) (core.Observation, error) {
	f.log.add("observe")
	if len(f.observeSeq) > 0 {
		i := f.observeN
		if i >= len(f.observeSeq) {
			i = len(f.observeSeq) - 1
		}
		f.observeN++
		return f.observeSeq[i], nil
	}
	if o, ok := f.obsByPR[pr]; ok {
		return o, nil
	}
	return f.obs, nil
}
func (f *fakeSCM) UpdateBranch(_ context.Context, _ int, sha string) (string, error) {
	if f.updateErr != nil {
		return "", f.updateErr
	}
	f.rebases++
	return "new-" + sha, nil
}
func (f *fakeSCM) Merge(_ context.Context, _ int, sha string, _ string) error {
	f.mergeSHA = sha
	f.merges++
	f.log.add("merge")
	return nil
}
func (f *fakeSCM) BaseBranchStatus(context.Context, string) (core.BaseStatus, error) {
	return f.base, nil
}
func (f *fakeSCM) FindOpenPRByBranch(context.Context, string) (int, error) {
	return f.foundPR, nil
}
func (f *fakeSCM) OpenPR(_ context.Context, head, base, title string) (int, error) {
	f.log.add("open_pr")
	f.openedPRs = append(f.openedPRs, openedPR{head: head, base: base, title: title})
	return f.foundPR, nil
}

type fakePolicy struct{ decision core.MergeDecision }

func (f fakePolicy) Evaluate(core.Observation) core.MergeDecision { return f.decision }

type fakeNotifier struct{ count int }

func (f *fakeNotifier) Notify(context.Context, core.Notification) error { f.count++; return nil }

func makeRun(id core.RunID, pr int, state core.RunState) *core.Run {
	r := core.NewRun(id, "T-1", time.Now().UTC())
	r.State = state
	r.PRNumber = pr
	r.SessionID = "sess"
	r.WorkspacePath = "/ws/T-1"
	r.HeadSHA = "sha1"
	return r
}
