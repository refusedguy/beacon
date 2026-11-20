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
	"unicode"
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

// FieldValue returns the trimmed value and 1-based line of the first
// "Key: value" line in lines, or ("", 0) when the key is absent. The wiring
// gate and the docs gate use the same field grammar so a card validated by one
// reads the same way in the other.
func FieldValue(lines []string, key string) (string, int) {
	prefix := key + ":"
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), i + 1
		}
	}
	return "", 0
}

// HasSection reports whether lines contain a trimmed markdown heading equal
// to heading, for example "## Причина".
func HasSection(lines []string, heading string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == heading {
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

// Rules reported by CheckLinks. A row in config/baseline.json with one of
// these rules suppresses the matching source file.
const (
	linkPathRule   = "DOC-LINK-PATH"
	linkAnchorRule = "DOC-LINK-ANCHOR"
)

// CheckLinks reports local markdown links whose target file or heading anchor
// does not exist. It walks root recursively and skips .git, worktrees and
// generated cache directories. External URLs, absolute paths, angle-bracket
// targets and line-only fragments such as #L42 are ignored. The path is the
// source markdown file and the line is the line holding the link.
func CheckLinks(root string) ([]Finding, error) {
	var findings []Finding
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "worktrees", ".gocache", ".gotmp":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		ownAnchors := markdownAnchors(lines)
		for i, line := range lines {
			for _, target := range inlineLinkTargets(line) {
				if !isLocalLinkTarget(target) {
					continue
				}
				linkPath, frag := splitLinkTarget(target)
				resolved := rel
				if linkPath != "" {
					clean := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(rel), filepath.FromSlash(linkPath))))
					if clean == ".." || strings.HasPrefix(clean, "../") {
						continue
					}
					resolved = clean
				}
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(resolved))); err != nil {
					findings = append(findings, Finding{
						Rule:    linkPathRule,
						Path:    rel,
						Line:    i + 1,
						Message: fmt.Sprintf("missing link target %q", target),
					})
					continue
				}
				if frag == "" || isLineFragment(frag) {
					continue
				}
				anchors := ownAnchors
				if resolved != rel {
					b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(resolved)))
					if err != nil {
						continue
					}
					anchors = markdownAnchors(strings.Split(string(b), "\n"))
				}
				if !anchors[frag] {
					findings = append(findings, Finding{
						Rule:    linkAnchorRule,
						Path:    rel,
						Line:    i + 1,
						Message: fmt.Sprintf("missing anchor %q in %s", frag, resolved),
					})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return suppressLinkFindings(findings), nil
}

// suppressLinkFindings applies only the link rows of the shared baseline
// table. Other rules keep their own liveness checks in their own gates, so a
// link scan must not report their stale exceptions as link findings.
func suppressLinkFindings(findings []Finding) []Finding {
	ex, err := LoadExceptions()
	if err != nil {
		return findings
	}
	covered := map[string]bool{}
	for _, b := range ex.Baseline {
		if b.Rule == linkPathRule || b.Rule == linkAnchorRule {
			covered[entryKey(b.Rule, b.Path)] = true
		}
	}
	out := findings[:0]
	for _, f := range findings {
		if !covered[entryKey(f.Rule, f.Path)] {
			out = append(out, f)
		}
	}
	SortFindings(out)
	return out
}

// inlineLinkTargets returns the raw targets of inline markdown links and
// images on one line. Code spans are skipped. A target ends at the closing
// parenthesis that balances the opening one, and an optional link title is
// cut off at the first whitespace.
func inlineLinkTargets(line string) []string {
	var out []string
	inCode := false
	for i := 0; i < len(line); i++ {
		if line[i] == '`' {
			inCode = !inCode
			continue
		}
		if inCode || i+1 >= len(line) || line[i] != ']' || line[i+1] != '(' {
			continue
		}
		start := i + 2
		depth := 1
		for j := start; j < len(line); j++ {
			switch line[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					target := strings.TrimSpace(line[start:j])
					if k := strings.IndexAny(target, " \t"); k >= 0 {
						target = target[:k]
					}
					out = append(out, strings.Trim(target, "<>"))
					i = j
				}
			}
		}
	}
	return out
}

