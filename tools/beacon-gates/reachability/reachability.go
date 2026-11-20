// Package reachability implements the AST gate that finds exported adapters,
// providers, bridges, services and handlers with no caller in cmd/ or in the
// production flow wired from it. It also checks that every test file parses,
// sits in a valid package and is not disabled by a build tag or t.Skip.
package reachability

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

// Finding rules. REACH-UNWIRED is keyed by symbol ("pkg/Name") so that one
// exception row names one declaration, even when several live in one file.
const (
	ruleUnwired    = "REACH-UNWIRED"
	ruleTestIgnore = "REACH-TEST-IGNORE"
	ruleTestSkip   = "REACH-TEST-SKIP"
	ruleTestPkg    = "REACH-TEST-PKG"
	ruleTestParse  = "REACH-TEST-PARSE"
	ruleParse      = "REACH-PARSE"
)

const modulePath = "github.com/refusedguy/beacon/"

// nameSuffixes are the exported declaration suffixes this gate watches.
var nameSuffixes = []string{"Provider", "Adapter", "Bridge", "Service", "Handler"}

// decl is one top-level type, var, const or function. Methods are not nodes:
// they cannot be referenced by a bare identifier from another declaration.
type decl struct {
	pkg  string // declaring directory, slash separated
	name string
	kind token.Token // token.TYPE or token.FUNC for reachable targets
	refs []string
}

// source is the parsed shape of one .go file.
type source struct {
	rel         string
	dir         string
	pkgName     string
	isTest      bool
	aliases     map[string]string
	decl        []decl
	skipLines   []int
	buildIgnore bool
}

// analyse returns all reachability findings for root.
func analyse(root string) ([]gate.Finding, error) {
	files, err := parseTree(root)
	if err != nil {
		return nil, err
	}

	// One node per (package, name). cmd declarations are the seed of the
	// production flow: everything they reference is wired by definition.
	nodes := map[string]decl{}
	for _, f := range files {
		for _, d := range f.decl {
			nodes[d.pkg+"/"+d.name] = d
		}
	}
	reachable := map[string]bool{}
	queue := make([]string, 0, len(nodes))
	for key, d := range nodes {
		if strings.HasPrefix(d.pkg, "cmd/") || d.pkg == "cmd" {
			reachable[key] = true
			queue = append(queue, key)
		}
	}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		for _, ref := range nodes[key].refs {
			if _, ok := nodes[ref]; ok && !reachable[ref] {
				reachable[ref] = true
				queue = append(queue, ref)
			}
		}
	}

	var findings []gate.Finding
	// Declaration sites for targets, used to attach the source file.
	targetSites := map[string]gate.Finding{}
	for _, f := range files {
		if f.isTest {
			findings = append(findings, testFindings(f)...)
			continue
		}
		for _, d := range f.decl {
			if !isTarget(d.pkg, d.name, d.kind) {
				continue
			}
			key := d.pkg + "/" + d.name
			targetSites[key] = gate.Finding{
				Rule:    ruleUnwired,
				Path:    key,
				Message: fmt.Sprintf("declared in %s, no reference from cmd/ or connected production flow", f.rel),
			}
		}
	}
	for key, finding := range targetSites {
		if !reachable[key] {
			findings = append(findings, finding)
		}
	}

	// Package validity uses the non-test package name of each directory.
	dirPkg := map[string]string{}
	for _, f := range files {
		if !f.isTest && f.pkgName != "" {
			if _, ok := dirPkg[f.dir]; !ok {
				dirPkg[f.dir] = f.pkgName
			}
		}
	}
	for _, f := range files {
		if !f.isTest {
			continue
		}
		if f.pkgName == "" || f.pkgName == "_" {
			findings = append(findings, gate.Finding{
				Rule: ruleTestPkg, Path: f.rel,
				Message: fmt.Sprintf("invalid test package %q", f.pkgName),
			})
			continue
		}
		base, ok := dirPkg[f.dir]
		if !ok {
			continue // test-only directory: any valid package name is fine
		}
		if f.pkgName != base && f.pkgName != base+"_test" {
			findings = append(findings, gate.Finding{
				Rule: ruleTestPkg, Path: f.rel,
				Message: fmt.Sprintf("test package %q does not match %q in %s", f.pkgName, base, f.dir),
			})
		}
	}

	gate.SortFindings(findings)
	return findings, nil
}

