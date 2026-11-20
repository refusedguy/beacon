package gate

import "testing"

// goodWiringCard is the smallest card that satisfies every WIRING rule.
const goodWiringCard = "# WIRING-777. Valid card\n" +
	"Статус: todo\n" +
	"OwnerFiles: docs/a.go\n" +
	"\n" +
	"## Причина\n" +
	" deferred wiring\n"

// badWiringCard has all three defects at known lines.
const badWiringCard = "# WIRING-888. Bad\nСтатус: done\nOwnerFiles: \n"

func wiringConfig(t *testing.T) WiringConfig {
	t.Helper()
	cfg, err := LoadWiringConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func wiringFindings(t *testing.T, files map[string]string) []Finding {
	t.Helper()
	root, err := Fixture(files)
	if err != nil {
		t.Fatal(err)
	}
	defer RemoveFixture(root)
	findings, err := CheckWiring(root, wiringConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func TestLoadWiringConfig(t *testing.T) {
	cfg := wiringConfig(t)
	if cfg.CardDir != "docs/tasks" || cfg.CardPrefix != "WIRING-" ||
		cfg.Status != "todo" || cfg.OwnerField != "OwnerFiles" ||
		cfg.ReasonSection != "## Причина" {
		t.Fatalf("LoadWiringConfig = %+v, want documented defaults", cfg)
	}
}

func TestCheckWiringValidCard(t *testing.T) {
	if fs := wiringFindings(t, map[string]string{
		"docs/tasks/WIRING-777.md": goodWiringCard,
	}); len(fs) != 0 {
		t.Fatalf("valid card produced findings: %v", fs)
	}
}

func TestCheckWiringEmptyTree(t *testing.T) {
	if fs := wiringFindings(t, nil); len(fs) != 0 {
		t.Fatalf("empty tree produced findings: %v", fs)
	}
}

func TestCheckWiringCardDefects(t *testing.T) {
	fs := wiringFindings(t, map[string]string{
		"docs/tasks/WIRING-888.md": badWiringCard,
	})
	for _, rule := range []string{"WIRING-STATUS", "WIRING-OWNERFILES", "WIRING-REASON"} {
		if !HasRule(fs, rule) {
			t.Errorf("missing %s in %v", rule, fs)
		}
	}
	if !HasRuleAt(fs, "WIRING-STATUS", "docs/tasks/WIRING-888.md", 2) {
		t.Errorf("status finding at wrong line: %v", fs)
	}
	if !HasRuleAt(fs, "WIRING-OWNERFILES", "docs/tasks/WIRING-888.md", 3) {
		t.Errorf("owner finding at wrong line: %v", fs)
	}
}

func TestCheckWiringIgnoresMarker(t *testing.T) {
	marker := map[string]string{
		".beacon/wiring-pending": "WIRING: 999\nWIRING: 555\n",
	}

	// A stale marker must not add a finding to a valid card.
	files := map[string]string{"docs/tasks/WIRING-777.md": goodWiringCard}
	for k, v := range marker {
		files[k] = v
	}
	if fs := wiringFindings(t, files); len(fs) != 0 {
		t.Fatalf("stale marker produced findings: %v", fs)
	}

	// A marker without any card is ignored as well.
	if fs := wiringFindings(t, marker); len(fs) != 0 {
		t.Fatalf("marker alone produced findings: %v", fs)
	}

	// A marker must not hide defects in a card.
	files = map[string]string{"docs/tasks/WIRING-888.md": badWiringCard}
	for k, v := range marker {
		files[k] = v
	}
	fs := wiringFindings(t, files)
	for _, rule := range []string{"WIRING-STATUS", "WIRING-OWNERFILES", "WIRING-REASON"} {
		if !HasRule(fs, rule) {
			t.Errorf("marker hid %s: %v", rule, fs)
		}
	}
}

func TestCheckWiringOnlyReportsBrokenCards(t *testing.T) {
	fs := wiringFindings(t, map[string]string{
		"docs/tasks/WIRING-777.md": goodWiringCard,
		"docs/tasks/WIRING-888.md": badWiringCard,
	})
	if len(fs) != 3 {
		t.Fatalf("got %d findings, want 3: %v", len(fs), fs)
	}
	for _, f := range fs {
		if f.Path != "docs/tasks/WIRING-888.md" {
			t.Errorf("finding for valid card: %v", f)
		}
	}
}

func TestWiringSelfTestRun(t *testing.T) {
	if got := WiringSelfTestRun(); got != Green {
		t.Fatalf("WiringSelfTestRun = %d, want %d", got, Green)
	}
}
