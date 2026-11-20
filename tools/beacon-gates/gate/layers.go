package gate

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed config/layers.json
var layersJSON []byte

// MatrixLayer is one row of the layer matrix. Name is a path prefix: a
// package whose module-relative path equals Name or starts with Name+"/"
// belongs to this layer. Rank grows outward: domain is innermost, cmd is
// outermost. tools/beacon-gates shares infra's rank because it is a tool,
// not a production layer.
type MatrixLayer struct {
	Name string `json:"name"`
	Rank int    `json:"rank"`
}

// AllowedEntry is one permission in the matrix. The source of truth is the
// explicit edge list, not the cross product of ranks: an unused permission
// is a finding, so the config must contain only edges the tree really uses.
// Shared marks a facade edge into a leaf package that every layer may import.
// It is validated like any other edge, but excluded from liveness checks: a
// facade is allowed to have no callers in a given tree.
type AllowedEntry struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Shared bool   `json:"shared,omitempty"`
}

type Matrix struct {
	Layers  []MatrixLayer  `json:"layers"`
	Allowed []AllowedEntry `json:"allowed"`
	byName  map[string]MatrixLayer
	allowed map[[2]string]bool
}

// LoadMatrix reads the matrix as data from the embedded config file. It also
// enforces ADR 0005 on the config itself: every listed edge must point from a
// higher rank to a strictly lower one, so the matrix can never encode an
// inward-to-outward dependency.
func LoadMatrix() (*Matrix, error) {
	var m Matrix
	if err := json.Unmarshal(layersJSON, &m); err != nil {
		return nil, fmt.Errorf("parse layer matrix: %w", err)
	}
	m.byName = make(map[string]MatrixLayer, len(m.Layers))
	for _, l := range m.Layers {
		if l.Name == "" || strings.HasSuffix(l.Name, "/") {
			return nil, fmt.Errorf("invalid layer name %q in matrix", l.Name)
		}
		if _, dup := m.byName[l.Name]; dup {
			return nil, fmt.Errorf("duplicate layer %q in matrix", l.Name)
		}
		m.byName[l.Name] = l
	}
	m.allowed = make(map[[2]string]bool, len(m.Allowed))
	for _, e := range m.Allowed {
		from, okFrom := m.byName[e.From]
		to, okTo := m.byName[e.To]
		if !okFrom || !okTo {
			return nil, fmt.Errorf("allowed edge %q -> %q references an unknown layer", e.From, e.To)
		}
		if to.Rank >= from.Rank {
			return nil, fmt.Errorf("allowed edge %q -> %q violates ADR 0005: inward rank must decrease", e.From, e.To)
		}
		key := [2]string{e.From, e.To}
		if m.allowed[key] {
			return nil, fmt.Errorf("duplicate allowed edge %q -> %q in matrix", e.From, e.To)
		}
		m.allowed[key] = true
	}
	return &m, nil
}

// LayerOf resolves a module-relative package path to its layer using the
// longest matching prefix. Prefix matching is what makes nested packages such
// as internal/app/taskcli inherit their parent's layer (ADR 0007).
func (m *Matrix) LayerOf(path string) (MatrixLayer, bool) {
	var best MatrixLayer
	found := false
	for _, l := range m.Layers {
		if path == l.Name || strings.HasPrefix(path, l.Name+"/") {
			if !found || len(l.Name) > len(best.Name) {
				best = l
				found = true
			}
		}
	}
	return best, found
}

// Has reports whether a package path is covered by any matrix row.
func (m *Matrix) Has(name string) bool {
	_, ok := m.LayerOf(name)
	return ok
}

// Allows reports whether importer may import imported. Both sides are
// resolved through prefixes, then the pair is looked up in the explicit
// permission set.
func (m *Matrix) Allows(importer, imported string) bool {
	a, okA := m.LayerOf(importer)
	b, okB := m.LayerOf(imported)
	if !okA || !okB {
		return false
	}
	return m.allowed[[2]string{a.Name, b.Name}]
}

