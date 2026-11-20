// Package gate: the WIRING-* rule keeps deferred wiring honest.
//
// A PR that defers part of a feature past its Watch handoff must carry two
// things in the same tree the local gate can see:
//
//   - docs/tasks/WIRING-<PR>.md, a card with "Статус: todo", a non-empty
//     "OwnerFiles" field and a "## Причина" section;
//   - .beacon/wiring-pending, written by beacon_defer_wiring, with one
//     "WIRING:<PR>" line per deferral.
//
// The gate cannot read the GitHub API, so the pending marker stands in for the
// "WIRING:" token in the PR body. A marker without a card is a finding, and
// every referenced id must resolve to a card. The card template lives in
// docs/templates/wiring-card.md.
package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WiringConfig is the wiring section of config/baseline.json. It is embedded
// into the committed binary, so changing it requires a binary rebuild.
type WiringConfig struct {
	CardDir       string `json:"card_dir"`
	CardPrefix    string `json:"card_prefix"`
	Marker        string `json:"marker"`
	MarkerToken   string `json:"marker_token"`
	Status        string `json:"status"`
	OwnerField    string `json:"owner_field"`
	ReasonSection string `json:"reason_section"`
}

// LoadWiringConfig reads the wiring section of the embedded baseline config
// and fills defaults for any omitted field. Empty strings in JSON mean "use
// the documented contract".
func LoadWiringConfig() (WiringConfig, error) {
	var raw struct {
		Wiring WiringConfig `json:"wiring"`
	}
	if err := json.Unmarshal(baselineJSON, &raw); err != nil {
		return WiringConfig{}, fmt.Errorf("parse wiring config: %w", err)
	}
	cfg := raw.Wiring
	if cfg.CardDir == "" {
		cfg.CardDir = "docs/tasks"
	}
	if cfg.CardPrefix == "" {
		cfg.CardPrefix = "WIRING-"
	}
	if cfg.Marker == "" {
		cfg.Marker = ".beacon/wiring-pending"
	}
	if cfg.MarkerToken == "" {
		cfg.MarkerToken = "WIRING:"
	}
	if cfg.Status == "" {
		cfg.Status = "todo"
	}
	if cfg.OwnerField == "" {
		cfg.OwnerField = "OwnerFiles"
	}
	if cfg.ReasonSection == "" {
		cfg.ReasonSection = "## Причина"
	}
	return cfg, nil
}

type wiringCard struct {
	path  string
	lines []string
}

// readWiringCards loads every <prefix>*.md directly under dir, sorted by path.
func readWiringCards(root, dir, prefix string) ([]wiringCard, error) {
	fsys := os.DirFS(root)
	pattern := filepath.ToSlash(filepath.Join(dir, prefix+"*.md"))
	matches, err := fs.Glob(fsys, pattern)
	if err != nil {
		return nil, err
	}
	out := make([]wiringCard, 0, len(matches))
	for _, rel := range matches {
		b, err := fs.ReadFile(fsys, rel)
		if err != nil {
			return nil, fmt.Errorf("read wiring card %s: %w", rel, err)
		}
		out = append(out, wiringCard{
			path:  filepath.ToSlash(rel),
			lines: strings.Split(string(b), "\n"),
		})
	}
	return out, nil
}

// CheckWiring validates every WIRING card and the pending marker. A marker
// with no card fails, and every "WIRING:<id>" line in the marker must resolve
// to a card in the same tree.
func CheckWiring(root string, cfg WiringConfig) ([]Finding, error) {
	cards, err := readWiringCards(root, cfg.CardDir, cfg.CardPrefix)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, c := range cards {
		v, line := FieldValue(c.lines, "Статус")
		if line == 0 {
			line = 1
		}
		if v != cfg.Status {
			findings = append(findings, Finding{
				Rule:    "WIRING-STATUS",
				Path:    c.path,
				Line:    line,
				Message: fmt.Sprintf("Статус must be %q", cfg.Status),
			})
		}
		v, line = FieldValue(c.lines, cfg.OwnerField)
		if line == 0 {
			line = 1
		}
		if v == "" {
			findings = append(findings, Finding{
				Rule:    "WIRING-OWNERFILES",
				Path:    c.path,
				Line:    line,
				Message: cfg.OwnerField + " must list the files the follow-up touches",
			})
		}
		if !HasSection(c.lines, cfg.ReasonSection) {
			findings = append(findings, Finding{
				Rule:    "WIRING-REASON",
				Path:    c.path,
				Line:    1,
				Message: "missing " + cfg.ReasonSection + " section",
			})
		}
	}

	markerPath := filepath.ToSlash(cfg.Marker)
	body, err := fs.ReadFile(os.DirFS(root), markerPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			SortFindings(findings)
			return findings, nil
		}
		return nil, fmt.Errorf("read wiring marker: %w", err)
	}
	if len(cards) == 0 {
		findings = append(findings, Finding{
			Rule:    "WIRING-MARKER-NO-CARD",
			Path:    markerPath,
			Line:    1,
			Message: "deferred wiring marker present but no " + cfg.CardPrefix + "*.md card in " + cfg.CardDir,
		})
		SortFindings(findings)
		return findings, nil
	}

	// beacon_defer_wiring writes one "<marker token><id>" per deferral. The id
	// is the PR number used in the card filename.
	for i, line := range strings.Split(string(body), "\n") {
		rest := line
		for {
			at := strings.Index(rest, cfg.MarkerToken)
			if at < 0 {
				break
			}
			tail := strings.TrimSpace(rest[at+len(cfg.MarkerToken):])
			rest = rest[at+len(cfg.MarkerToken):]
			id := ""
			for _, r := range tail {
				if r == ' ' || r == '\t' || r == ',' || r == ';' || r == ')' {
					break
				}
				id += string(r)
			}
			if id == "" {
				continue
			}
			want := cfg.CardPrefix + id + ".md"
			found := false
			for _, c := range cards {
				base := filepath.Base(c.path)
				if base == want || strings.HasPrefix(base, cfg.CardPrefix+id+"-") {
					found = true
					break
				}
			}
			if !found {
				findings = append(findings, Finding{
					Rule:    "WIRING-CARD-MISSING",
					Path:    markerPath,
					Line:    i + 1,
					Message: fmt.Sprintf("marker references %s but %s is absent", id, want),
				})
			}
		}
	}
	SortFindings(findings)
	return findings, nil
}

