// Command tools-list-dump prints a candidate size and SHA-256 for tools/list.
//
// It reads the descriptor table in internal/infra/mcp/server.go with go/parser.
// The candidate is derived from descriptor names and descriptions, so editing a
// description changes the fingerprint. The command never writes to
// internal/infra/mcp/contract_test.go: the hand-written golden stays an
// independent witness. --write updates only the candidate fixture, and --check
// compares against that fixture without writing anything.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	descriptorDir  = "internal/infra/mcp"
	descriptorFile = "internal/infra/mcp/server.go"
	fixtureRel     = "tools/beacon-gates/tools-list-dump/testdata/tools-list-candidate.txt"
	rootModule     = "module github.com/refusedguy/beacon\n"
)

const (
	green  = 0
	red    = 1
	broken = 2
)

// descriptor is the part of a toolDescriptor that feeds the candidate.
type descriptor struct {
	Name        string
	Description string
}

// findRoot walks up from start to the main module root. The gates module has a
// go.mod of its own, so a bare "current directory" is not a reliable root.
func findRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		data, err := fs.ReadFile(os.DirFS(dir), "go.mod")
		if err == nil && strings.HasPrefix(string(data), rootModule) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no main module go.mod found above %s", start)
		}
		dir = parent
	}
}

// evalString evaluates a string expression with the constants declared in the
// descriptor package. Only string literals, identifier references and "+"
// concatenation are supported. Those are the forms the descriptor table uses.
func evalString(expr ast.Expr, consts map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := consts[e.Name]
		return v, ok
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, okLeft := evalString(e.X, consts)
		right, okRight := evalString(e.Y, consts)
		if !okLeft || !okRight {
			return "", false
		}
		return left + right, true
	default:
		return "", false
	}
}

// loadConstants collects string constants from the descriptor package. A few
// descriptors use a shared constant for their description, so the table alone
// is not enough to build a candidate.
func loadConstants(root string) (map[string]string, error) {
	dir := filepath.Join(root, filepath.FromSlash(descriptorDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read descriptor directory: %w", err)
	}

	consts := map[string]string{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := fs.ReadFile(os.DirFS(dir), entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		file, err := parser.ParseFile(fset, entry.Name(), data, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					if text, ok := evalString(value.Values[i], consts); ok {
						consts[name.Name] = text
					}
				}
			}
		}
	}
	return consts, nil
}

// loadDescriptors extracts name and description from the toolDescriptors
// composite literal. It does not import the package, so it stays stdlib-only.
func loadDescriptors(root string) ([]descriptor, error) {
	consts, err := loadConstants(root)
	if err != nil {
		return nil, err
	}
	data, err := fs.ReadFile(os.DirFS(root), filepath.FromSlash(descriptorFile))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", descriptorFile, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, descriptorFile, data, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", descriptorFile, err)
	}

	var out []descriptor
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Values) == 0 {
				continue
			}
			isTable := false
			for _, name := range value.Names {
				if name.Name == "toolDescriptors" {
					isTable = true
				}
			}
			if !isTable {
				continue
			}
			lit, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, fmt.Errorf("%s: toolDescriptors is not a composite literal", descriptorFile)
			}
			for _, element := range lit.Elts {
				entry, ok := element.(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("%s: toolDescriptors has a non-struct element", descriptorFile)
				}
				var d descriptor
				for _, field := range entry.Elts {
					kv, ok := field.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					text, ok := evalString(kv.Value, consts)
					if !ok {
						continue
					}
					switch key.Name {
					case "Name":
						d.Name = text
					case "Description":
						d.Description = text
					}
				}
				if d.Name == "" || d.Description == "" {
					return nil, fmt.Errorf("%s: descriptor with an empty name or description", descriptorFile)
				}
				out = append(out, d)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no toolDescriptors table found", descriptorFile)
	}
	return out, nil
}