// isLocalLinkTarget drops link forms that cannot be resolved against the
// scanned tree: absolute URLs, system or repo-root paths, angle-bracket
// targets and empty strings.
func isLocalLinkTarget(target string) bool {
	if target == "" || strings.HasPrefix(target, "<") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return false
	}
	lower := strings.ToLower(target)
	for _, scheme := range []string{"http://", "https://", "mailto:", "tel:", "data:", "ftp:", "file:"} {
		if strings.HasPrefix(lower, scheme) {
			return false
		}
	}
	return true
}

// splitLinkTarget separates a path from a fragment and decodes %20 in the
// fragment, which is how markdown editors encode spaces in heading anchors.
func splitLinkTarget(target string) (string, string) {
	i := strings.IndexByte(target, '#')
	if i < 0 {
		return target, ""
	}
	return target[:i], strings.ReplaceAll(target[i+1:], "%20", " ")
}

// isLineFragment reports whether frag is a source line reference (#L42),
// which is not a markdown heading anchor.
func isLineFragment(frag string) bool {
	if len(frag) < 2 || frag[0] != 'L' {
		return false
	}
	for i := 1; i < len(frag); i++ {
		if frag[i] < '0' || frag[i] > '9' {
			return false
		}
	}
	return true
}

// markdownAnchors collects the GitHub-style fragments available in a
// markdown file: heading slugs, duplicate heading suffixes and explicit
// <a name="..."> or <a id="..."> anchors. Fenced code is not a heading.
func markdownAnchors(lines []string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	fence := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if strings.HasPrefix(trimmed, "<a ") {
			for _, attr := range []string{`name="`, `id="`} {
				if p := strings.Index(trimmed, attr); p >= 0 {
					rest := trimmed[p+len(attr):]
					if q := strings.IndexByte(rest, '"'); q >= 0 {
						out[rest[:q]] = true
					}
				}
			}
		}
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		if heading == "" {
			continue
		}
		slug := markdownSlug(heading)
		seen[slug]++
		if seen[slug] == 1 {
			out[slug] = true
		} else {
			out[slug+"-"+strconv.Itoa(seen[slug]-1)] = true
		}
	}
	return out
}

// markdownSlug turns a heading into the fragment GitHub generates: lowercase,
// spaces as hyphens, punctuation removed. Unicode letters and digits are
// kept, so Russian headings get usable anchors.
func markdownSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	return b.String()
}

// CheckLinksSelfTest proves the link checker is not vacuous: a good tree is
// green, while a planted missing file and a planted broken anchor are both
// reported. It uses the shared Fixture helper so the only os.RemoveAll in the
// gate package stays in the covered fixture file.
func CheckLinksSelfTest() int {
	files := map[string]string{
		"docs/README.md": "# Docs\n\nSee [good](target.md#setup) and [external](https://example.com).\n",
		"docs/target.md": "# Target\n\n## Setup\n\ntext\n",
	}
	root, err := Fixture(files)
	if err != nil {
		fmt.Println("self-test FAILED: fixture:", err)
		return Red
	}
	defer RemoveFixture(root)

	fs, err := CheckLinks(root)
	if err != nil || len(fs) != 0 {
		fmt.Printf("self-test FAILED: good tree findings=%v err=%v\n", fs, err)
		return Red
	}

	dead := map[string]string{
		"docs/README.md": "# Docs\n\n[gone](missing.md)\n[bad](target.md#nope)\n",
		"docs/target.md": "# Target\n\n## Setup\n\ntext\n",
	}
	badRoot, err := Fixture(dead)
	if err != nil {
		fmt.Println("self-test FAILED: fixture:", err)
		return Red
	}
	defer RemoveFixture(badRoot)

	fs, err = CheckLinks(badRoot)
	if err != nil {
		fmt.Println("self-test FAILED: check:", err)
		return Red
	}
	if !HasRule(fs, linkPathRule) || !HasRule(fs, linkAnchorRule) {
		fmt.Printf("self-test FAILED: dead links not caught: %v\n", fs)
		return Red
	}
	fmt.Println("self-test passed: 2 fixtures, dead file and dead anchor caught")
	return Green
}
