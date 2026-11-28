package shared_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/checksource"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/policy"
	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

// workflowFixture contains a plain job, a matrix job whose strategy also has
// a fail-fast key, and a job that depends on the matrix job. The matrix job
// must be absent from every resolver's list.
const workflowFixture = "name: CI\njobs:\n" +
	"  verify:\n    runs-on: ubuntu-latest\n" +
	"  test-modules:\n    runs-on: ubuntu-latest\n" +
	"    strategy:\n      fail-fast: false\n      matrix:\n        module: [cmd, pkg]\n" +
	"  test:\n    needs: test-modules\n    runs-on: ubuntu-latest\n"

// advisoryWorkflowFixture has one job with a job-level continue-on-error and
// another with only a step-level flag. The step-level flag does not remove the
// job, so perf-nightly stays required. verify stays required as well.
const advisoryWorkflowFixture = "name: CI\njobs:\n" +
	"  perf-pr-advisory:\n    runs-on: ubuntu-latest\n    continue-on-error: true\n" +
	"  perf-nightly:\n    runs-on: ubuntu-latest\n    steps:\n" +
	"      - name: Optional download\n        continue-on-error: true\n        run: echo optional\n" +
	"  verify:\n    runs-on: ubuntu-latest\n"

const reusableWorkflowFixture = "name: Reusable\n" +
	"on:\n  workflow_call:\n    inputs:\n      target:\n        type: string\n" +
	"jobs:\n  inner:\n    runs-on: ubuntu-latest\n"

// matrixFixture mirrors the shape of ci.yml: a gated job whose strategy puts
// fail-fast before an include list, plus an aggregator. GitHub reports one
// check per include entry, named with the entry values in declaration order.
const matrixFixture = "name: CI\njobs:\n" +
	"  test-modules:\n" +
	"    if: github.event_name != 'schedule'\n" +
	"    runs-on: ubuntu-latest\n" +
	"    strategy:\n      fail-fast: false\n      matrix:\n        include:\n" +
	"          - module: root app\n            filter: go-core\n            race: true\n" +
	"          - module: tui\n            filter: go-tui\n            race: false\n" +
	"  test:\n    needs: test-modules\n    runs-on: ubuntu-latest\n"

var matrixCellNames = []string{
	"test-modules (root app, go-core, true)",
	"test-modules (tui, go-tui, false)",
}

func writeWorkflow(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	writeNamedWorkflow(t, root, "ci.yml", content)
	return root
}

func writeNamedWorkflow(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeWorkflowFixture(t *testing.T) string {
	t.Helper()
	return writeWorkflow(t, workflowFixture)
}

func TestResolversReturnIdenticalNames(t *testing.T) {
	root := writeWorkflowFixture(t)
	want := []string{"test", "verify"}

	fromConfig := config.DetectRequiredChecks(root)
	fromSource, err := (checksource.Workflow{Root: root}).Names(context.Background())
	if err != nil {
		t.Fatalf("Workflow.Names: %v", err)
	}
	if !reflect.DeepEqual(fromConfig, fromSource) {
		t.Fatalf("config resolved %v, check source resolved %v", fromConfig, fromSource)
	}
	if !reflect.DeepEqual(fromConfig, want) {
		t.Fatalf("resolved names = %v, want %v", fromConfig, want)
	}
}

func TestDetectWorkflowJobNamesUsesSharedParser(t *testing.T) {
	root := writeWorkflowFixture(t)
	got := shared.DetectWorkflowJobNames(root)
	want := []string{"test", "verify"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectWorkflowJobNames = %v, want %v", got, want)
	}
}

func TestParseWorkflowJobNamesKeepsMatrixWithFailFastSibling(t *testing.T) {
	got := shared.ParseWorkflowJobNames(workflowFixture)
	want := []string{"verify", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseWorkflowJobNames = %v, want %v", got, want)
	}
}

func TestAdvisoryJobsAreNotRequiredChecks(t *testing.T) {
	root := writeWorkflow(t, advisoryWorkflowFixture)
	want := []string{"perf-nightly", "verify"}

	if got := shared.ParseWorkflowJobNames(advisoryWorkflowFixture); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseWorkflowJobNames = %v, want %v", got, want)
	}
	if got := shared.DetectWorkflowJobNames(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectWorkflowJobNames = %v, want %v", got, want)
	}
	if got := config.DetectRequiredChecks(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("config resolved %v, want %v", got, want)
	}
	fromSource, err := (checksource.Workflow{Root: root}).Names(context.Background())
	if err != nil {
		t.Fatalf("Workflow.Names: %v", err)
	}
	if !reflect.DeepEqual(fromSource, want) {
		t.Fatalf("check source resolved %v, want %v", fromSource, want)
	}
}