// isTarget reports whether name is an exported Provider/Adapter/Bridge/
// Service/Handler declaration in the two upper layers.
func isTarget(pkg, name string, kind token.Token) bool {
	if kind != token.TYPE && kind != token.FUNC {
		return false
	}
	if !(strings.HasPrefix(pkg, "internal/infra") || strings.HasPrefix(pkg, "internal/app")) {
		return false
	}
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for _, suffix := range nameSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// testFindings turns the parsed test metadata into findings.
func testFindings(f *source) []gate.Finding {
	var out []gate.Finding
	if f.buildIgnore {
		out = append(out, gate.Finding{
			Rule: ruleTestIgnore, Path: f.rel,
			Message: "test file has a //go:build ignore build constraint",
		})
	}
	for _, line := range f.skipLines {
		out = append(out, gate.Finding{
			Rule: ruleTestSkip, Path: f.rel, Line: line,
			Message: "test is skipped with t.Skip/t.Skipf/t.SkipNow",
		})
	}
	return out
}

// parseTree walks root and parses every .go file outside vendored, generated
// and worktree trees.
func parseTree(root string) ([]*source, error) {
	var files []*source
	err := filepath.WalkDir(root, func(full string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if full != root {
				name := d.Name()
				if name == "worktrees" || name == "tmp" || name == "vendor" || strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(full, ".go") {
			return nil
		}
		f, err := parseFile(root, full)
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	return files, err
}

func parseFile(root, full string) (*source, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, full, nil, parser.ParseComments)
	rel, relErr := filepath.Rel(root, full)
	if relErr != nil {
		rel = full
	}
	rel = filepath.ToSlash(rel)
	isTest := strings.HasSuffix(rel, "_test.go")
	if err != nil {
		rule := ruleParse
		if isTest {
			rule = ruleTestParse
		}
		return nil, fmt.Errorf("%s at %s: %w", rule, rel, err)
	}

	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		dir = ""
	}
	out := &source{
		rel:     rel,
		dir:     dir,
		pkgName: file.Name.Name,
		isTest:  isTest,
		aliases: map[string]string{},
	}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasPrefix(p, modulePath) {
			continue
		}
		local := filepath.Base(p)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		out.aliases[local] = strings.TrimPrefix(p, modulePath)
	}

	for _, cg := range file.Comments {
		for _, c := range cg.List {
			t := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			body := strings.TrimSpace(strings.TrimPrefix(t, "go:build"))
			if body != t && body == "ignore" {
				out.buildIgnore = true
			}
			if strings.HasPrefix(t, "+build") && strings.Contains(t, "ignore") {
				out.buildIgnore = true
			}
		}
	}

	for _, d := range file.Decls {
		switch v := d.(type) {
		case *ast.FuncDecl:
			if v.Recv != nil {
				continue // methods are not bare-name reference targets
			}
			out.decl = append(out.decl, decl{
				pkg: dir, name: v.Name.Name, kind: token.FUNC, refs: collectRefs(v, out.aliases, dir),
			})
			if isTest {
				out.skipLines = append(out.skipLines, skipCalls(fset, v)...)
			}
		case *ast.GenDecl:
			for _, spec := range v.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					out.decl = append(out.decl, decl{
						pkg: dir, name: s.Name.Name, kind: token.TYPE, refs: collectRefs(s, out.aliases, dir),
					})
				case *ast.ValueSpec:
					for _, name := range s.Names {
						out.decl = append(out.decl, decl{
							pkg: dir, name: name.Name, kind: v.Tok, refs: collectRefs(s, out.aliases, dir),
						})
					}
				}
			}
		}
	}
	return out, nil
}

// collectRefs resolves the identifiers used by node. Selector expressions with
// an internal import alias become cross-package edges; bare identifiers stay
// in the current package. Field names and composite-literal keys are ignored
// because they are not package-level references.
func collectRefs(node ast.Node, aliases map[string]string, pkg string) []string {
	skip := map[*ast.Ident]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			skip[v.Sel] = true
			if id, ok := v.X.(*ast.Ident); ok {
				if _, isAlias := aliases[id.Name]; isAlias {
					skip[id] = true
				}
			}
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok {
				skip[id] = true
			}
		case *ast.Field:
			for _, name := range v.Names {
				skip[name] = true
			}
		}
		return true
	})
	refs := map[string]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				if target, ok := aliases[id.Name]; ok {
					refs[target+"/"+sel.Sel.Name] = true
				}
			}
		}
		if id, ok := n.(*ast.Ident); ok && !skip[id] {
			refs[pkg+"/"+id.Name] = true
		}
		return true
	})
	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	return out
}

// skipCalls reports the lines of t.Skip/t.Skipf/t.SkipNow calls in node.
func skipCalls(fset *token.FileSet, node ast.Node) []int {
	var lines []int
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Skip", "Skipf", "SkipNow":
		default:
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "t" || strings.HasPrefix(id.Name, "t")) {
			lines = append(lines, fset.Position(call.Pos()).Line)
		}
		return true
	})
	return lines
}

// Run executes the gate and returns a process exit code.
func Run(args []string) int {
	opts, err := gate.ParseOptions(args)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	ex, err := gate.LoadReachabilityExceptions()
	if err != nil {
		return gate.Brokenf("load exceptions: %v", err)
	}
	findings, err := analyse(opts.Root)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	findings = ex.Apply(findings, opts.Root)
	return gate.Finish(findings, opts.MinFindings)
}

// SelfTestRun runs the planted-defect fixtures.
func SelfTestRun() int { return selfTestRun() }
