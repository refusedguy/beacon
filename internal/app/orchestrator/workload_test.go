package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type workloadSubscribeCall struct {
	watchID string
	sub     WorkloadSubscriber
}

// fakeWorkloadStore is the single fake behind the WorkloadStore port. On
// Subscribe it adds the orchestrator role to the stored Watch, so a second
// RunWorkload sees the subscription and must not register again. That mirrors
// domain.Fanout's renew behaviour without importing the domain package.
type fakeWorkloadStore struct {
	watches      []WorkloadWatch
	activeErr    error
	subscribeErr error
	subscribed   []workloadSubscribeCall
}

var _ WorkloadStore = (*fakeWorkloadStore)(nil)

func (s *fakeWorkloadStore) ActiveWorkloadWatches(context.Context) ([]WorkloadWatch, error) {
	return s.watches, s.activeErr
}

func (s *fakeWorkloadStore) Subscribe(_ context.Context, w WorkloadWatch, sub WorkloadSubscriber) error {
	s.subscribed = append(s.subscribed, workloadSubscribeCall{watchID: w.ID, sub: sub})
	if s.subscribeErr != nil {
		return s.subscribeErr
	}
	for i := range s.watches {
		if s.watches[i].ID != w.ID {
			continue
		}
		if !hasWorkloadRole(s.watches[i], sub.Role) {
			s.watches[i].Subscribers = append(s.watches[i].Subscribers, sub)
		}
		break
	}
	return nil
}

func workloadTestWatch(id, project, verdict string, roles ...string) WorkloadWatch {
	w := WorkloadWatch{ID: id, Repo: "refusedguy/" + project, Project: project, Verdict: verdict}
	for _, role := range roles {
		w.Subscribers = append(w.Subscribers, WorkloadSubscriber{
			ID:   id + "-" + role,
			Role: role,
		})
	}
	return w
}

func TestBuildWorkloadGroupsByVerdictRoleAndProject(t *testing.T) {
	watches := []WorkloadWatch{
		workloadTestWatch("w1", "alpha", "ready", RoleExecutor),
		workloadTestWatch("w2", "alpha", "retry", RoleExecutor, RoleOrchestrator),
		workloadTestWatch("w3", "beta", "watching", RoleExecutor),
		workloadTestWatch("w4", "beta", "rebase", RoleOrchestrator),
		workloadTestWatch("w5", "gamma", "keep_working", RoleExecutor),
		// Case and spaces must fold into the same buckets.
		workloadTestWatch("w6", "gamma", " READY ", RoleExecutor),
	}

	got := BuildWorkload(watches)

	if got.Total != 6 {
		t.Errorf("total = %d, want 6", got.Total)
	}
	if got.Ready != 2 {
		t.Errorf("ready = %d, want 2", got.Ready)
	}
	if got.NeedsAttention != 3 {
		t.Errorf("needsAttention = %d, want 3", got.NeedsAttention)
	}
	if got.Watching != 1 {
		t.Errorf("watching = %d, want 1", got.Watching)
	}
	wantVerdict := map[string]int{"ready": 2, "retry": 1, "watching": 1, "rebase": 1, "keep_working": 1}
	if !reflect.DeepEqual(got.ByVerdict, wantVerdict) {
		t.Errorf("byVerdict = %v, want %v", got.ByVerdict, wantVerdict)
	}
	wantRole := map[string]int{RoleExecutor: 5, RoleOrchestrator: 2}
	if !reflect.DeepEqual(got.ByRole, wantRole) {
		t.Errorf("byRole = %v, want %v", got.ByRole, wantRole)
	}
	wantProject := map[string]int{"alpha": 2, "beta": 2, "gamma": 2}
	if !reflect.DeepEqual(got.ByProject, wantProject) {
		t.Errorf("byProject = %v, want %v", got.ByProject, wantProject)
	}
}