func TestTolerantStepDoesNotRemoveRequiredJob(t *testing.T) {
	const content = "name: CI\njobs:\n  verify:\n    runs-on: ubuntu-latest\n" +
		"    steps:\n      - name: Optional\n        continue-on-error: true\n        run: echo optional\n"
	got := shared.ParseWorkflowJobNames(content)
	if want := []string{"verify"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseWorkflowJobNames = %v, want %v", got, want)
	}
}

func TestMatrixDetectionToleratesAlternateForms(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name: "matrix after two strategy keys",
			content: "jobs:\n  suite:\n    strategy:\n      fail-fast: false\n" +
				"      max-parallel: 2\n      matrix:\n        os: [ubuntu]\n",
		},
		{
			name: "matrix before sibling keys",
			content: "jobs:\n  suite:\n    strategy:\n      matrix:\n        os: [ubuntu]\n" +
				"      fail-fast: false\n",
		},
		{
			name:    "inline matrix",
			content: "jobs:\n  suite:\n    strategy: {matrix: {os: [ubuntu]}}\n",
		},
		{
			name: "four space job indentation",
			content: "jobs:\n    suite:\n        strategy:\n            matrix:\n" +
				"                os: [ubuntu]\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shared.ParseWorkflowJobNames(tc.content)
			if len(got) != 0 {
				t.Fatalf("ParseWorkflowJobNames = %v, want no job names", got)
			}
		})
	}
}

func TestReusableWorkflowJobsAreNotRequiredChecks(t *testing.T) {
	root := t.TempDir()
	writeNamedWorkflow(t, root, "ci.yml", "name: CI\njobs:\n  verify:\n    runs-on: ubuntu-latest\n")
	writeNamedWorkflow(t, root, "reusable.yml", reusableWorkflowFixture)

	if got, want := shared.DetectWorkflowJobNames(root), []string{"verify"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectWorkflowJobNames = %v, want %v", got, want)
	}
	if got, want := config.DetectRequiredChecks(root), []string{"verify"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("config resolved %v, want %v", got, want)
	}
}

func TestDetectUnreportableJobNames(t *testing.T) {
	root := t.TempDir()
	writeNamedWorkflow(t, root, "ci.yml", workflowFixture)
	writeNamedWorkflow(t, root, "reusable.yml", reusableWorkflowFixture)

	got := shared.DetectUnreportableJobNames(root)
	want := []string{"inner", "test-modules"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectUnreportableJobNames = %v, want %v", got, want)
	}
}

func TestMatrixCellsAreRequiredChecks(t *testing.T) {
	root := writeWorkflow(t, matrixFixture)
	want := append([]string{"test"}, matrixCellNames...)

	got := shared.DetectWorkflowJobNames(root)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectWorkflowJobNames = %v, want %v", got, want)
	}
	if fromConfig := config.DetectRequiredChecks(root); !reflect.DeepEqual(fromConfig, want) {
		t.Fatalf("config resolved %v, want %v", fromConfig, want)
	}
	fromSource, err := (checksource.Workflow{Root: root}).Names(context.Background())
	if err != nil {
		t.Fatalf("Workflow.Names: %v", err)
	}
	if !reflect.DeepEqual(fromSource, want) {
		t.Fatalf("check source resolved %v, want %v", fromSource, want)
	}
}

func TestMatrixCellsInheritJobIfGate(t *testing.T) {
	root := writeWorkflow(t, matrixFixture)
	src := checksource.Workflow{Root: root}
	gated, err := src.ConditionalNames(context.Background())
	if err != nil {
		t.Fatalf("ConditionalNames: %v", err)
	}
	if !reflect.DeepEqual(gated, matrixCellNames) {
		t.Fatalf("ConditionalNames = %v, want %v", gated, matrixCellNames)
	}
}

func TestFailedMatrixCellBlocksMerge(t *testing.T) {
	root := writeWorkflow(t, matrixFixture)
	source := checksource.Workflow{Root: root}
	obs := core.Observation{
		PRState: "open",
		Checks: []core.CheckResult{
			{Name: "test", State: core.CheckSuccess},
			{Name: matrixCellNames[0], State: core.CheckFailure},
			{Name: matrixCellNames[1], State: core.CheckSuccess},
		},
	}

	verdict, reason := (policy.SourceChecksRule{Source: source}).Evaluate(obs)
	if verdict != core.VerdictWait {
		t.Fatalf("verdict with red cell = %s (%s), want %s", verdict, reason, core.VerdictWait)
	}
	if !strings.Contains(reason, matrixCellNames[0]) {
		t.Fatalf("reason = %q, want the failing cell name", reason)
	}
	if decision := policy.New(policy.Config{RequiredCheckSource: source}).Evaluate(obs); decision.Verdict == core.VerdictMerge {
		t.Fatalf("policy merged a red matrix cell: %+v", decision)
	}

	obs.Checks[1].State = core.CheckSuccess
	if decision := policy.New(policy.Config{RequiredCheckSource: source}).Evaluate(obs); decision.Verdict != core.VerdictMerge {
		t.Fatalf("policy verdict with green cells = %+v, want merge", decision)
	}
}
