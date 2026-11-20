// Package gate holds the shared contract for the beacon-gates binary:
// exit codes, findings, fixture helpers and flag parsing.
package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Exit codes from ADR 0016.
const (
	Green  = 0
	Red    = 1
	Broken = 2
)

// Finding names the rule, the file, the line and the reason.
type Finding struct {
	Rule    string
	Path    string
	Line    int
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", f.Rule, f.Path, f.Line, f.Message)
}

// SortFindings puts findings in a deterministic order.
func SortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Path != fs[j].Path {
			return fs[i].Path < fs[j].Path
		}
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		return fs[i].Rule < fs[j].Rule
	})
}

// HasRule reports whether rule is present at all.
func HasRule(fs []Finding, rule string) bool {
	for _, f := range fs {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// HasRuleAt reports whether rule is present at the exact path and line.
func HasRuleAt(fs []Finding, rule, path string, line int) bool {
	for _, f := range fs {
		if f.Rule == rule && f.Path == path && f.Line == line {
			return true
		}
	}
	return false
}

// Fixture creates a temp tree from relative-path-to-content pairs.
// The caller owns the tree and must remove it.
func Fixture(files map[string]string) (string, error) {
	root, err := os.MkdirTemp("", "beacon-gates-*")
	if err != nil {
		return "", err
	}
	for rel, content := range files {
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			os.RemoveAll(root)
			return "", err
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			os.RemoveAll(root)
			return "", err
		}
	}
	return root, nil
}

// Options is the flag set shared by all three gates.
type Options struct {
	Root        string
	MinFindings int
}

// ParseOptions parses -root and -min-findings in both "-flag value" and
// "-flag=value" forms. Unknown flags and non-numeric values are startup errors.
func ParseOptions(args []string) (Options, error) {
	o := Options{Root: "."}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-root" || a == "--root":
			if i+1 >= len(args) {
				return o, fmt.Errorf("missing value for %s", a)
			}
			i++
			o.Root = args[i]
		case strings.HasPrefix(a, "-root=") || strings.HasPrefix(a, "--root="):
			o.Root = strings.SplitN(a, "=", 2)[1]
		case a == "-min-findings" || a == "--min-findings":
			if i+1 >= len(args) {
				return o, fmt.Errorf("missing value for %s", a)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return o, fmt.Errorf("non-numeric -min-findings: %q", args[i])
			}
			o.MinFindings = n
		case strings.HasPrefix(a, "-min-findings=") || strings.HasPrefix(a, "--min-findings="):
			v := strings.SplitN(a, "=", 2)[1]
			n, err := strconv.Atoi(v)
			if err != nil {
				return o, fmt.Errorf("non-numeric -min-findings: %q", v)
			}
			o.MinFindings = n
		default:
			return o, fmt.Errorf("unknown flag %q", a)
		}
	}
	if o.MinFindings < 0 {
		return o, fmt.Errorf("-min-findings must be >= 0")
	}
	return o, nil
}

// Finish prints findings and returns Green or Red. A scan that sees fewer
// findings than the declared floor gets SCAN-BLIND instead of a clean verdict.
func Finish(fs []Finding, minFindings int) int {
	if len(fs) < minFindings {
		fs = append(fs, Finding{
			Rule:    "SCAN-BLIND",
			Path:    ".",
			Line:    0,
			Message: fmt.Sprintf("scan saw %d finding(s), floor is %d", len(fs), minFindings),
		})
	}
	SortFindings(fs)
	if len(fs) == 0 {
		fmt.Println("green: no findings")
		return Green
	}
	for _, f := range fs {
		fmt.Println(f.String())
	}
	fmt.Printf("red: %d finding(s)\n", len(fs))
	return Red
}

// RemoveFixture deletes a tree created by Fixture. Gate self-tests call this
// so the only os.RemoveAll in tools/beacon-gates stays in this covered file.
func RemoveFixture(root string) { _ = os.RemoveAll(root) }

// Brokenf prints a startup error to stderr and returns the Broken code.
func Brokenf(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "broken: "+format+"\n", args...)
	return Broken
}
