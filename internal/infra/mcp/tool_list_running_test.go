package mcp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// snapshotReaderFake serves a fixed snapshot. When path is set it reads that
// file on every call, so a test can compare the file before and after a tool
// call. The fake has no method that writes, just like SnapshotReader.
type snapshotReaderFake struct {
	snap  StateSnapshot
	err   error
	path  string
	calls int
}

func (f *snapshotReaderFake) Snapshot() (StateSnapshot, error) {
	f.calls++
	if f.path != "" {
		if _, err := os.ReadFile(f.path); err != nil {
			return StateSnapshot{}, err
		}
	}
	if f.err != nil {
		return StateSnapshot{}, f.err
	}
	return f.snap, nil
}

func sampleWatch(id string, verdict domain.Verdict, subscribers int) domain.Watch {
	subs := make([]domain.Subscriber, subscribers)
	for i := range subs {
		subs[i] = domain.Subscriber{ID: id + "-s" + string(rune('a'+i)), Role: domain.RoleExecutor}
	}
	return domain.Watch{
		ID:          id,
		Repo:        "refusedguy/beacon",
		PR:          1,
		Status:      domain.StatusWatching,
		Verdict:     verdict,
		Subscribers: subs,
	}
}

func sampleSnapshot() StateSnapshot {
	return StateSnapshot{
		ActiveWatches: []domain.Watch{
			sampleWatch("w1", domain.VerdictReady, 2),
			sampleWatch("w2", domain.VerdictWatching, 1),
			sampleWatch("w3", domain.VerdictReady, 0),
		},
		PointsThisHour:  42,
		Budget:          500,
		PersistSeq:      7,
		LastPersistErr:  "disk full",
		LoadErrors:      []string{"line 3: bad payload"},
		LadderPending:   2,
		LadderEscalated: 1,
		Version:         "4",
	}
}

func stateFingerprint(t *testing.T, path string) ([]byte, time.Time) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	return data, info.ModTime()
}

func TestListRunningCountsMatchWatches(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewListRunningHandler(reader)

	out := callListRunning(t, handler, ListRunningInput{})

	if out.Counts.Watches != len(out.Watches) {
		t.Errorf("counts.watches = %d, len(watches) = %d", out.Counts.Watches, len(out.Watches))
	}
	sum := 0
	for _, n := range out.Counts.ByVerdict {
		sum += n
	}
	if sum != out.Counts.Watches {
		t.Errorf("byVerdict sum = %d, counts.watches = %d", sum, out.Counts.Watches)
	}
	subs := 0
	for _, w := range out.Watches {
		subs += len(w.Subscribers)
	}
	if out.Counts.Subscribers != subs {
		t.Errorf("counts.subscribers = %d, want %d", out.Counts.Subscribers, subs)
	}
	if got := out.Counts.ByVerdict[string(domain.VerdictReady)]; got != 2 {
		t.Errorf("byVerdict[ready] = %d, want 2", got)
	}
}

func TestListRunningFiltersKeepCountsConsistent(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewListRunningHandler(reader)

	in := ListRunningInput{Filters: &ListRunningFilters{Verdict: string(domain.VerdictReady)}}
	out := callListRunning(t, handler, in)

	if len(out.Watches) != 2 {
		t.Fatalf("filtered watches = %d, want 2", len(out.Watches))
	}
	for _, w := range out.Watches {
		if w.Verdict != domain.VerdictReady {
			t.Errorf("watch %s has verdict %s, want ready", w.ID, w.Verdict)
		}
	}
	if out.Counts.Watches != len(out.Watches) {
		t.Errorf("counts.watches = %d, len(watches) = %d", out.Counts.Watches, len(out.Watches))
	}
	if out.Counts.ByVerdict[string(domain.VerdictWatching)] != 0 {
		t.Errorf("byVerdict still includes watching watches: %v", out.Counts.ByVerdict)
	}
	if out.Counts.Subscribers != 2 {
		t.Errorf("counts.subscribers = %d, want 2", out.Counts.Subscribers)
	}
}

