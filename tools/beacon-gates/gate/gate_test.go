package gate

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseOptions(t *testing.T) {
	o, err := ParseOptions([]string{"-root", "/tmp/x", "-min-findings=3"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Root != "/tmp/x" || o.MinFindings != 3 {
		t.Fatalf("got %+v", o)
	}
	for _, args := range [][]string{
		{"-min-findings=abc"},
		{"--bogus"},
		{"-root"},
		{"-min-findings=-1"},
	} {
		if _, err := ParseOptions(args); err == nil {
			t.Errorf("ParseOptions(%q) should fail", args)
		}
	}
}

func TestFixture(t *testing.T) {
	root, err := Fixture(map[string]string{"internal/domain/plant.go": "package domain\n"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	b, err := os.ReadFile(filepath.Join(root, "internal/domain/plant.go"))
	if err != nil || string(b) != "package domain\n" {
		t.Fatalf("fixture content: %q, err %v", b, err)
	}
}

func TestHasRuleAt(t *testing.T) {
	fs := []Finding{{Rule: "R", Path: "a/b.go", Line: 3}}
	if !HasRule(fs, "R") || !HasRuleAt(fs, "R", "a/b.go", 3) {
		t.Fatal("finding not matched")
	}
	if HasRuleAt(fs, "R", "a/b.go", 4) || HasRule(fs, "X") {
		t.Fatal("false positive")
	}
}

func TestMatrix(t *testing.T) {
	m, err := LoadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	// Nested packages inherit their parent's layer by prefix.
	for _, p := range []string{"internal/domain", "internal/app/taskcli", "internal/infra/taskstore", "cmd/beacon", "tools/beacon-gates/gate"} {
		if !m.Has(p) {
			t.Errorf("package %q is not covered by the matrix", p)
		}
	}
	if !m.Allows("cmd/beacon", "internal/app/taskcli") {
		t.Fatal("matrix does not allow outer to inner")
	}
	if m.Allows("internal/domain", "internal/infra") || m.Allows("internal/infra", "internal/app") {
		t.Fatal("matrix allows an outward dependency")
	}
	// Every permission must be one the real tree uses; no dead rows.
	// cmd -> internal/tui is used by the beacon board adapter.
	if len(m.AllowedEdges()) != 5 {
		t.Fatalf("AllowedEdges = %d, want 5: %v", len(m.AllowedEdges()), m.AllowedEdges())
	}
}

func TestLayerPassesSkipTmp(t *testing.T) {
	root, err := Fixture(map[string]string{
		"go.mod":                    "module " + LayerFixtureModule + "\n\ngo 1.26.0\n",
		"internal/domain/domain.go": "package domain\n",
		"tmp/spike/spike.go":        "package spike\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	m, err := LoadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	passes := map[string]func() ([]Edge, []string, error){
		"ast":  func() ([]Edge, []string, error) { return ASTEdges(root, m) },
		"list": func() ([]Edge, []string, error) { return ListEdges(root, m) },
	}
	for name, pass := range passes {
		edges, pkgs, err := pass()
		if err != nil {
			t.Fatalf("%s pass: %v", name, err)
		}
		for _, p := range pkgs {
			if skippedByPolicy(p) {
				t.Errorf("%s pass reported tmp package %q", name, p)
			}
		}
		for _, f := range CheckMatrix(m, edges, pkgs) {
			if skippedByPolicy(f.Path) {
				t.Errorf("%s pass reported tmp finding: %v", name, f)
			}
		}
	}
}

func TestExceptionsApply(t *testing.T) {
	ex, err := LoadExceptions()
	if err != nil {
		t.Fatal(err)
	}
	fs := []Finding{{Rule: "CAP-EXEC-IN-DOMAIN", Path: "internal/domain/plant.go", Line: 3}}
	if got := ex.Apply(fs, "."); len(got) != 1 {
		t.Fatalf("empty tables changed findings: %v", got)
	}

	bad := Exceptions{Baseline: []BaselineEntry{{Rule: "R", Path: "p"}}}
	if got := bad.Apply(nil, "."); !HasRule(got, "CAP-BASELINE-EXCEEDED") || !HasRule(got, "CAP-BASELINE-STALE") {
		t.Fatalf("baseline ratchet and liveness not enforced: %v", got)
	}

	// A permanent row covers its matching finding and is marked seen, so Apply
	// does not also report that row as stale.
	perm := Exceptions{Permanent: []PermanentEntry{{Rule: "R", Path: "p"}}}
	if got := perm.Apply([]Finding{{Rule: "R", Path: "p"}}, "."); len(got) != 0 {
		t.Fatalf("permanent row did not suppress its finding: %v", got)
	}
	// The same row with no matching finding must be reported as stale instead
	// of silently rotting in the exception table.
	if got := perm.Apply(nil, "."); !HasRule(got, "CAP-PERMANENT-STALE") {
		t.Fatalf("stale permanent row not reported: %v", got)
	}
}

func TestPermanentRowTreeConditions(t *testing.T) {
	root, err := Fixture(map[string]string{
		"internal/app/taskcli/service.go": "package taskcli\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	old := PermanentEntry{Rule: "R", Path: "internal/app/taskcli/service.go", Reason: "before split", OnlyIfMissing: "internal/app/taskcli/cards.go"}
	next := PermanentEntry{Rule: "R", Path: "internal/app/taskcli/cards.go", Reason: "after split", OnlyIfExists: "internal/app/taskcli/cards.go"}
	ex := Exceptions{Permanent: []PermanentEntry{old, next}}

	// Before the split the old row covers the finding, and the new row is out of scope.
	fs := []Finding{{Rule: "R", Path: "internal/app/taskcli/service.go"}}
	if got := ex.Apply(fs, root); len(got) != 0 {
		t.Fatalf("before split: %v", got)
	}

	// After the split the old row is skipped, and the new row covers the finding.
	cards := filepath.Join(root, "internal/app/taskcli/cards.go")
	if err := os.WriteFile(cards, []byte("package taskcli\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs = []Finding{{Rule: "R", Path: "internal/app/taskcli/cards.go"}}
	if got := ex.Apply(fs, root); len(got) != 0 {
		t.Fatalf("after split: %v", got)
	}
}

// Property tests draw every value from a deterministic stream. A failure
// records its seed so the same input replays without manual reconstruction.
const (
	propertyMasterSeed = int64(20261121)
	propertyRuns       = 128
	propertySeedFile   = "testdata/seeds.txt"
)

// loadPropertySeeds returns the saved failing seeds for one property. Each
// non-comment line has the form "<property> <seed>".
func loadPropertySeeds(t *testing.T, name string) []int64 {
	t.Helper()
	data, err := os.ReadFile(propertySeedFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", propertySeedFile, err)
	}
	var seeds []int64
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Fatalf("%s:%d: want \"<property> <seed>\", got %q", propertySeedFile, n+1, line)
		}
		if parts[0] != name {
			continue
		}
		seed, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			t.Fatalf("%s:%d: bad seed %q: %v", propertySeedFile, n+1, parts[1], err)
		}
		seeds = append(seeds, seed)
	}
	return seeds
}

// savePropertySeed appends a failing seed so the next run reproduces it.
func savePropertySeed(t *testing.T, name string, seed int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(propertySeedFile), 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	line := name + " " + strconv.FormatInt(seed, 10)
	data, err := os.ReadFile(propertySeedFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read %s: %v", propertySeedFile, err)
	}
	for _, existing := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(existing) == line {
			return
		}
	}
	f, err := os.OpenFile(propertySeedFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", propertySeedFile, err)
	}
	defer f.Close()
	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	if _, err := fmt.Fprint(f, prefix+line+"\n"); err != nil {
		t.Fatalf("append seed: %v", err)
	}
}

// freshPropertySeeds is a fixed stream of seeds, independent of any saved
// regression seed.
func freshPropertySeeds() []int64 {
	rng := rand.New(rand.NewSource(propertyMasterSeed))
	seeds := make([]int64, propertyRuns)
	for i := range seeds {
		seeds[i] = rng.Int63()
	}
	return seeds
}

// runProperty replays saved seeds first, then explores the fixed stream.
func runProperty(t *testing.T, name string, check func(rng *rand.Rand, seed int64) error) {
	t.Helper()
	seeds := append(loadPropertySeeds(t, name), freshPropertySeeds()...)
	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		if err := check(rng, seed); err != nil {
			savePropertySeed(t, name, seed)
			t.Fatalf("%s failed with seed %d: %v", name, seed, err)
		}
	}
}

const propAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func propString(rng *rand.Rand, min, max int) string {
	n := min + rng.Intn(max-min+1)
	b := make([]byte, n)
	for i := range b {
		b[i] = propAlphabet[rng.Intn(len(propAlphabet))]
	}
	return string(b)
}

func propPath(rng *rand.Rand) string {
	return propString(rng, 1, 6) + "/" + propString(rng, 1, 6) + ".go"
}

func propFinding(rng *rand.Rand) Finding {
	return Finding{
		Rule:    "RULE-" + propString(rng, 1, 4),
		Path:    propPath(rng),
		Line:    rng.Intn(30),
		Message: propString(rng, 0, 8),
	}
}

func propFindings(rng *rand.Rand, max int) []Finding {
	n := rng.Intn(max + 1)
	out := make([]Finding, n)
	for i := range out {
		out[i] = propFinding(rng)
		// Distinct line ranges give every finding a unique sort key, so a
		// full-value idempotence check cannot trip over unstable ties.
		out[i].Line = i*40 + rng.Intn(40)
	}
	return out
}

func propKey(rng *rand.Rand) string { return "Key" + propString(rng, 1, 5) }

func copyFindings(fs []Finding) []Finding {
	out := make([]Finding, len(fs))
	copy(out, fs)
	return out
}

func equalFindings(a, b []Finding) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameTriple(a, b Finding) bool {
	return a.Path == b.Path && a.Line == b.Line && a.Rule == b.Rule
}

func equalTriples(a, b []Finding) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameTriple(a[i], b[i]) {
			return false
		}
	}
	return true
}

func lessTriple(a, b Finding) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Rule < b.Rule
}

func multisetContains(have, want []Finding) bool {
	counts := map[Finding]int{}
	for _, f := range have {
		counts[f]++
	}
	for _, f := range want {
		counts[f]--
		if counts[f] < 0 {
			return false
		}
	}
	return true
}

// finishWithBuffer gives Finish spare capacity so the injected SCAN-BLIND
// finding is visible in the caller's backing array, then returns the used
// prefix.
func finishWithBuffer(fs []Finding, min int) (int, []Finding) {
	buf := make([]Finding, len(fs), len(fs)+8)
	copy(buf, fs)
	code := Finish(buf, min)
	used := len(fs)
	if used < min {
		used++
	}
	return code, buf[:used]
}

func TestPropertyParseOptions(t *testing.T) {
	runProperty(t, "ParseOptions", func(rng *rand.Rand, seed int64) error {
		root := propString(rng, 1, 10)
		if rng.Intn(2) == 0 {
			root = "." + root + "/sub"
		}
		min := rng.Intn(1000)
		rootFlag := []string{"-root", "--root"}[rng.Intn(2)]
		minFlag := []string{"-min-findings", "--min-findings"}[rng.Intn(2)]
		var args []string
		if rng.Intn(2) == 0 {
			args = append(args, rootFlag, root)
		} else {
			args = append(args, rootFlag+"="+root)
		}
		if rng.Intn(2) == 0 {
			args = append(args, minFlag, strconv.Itoa(min))
		} else {
			args = append(args, minFlag+"="+strconv.Itoa(min))
		}
		o, err := ParseOptions(args)
		if err != nil {
			return fmt.Errorf("valid args %q: %w", args, err)
		}
		if o.Root != root || o.MinFindings != min {
			return fmt.Errorf("args %q: got %+v, want root %q and min %d", args, o, root, min)
		}
		return nil
	})
}

func TestPropertyParseOptionsRejects(t *testing.T) {
	runProperty(t, "ParseOptionsRejects", func(rng *rand.Rand, seed int64) error {
		bad := [][]string{
			{"-min-findings=abc"},
			{"--bogus"},
			{"-root"},
			{"-min-findings"},
			{"-min-findings=-1"},
			{"--min-findings=-" + strconv.Itoa(1+rng.Intn(50))},
			{"-min-findings=x" + propString(rng, 1, 5)},
			{"-" + propString(rng, 1, 6)},
		}
		args := bad[rng.Intn(len(bad))]
		if o, err := ParseOptions(args); err == nil {
			return fmt.Errorf("invalid args %q returned %+v", args, o)
		}
		return nil
	})
}

func TestPropertySortFindings(t *testing.T) {
	runProperty(t, "SortFindings", func(rng *rand.Rand, seed int64) error {
		in := propFindings(rng, 40)
		got := copyFindings(in)
		SortFindings(got)
		again := copyFindings(got)
		SortFindings(again)
		if !equalFindings(again, got) {
			return fmt.Errorf("not idempotent: %v then %v", got, again)
		}
		if !multisetContains(got, in) || len(got) != len(in) {
			return fmt.Errorf("findings changed: %v -> %v", in, got)
		}
		for i := 1; i < len(got); i++ {
			if lessTriple(got[i], got[i-1]) {
				return fmt.Errorf("key order broken: %v before %v", got[i-1], got[i])
			}
		}
		return nil
	})
}

func TestPropertySortFindingsIgnoresMessage(t *testing.T) {
	runProperty(t, "SortFindingsIgnoresMessage", func(rng *rand.Rand, seed int64) error {
		in := propFindings(rng, 30)
		a := copyFindings(in)
		b := copyFindings(in)
		for i := range b {
			b[i].Message = propString(rng, 1, 8)
		}
		SortFindings(a)
		SortFindings(b)
		if !equalTriples(a, b) {
			return fmt.Errorf("message changed the order: %v vs %v", a, b)
		}
		return nil
	})
}

func TestPropertyFinish(t *testing.T) {
	runProperty(t, "Finish", func(rng *rand.Rand, seed int64) error {
		fs := propFindings(rng, 30)
		min := rng.Intn(40)
		code, got := finishWithBuffer(fs, min)
		blind := HasRule(got, "SCAN-BLIND")
		if len(fs) < min {
			if !blind {
				return fmt.Errorf("%d findings, floor %d: no SCAN-BLIND", len(fs), min)
			}
			if code != Red {
				return fmt.Errorf("%d findings, floor %d: code %d, want Red", len(fs), min, code)
			}
		} else {
			if blind {
				return fmt.Errorf("%d findings, floor %d: SCAN-BLIND added above floor", len(fs), min)
			}
			want := Green
			if len(fs) > 0 {
				want = Red
			}
			if code != want {
				return fmt.Errorf("%d findings, floor %d: code %d, want %d", len(fs), min, code, want)
			}
		}
		if !multisetContains(got, fs) {
			return fmt.Errorf("original findings lost: %v -> %v", fs, got)
		}
		for i := 1; i < len(got); i++ {
			if lessTriple(got[i], got[i-1]) {
				return fmt.Errorf("Finish returned unsorted findings: %v", got)
			}
		}
		return nil
	})
}

func TestPropertyFinishEmptyNeverGreen(t *testing.T) {
	runProperty(t, "FinishEmptyNeverGreen", func(rng *rand.Rand, seed int64) error {
		min := 1 + rng.Intn(64)
		code, got := finishWithBuffer(nil, min)
		if code == Green {
			return fmt.Errorf("empty findings with floor %d returned Green", min)
		}
		if !HasRule(got, "SCAN-BLIND") {
			return fmt.Errorf("empty findings with floor %d has no SCAN-BLIND", min)
		}
		return nil
	})
}

func TestPropertyFieldValue(t *testing.T) {
	runProperty(t, "FieldValue", func(rng *rand.Rand, seed int64) error {
		key := propKey(rng)
		value := propString(rng, 0, 12)
		pos := rng.Intn(12)
		filler := make([]string, 12)
		for i := range filler {
			filler[i] = "Other: " + propString(rng, 1, 6)
		}
		lines := make([]string, 0, len(filler)+1)
		lines = append(lines, filler[:pos]...)
		lines = append(lines, key+":   "+value+" \t")
		lines = append(lines, filler[pos:]...)
		got, line := FieldValue(lines, key)
		if got != strings.TrimSpace(value) || line != pos+1 {
			return fmt.Errorf("FieldValue(%q) = (%q, %d), want (%q, %d)", key, got, line, strings.TrimSpace(value), pos+1)
		}
		absent := key + "X"
		if g2, l2 := FieldValue(lines, absent); g2 != "" || l2 != 0 {
			return fmt.Errorf("absent key %q returned (%q, %d)", absent, g2, l2)
		}
		return nil
	})
}

func TestPropertyFieldValueFirstMatch(t *testing.T) {
	runProperty(t, "FieldValueFirstMatch", func(rng *rand.Rand, seed int64) error {
		key := propKey(rng)
		first := rng.Intn(6)
		second := first + 1 + rng.Intn(6)
		n := second + 1 + rng.Intn(4)
		lines := make([]string, n)
		for i := range lines {
			lines[i] = "Other: filler"
		}
		want := "first " + strconv.FormatInt(seed, 10)
		lines[first] = key + ": " + want
		lines[second] = key + ": second"
		got, line := FieldValue(lines, key)
		if got != want || line != first+1 {
			return fmt.Errorf("FieldValue(%q) = (%q, %d), want (%q, %d)", key, got, line, want, first+1)
		}
		return nil
	})
}

type linkWant struct {
	rule string
	path string
	line int
}

func TestPropertyCheckLinks(t *testing.T) {
	runProperty(t, "CheckLinks", func(rng *rand.Rand, seed int64) error {
		name := "t" + propString(rng, 2, 6)
		target := name + ".md"
		heading := propString(rng, 2, 8)
		slug := markdownSlug(heading)
		leading := rng.Intn(5)
		kind := rng.Intn(5)

		lines := []string{"# Doc"}
		for i := 0; i < leading; i++ {
			lines = append(lines, "filler "+strconv.Itoa(i))
		}
		linkLine := len(lines) + 1
		var link string
		files := map[string]string{}
		var want []linkWant
		switch kind {
		case 0:
			link = "[go](" + target + ")"
			files = map[string]string{target: "# Target\n"}
		case 1:
			link = "[go](" + target + "#" + slug + ")"
			files = map[string]string{target: "# " + heading + "\n"}
		case 2:
			link = "[bad](missing-" + name + ".md)"
			want = []linkWant{{rule: linkPathRule, path: "doc.md", line: linkLine}}
		case 3:
			link = "[bad](" + target + "#no-such-anchor)"
			files = map[string]string{target: "# " + heading + "\n"}
			want = []linkWant{{rule: linkAnchorRule, path: "doc.md", line: linkLine}}
		case 4:
			link = "[ext](https://example.com) [abs](/abs-" + name + ".md) [line](" + target + "#L42)"
			files = map[string]string{target: "# Target\n"}
		}
		lines = append(lines, link)
		files["doc.md"] = strings.Join(lines, "\n") + "\n"

		root, err := Fixture(files)
		if err != nil {
			return fmt.Errorf("fixture: %w", err)
		}
		defer RemoveFixture(root)
		got, err := CheckLinks(root)
		if err != nil {
			return fmt.Errorf("CheckLinks: %w", err)
		}
		if len(got) != len(want) {
			return fmt.Errorf("case %d: got %v, want %v", kind, got, want)
		}
		for i := range want {
			if got[i].Rule != want[i].rule || got[i].Path != want[i].path || got[i].Line != want[i].line {
				return fmt.Errorf("case %d: finding %d = %v, want %+v", kind, i, got[i], want[i])
			}
		}
		return nil
	})
}