func TestBuildWorkloadFallsBackToRepoAndDeduplicatesRole(t *testing.T) {
	watches := []WorkloadWatch{
		{ID: "w1", Repo: "org/one", Verdict: ""},
		{ID: "w2", Verdict: "timeout", Subscribers: []WorkloadSubscriber{
			{ID: "a", Role: RoleExecutor}, {ID: "b", Role: "executor"},
		}},
	}

	got := BuildWorkload(watches)

	if got.ByProject["org/one"] != 1 || got.ByProject[unknownProject] != 1 {
		t.Errorf("byProject = %v, want repo fallback and unknown bucket", got.ByProject)
	}
	if got.ByRole[RoleExecutor] != 1 {
		t.Errorf("byRole[executor] = %d, want 1", got.ByRole[RoleExecutor])
	}
	if got.Watching != 1 || got.NeedsAttention != 1 {
		t.Errorf("watching=%d needsAttention=%d, want 1 and 1", got.Watching, got.NeedsAttention)
	}
}

func TestRunWorkloadRegistersOrchestratorIdempotently(t *testing.T) {
	st := &fakeWorkloadStore{watches: []WorkloadWatch{
		workloadTestWatch("w1", "alpha", "watching", RoleExecutor),
		workloadTestWatch("w2", "alpha", "ready", RoleExecutor, RoleOrchestrator),
		workloadTestWatch("w3", "beta", "retry"),
	}}

	first, err := RunWorkload(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if first.Registered != 2 {
		t.Errorf("first registered = %d, want 2", first.Registered)
	}
	if len(st.subscribed) != 2 {
		t.Fatalf("subscribe calls = %d, want 2", len(st.subscribed))
	}
	for i, wantID := range []string{"w1", "w3"} {
		call := st.subscribed[i]
		if call.watchID != wantID {
			t.Errorf("call %d watch = %q, want %q", i, call.watchID, wantID)
		}
		if call.sub.ID != OrchestratorSubscriberID || call.sub.Role != RoleOrchestrator {
			t.Errorf("call %d sub = %+v, want orchestrator subscriber", i, call.sub)
		}
	}
	// The report is the pre-registration snapshot: only w2 had the role.
	if first.ByRole[RoleOrchestrator] != 1 {
		t.Errorf("byRole[orchestrator] = %d, want 1", first.ByRole[RoleOrchestrator])
	}

	second, err := RunWorkload(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if second.Registered != 0 {
		t.Errorf("second registered = %d, want 0", second.Registered)
	}
	if len(st.subscribed) != 2 {
		t.Errorf("subscribe calls after second pass = %d, want 2", len(st.subscribed))
	}
	if second.ByRole[RoleOrchestrator] != 3 {
		t.Errorf("second byRole[orchestrator] = %d, want 3", second.ByRole[RoleOrchestrator])
	}
}

func TestRunWorkloadJSONShape(t *testing.T) {
	st := &fakeWorkloadStore{watches: []WorkloadWatch{
		workloadTestWatch("w1", "alpha", "ready", RoleExecutor),
	}}
	report, err := RunWorkload(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"total", "ready", "needsAttention", "watching", "registered", "byVerdict", "byRole", "byProject"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("json missing key %q in %s", key, raw)
		}
	}

	var roundTrip Workload
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, report) {
		t.Errorf("round trip = %+v, want %+v", roundTrip, report)
	}
}

func TestRunWorkloadErrors(t *testing.T) {
	if _, err := RunWorkload(context.Background(), nil); err == nil {
		t.Error("nil store should fail")
	}

	failing := &fakeWorkloadStore{activeErr: errors.New("boom")}
	if _, err := RunWorkload(context.Background(), failing); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("active read error = %v, want wrapped boom", err)
	}

	// A failed subscribe is reported but the counters still describe the read.
	badSub := &fakeWorkloadStore{
		watches:      []WorkloadWatch{workloadTestWatch("w1", "alpha", "ready")},
		subscribeErr: errors.New("subscribe denied"),
	}
	report, err := RunWorkload(context.Background(), badSub)
	if err == nil || !strings.Contains(err.Error(), "subscribe denied") {
		t.Errorf("subscribe error = %v, want wrapped denial", err)
	}
	if report.Total != 1 || report.Ready != 1 || report.Registered != 0 {
		t.Errorf("report = %+v, want counters kept and no registration", report)
	}
}
