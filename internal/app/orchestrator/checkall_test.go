package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
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
		"statusCheckRollup",
		"... on CheckRun { name conclusion status }",
		"... on StatusContext { context state }",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("query missing %q:\n%s", want, q)
		}
	}
	if strings.Contains(q, "conclusion state") {
		t.Fatalf("CheckRun fragment asks for nonexistent state field:\n%s", q)
	}
}

func TestCheckFromNodeUsesStatusForCheckRun(t *testing.T) {
	cases := []struct {
		raw  string
		want Check
	}{
		{`{"__typename":"CheckRun","name":"build","conclusion":"SUCCESS","status":"COMPLETED"}`, Check{Name: "build", Conclusion: "SUCCESS"}},
		{`{"__typename":"CheckRun","name":"build","status":"IN_PROGRESS"}`, Check{Name: "build", Conclusion: "PENDING"}},
		{`{"__typename":"StatusContext","context":"lint","state":"FAILURE"}`, Check{Name: "lint", Conclusion: "FAILURE"}},
	}
	for _, tc := range cases {
		got, ok := checkFromNode([]byte(tc.raw))
		if !ok || got != tc.want {
			t.Fatalf("checkFromNode(%s) = %+v, %v, want %+v", tc.raw, got, ok, tc.want)
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

func TestNormalizeConclusionAllowedResultsAreSuccess(t *testing.T) {
	for _, in := range []string{"SKIPPED", "skipped", "NEUTRAL", "neutral", "SUCCESS"} {
		if got := normalizeConclusion(in); got != "SUCCESS" {
			t.Errorf("normalizeConclusion(%q) = %q, want SUCCESS", in, got)
		}
	}
	for _, in := range []string{"CANCELLED", "CANCELED"} {
		if got := normalizeConclusion(in); got != "CANCELLED" {
			t.Errorf("normalizeConclusion(%q) = %q, want CANCELLED", in, got)
		}
	}
	if got := VerdictFromChecks([]Check{{Name: "skip", Conclusion: "SKIPPED"}, {Name: "neutral", Conclusion: "NEUTRAL"}}); got != VerdictReady {
		t.Fatalf("verdict for SKIPPED/NEUTRAL = %s, want ready", got)
	}
}

// isolateConfig points the global layer at a path with no file, so a test
// never depends on the developer's ~/.config/beacon/config.yaml.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv(configPathEnv, filepath.Join(t.TempDir(), "no-global.yaml"))
}

func writeConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProjectConfigDefaultsWithoutFiles(t *testing.T) {
	isolateConfig(t)
	got, err := LoadProjectConfig(filepath.Join(t.TempDir(), "missing-project"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Config{MaxRebases: 3, SlotsTotal: 4}); !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
}

func TestLoadProjectConfigProjectOverridesGlobal(t *testing.T) {
	isolateConfig(t)
	global := filepath.Join(t.TempDir(), "global.yaml")
	writeConfigFile(t, global, `# global layer
maxRebases: 5
orchestratorID: ses_global
slotsTotal: 2
wakeTarget: 'ses_global_wake'
`)
	t.Setenv(configPathEnv, global)

	project := t.TempDir()
	writeConfigFile(t, filepath.Join(project, configDirName, configFileName),
		"max_rebases: 7\nwakeTarget: \"ses_project_wake\"\n")

	got, err := LoadProjectConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{MaxRebases: 7, OrchestratorID: "ses_global", SlotsTotal: 2, WakeTarget: "ses_project_wake"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
}

func TestLoadProjectConfigUsesGlobalWhenProjectAbsent(t *testing.T) {
	isolateConfig(t)
	global := filepath.Join(t.TempDir(), "global.yaml")
	writeConfigFile(t, global, "maxRebases: 6\nslotsTotal: 0\n")
	t.Setenv(configPathEnv, global)

	got, err := LoadProjectConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := Config{MaxRebases: 6, SlotsTotal: 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
}

func TestLoadProjectConfigReadsNestedFlakySection(t *testing.T) {
	isolateConfig(t)
	project := t.TempDir()
	writeConfigFile(t, filepath.Join(project, configDirName, configFileName), `
maxRebases: 4
flaky:
  names:
    - ci/flaky
    - ci/network
  maxRetries: 2
`)

	got, err := LoadProjectConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		MaxRebases: 4, SlotsTotal: 4,
		Flaky: FlakyConfig{Names: []string{"ci/flaky", "ci/network"}, MaxRetries: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
}

func TestLoadProjectConfigMergesFlakyLayers(t *testing.T) {
	isolateConfig(t)
	global := filepath.Join(t.TempDir(), "global.yaml")
	writeConfigFile(t, global, "flaky:\n  names: [ci/global]\n  maxRetries: 1\n")
	t.Setenv(configPathEnv, global)

	project := t.TempDir()
	writeConfigFile(t, filepath.Join(project, configDirName, configFileName),
		"flaky:\n  maxRetries: 3\n")

	got, err := LoadProjectConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Flaky.Names, []string{"ci/global"}) || got.Flaky.MaxRetries != 3 {
		t.Fatalf("flaky = %+v", got.Flaky)
	}
}

func TestLoadProjectConfigRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name, body, wantErr string
	}{
		{"above range", "maxRebases: 11\n", "maxRebases"},
		{"below range", "maxRebases: 0\n", "maxRebases"},
		{"negative slots", "slotsTotal: -1\n", "slotsTotal"},
		{"not an integer", "maxRebases: many\n", "неверное целое"},
		{"malformed line", "maxRebases 4\n", "key: value"},
		{"unknown key", "maxRebase: 4\n", "неизвестный ключ"},
		{"invalid wake target", "wakeTarget: beacon-ci\n", "ses_"},
		{"invalid orchestrator id", "orchestratorID: beacon-ci\n", "ses_"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateConfig(t)
			project := t.TempDir()
			writeConfigFile(t, filepath.Join(project, configDirName, configFileName), tc.body)
			_, err := LoadProjectConfig(project)
			if err == nil {
				t.Fatalf("LoadProjectConfig accepted %q", tc.body)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q must contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckAllRejectsInvalidProjectConfig(t *testing.T) {
	isolateConfig(t)
	project := t.TempDir()
	writeConfigFile(t, filepath.Join(project, configDirName, configFileName), "maxRebases: 99\n")
	_, err := CheckAll(context.Background(), Deps{ConfigDir: project})
	if err == nil || !strings.Contains(err.Error(), "maxRebases") {
		t.Fatalf("CheckAll error = %v, want maxRebases", err)
	}
}

func TestValidateWakeTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		target  string
		wantErr bool
	}{
		{name: "session id", target: "ses_abc123"},
		{name: "explicit configured session", target: "ses_orchestrator_1"},
		{name: "service name is not a session", target: "beacon-ci-checkall", wantErr: true},
		{name: "empty", target: "", wantErr: true},
		{name: "prefix only", target: "ses_", wantErr: true},
		{name: "wrong prefix", target: "ses-abc123", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWakeTarget(tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateWakeTarget(%q) accepted an invalid target", tc.target)
				}
				if !strings.Contains(err.Error(), "ses_") {
					t.Fatalf("error %q must name the required prefix", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateWakeTarget(%q) = %v, want nil", tc.target, err)
			}
		})
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

func TestClassifyArtifactPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want ArtifactCategory
	}{
		{"workflow flat file", ".github/workflows/ci.yml", ArtifactWorkflow},
		{"workflow docs gate", ".github/workflows/docs.yml", ArtifactWorkflow},
		{"skill root file", ".opencode/skills/night-shift/SKILL.md", ArtifactKnowledge},
		{"skill nested reference", ".opencode/skills/night-shift/references/comment-rules.md", ArtifactKnowledge},
		{"skill directory", ".opencode/skills/night-shift", ArtifactKnowledge},
		{"root agents rules", "AGENTS.md", ArtifactRules},
		{"root claude rules", "CLAUDE.md", ArtifactRules},
		{"cursor rules", ".cursorrules", ArtifactRules},
		{"cursor rules dir", ".cursor/rules/general.mdc", ArtifactRules},
		{"docs rules dir", "docs/rules/conventions.md", ArtifactRules},
		{"unknown source", "internal/app/orchestrator/checkall.go", ArtifactUnknown},
		{"unknown adr", "docs/adrs/0001-example.md", ArtifactUnknown},
		{"unknown readme", "README.md", ArtifactUnknown},
		{"empty", "", ArtifactUnknown},
		{"absolute path", "/etc/passwd", ArtifactUnknown},
		{"nested workflow is not flat", ".github/workflows/sub/ci.yml", ArtifactUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyArtifactPath(tc.path); got != tc.want {
				t.Fatalf("classifyArtifactPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyArtifactsGroupsWithoutOverlap(t *testing.T) {
	paths := []string{
		".github/workflows/ci.yml",
		".opencode/skills/night-shift/SKILL.md",
		"AGENTS.md",
		"internal/app/orchestrator/checkall.go",
	}
	got := ClassifyArtifacts(paths)
	if !reflect.DeepEqual(got.Workflow, []string{".github/workflows/ci.yml"}) {
		t.Fatalf("workflow = %v", got.Workflow)
	}
	if !reflect.DeepEqual(got.Knowledge, []string{".opencode/skills/night-shift/SKILL.md"}) {
		t.Fatalf("knowledge = %v", got.Knowledge)
	}
	if !reflect.DeepEqual(got.Rules, []string{"AGENTS.md"}) {
		t.Fatalf("rules = %v", got.Rules)
	}
	if !reflect.DeepEqual(got.Unknown, []string{"internal/app/orchestrator/checkall.go"}) {
		t.Fatalf("unknown = %v", got.Unknown)
	}

	counts := map[string]int{}
	for _, bucket := range [][]string{got.Workflow, got.Knowledge, got.Rules, got.Unknown} {
		for _, p := range bucket {
			counts[p]++
		}
	}
	if len(counts) != len(paths) {
		t.Fatalf("distinct paths = %d, want %d: %v", len(counts), len(paths), counts)
	}
	for p, n := range counts {
		if n != 1 {
			t.Fatalf("path %q appears in %d buckets", p, n)
		}
	}
}

func TestClassifyArtifactsEmptyList(t *testing.T) {
	for name, paths := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			if got := ClassifyArtifacts(paths); !reflect.DeepEqual(got, ArtifactClassification{}) {
				t.Fatalf("classification = %+v, want empty", got)
			}
		})
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

// TestValidateStrictJSONAcceptsValidJSON: a well-formed fence body passes.
// Surrounding whitespace is allowed because fences add newlines around JSON.
func TestValidateStrictJSONAcceptsValidJSON(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":1,"gate":"rules","status":"pass","blocking":false}`,
		"  {\n  \"status\": \"warn\",\n  \"blockers\": []\n}\n ",
		`[1, 2, 3]`,
	} {
		if err := ValidateStrictJSON(body); err != nil {
			t.Errorf("ValidateStrictJSON(%q) = %v, want nil", body, err)
		}
	}
}

// TestValidateStrictJSONRejectsNonStrictBodies: a comment, a trailing comma,
// decoration around the value, trailing garbage and an empty fence all fail.
func TestValidateStrictJSONRejectsNonStrictBodies(t *testing.T) {
	cases := []struct {
		name, body string
	}{
		{"line comment", "{\n  // note\n  \"status\": \"pass\"\n}"},
		{"block comment", `{"status":"pass" /* ok */}`},
		{"trailing comma object", `{"status":"pass",}`},
		{"trailing comma array", `[1, 2,]`},
		{"prose before", "Here is the result:\n{\"status\":\"pass\"}"},
		{"prose after", "{\"status\":\"pass\"}\nThat is all."},
		{"trailing garbage", `{"status":"pass"} {}`},
		{"empty fence", ""},
		{"whitespace only fence", " \n\t "},
		{"fence markers kept", "```aif-gate-result\n{\"status\":\"pass\"}\n```"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateStrictJSON(tc.body); err == nil {
				t.Fatalf("ValidateStrictJSON accepted %q", tc.body)
			}
		})
	}
}

// TestBuildDocsIndexIsDerivedFromSources proves the index is generated from
// markdown files, keeps current docs grouped by section, and ignores the
// backlog subtrees that have their own gates.
func TestBuildDocsIndexIsDerivedFromSources(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	files := map[string]string{
		"README.md":              "# Beacon\n\nIntro.\n",
		"design/ARCH.md":         "# Architecture\n",
		"design/deep.md":         "no heading here\n",
		"tasks/HD-089.md":        "# HD-089. Index\n",
		"adrs/0001-example.md":   "# ADR 1\n",
		"INDEX.md":               "stale index that must be ignored\n",
		"gates/[brackets].md":    "# Gate [special]\n",
		"gates/not-markdown.txt": "ignored\n",
	}
	for rel, body := range files {
		path := filepath.Join(docs, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := BuildDocsIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		docsIndexMarker,
		"# Documentation index",
		"## Root",
		"- [Beacon](README.md)",
		"## design",
		"- [Architecture](design/ARCH.md)",
		"- [deep](design/deep.md)",
		"## gates",
		"- [Gate \\[special\\]](gates/[brackets].md)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated index missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"tasks/HD-089.md",
		"adrs/0001-example.md",
		"not-markdown.txt",
		"stale index that must be ignored",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("generated index contains %q:\n%s", unwanted, got)
		}
	}

	again, err := BuildDocsIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Fatal("BuildDocsIndex is not deterministic")
	}
}

// TestCheckDocsIndexCatchesStaleAndManualIndex is the negative control for
// HD-089: a generated index passes, a hand edit or a missing file fails.
func TestCheckDocsIndexCatchesStaleAndManualIndex(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(filepath.Join(docs, "design"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(docs, "design", "ARCH.md")
	if err := os.WriteFile(source, []byte("# Architecture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(docs, docsIndexFile)

	generated, err := BuildDocsIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckDocsIndex(root); err != nil {
		t.Fatalf("generated index failed check: %v", err)
	}

	// A manual index fails even when it looks plausible.
	if err := os.WriteFile(index, []byte("# Documentation index\n\n- [Hand written](design/ARCH.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckDocsIndex(root); err == nil {
		t.Fatal("CheckDocsIndex accepted a hand-edited index")
	}

	// A source change makes the once-current index stale.
	if err := os.WriteFile(index, []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("# Architecture v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckDocsIndex(root); err == nil {
		t.Fatal("CheckDocsIndex accepted a stale index")
	}

	// A missing index fails as well.
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := CheckDocsIndex(root); err == nil {
		t.Fatal("CheckDocsIndex accepted a missing index")
	}
}

// TestBuildDocsIndexEmptyTree keeps the generated marker even when there is
// nothing to list, so an empty index is never mistaken for a hand-written one.
func TestBuildDocsIndexEmptyTree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := BuildDocsIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, docsIndexMarker) || !strings.Contains(got, "_No documentation files._") {
		t.Fatalf("empty index lost its marker or placeholder:\n%s", got)
	}
}