func TestListRunningDoesNotWriteState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"seq":1}`), 0600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	before, beforeMod := stateFingerprint(t, path)

	reader := &snapshotReaderFake{path: path, snap: sampleSnapshot()}
	handler := NewListRunningHandler(reader)
	callListRunning(t, handler, ListRunningInput{})
	callListRunning(t, handler, ListRunningInput{})

	after, afterMod := stateFingerprint(t, path)
	if !bytes.Equal(before, after) {
		t.Errorf("state file bytes changed: %q -> %q", before, after)
	}
	if !beforeMod.Equal(afterMod) {
		t.Errorf("state file mtime changed: %v -> %v", beforeMod, afterMod)
	}
	if reader.calls != 2 {
		t.Errorf("snapshot reads = %d, want 2", reader.calls)
	}
}

func TestListRunningRepeatable(t *testing.T) {
	reader := &snapshotReaderFake{snap: sampleSnapshot()}
	handler := NewListRunningHandler(reader)

	first := callListRunning(t, handler, ListRunningInput{})
	second := callListRunning(t, handler, ListRunningInput{})

	if !reflect.DeepEqual(first, second) {
		t.Errorf("two calls with unchanged state differ:\n%#v\n%#v", first, second)
	}
}

func TestListRunningPropagatesReadError(t *testing.T) {
	want := errors.New("store unavailable")
	reader := &snapshotReaderFake{err: want}
	handler := NewListRunningHandler(reader)

	_, _, err := handler.Handle(context.Background(), nil, ListRunningInput{})
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want it to wrap %v", err, want)
	}
}

func callListRunning(t *testing.T, handler *ListRunningHandler, in ListRunningInput) ListRunningOutput {
	t.Helper()
	result, out, err := handler.Handle(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("beacon_list_running: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("beacon_list_running returned an error result: %#v", result)
	}
	return out
}

// watchWithSubscriber builds a watch subscribed by one agent session. The
// subscriber Target is the sessionID a SessionLiveness row is keyed by.
func watchWithSubscriber(id, sessionID string) domain.Watch {
	return domain.Watch{
		ID:      id,
		Repo:    "refusedguy/beacon",
		PR:      1,
		Status:  domain.StatusWatching,
		Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{
			{ID: id + "-sub", Target: sessionID, Role: domain.RoleExecutor},
		},
	}
}

// withProcessAlive replaces the PID probe for one test and records the PIDs it
// was asked about. The default probe uses syscall.Kill(pid, 0) and starts no
// process; this seam keeps the tests independent of the machine's process
// table.
func withProcessAlive(t *testing.T, alive map[int]bool) *[]int {
	t.Helper()
	original := processAlive
	calls := new([]int)
	processAlive = func(pid int) bool {
		*calls = append(*calls, pid)
		return alive[pid]
	}
	t.Cleanup(func() { processAlive = original })
	return calls
}

func writeArtifact(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

func TestListRunningDeadPIDTurnsWatchIdle(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const deadPID = 424242
	calls := withProcessAlive(t, map[int]bool{deadPID: false})

	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
		Sessions: []SessionLiveness{{
			SessionID: "s1",
			Artifacts: []string{artifact},
			PID:       deadPID,
		}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})

	if len(out.Watches) != 1 {
		t.Fatalf("watches = %d, want 1", len(out.Watches))
	}
	if got := out.Watches[0].Status; got != domain.Status(StatusIdle) {
		t.Errorf("watch status = %q, want %q", got, StatusIdle)
	}
	if out.Status != StatusIdle {
		t.Errorf("aggregate status = %q, want %q", out.Status, StatusIdle)
	}
	if len(out.Events) != 1 || out.Events[0].Status != StatusIdle {
		t.Errorf("events = %#v, want one idle event", out.Events)
	}
	if len(*calls) == 0 {
		t.Fatal("PID probe was not called")
	}
	for _, pid := range *calls {
		if pid != deadPID {
			t.Errorf("PID probe called with %d, want %d", pid, deadPID)
		}
	}
}

func TestListRunningLivePIDKeepsStatus(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const livePID = 424243
	withProcessAlive(t, map[int]bool{livePID: true})

	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
		Sessions:      []SessionLiveness{{SessionID: "s1", Artifacts: []string{artifact}, PID: livePID}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})

	if got := out.Watches[0].Status; got != domain.StatusWatching {
		t.Errorf("watch status = %q, want watching", got)
	}
	if out.Status != string(domain.StatusWatching) {
		t.Errorf("aggregate status = %q, want watching", out.Status)
	}
}

func TestListRunningMissingArtifactTurnsIdle(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.json")
	const livePID = 424244
	withProcessAlive(t, map[int]bool{livePID: true})

	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
		Sessions:      []SessionLiveness{{SessionID: "s1", Artifacts: []string{missing}, PID: livePID}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})

	if got := out.Watches[0].Status; got != domain.Status(StatusIdle) {
		t.Errorf("watch status = %q, want %q", got, StatusIdle)
	}
}

func TestListRunningUnknownSessionKeepsStatus(t *testing.T) {
	// A watch with a subscriber but no SessionLiveness row is left alone: the
	// absence of liveness data is not evidence of a dead session.
	withProcessAlive(t, map[int]bool{})

	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "unknown")},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})

	if got := out.Watches[0].Status; got != domain.StatusWatching {
		t.Errorf("watch status = %q, want watching", got)
	}
}

func TestListRunningClosedWatchStaysClosedWithDeadPID(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const deadPID = 424245
	withProcessAlive(t, map[int]bool{deadPID: false})

	w := watchWithSubscriber("w1", "s1")
	w.Status = domain.StatusClosed
	w.Verdict = domain.VerdictMerged
	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{w},
		Sessions:      []SessionLiveness{{SessionID: "s1", Artifacts: []string{artifact}, PID: deadPID}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})

	if got := out.Watches[0].Status; got != domain.StatusClosed {
		t.Errorf("watch status = %q, want closed", got)
	}
	if out.Status != string(domain.StatusClosed) {
		t.Errorf("aggregate status = %q, want closed", out.Status)
	}
}

func TestProcessAliveUsesSignalZero(t *testing.T) {
	// The default probe asks the kernel about one PID with signal 0. It must
	// see this process as alive and a PID far above Linux pid_max as dead,
	// without enumerating processes or starting ps.
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(self) = false, want true")
	}
	const impossiblePID = 1 << 30
	if processAlive(impossiblePID) {
		t.Errorf("processAlive(%d) = true, want false", impossiblePID)
	}
}

// withCurrentTime freezes the clock used to age the activity and heartbeat
// signals (ADR 0042 §2) for one test.
func withCurrentTime(t *testing.T, now time.Time) {
	t.Helper()
	original := currentTime
	currentTime = func() time.Time { return now }
	t.Cleanup(func() { currentTime = original })
}

func TestSessionDeadReadsThreeSignalsTogether(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	withCurrentTime(t, now)
	const pid = 424246
	alive := map[int]bool{pid: true}
	withProcessAlive(t, alive)

	staleActivity := now.Add(-10 * time.Minute)  // older than activityStaleAfter
	staleHeartbeat := now.Add(-10 * time.Minute) // older than heartbeatStaleAfter

	cases := []struct {
		name      string
		alive     bool
		activity  time.Time
		heartbeat time.Time
		wantDead  bool
	}{
		{"dead pid is the quick sign", false, now, now, true},
		{"dead pid with stale signals", false, staleActivity, staleHeartbeat, true},
		{"live pid with both stale", true, staleActivity, staleHeartbeat, true},
		{"live pid, stale activity only", true, staleActivity, now, false},
		{"live pid, stale heartbeat only", true, now, staleHeartbeat, false},
		{"live pid with both fresh", true, now, now, false},
		{"live pid with unknown signals", true, time.Time{}, time.Time{}, false},
		{"live pid, stale activity with unknown heartbeat", true, staleActivity, time.Time{}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alive[pid] = tc.alive
			s := SessionLiveness{
				SessionID:     "s1",
				PID:           pid,
				LastActivity:  tc.activity,
				LastHeartbeat: tc.heartbeat,
			}
			if got := sessionDead(s); got != tc.wantDead {
				t.Errorf("sessionDead = %v, want %v", got, tc.wantDead)
			}
		})
	}
}

func TestSessionLiveUnknownSessionWithTimeSignalsIsLeftAlone(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	withCurrentTime(t, now)
	// No PID and no artifacts: even stale activity and heartbeat must not
	// change the "unknown session" default, which stays live.
	s := SessionLiveness{
		SessionID:     "s1",
		LastActivity:  now.Add(-time.Hour),
		LastHeartbeat: now.Add(-time.Hour),
	}
	if !sessionLive(s) {
		t.Error("sessionLive = false for a session with only time signals, want true")
	}
}

func TestListRunningLivePIDStalledTaskTurnsIdle(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const livePID = 424247
	withProcessAlive(t, map[int]bool{livePID: true})
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	withCurrentTime(t, now)

	// The PID is alive and the artifact is present, but both activity and
	// heartbeat are stale. T3.44 alone would call this session live; ADR 0042
	// §2 calls the task dead from the three signals together.
	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
		Sessions: []SessionLiveness{{
			SessionID:     "s1",
			Artifacts:     []string{artifact},
			PID:           livePID,
			LastActivity:  now.Add(-10 * time.Minute),
			LastHeartbeat: now.Add(-10 * time.Minute),
		}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})

	if got := out.Watches[0].Status; got != domain.Status(StatusIdle) {
		t.Errorf("watch status = %q, want %q", got, StatusIdle)
	}
	if out.Status != StatusIdle {
		t.Errorf("aggregate status = %q, want %q", out.Status, StatusIdle)
	}
	if len(out.Events) != 1 || out.Events[0].Status != StatusIdle {
		t.Errorf("events = %#v, want one idle event", out.Events)
	}
}

func TestListRunningSingleStaleSignalKeepsStatus(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const livePID = 424248
	withProcessAlive(t, map[int]bool{livePID: true})
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	withCurrentTime(t, now)

	cases := []struct {
		name      string
		activity  time.Time
		heartbeat time.Time
	}{
		{"stale activity only", now.Add(-10 * time.Minute), now},
		{"stale heartbeat only", now, now.Add(-10 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &snapshotReaderFake{snap: StateSnapshot{
				ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
				Sessions: []SessionLiveness{{
					SessionID:     "s1",
					Artifacts:     []string{artifact},
					PID:           livePID,
					LastActivity:  tc.activity,
					LastHeartbeat: tc.heartbeat,
				}},
			}}
			out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})
			if got := out.Watches[0].Status; got != domain.StatusWatching {
				t.Errorf("watch status = %q, want watching", got)
			}
		})
	}
}

func TestListRunningDeadPIDBeatsFreshSignals(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const deadPID = 424249
	withProcessAlive(t, map[int]bool{deadPID: false})
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	withCurrentTime(t, now)

	// The two new signals are fresh, but a dead PID stays the quick sign from
	// T3.44 and still turns the watch idle.
	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
		Sessions: []SessionLiveness{{
			SessionID:     "s1",
			Artifacts:     []string{artifact},
			PID:           deadPID,
			LastActivity:  now,
			LastHeartbeat: now,
		}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})

	if got := out.Watches[0].Status; got != domain.Status(StatusIdle) {
		t.Errorf("watch status = %q, want %q", got, StatusIdle)
	}
}
