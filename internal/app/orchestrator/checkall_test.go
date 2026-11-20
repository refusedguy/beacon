package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// fakeGH is the fake gh runner. It answers the two command families the pass
// is allowed to issue and records every call so tests can prove the batching.
type fakeGH struct {
	prList   []byte
	graphQL  []byte
	listErr  error
	graphErr error
	calls    [][]string
}

func (f *fakeGH) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
		return f.prList, f.listErr
	}
	if len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
		return f.graphQL, f.graphErr
	}
	return nil, fmt.Errorf("unexpected gh call: %v", args)
}

func TestGHSourceOpenPRsUsesOneListCall(t *testing.T) {
	gh := &fakeGH{prList: []byte(`[{"number":7,"headRefOid":"sha7"},{"number":3,"headRefOid":"sha3"}]`)}
	src := NewGHSource("refusedguy/beacon", gh.run)

	prs, err := src.OpenPRs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []PR{{Repo: "refusedguy/beacon", Number: 3, HeadSHA: "sha3"}, {Repo: "refusedguy/beacon", Number: 7, HeadSHA: "sha7"}}
	if !reflect.DeepEqual(prs, want) {
		t.Fatalf("PRs = %+v, want %+v", prs, want)
	}
	if len(gh.calls) != 1 {
		t.Fatalf("gh calls = %d, want 1: %v", len(gh.calls), gh.calls)
	}
	wantCall := []string{"pr", "list", "--json", "number,headRefOid"}
	if !reflect.DeepEqual(gh.calls[0], wantCall) {
		t.Fatalf("gh call = %v, want %v", gh.calls[0], wantCall)
	}
}

func TestGHSourceChecksUsesOneGraphQLBatch(t *testing.T) {
	gh := &fakeGH{graphQL: []byte(`{
	  "data": {
	    "p0": {"pullRequest": {"statusCheckRollup": {"contexts": {"nodes": [
	      {"__typename":"CheckRun","name":"build","conclusion":"SUCCESS","state":"COMPLETED"}
	    ]}}}},
	    "p1": {"pullRequest": {"statusCheckRollup": {"contexts": {"nodes": [
	      {"__typename":"CheckRun","name":"test","conclusion":"FAILURE","state":"COMPLETED"},
	      {"__typename":"StatusContext","context":"lint","state":"SUCCESS"}
	    ]}}}},
	    "p2": null
	  }
	}`)}
	src := NewGHSource("refusedguy/beacon", gh.run)
	prs := []PR{
		{Repo: "refusedguy/beacon", Number: 3, HeadSHA: "sha3"},
		{Repo: "refusedguy/beacon", Number: 7, HeadSHA: "sha7"},
		{Repo: "refusedguy/beacon", Number: 9, HeadSHA: "sha9"},
	}

	got, err := src.Checks(context.Background(), prs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int][]Check{
		3: {{Name: "build", Conclusion: "SUCCESS"}},
		7: {{Name: "test", Conclusion: "FAILURE"}, {Name: "lint", Conclusion: "SUCCESS"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checks = %+v, want %+v", got, want)
	}
	if len(gh.calls) != 1 {
		t.Fatalf("gh calls = %d, want 1: %v", len(gh.calls), gh.calls)
	}
	call := gh.calls[0]
	if call[0] != "api" || call[1] != "graphql" || call[2] != "-f" || !strings.HasPrefix(call[3], "query=") {
		t.Fatalf("gh batch call = %v", call)
	}
	for _, c := range gh.calls {
		for _, arg := range c {
			if arg == "checks" {
				t.Fatalf("per-PR gh pr checks is forbidden: %v", c)
			}
		}
	}
}

func TestBuildChecksQueryHasOneAliasPerPR(t *testing.T) {
	q := BuildChecksQuery([]PR{
		{Repo: "refusedguy/beacon", Number: 3},
		{Repo: "refusedguy/beacon", Number: 7},
	})
	for _, want := range []string{
		`p0: repository(owner:"refusedguy", name:"beacon")`,
		`p1: repository(owner:"refusedguy", name:"beacon")`,
		"pullRequest(number:3)", "pullRequest(number:7)",
		"statusCheckRollup", "... on CheckRun", "... on StatusContext",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("query missing %q:\n%s", want, q)
		}
	}
}

func TestVerdictFromChecks(t *testing.T) {
	cases := []struct {
		name   string
		checks []Check
		want   Verdict
	}{
		{"no checks", nil, CheckVerdictWatching},
		{"all green", []Check{{Name: "a", Conclusion: "SUCCESS"}}, VerdictReady},
		{"failure", []Check{{Name: "a", Conclusion: "SUCCESS"}, {Name: "b", Conclusion: "FAILURE"}}, VerdictRetry},
		{"cancelled", []Check{{Name: "a", Conclusion: "CANCELLED"}}, VerdictRetry},
		{"pending", []Check{{Name: "a", Conclusion: "SUCCESS"}, {Name: "b", Conclusion: "PENDING"}}, CheckVerdictWatching},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerdictFromChecks(tc.checks); got != tc.want {
				t.Fatalf("verdict = %s, want %s", got, tc.want)
			}
		})
	}
	if got := FailedChecks([]Check{{Name: "z", Conclusion: "FAILURE"}, {Name: "a", Conclusion: "CANCELLED"}, {Name: "ok", Conclusion: "SUCCESS"}}); !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("failed checks = %v", got)
	}
}