// candidateText renders one sorted name/description pair per line. The format
// is plain UTF-8 text, so its size and SHA-256 are reproducible on every
// platform without JSON escaping rules.
func candidateText(descriptors []descriptor) []byte {
	sorted := append([]descriptor(nil), descriptors...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var b strings.Builder
	for _, d := range sorted {
		b.WriteString(d.Name)
		b.WriteByte('\t')
		b.WriteString(d.Description)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// fingerprint returns the byte size and the hex SHA-256 of a candidate.
func fingerprint(data []byte) (int, string) {
	sum := sha256.Sum256(data)
	return len(data), hex.EncodeToString(sum[:])
}

func readFixture(root string) ([]byte, error) {
	return fs.ReadFile(os.DirFS(root), filepath.FromSlash(fixtureRel))
}

func writeFixture(root string, data []byte) error {
	path := filepath.Join(root, filepath.FromSlash(fixtureRel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// compare reports a mismatch between a candidate and the committed fixture.
// It never writes to either side.
func compare(candidate, committed []byte) error {
	if bytes.Equal(candidate, committed) {
		return nil
	}
	gotSize, gotSHA := fingerprint(candidate)
	wantSize, wantSHA := fingerprint(committed)
	return fmt.Errorf("tools/list candidate drift: got %d bytes sha256=%s, committed %d bytes sha256=%s", gotSize, gotSHA, wantSize, wantSHA)
}

// plantedCandidate changes one description in memory. The self-test uses it to
// prove the fingerprint and the check are not vacuous.
func plantedCandidate(descriptors []descriptor) []byte {
	modified := append([]descriptor(nil), descriptors...)
	modified[0].Description += " PLANTED-DESCRIPTION"
	return candidateText(modified)
}

// selfTest runs the planted-discrepancy control without the testing package.
func selfTest(root string) error {
	descriptors, err := loadDescriptors(root)
	if err != nil {
		return err
	}
	base := candidateText(descriptors)
	planted := plantedCandidate(descriptors)
	if bytes.Equal(base, planted) {
		return fmt.Errorf("self-test failed: planted description did not change the candidate")
	}
	if err := compare(planted, base); err == nil {
		return fmt.Errorf("self-test failed: planted candidate passed the check")
	}
	_, baseSHA := fingerprint(base)
	_, plantedSHA := fingerprint(planted)
	if baseSHA == plantedSHA {
		return fmt.Errorf("self-test failed: planted candidate kept the same SHA-256")
	}
	return nil
}

func run(args []string) int {
	var root string
	var checkMode, writeMode, printMode, testMode bool
	flags := flag.NewFlagSet("tools-list-dump", flag.ContinueOnError)
	flags.StringVar(&root, "root", "", "repository root (auto-detected when empty)")
	flags.BoolVar(&checkMode, "check", false, "compare the candidate with the committed fixture; never write")
	flags.BoolVar(&writeMode, "write", false, "regenerate the candidate fixture only")
	flags.BoolVar(&printMode, "print", false, "print the candidate size and SHA-256")
	flags.BoolVar(&testMode, "self-test", false, "run the planted-discrepancy control")
	if err := flags.Parse(args); err != nil {
		return broken
	}
	if root == "" {
		detected, err := findRoot(".")
		if err != nil {
			fmt.Fprintf(os.Stderr, "tools-list-dump: %v\n", err)
			return broken
		}
		root = detected
	}

	descriptors, err := loadDescriptors(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tools-list-dump: %v\n", err)
		return broken
	}
	candidate := candidateText(descriptors)
	size, sha := fingerprint(candidate)

	switch {
	case testMode:
		if err := selfTest(root); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return red
		}
		fmt.Println("self-test passed: planted description changes the candidate and fails the check")
		return green
	case writeMode:
		if err := writeFixture(root, candidate); err != nil {
			fmt.Fprintf(os.Stderr, "tools-list-dump: write fixture: %v\n", err)
			return broken
		}
		fmt.Printf("tools-list candidate fixture written: %d bytes sha256=%s\n", size, sha)
		return green
	case checkMode:
		committed, err := readFixture(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tools-list-dump: read fixture: %v\n", err)
			return broken
		}
		if err := compare(candidate, committed); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return red
		}
		fmt.Printf("tools-list candidate matches committed fixture: %d bytes sha256=%s\n", size, sha)
		return green
	default:
		fmt.Printf("tools-list candidate: %d bytes sha256=%s\n", size, sha)
		return green
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}
