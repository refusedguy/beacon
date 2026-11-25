package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const maxProductionLines = 350
const maxFunctionComplexity = 20

// complexityGrandfathered exempts two functions from the complexity limit
// until they are decomposed.
var complexityGrandfathered = map[string]bool{
	"internal/app/reconciler.go:Reconcile":                true,
	"internal/infra/tasksource/markdown.go:rewriteStatus": true,
}

var skippedDirs = map[string]bool{
	".git": true, "worktrees": true, ".gocache": true,
	".gotmp": true, "tmp": true, "node_modules": true,
}

func goFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, root := range []string{".", "harnesses/opencode", "ui/tui"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if skippedDirs[info.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") {
				files = append(files, filepath.ToSlash(path))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, spec := range f.Imports {
		v, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func isStdlib(imp string) bool {
	return !strings.Contains(strings.Split(imp, "/")[0], ".")
}

func TestProductionFileSize(t *testing.T) {
	for _, path := range goFiles(t) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Count(string(data), "\n") + 1
		if lines > maxProductionLines {
			t.Logf("ADVISORY: %s has %d lines, consider decomposition", path, lines)
		}
	}
}

func functionComplexity(fn *ast.FuncDecl) int {
	complexity := 1
	if fn.Body == nil {
		return complexity
	}
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch expr := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			complexity++
		case *ast.CaseClause:
			if expr.List != nil {
				complexity++
			}
		case *ast.CommClause:
			if expr.Comm != nil {
				complexity++
			}
		case *ast.BinaryExpr:
			if expr.Op == token.LAND || expr.Op == token.LOR {
				complexity++
			}
		}
		return true
	})
	return complexity
}

func TestProductionFunctionComplexity(t *testing.T) {
	for _, path := range goFiles(t) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			complexity := functionComplexity(fn)
			if complexity <= maxFunctionComplexity {
				continue
			}
			if complexityGrandfathered[path+":"+fn.Name.Name] {
				continue
			}
			t.Errorf("%s: %s has cyclomatic complexity %d, limit is %d", path, fn.Name.Name, complexity, maxFunctionComplexity)
		}
	}
}

func TestCoreImportsOnlyStdlib(t *testing.T) {
	for _, path := range goFiles(t) {
		if !strings.HasPrefix(path, "pkg/core/") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if !isStdlib(imp) {
				t.Errorf("pkg/core file %s imports %s", path, imp)
			}
		}
	}
}

func TestAppDoesNotImportInfraOrCmd(t *testing.T) {
	for _, path := range goFiles(t) {
		if !strings.HasPrefix(path, "internal/app/") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if strings.Contains(imp, "/internal/infra") || strings.Contains(imp, "/cmd/") {
				t.Errorf("internal/app file %s imports %s", path, imp)
			}
		}
	}
}

func TestNoSiblingInfraImports(t *testing.T) {
	prefix := "github.com/refusedguy/beacon/internal/infra/"
	for _, path := range goFiles(t) {
		if !strings.HasPrefix(path, "internal/infra/") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(path, "internal/infra/"), "/")
		if len(parts) < 2 {
			continue
		}
		owner := parts[0]
		for _, imp := range importsOf(t, path) {
			if !strings.HasPrefix(imp, prefix) {
				continue
			}
			target := strings.Split(strings.TrimPrefix(imp, prefix), "/")[0]
			if target != owner && target != "shared" {
				t.Errorf("%s imports sibling %s", path, imp)
			}
		}
	}
}

func TestSingleRedactHandler(t *testing.T) {
	count := 0
	for _, path := range goFiles(t) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "type redactHandler") {
			continue
		}
		count++
		if path != "internal/infra/shared/logger.go" {
			t.Errorf("redactHandler declared outside shared in %s", path)
		}
	}
	if count != 1 {
		t.Errorf("found %d redactHandler declarations, want 1", count)
	}
}

func TestSecretPatternsOnlyInRedact(t *testing.T) {
	markers := []string{"gh[pousr]_", "github_pat_", "xox[baprs]", "AIza[A-Za-z0-9", "[0-9]+:[A-Za-z0-9_-]{35}"}
	for _, path := range goFiles(t) {
		if strings.HasSuffix(path, "_test.go") || path == "arch_test.go" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range markers {
			if strings.Contains(string(data), marker) && path != "pkg/core/redact/redact.go" {
				t.Errorf("secret pattern %q found outside redact in %s", marker, path)
			}
		}
	}
}

func TestExternalModulesDoNotImportInternal(t *testing.T) {
	for _, path := range goFiles(t) {
		if !strings.HasPrefix(path, "harnesses/") && !strings.HasPrefix(path, "ui/") {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if strings.HasPrefix(imp, "github.com/refusedguy/beacon/internal/") {
				t.Errorf("external module %s imports %s", path, imp)
			}
		}
	}
}
