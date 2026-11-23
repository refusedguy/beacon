package projection

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// forbiddenUIImports are the concrete layers a UI must never reach. The UI
// reads projections and sends decisions; it does not open a store or call a
// transport directly.
var forbiddenUIImports = []string{
	"github.com/refusedguy/beacon/internal/infra/store",
	"github.com/refusedguy/beacon/internal/infra/cibridge",
	"github.com/refusedguy/beacon/internal/infra/mcp",
}

type importSite struct {
	path string
	at   string
}

// A UI can implement all three ports without importing a transport or a
// store. The fakes exist only for these compile-time assertions.
type fakeProvider struct{}

func (fakeProvider) Snapshot(context.Context) (Snapshot[int, string, int], error) {
	return Snapshot[int, string, int]{}, nil
}

type fakeSink struct{}

func (fakeSink) Apply(context.Context, Decision) (DecisionResult, error) {
	return DecisionResult{}, nil
}

type fakeSource struct{}

func (fakeSource) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event)
	return ch, func() { close(ch) }
}

func (fakeSource) Since(uint64) []Event { return nil }

var (
	_ StateProvider[Snapshot[int, string, int]] = fakeProvider{}
	_ DecisionSink[Decision, DecisionResult]    = fakeSink{}
	_ EventSource[Event]                        = fakeSource{}
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s: %v", root, err)
	}
	return root
}

func importsOfDir(t *testing.T, dir string) []importSite {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []importSite
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("import in %s: %v", path, err)
			}
			out = append(out, importSite{path: p, at: fset.Position(spec.Pos()).String()})
		}
	}
	return out
}

func TestUIDoesNotImportInfrastructureLayers(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal/tui"} {
		for _, site := range importsOfDir(t, filepath.Join(root, filepath.FromSlash(dir))) {
			for _, bad := range forbiddenUIImports {
				if site.path == bad || strings.HasPrefix(site.path, bad+"/") {
					t.Errorf("%s imports %s at %s", dir, site.path, site.at)
				}
			}
		}
	}
}

func TestProjectionDependsOnStdlibOnly(t *testing.T) {
	root := repoRoot(t)
	for _, site := range importsOfDir(t, filepath.Join(root, "internal", "domain", "projection")) {
		if strings.HasPrefix(site.path, "github.com/refusedguy/beacon/") {
			t.Errorf("projection imports %s at %s", site.path, site.at)
		}
	}
}

func TestHTTPAdapterHasNoSecondDefinition(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "infra", "httpapi", "server.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	owned := map[string]bool{
		"Snapshot": true, "Decision": true, "DecisionResult": true,
		"StateProvider": true, "DecisionSink": true, "EventSource": true, "Event": true,
	}
	imported := false
	for _, site := range importsOfDir(t, filepath.Dir(path)) {
		if site.path == "github.com/refusedguy/beacon/internal/domain/projection" {
			imported = true
		}
	}
	if !imported {
		t.Error("httpapi does not import projection")
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || !owned[ts.Name.Name] {
				continue
			}
			if !ts.Assign.IsValid() {
				t.Errorf("%s redeclares %s at %s", filepath.Base(path), ts.Name.Name, fset.Position(ts.Pos()))
			}
		}
	}
}
