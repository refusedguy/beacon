// Package gate: the WIRING-* rule keeps deferred wiring honest.
//
// A PR that defers part of a feature past its Watch handoff must carry one
// thing in the same tree the local gate can see:
//
//   - docs/tasks/WIRING-<PR>.md, a card with "Статус: todo", a non-empty
//     owner field ("OwnerFiles" or "Владелец файлов") and a "## Причина" section.
//
// The card is the whole contract. beacon_defer_wiring adds it to the PR and
// records the deferral in the durable store, so the gate reads no marker file.
// An old .beacon/wiring-pending file left in a tree is ignored. The card
// template lives in docs/templates/wiring-card.md.
package gate

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ownerFieldFallback is the field name beacon_defer_wiring writes into the
// cards it creates. CheckWiring prefers the configured field and falls back
// to this one, so tool cards and template cards pass the same gate.
const ownerFieldFallback = "Владелец файлов"

// WiringConfig is the wiring section of config/baseline.json. It is embedded
// into the committed binary, so changing it requires a binary rebuild.
type WiringConfig struct {
	CardDir       string `json:"card_dir"`
	CardPrefix    string `json:"card_prefix"`
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

// CheckWiring validates every WIRING card. A card is the only proof of a
// deferred wiring: it must carry the configured status, a non-empty owner
// field and the reason section. A leftover marker file is not read, so it
// cannot add a finding or hide a defect.
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
		if v == "" {
			// beacon_defer_wiring writes the human-readable name instead of
			// the configured one. Accept it when the configured field is
			// absent or empty; the template writes both, so it is fine too.
			// Using the fallback's line also keeps an empty-fields finding
			// pointed at the field that is actually present.
			if alt, altLine := FieldValue(c.lines, ownerFieldFallback); altLine != 0 {
				v, line = alt, altLine
			}
		}
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

// WiringSelfTestRun plants broken cards in fixtures and checks that the
// production scanner reports every rule. It also plants a stale marker and
// proves the gate ignores it. It proves the gate is not vacuous.
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
	toolCard := "# WIRING-999. Tool card\nСтатус: todo\nВладелец файлов: docs/b.go\n" +
		"## Причина\n deferred wiring\n"
	badCard := "# WIRING-888. Bad\nСтатус: done\nOwnerFiles: \n"

	hasAllDefects := func(fs []Finding) bool {
		return HasRule(fs, "WIRING-STATUS") &&
			HasRule(fs, "WIRING-OWNERFILES") &&
			HasRule(fs, "WIRING-REASON")
	}

	ok := true

	// Positive control: a complete card and no marker is green.
	ok = check(map[string]string{
		"docs/tasks/WIRING-777.md": goodCard,
	}, func(fs []Finding) bool { return len(fs) == 0 }) && ok

	// A card from beacon_defer_wiring uses the Russian owner field name.
	ok = check(map[string]string{
		"docs/tasks/WIRING-999.md": toolCard,
	}, func(fs []Finding) bool { return len(fs) == 0 }) && ok

	// Planted defects: wrong status, empty OwnerFiles, missing reason section.
	ok = check(map[string]string{
		"docs/tasks/WIRING-888.md": badCard,
	}, hasAllDefects) && ok

	// A stale marker next to a valid card changes nothing.
	ok = check(map[string]string{
		".beacon/wiring-pending":   "WIRING: 999\n",
		"docs/tasks/WIRING-777.md": goodCard,
	}, func(fs []Finding) bool { return len(fs) == 0 }) && ok

	// A marker with no card is ignored too: the card is the only contract.
	ok = check(map[string]string{
		".beacon/wiring-pending": "WIRING: 555\n",
	}, func(fs []Finding) bool { return len(fs) == 0 }) && ok

	// A marker does not hide defects in a card.
	ok = check(map[string]string{
		".beacon/wiring-pending":   "WIRING: 888\n",
		"docs/tasks/WIRING-888.md": badCard,
	}, hasAllDefects) && ok

	if !ok {
		fmt.Println("self-test FAILED: 6 fixtures")
		return Red
	}
	fmt.Println("self-test passed: 6 fixtures")
	return Green
}
