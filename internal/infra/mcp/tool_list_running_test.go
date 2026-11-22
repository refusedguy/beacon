package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

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

// TestListRunningKeepsBlockedAndOrphanedOnRadar proves a blocked Watch in
// LiveWatches is never dropped just because it left ActiveWatches. The compact
// events carry the blocker link and every subscriber phase.
func TestListRunningKeepsBlockedAndOrphanedOnRadar(t *testing.T) {
	blocked := domain.Watch{
		ID:        "watch-blocked",
		Status:    domain.StatusBlocked,
		Verdict:   domain.VerdictBlocked,
		BlockerID: "blk_main_red",
		Blocked:   &domain.BlockedInfo{Reason: "not_mine", FailedChecks: []string{"ci/test"}},
		Subscribers: []domain.Subscriber{
			{ID: "ses_orphan", Role: domain.RoleExecutor, Phase: domain.WorkerPhaseOrphaned},
			{ID: "ses_fixer", Role: domain.RoleFixer, Phase: domain.WorkerPhaseBlocked},
		},
	}
	snap := sampleSnapshot()
	snap.ActiveWatches = []domain.Watch{sampleWatch("w1", domain.VerdictReady, 0)}
	snap.LiveWatches = append(append([]domain.Watch(nil), snap.ActiveWatches...), blocked)

	out := callListRunning(t, NewListRunningHandler(&snapshotReaderFake{snap: snap}), ListRunningInput{})

	if out.Counts.Watches != 2 {
		t.Fatalf("counts.watches = %d, want 2 (blocked Watch must stay on radar)", out.Counts.Watches)
	}
	var found *WatchOutput
	for i := range out.Watches {
		if out.Watches[i].ID == blocked.ID {
			found = &out.Watches[i]
		}
	}
	if found == nil {
		t.Fatal("blocked Watch missing from watches")
	}
	if found.Status != domain.StatusBlocked || found.BlockerID != "blk_main_red" || found.Blocked == nil {
		t.Errorf("blocked Watch projection = %+v", found)
	}
	var event *WatchEvent
	for i := range out.Events {
		if out.Events[i].WatchID == blocked.ID {
			event = &out.Events[i]
		}
	}
	if event == nil {
		t.Fatal("blocked Watch missing from events")
	}
	if !event.Blocked || event.BlockerID != "blk_main_red" {
		t.Errorf("blocked event = %+v", event)
	}
	wantPhases := map[string]string{"ses_orphan": "orphaned", "ses_fixer": "blocked"}
	if !reflect.DeepEqual(event.Phases, wantPhases) {
		t.Errorf("event phases = %v, want %v", event.Phases, wantPhases)
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
	if sessionDead(s) {
		t.Error("sessionDead = true without a recorded PID, want false")
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

// withOwnerObserved replaces the /proc and boot-id seams for one test, so the
// recorded owner identity is compared with fixed observed values.
func withOwnerObserved(t *testing.T, startTime uint64, bootID string) {
	t.Helper()
	originalStart, originalBoot := readProcStartTime, readBootID
	readProcStartTime = func(int) uint64 { return startTime }
	readBootID = func() string { return bootID }
	t.Cleanup(func() {
		readProcStartTime = originalStart
		readBootID = originalBoot
	})
}

func TestParseProcStatStartTime(t *testing.T) {
	// Field 22 is starttime. Field 2 (comm) may contain spaces and parens, so
	// the parse must start after the last ')'.
	cases := []struct {
		name string
		stat string
		want uint64
		ok   bool
	}{
		{
			name: "simple comm",
			stat: "1 (init) S " + strings.Repeat("0 ", 18) + "123 0 0 0 0",
			want: 123,
			ok:   true,
		},
		{
			name: "comm with spaces and parens",
			stat: "42 (my (agent) proc) R 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 456 789 0 0",
			want: 456,
			ok:   true,
		},
		{name: "no closing paren", stat: "1 (init S 0 0", ok: false},
		{name: "too few fields", stat: "1 (init) S 0 0 0", ok: false},
		{
			name: "non numeric starttime",
			stat: "1 (init) S " + strings.Repeat("0 ", 18) + "X 0 0 0 0",
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcStatStartTime(tc.stat)
			if ok != tc.ok {
				t.Fatalf("parseProcStatStartTime ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("parseProcStatStartTime = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOwnerVerdictDecisionTable(t *testing.T) {
	const start = uint64(900)
	const boot = "boot-a"
	recorded := OwnerIdentity{PID: 100, StartTime: start, BootID: boot}

	cases := []struct {
		name     string
		recorded OwnerIdentity
		observed OwnerIdentity
		alive    bool
		want     OwnerVerdict
	}{
		{"verified live", recorded, OwnerIdentity{PID: 100, StartTime: start, BootID: boot}, true, OwnerVerdictLive},
		{"reboot is dead", recorded, OwnerIdentity{PID: 100, StartTime: start, BootID: "boot-b"}, true, OwnerVerdictDead},
		{"dead pid is dead", recorded, OwnerIdentity{PID: 100, StartTime: start, BootID: boot}, false, OwnerVerdictDead},
		{"pid reused is dead", recorded, OwnerIdentity{PID: 100, StartTime: 901, BootID: boot}, true, OwnerVerdictDead},
		{"missing recorded pid", OwnerIdentity{StartTime: start, BootID: boot}, OwnerIdentity{PID: 100, StartTime: start, BootID: boot}, true, OwnerVerdictUnverifiable},
		{"missing recorded start", OwnerIdentity{PID: 100, BootID: boot}, OwnerIdentity{PID: 100, StartTime: start, BootID: boot}, true, OwnerVerdictUnverifiable},
		{"missing recorded boot", OwnerIdentity{PID: 100, StartTime: start}, OwnerIdentity{PID: 100, StartTime: start, BootID: boot}, true, OwnerVerdictUnverifiable},
		{"missing observed start", recorded, OwnerIdentity{PID: 100, BootID: boot}, true, OwnerVerdictUnverifiable},
		{"missing observed boot", recorded, OwnerIdentity{PID: 100, StartTime: start}, true, OwnerVerdictUnverifiable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownerVerdict(tc.recorded, tc.observed, tc.alive); got != tc.want {
				t.Errorf("ownerVerdict = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnverifiableOwnerIsLiveAndRefusesReclaim(t *testing.T) {
	for _, v := range []OwnerVerdict{OwnerVerdictLive, OwnerVerdictUnverifiable} {
		if !ownerIsLive(v) {
			t.Errorf("ownerIsLive(%q) = false, want true", v)
		}
	}
	if ownerIsLive(OwnerVerdictDead) {
		t.Error("ownerIsLive(dead) = true, want false")
	}

	unknown := ownerReclaimCode(OwnerVerdictUnverifiable)
	if unknown != OwnerReclaimRefusedUnknown {
		t.Errorf("reclaim(unverifiable) = %q, want %q", unknown, OwnerReclaimRefusedUnknown)
	}
	if unknown == ownerReclaimCode(OwnerVerdictLive) {
		t.Error("unverifiable and live must have distinct reclaim codes")
	}
	if got := ownerReclaimCode(OwnerVerdictDead); got != OwnerReclaimGranted {
		t.Errorf("reclaim(dead) = %q, want %q", got, OwnerReclaimGranted)
	}
}

func TestSessionOwnerVerdictUsesRecordedAndObservedIdentity(t *testing.T) {
	const pid = 424250
	withProcessAlive(t, map[int]bool{pid: true})
	withOwnerObserved(t, 900, "boot-a")

	cases := []struct {
		name string
		s    SessionLiveness
		want OwnerVerdict
	}{
		{"all fields absent", SessionLiveness{SessionID: "s", PID: pid}, OwnerVerdictUnverifiable},
		{"partial fields", SessionLiveness{SessionID: "s", PID: pid, StartTime: 900}, OwnerVerdictUnverifiable},
		{"matching identity", SessionLiveness{SessionID: "s", PID: pid, StartTime: 900, BootID: "boot-a"}, OwnerVerdictLive},
		{"boot changed", SessionLiveness{SessionID: "s", PID: pid, StartTime: 900, BootID: "boot-old"}, OwnerVerdictDead},
		{"start time changed", SessionLiveness{SessionID: "s", PID: pid, StartTime: 111, BootID: "boot-a"}, OwnerVerdictDead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionOwnerVerdict(tc.s); got != tc.want {
				t.Errorf("sessionOwnerVerdict = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListRunningRebootTurnsWatchIdle(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const pid = 424251
	// The pid is still alive after the reboot; only the boot id changed, so
	// T3.86 alone would keep the watch live.
	withProcessAlive(t, map[int]bool{pid: true})
	withOwnerObserved(t, 900, "boot-new")

	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
		Sessions: []SessionLiveness{{
			SessionID: "s1", Artifacts: []string{artifact}, PID: pid,
			StartTime: 900, BootID: "boot-old",
		}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})
	if got := out.Watches[0].Status; got != domain.Status(StatusIdle) {
		t.Errorf("watch status = %q, want %q", got, StatusIdle)
	}
}

func TestListRunningPIDReuseTurnsWatchIdle(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const pid = 424252
	withProcessAlive(t, map[int]bool{pid: true})
	withOwnerObserved(t, 901, "boot-same")

	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
		Sessions: []SessionLiveness{{
			SessionID: "s1", Artifacts: []string{artifact}, PID: pid,
			StartTime: 900, BootID: "boot-same",
		}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})
	if got := out.Watches[0].Status; got != domain.Status(StatusIdle) {
		t.Errorf("watch status = %q, want %q", got, StatusIdle)
	}
}

func TestListRunningIncompleteOwnerKeepsStatus(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const pid = 424253
	withProcessAlive(t, map[int]bool{pid: true})
	withOwnerObserved(t, 901, "boot-new") // would be dead if the identity were complete

	cases := []struct {
		name  string
		owner SessionLiveness
	}{
		{"no owner fields", SessionLiveness{SessionID: "s1", PID: pid}},
		{"missing boot id", SessionLiveness{SessionID: "s1", PID: pid, StartTime: 900}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := tc.owner
			owner.Artifacts = []string{artifact}
			reader := &snapshotReaderFake{snap: StateSnapshot{
				ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
				Sessions:      []SessionLiveness{owner},
			}}
			out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})
			if got := out.Watches[0].Status; got != domain.StatusWatching {
				t.Errorf("watch status = %q, want watching (unverifiable owner is live)", got)
			}
		})
	}
}

func TestListRunningVerifiedOwnerKeepsStatus(t *testing.T) {
	artifact := writeArtifact(t, t.TempDir(), "session.json")
	const pid = 424254
	withProcessAlive(t, map[int]bool{pid: true})
	withOwnerObserved(t, 900, "boot-same")

	reader := &snapshotReaderFake{snap: StateSnapshot{
		ActiveWatches: []domain.Watch{watchWithSubscriber("w1", "s1")},
		Sessions: []SessionLiveness{{
			SessionID: "s1", Artifacts: []string{artifact}, PID: pid,
			StartTime: 900, BootID: "boot-same",
		}},
	}}
	out := callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})
	if got := out.Watches[0].Status; got != domain.StatusWatching {
		t.Errorf("watch status = %q, want watching", got)
	}
}

// sampleRegistry is one consistent configuration with two registered agents.
// One is stale so a reader can tell registry state from a Watch verdict.
func sampleRegistry() []RegistryEntry {
	return []RegistryEntry{
		{
			ID:           "exec-1",
			Role:         domain.RoleExecutor,
			Capabilities: []domain.Capability{domain.CapabilityWatch, domain.CapabilityAck},
			Config:       "ci=stub,gpt=codex",
			Status:       RegistryActive,
		},
		{
			ID:           "orch-1",
			Role:         domain.RoleOrchestrator,
			Capabilities: []domain.Capability{domain.CapabilityReadVerdict, domain.CapabilityMerge},
			Config:       "ci=stub,gpt=codex",
			Status:       RegistryStale,
		},
	}
}

func TestSnapshotCarriesRegistryProjection(t *testing.T) {
	snap := sampleSnapshot()
	snap.Registry = sampleRegistry()

	if got := snap.RegisteredAgents(); got != 2 {
		t.Errorf("registered agents = %d, want 2", got)
	}
	if got := snap.StaleAgents(); got != 1 {
		t.Errorf("stale agents = %d, want 1", got)
	}
	if got := snap.RegistryConfig(); got != "ci=stub,gpt=codex" {
		t.Errorf("registry config = %q, want ci=stub,gpt=codex", got)
	}

	entry, ok := snap.FindRegistryEntry("exec-1")
	if !ok {
		t.Fatal("exec-1 not found in registry")
	}
	if entry.Role != domain.RoleExecutor {
		t.Errorf("role = %q, want executor", entry.Role)
	}
	wantCaps := []domain.Capability{domain.CapabilityWatch, domain.CapabilityAck}
	if !reflect.DeepEqual(entry.Capabilities, wantCaps) {
		t.Errorf("capabilities = %v, want %v", entry.Capabilities, wantCaps)
	}
	if entry.Config != "ci=stub,gpt=codex" {
		t.Errorf("config = %q, want ci=stub,gpt=codex", entry.Config)
	}
	if entry.Status != RegistryActive {
		t.Errorf("status = %q, want active", entry.Status)
	}
	if _, ok := snap.FindRegistryEntry("missing"); ok {
		t.Error("unknown executor was found in registry")
	}
}

func TestRegistryConfigRejectsMixedConfigurations(t *testing.T) {
	snap := sampleSnapshot()
	if got := snap.RegistryConfig(); got != "" {
		t.Errorf("empty registry config = %q, want empty", got)
	}
	snap.Registry = sampleRegistry()
	snap.Registry[1].Config = "other=config"
	if got := snap.RegistryConfig(); got != "" {
		t.Errorf("mixed registry config = %q, want empty", got)
	}
}

func TestListRunningRegistryFieldsDoNotChangeOutput(t *testing.T) {
	plain := sampleSnapshot()
	withRegistry := sampleSnapshot()
	withRegistry.Registry = sampleRegistry()

	plainOut := callListRunning(t, NewListRunningHandler(&snapshotReaderFake{snap: plain}), ListRunningInput{})
	regOut := callListRunning(t, NewListRunningHandler(&snapshotReaderFake{snap: withRegistry}), ListRunningInput{})

	if !reflect.DeepEqual(plainOut, regOut) {
		t.Errorf("registry fields changed the list_running output:\n%#v\n%#v", plainOut, regOut)
	}
	if regOut.Counts.Watches != 3 || regOut.Counts.Subscribers != 3 {
		t.Errorf("existing counts changed: %#v", regOut.Counts)
	}
	if regOut.Status != string(domain.StatusWatching) {
		t.Errorf("existing status = %q, want watching", regOut.Status)
	}
	if len(regOut.Watches) != 3 || len(regOut.Events) != 3 {
		t.Errorf("existing arrays changed: watches=%d events=%d", len(regOut.Watches), len(regOut.Events))
	}
}

func TestListRunningDoesNotMutateRegistry(t *testing.T) {
	snap := sampleSnapshot()
	snap.Registry = sampleRegistry()
	reader := &snapshotReaderFake{snap: snap}
	before := append([]RegistryEntry(nil), reader.snap.Registry...)

	callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})
	callListRunning(t, NewListRunningHandler(reader), ListRunningInput{})

	if !reflect.DeepEqual(reader.snap.Registry, before) {
		t.Errorf("registry was mutated: %#v -> %#v", before, reader.snap.Registry)
	}
}

// limitWatchSnapshot builds a snapshot with exactly n watches for the response
// limit controls.
func limitWatchSnapshot(n int) StateSnapshot {
	watches := make([]domain.Watch, n)
	for i := range watches {
		watches[i] = sampleWatch(fmt.Sprintf("limit-w-%d", i), domain.VerdictWatching, 0)
	}
	return StateSnapshot{ActiveWatches: watches}
}

// assertLimitError checks the error a caller can branch on and the hint the
// tool must show when it refuses an oversized response.
func assertLimitError(t *testing.T, err error, wantText ...string) {
	t.Helper()
	var app *domain.AppError
	if !errors.As(err, &app) || app.Code != domain.CodeLimitExceeded {
		t.Fatalf("error = %v, want LIMIT_EXCEEDED", err)
	}
	for _, text := range wantText {
		if !strings.Contains(app.Message, text) {
			t.Errorf("error message %q missing %q", app.Message, text)
		}
	}
}

func TestListRunningRecordLimit(t *testing.T) {
	cases := []struct {
		name    string
		watches int
		wantErr bool
	}{
		{"empty", 0, false},
		{"at limit", listRunningMaxRecords, false},
		{"over limit", listRunningMaxRecords + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewListRunningHandler(&snapshotReaderFake{snap: limitWatchSnapshot(tc.watches)})
			_, out, err := handler.Handle(context.Background(), nil, ListRunningInput{})
			if tc.wantErr {
				assertLimitError(t, err, "200-record", "filters.verdict", "cursor")
				return
			}
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if len(out.Watches) != tc.watches || len(out.Events) != tc.watches {
				t.Fatalf("watches=%d events=%d, want %d of each", len(out.Watches), len(out.Events), tc.watches)
			}
		})
	}
}

func TestListRunningByteLimit(t *testing.T) {
	snap := StateSnapshot{ActiveWatches: []domain.Watch{{
		ID:        "w-huge",
		Status:    domain.StatusWatching,
		Verdict:   domain.VerdictWatching,
		BlockerID: strings.Repeat("b", listRunningMaxBytes+1),
	}}}
	_, _, err := NewListRunningHandler(&snapshotReaderFake{snap: snap}).Handle(context.Background(), nil, ListRunningInput{})
	assertLimitError(t, err, "262144-byte", "filters.verdict")
}

// TestListRunningLimitRefusalKeepsCursor proves a refused call cannot consume
// the client's place in the event stream. The retry with the same cursor still
// sees the events added after the refusal.
func TestListRunningLimitRefusalKeepsCursor(t *testing.T) {
	reader := &snapshotReaderFake{snap: limitWatchSnapshot(2)}
	handler := NewListRunningHandler(reader)
	first := callListRunning(t, handler, ListRunningInput{})
	second := callListRunning(t, handler, ListRunningInput{Cursor: first.Cursor})
	if len(second.Events) != 0 {
		t.Fatalf("events on unchanged snapshot = %+v, want none", second.Events)
	}
	if handler.cursor != 2 {
		t.Fatalf("cursor after second call = %d, want 2", handler.cursor)
	}

	reader.snap = limitWatchSnapshot(listRunningMaxRecords + 1)
	_, _, err := handler.Handle(context.Background(), nil, ListRunningInput{Cursor: first.Cursor})
	assertLimitError(t, err, "200-record", "cursor")
	if handler.cursor != 2 {
		t.Fatalf("cursor after refusal = %d, want 2", handler.cursor)
	}

	reader.snap = limitWatchSnapshot(3)
	out := callListRunning(t, handler, ListRunningInput{Cursor: first.Cursor})
	if len(out.Events) != 1 || out.Events[0].WatchID != "limit-w-2" {
		t.Fatalf("events after retry = %+v, want only the third watch", out.Events)
	}
}

func TestListRunningLimitIsToolError(t *testing.T) {
	session := connectTestSession(t, Dependencies{Snapshot: SnapshotFunc(func() (StateSnapshot, error) {
		return limitWatchSnapshot(listRunningMaxRecords + 1), nil
	})})
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "beacon_list_running",
		Arguments: ListRunningInput{},
	})
	if err != nil {
		t.Fatalf("tools/call beacon_list_running: %v", err)
	}
	if !result.IsError {
		t.Fatalf("oversized list_running did not set isError: %+v", result.StructuredContent)
	}
	body, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal error content: %v", err)
	}
	if !strings.Contains(string(body), "200-record") || !strings.Contains(string(body), "filters.verdict") {
		t.Fatalf("tool error text = %s, want the limit and the narrowing hint", body)
	}
}

func callStatsFromSnapshot(t *testing.T, snap StateSnapshot) StatsOutput {
	t.Helper()
	result, out, err := NewStatsHandler(&snapshotReaderFake{snap: snap}).Handle(context.Background(), nil, StatsInput{})
	if err != nil {
		t.Fatalf("beacon_stats: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("beacon_stats returned an error result: %#v", result)
	}
	return out
}

func TestStatsRegistryFieldsDoNotChangeOutput(t *testing.T) {
	plain := sampleSnapshot()
	withRegistry := sampleSnapshot()
	withRegistry.Registry = sampleRegistry()

	plainOut := callStatsFromSnapshot(t, plain)
	regOut := callStatsFromSnapshot(t, withRegistry)
	if !reflect.DeepEqual(plainOut, regOut) {
		t.Errorf("registry fields changed the stats output:\n%#v\n%#v", plainOut, regOut)
	}
}
