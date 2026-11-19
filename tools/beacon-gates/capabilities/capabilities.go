// Package capabilities implements the capabilities gate from ADR 0005.
package capabilities

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

type capability struct {
	name       string
	importPath string
	onImport   bool
	funcs      []string
}

// table lists capabilities that only internal/infra may use.
var table = []capability{
	{name: "EXEC", importPath: "os/exec", onImport: true},
	{name: "FILE", importPath: "os", funcs: []string{
		"Open", "OpenFile", "Create", "ReadFile", "WriteFile", "Mkdir",
		"MkdirAll", "Remove", "RemoveAll", "Rename", "Stat", "Truncate",
	}},
	{name: "FILE", importPath: "io/ioutil", onImport: true},
	{name: "NETHTTP", importPath: "net/http", onImport: true},
	{name: "NETDIAL", importPath: "net", funcs: []string{"Dial", "DialTimeout", "DialContext"}},
	{name: "PLUGIN", importPath: "plugin", onImport: true},
	{name: "REFLECT", importPath: "reflect", onImport: true},
	{name: "UNSAFE", importPath: "unsafe", onImport: true},
}

func layerOf(dir string) string {
	switch {
	case dir == "internal/infra" || strings.HasPrefix(dir, "internal/infra/"):
		return "infra"
	case dir == "internal/domain" || strings.HasPrefix(dir, "internal/domain/"):
		return "domain"
	case dir == "internal/app" || strings.HasPrefix(dir, "internal/app/"):
		return "app"
	case dir == "cmd/beacon" || strings.HasPrefix(dir, "cmd/beacon/"):
		return "cmd"
	default:
		return "outside"
	}
}

// analyse walks root and reports capabilities used outside internal/infra.
func analyse(root string, ex gate.Exceptions) ([]gate.Finding, error) {
	var findings []gate.Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "tmp" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := relSlash(root, path)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		layer := layerOf(filepath.ToSlash(filepath.Dir(rel)))
		if layer == "infra" {
			return nil
		}
		aliases := map[string]string{}
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			local := filepath.Base(p)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			aliases[local] = p
		}
		add := func(capName string, node ast.Node, msg string) {
			findings = append(findings, gate.Finding{
				Rule:    "CAP-" + capName + "-IN-" + strings.ToUpper(layer),
				Path:    rel,
				Line:    fset.Position(node.Pos()).Line,
				Message: msg,
			})
		}
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			for _, c := range table {
				if c.onImport && c.importPath == p {
					add(c.name, imp, fmt.Sprintf("%s use of %s is not allowed in %s", c.name, p, layer))
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			p, ok := aliases[ident.Name]
			if !ok {
				return true
			}
			for _, c := range table {
				if c.onImport || c.importPath != p {
					continue
				}
				for _, fn := range c.funcs {
					if sel.Sel.Name == fn {
						add(c.name, call, fmt.Sprintf("%s call %s.%s is not allowed in %s", c.name, ident.Name, fn, layer))
						break
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	findings = ex.Apply(findings, root)
	gate.SortFindings(findings)
	return findings, nil
}

func relSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// Run executes the gate and returns a process exit code.
func Run(args []string) int {
	opts, err := gate.ParseOptions(args)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	ex, err := gate.LoadExceptions()
	if err != nil {
		return gate.Brokenf("load exceptions: %v", err)
	}
	findings, err := analyse(opts.Root, ex)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	return gate.Finish(findings, opts.MinFindings)
}

// SelfTestRun runs the planted-defect fixtures.
func SelfTestRun() int { return selfTestRun() }
