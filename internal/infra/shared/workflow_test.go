package shared_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/checksource"
	"github.com/refusedguy/beacon/internal/infra/config"
	"github.com/refusedguy/beacon/internal/infra/shared"
)

// workflowFixture contains a plain job, a matrix job whose strategy also has
// a fail-fast key, and a job that depends on the matrix job. The matrix job
// must be absent from every resolver's list.
const workflowFixture = "name: CI\njobs:\n" +
	"  verify:\n    runs-on: ubuntu-latest\n" +
	"  test-modules:\n    runs-on: ubuntu-latest\n" +
	"    strategy:\n      fail-fast: false\n      matrix:\n        module: [cmd, pkg]\n" +
	"  test:\n    needs: test-modules\n    runs-on: ubuntu-latest\n"

func writeWorkflowFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(workflowFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
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