// AllowedEdges returns every ordinary permission in the matrix, sorted.
// Facade edges are excluded because they are not subject to liveness checks.
func (m *Matrix) AllowedEdges() [][2]string {
	out := make([][2]string, 0, len(m.Allowed))
	for _, e := range m.Allowed {
		if e.Shared {
			continue
		}
		out = append(out, [2]string{e.From, e.To})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// Edge is one importer -> imported pair with its source location.
type Edge struct {
	From string
	To   string
	File string
	Line int
}

// Key ignores the location so two independent passes can be compared.
func (e Edge) Key() string { return e.From + "\x00" + e.To }

// ReadModulePath reads the module directive of a go.mod.
func ReadModulePath(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`), nil
		}
	}
	return "", fmt.Errorf("no module directive in go.mod")
}

func moduleRel(importPath, modulePath string) (string, bool) {
	if importPath == modulePath {
		return ".", true
	}
	if strings.HasPrefix(importPath, modulePath+"/") {
		return strings.TrimPrefix(importPath, modulePath+"/"), true
	}
	return "", false
}

// FindImportLine locates a quoted import path inside a package directory.
// go list reports a package directory, not a line; this closes that gap.
func FindImportLine(root, dir, importPath string) (string, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return relPath(root, dir), 1
	}
	needle := `"` + importPath + `"`
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, needle) {
				return relPath(root, filepath.Join(dir, e.Name())), i + 1
			}
		}
	}
	return relPath(root, dir), 1
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// skippedByPolicy reports whether a module-relative path lives under tmp/.
// tmp holds local spikes and is listed in .gitignore, so neither layer pass
// treats it as part of the committed tree.
func skippedByPolicy(rel string) bool {
	return rel == "tmp" || strings.HasPrefix(rel, "tmp/")
}

// ListEdges discovers layer edges with "go list -deps -json ./...".
func ListEdges(root string, m *Matrix) ([]Edge, []string, error) {
	modulePath, err := ReadModulePath(root)
	if err != nil {
		return nil, nil, err
	}
	// -e keeps reporting imports when a planted edge creates a cycle. A cycle
	// is a finding for this gate, not a reason to stop the scan.
	cmd := exec.Command("go", "list", "-e", "-deps", "-json", "./...")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("go list failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var edges []Edge
	var pkgs []string
	seen := map[string]bool{}
	dec := json.NewDecoder(&stdout)
	for {
		var p struct {
			ImportPath string
			Imports    []string
			Dir        string
		}
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("decode go list output: %w", err)
		}
		rel, ok := moduleRel(p.ImportPath, modulePath)
		if !ok || skippedByPolicy(rel) || seen[rel] {
			continue
		}
		seen[rel] = true
		pkgs = append(pkgs, rel)
		for _, imp := range p.Imports {
			to, ok := moduleRel(imp, modulePath)
			if !ok || skippedByPolicy(to) || !m.Has(to) {
				continue
			}
			file, line := FindImportLine(root, p.Dir, imp)
			edges = append(edges, Edge{From: rel, To: to, File: file, Line: line})
		}
	}
	sort.Strings(pkgs)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Key() != edges[j].Key() {
			return edges[i].Key() < edges[j].Key()
		}
		if edges[i].File != edges[j].File {
			return edges[i].File < edges[j].File
		}
		return edges[i].Line < edges[j].Line
	})
	return edges, pkgs, nil
}

// ASTEdges discovers the same edges with go/parser over import blocks.
// Test files are excluded, matching go list -deps without -test. Directories
// that contain their own go.mod are skipped: go list ./... does not descend
// into nested modules either, so both passes must scan the same package set.
func ASTEdges(root string, m *Matrix) ([]Edge, []string, error) {
	modulePath, err := ReadModulePath(root)
	if err != nil {
		return nil, nil, err
	}
	var edges []Edge
	var pkgs []string
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root {
				name := d.Name()
				if name == "tmp" || name == "vendor" || strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relFile := relPath(root, path)
		relDir := filepath.ToSlash(filepath.Dir(relFile))
		if !seen[relDir] {
			seen[relDir] = true
			pkgs = append(pkgs, relDir)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", relFile, err)
		}
		for _, spec := range file.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				imp = strings.Trim(spec.Path.Value, `"`)
			}
			to, ok := moduleRel(imp, modulePath)
			if !ok || !m.Has(to) {
				continue
			}
			edges = append(edges, Edge{
				From: relDir,
				To:   to,
				File: relFile,
				Line: fset.Position(spec.Pos()).Line,
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(pkgs)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Key() != edges[j].Key() {
			return edges[i].Key() < edges[j].Key()
		}
		if edges[i].File != edges[j].File {
			return edges[i].File < edges[j].File
		}
		return edges[i].Line < edges[j].Line
	})
	return edges, pkgs, nil
}

// CheckMatrix finds packages without a row, forbidden edges and unused
// permissions. It is shared by both import passes; the passes differ only in
// how edges are discovered. Liveness is tracked per resolved layer pair so
// that a permission used through a subpackage counts as used.
func CheckMatrix(m *Matrix, edges []Edge, pkgs []string) []Finding {
	var fs []Finding
	noRule := map[string]bool{}
	for _, p := range pkgs {
		if !m.Has(p) && !noRule[p] {
			noRule[p] = true
			fs = append(fs, Finding{
				Rule:    "LAYER-NO-RULE",
				Path:    p,
				Line:    1,
				Message: fmt.Sprintf("package %s has no row in the layer matrix", p),
			})
		}
	}
	observed := map[string]bool{}
	for _, e := range edges {
		a, okA := m.LayerOf(e.From)
		b, okB := m.LayerOf(e.To)
		if !okA || !okB {
			continue
		}
		observed[a.Name+"\x00"+b.Name] = true
		if !m.Allows(e.From, e.To) {
			fs = append(fs, Finding{
				Rule:    "LAYER-FORBIDDEN-EDGE",
				Path:    e.File,
				Line:    e.Line,
				Message: fmt.Sprintf("%s imports %s against the layer matrix", e.From, e.To),
			})
		}
	}
	for _, pair := range m.AllowedEdges() {
		key := pair[0] + "\x00" + pair[1]
		if !observed[key] {
			fs = append(fs, Finding{
				Rule:    "LAYER-UNUSED-RULE",
				Path:    pair[0],
				Line:    0,
				Message: fmt.Sprintf("allowed edge %s -> %s is unused", pair[0], pair[1]),
			})
		}
	}
	SortFindings(fs)
	return fs
}

// EdgeDiff returns sorted keys present in only one of the two layer-edge
// sets. A non-empty diff is a gate error, not a difference of opinions.
func EdgeDiff(a, b []Edge, m *Matrix) []string {
	set := func(edges []Edge) map[string]bool {
		out := map[string]bool{}
		for _, e := range edges {
			if m.Has(e.From) && m.Has(e.To) {
				out[e.Key()] = true
			}
		}
		return out
	}
	as, bs := set(a), set(b)
	keys := map[string]bool{}
	for k := range as {
		if !bs[k] {
			keys["AST-only: "+strings.ReplaceAll(k, "\x00", " -> ")] = true
		}
	}
	for k := range bs {
		if !as[k] {
			keys["go-list-only: "+strings.ReplaceAll(k, "\x00", " -> ")] = true
		}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