func TestLoadProjectConfigIsAStub(t *testing.T) {
	a, err := LoadProjectConfig("/one")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadProjectConfig("/two")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a, Config{}) {
		t.Fatalf("config = %+v / %+v, want empty", a, b)
	}
}

type publishCall struct {
	ref     WatchRef
	verdict Verdict
	checks  []Check
}

func TestCheckAllRegistersMissingAndPublishesVerdicts(t *testing.T) {
	gh := &fakeGH{
		prList: []byte(`[{"number":3,"headRefOid":"sha3"},{"number":7,"headRefOid":"sha7"}]`),
		graphQL: []byte(`{"data":{
		  "p0":{"pullRequest":{"statusCheckRollup":{"contexts":{"nodes":[
		    {"__typename":"CheckRun","name":"build","conclusion":"SUCCESS"}]}}}},
		  "p1":{"pullRequest":{"statusCheckRollup":{"contexts":{"nodes":[
		    {"__typename":"CheckRun","name":"test","conclusion":"FAILURE"}]}}}}
		}}`),
	}
	var registered []int
	var published []publishCall
	deps := Deps{
		Source: NewGHSource("refusedguy/beacon", gh.run),
		Watches: FuncRegistry{
			FindFn: func(_ context.Context, pr PR) (WatchRef, bool, error) {
				if pr.Number != 3 {
					return WatchRef{}, false, nil
				}
				return WatchRef{ID: "watch:3", Repo: pr.Repo, PR: 3, HeadSHA: pr.HeadSHA}, true, nil
			},
			RegisterFn: func(_ context.Context, pr PR) (WatchRef, error) {
				registered = append(registered, pr.Number)
				return WatchRef{ID: fmt.Sprintf("watch:%d", pr.Number), Repo: pr.Repo, PR: pr.Number, HeadSHA: pr.HeadSHA}, nil
			},
		},
		Writer: FuncWriter{PublishFn: func(_ context.Context, ref WatchRef, v Verdict, checks []Check) error {
			published = append(published, publishCall{ref, v, checks})
			return nil
		}},
	}

	sum, err := CheckAll(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Registered != 1 || !reflect.DeepEqual(registered, []int{7}) {
		t.Fatalf("registered = %d %v", sum.Registered, registered)
	}
	if sum.Published != 2 || len(published) != 2 {
		t.Fatalf("published = %d %v", sum.Published, published)
	}
	if published[0].verdict != VerdictReady || published[1].verdict != VerdictRetry {
		t.Fatalf("verdicts = %s %s", published[0].verdict, published[1].verdict)
	}
	if len(sum.Rows) != 2 || sum.Rows[0].WatchID != "watch:3" || sum.Rows[1].WatchID != "watch:7" {
		t.Fatalf("rows = %+v", sum.Rows)
	}
	if sum.Rows[0].Registered || sum.Rows[0].Verdict != VerdictReady {
		t.Fatalf("row 3 = %+v", sum.Rows[0])
	}
	if !sum.Rows[1].Registered || sum.Rows[1].Verdict != VerdictRetry {
		t.Fatalf("row 7 = %+v", sum.Rows[1])
	}
	if !reflect.DeepEqual(sum.Rows[1].FailedChecks, []string{"test"}) {
		t.Fatalf("failed checks = %v", sum.Rows[1].FailedChecks)
	}
	if len(gh.calls) != 2 {
		t.Fatalf("gh calls = %d, want list + one batch: %v", len(gh.calls), gh.calls)
	}
}

func TestCheckAllDryRunOnlyPlansWrites(t *testing.T) {
	gh := &fakeGH{
		prList:  []byte(`[{"number":7,"headRefOid":"sha7"}]`),
		graphQL: []byte(`{"data":{"p0":{"pullRequest":{"statusCheckRollup":{"contexts":{"nodes":[{"__typename":"CheckRun","name":"build","conclusion":"SUCCESS"}]}}}}}}`),
	}
	deps := Deps{
		Source: NewGHSource("refusedguy/beacon", gh.run),
		Watches: FuncRegistry{
			FindFn: func(context.Context, PR) (WatchRef, bool, error) { return WatchRef{}, false, nil },
			RegisterFn: func(context.Context, PR) (WatchRef, error) {
				t.Fatal("dry run must not register")
				return WatchRef{}, nil
			},
		},
		Writer: FuncWriter{PublishFn: func(context.Context, WatchRef, Verdict, []Check) error {
			t.Fatal("dry run must not publish")
			return nil
		}},
		DryRun: true,
	}
	sum, err := CheckAll(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.DryRun || sum.Registered != 1 || sum.Published != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(sum.Rows) != 1 || sum.Rows[0].Verdict != VerdictReady || !sum.Rows[0].Registered {
		t.Fatalf("row = %+v", sum.Rows)
	}
}

func TestCheckAllKeepsGoingAfterPerPRFailures(t *testing.T) {
	gh := &fakeGH{
		prList:  []byte(`[{"number":3,"headRefOid":"sha3"},{"number":7,"headRefOid":"sha7"}]`),
		graphQL: []byte(`{"data":{"p0":null,"p1":{"pullRequest":{"statusCheckRollup":{"contexts":{"nodes":[{"__typename":"CheckRun","name":"build","conclusion":"SUCCESS"}]}}}}}}`),
	}
	deps := Deps{
		Source: NewGHSource("refusedguy/beacon", gh.run),
		Watches: FuncRegistry{
			FindFn: func(_ context.Context, pr PR) (WatchRef, bool, error) {
				if pr.Number == 3 {
					return WatchRef{}, false, errors.New("find failed")
				}
				return WatchRef{ID: "watch:7", PR: 7, HeadSHA: pr.HeadSHA}, true, nil
			},
			RegisterFn: func(context.Context, PR) (WatchRef, error) { return WatchRef{}, errors.New("unused") },
		},
		Writer: FuncWriter{PublishFn: func(context.Context, WatchRef, Verdict, []Check) error {
			return errors.New("publish failed")
		}},
	}
	sum, err := CheckAll(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Rows) != 2 || !strings.Contains(sum.Rows[0].Error, "find failed") {
		t.Fatalf("rows = %+v", sum.Rows)
	}
	if !strings.Contains(sum.Rows[1].Error, "publish failed") || sum.Published != 0 {
		t.Fatalf("row 7 = %+v, published = %d", sum.Rows[1], sum.Published)
	}
}

func TestSQLVerdictWriterPublishesAndStagesObligations(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	w := domain.Watch{
		ID:          "watch:refusedguy/beacon:3:sha3",
		Repo:        "refusedguy/beacon",
		PR:          3,
		HeadSHA:     "sha3",
		Status:      domain.StatusWatching,
		Verdict:     domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{ID: "orch-1", Role: domain.RoleOrchestrator}},
	}
	if err := db.SaveWatch(w); err != nil {
		t.Fatal(err)
	}

	writer := NewSQLVerdictWriter(db)
	ref := WatchRef{ID: w.ID, Repo: w.Repo, PR: w.PR, HeadSHA: w.HeadSHA}
	ctx := context.Background()
	if err := writer.Publish(ctx, ref, VerdictReady, []Check{{Name: "build", Conclusion: "SUCCESS"}}); err != nil {
		t.Fatal(err)
	}

	got, err := db.Watch(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != domain.VerdictReady || got.Status != domain.StatusNotifying || got.Revision != 1 {
		t.Fatalf("watch = %+v", got)
	}
	if c, ok := got.Checks["build"]; !ok || c.Conclusion != "SUCCESS" {
		t.Fatalf("checks = %+v", got.Checks)
	}
	o, err := db.Obligation(obligationKey(w.ID, 1, "orch-1"))
	if err != nil {
		t.Fatal(err)
	}
	if o.VerdictPointer != domain.VerdictReady || o.SubscriberID != "orch-1" {
		t.Fatalf("obligation = %+v", o)
	}

	// A second verdict on the same head is a no-op: the Watch left watching.
	if err := writer.Publish(ctx, ref, VerdictRetry, nil); err != nil {
		t.Fatal(err)
	}
	again, err := db.Watch(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != 1 || again.Verdict != domain.VerdictReady {
		t.Fatalf("repeat changed watch = %+v", again)
	}
}
