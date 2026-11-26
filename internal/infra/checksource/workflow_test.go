package checksource

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
	if want := []string{"test", "verify"}; !reflect.DeepEqual(got, want) {
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
