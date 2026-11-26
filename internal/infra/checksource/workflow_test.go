package checksource

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/config"
)

func TestWorkflowNamesParsesJobNames(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "name: CI\non: push\njobs:\n  verify:\n    runs-on: ubuntu-latest\n  test:\n    strategy:\n      matrix:\n        go: ['1.22']\n"
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := (Workflow{Root: root}).Names(context.Background())
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	// The matrix job is omitted: GitHub reports only expanded cell names.
	if want := []string{"verify"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
}

func TestWorkflowNamesUsesInjectedDetect(t *testing.T) {
	var gotRoot string
	src := Workflow{Root: "/project", Detect: func(root string) []string {
		gotRoot = root
		return []string{"alpha", "beta"}
	}}
	got, err := src.Names(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != "/project" {
		t.Fatalf("detect root = %q, want /project", gotRoot)
	}
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
}

func TestWorkflowNamesAgreeWithConfigDetector(t *testing.T) {
	// config and checksource keep two copies of the workflow parser. Both
	// adapters must return the same names for one fixture, fail-fast matrix
	// jobs included, or the daemon and the operator would gate on different
	// check sets.
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(dir): %v", err)
	}
	content := "name: CI\njobs:\n" +
		"  verify:\n    runs-on: ubuntu-latest\n" +
		"  test-modules:\n    runs-on: ubuntu-latest\n" +
		"    strategy:\n      fail-fast: false\n      matrix:\n        module: [cmd, pkg]\n" +
		"  build:\n    runs-on: ubuntu-latest\n"
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile(ci.yml): %v", err)
	}
	got, err := (Workflow{Root: root}).Names(context.Background())
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	want := config.DetectRequiredChecks(root)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checksource names = %v, config detection = %v", got, want)
	}
	if want := []string{"build", "verify"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}