// RunWiring is the CLI entry for the wiring gate. The beacon-gates registry
// can register it as a subcommand without changing this package.
func RunWiring(args []string) int {
	opts, err := ParseOptions(args)
	if err != nil {
		return Brokenf("%v", err)
	}
	cfg, err := LoadWiringConfig()
	if err != nil {
		return Brokenf("%v", err)
	}
	findings, err := CheckWiring(opts.Root, cfg)
	if err != nil {
		return Brokenf("%v", err)
	}
	return Finish(findings, opts.MinFindings)
}

// WiringSelfTestRun plants broken cards and markers in fixtures and checks
// that the production scanner reports every rule. It proves the gate is not
// vacuous.
func WiringSelfTestRun() int {
	cfg, err := LoadWiringConfig()
	if err != nil {
		fmt.Println("self-test FAILED: config:", err)
		return Red
	}
	check := func(files map[string]string, want func([]Finding) bool) bool {
		root, err := Fixture(files)
		if err != nil {
			fmt.Println("self-test FAILED: fixture:", err)
			return false
		}
		defer RemoveFixture(root)
		findings, err := CheckWiring(root, cfg)
		if err != nil {
			fmt.Println("self-test FAILED: CheckWiring:", err)
			return false
		}
		if !want(findings) {
			fmt.Printf("self-test FAILED: unexpected findings: %v\n", findings)
			return false
		}
		return true
	}

	goodCard := "# WIRING-777. Valid card\nСтатус: todo\nOwnerFiles: docs/a.go\n" +
		"## Причина\n deferred wiring\n"

	ok := true

	// Positive control: a complete card with no marker is green.
	ok = check(map[string]string{
		"docs/tasks/WIRING-777.md": goodCard,
	}, func(fs []Finding) bool { return len(fs) == 0 }) && ok

	// Planted defects: wrong status, empty OwnerFiles, missing reason section.
	ok = check(map[string]string{
		"docs/tasks/WIRING-888.md": "# WIRING-888. Bad\nСтатус: done\nOwnerFiles: \n",
	}, func(fs []Finding) bool {
		return HasRule(fs, "WIRING-STATUS") &&
			HasRule(fs, "WIRING-OWNERFILES") &&
			HasRule(fs, "WIRING-REASON")
	}) && ok

	// A marker with no card is the false-deferral case.
	ok = check(map[string]string{
		".beacon/wiring-pending": "WIRING: 999\n",
	}, func(fs []Finding) bool {
		return HasRule(fs, "WIRING-MARKER-NO-CARD")
	}) && ok

	// A marker token that resolves to a card is green.
	ok = check(map[string]string{
		".beacon/wiring-pending":   "WIRING: 777\n",
		"docs/tasks/WIRING-777.md": goodCard,
	}, func(fs []Finding) bool { return len(fs) == 0 }) && ok

	// A marker token with no matching card is reported against the marker.
	ok = check(map[string]string{
		".beacon/wiring-pending":   "WIRING: 555\n",
		"docs/tasks/WIRING-777.md": goodCard,
	}, func(fs []Finding) bool {
		return HasRule(fs, "WIRING-CARD-MISSING")
	}) && ok

	if !ok {
		fmt.Println("self-test FAILED: 5 fixtures")
		return Red
	}
	fmt.Println("self-test passed: 5 fixtures")
	return Green
}
