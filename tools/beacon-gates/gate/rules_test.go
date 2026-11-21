package gate

import "testing"

func TestRuleRegistryHasNoDuplicates(t *testing.T) {
	if dup := CheckRuleRegistry(); len(dup) != 0 {
		t.Fatalf("rule registry declares names twice: %v", dup)
	}
}

func TestValidateRuleAcceptsRegisteredNames(t *testing.T) {
	for _, name := range []string{
		RuleScanBlind,
		RuleDocLinkPath,
		ToolNameFormatRule,
		RuleWiringStatus,
		"CAP-FILE-IN-OUTSIDE",
		"CAP-EXEC-IN-CMD",
		"CAP-NETHTTP-IN-OUTSIDE",
	} {
		if err := ValidateRule(name); err != nil {
			t.Errorf("ValidateRule(%q) rejected a registered name: %v", name, err)
		}
	}
}

func TestValidateRuleRejectsUnknownNames(t *testing.T) {
	for _, name := range []string{
		"",
		"TOOL-NAME-FORMOT",
		"DOC-TASK-STATS",
		"WIRING-STATUSX",
		"CAP-FILE-IN-NOWHERE",
		"CAP-NOPE-IN-CMD",
	} {
		if err := ValidateRule(name); err == nil {
			t.Errorf("ValidateRule(%q) accepted an unknown name", name)
		}
	}
}

func TestNewFindingRejectsUnknownRule(t *testing.T) {
	if _, err := NewFinding("TOOL-NAME-FORMOT", "x.go", 1, "typo"); err == nil {
		t.Fatal("NewFinding accepted a misspelled rule name")
	}
	f, err := NewFinding(RuleToolNameFormat, "x.go", 1, "ok")
	if err != nil {
		t.Fatalf("NewFinding rejected a registered rule: %v", err)
	}
	if f.Rule != RuleToolNameFormat {
		t.Fatalf("NewFinding built rule %q, want %q", f.Rule, RuleToolNameFormat)
	}
}

func TestConfigRulesAreRegistered(t *testing.T) {
	if err := ValidateConfigRules(); err != nil {
		t.Fatalf("exception table uses an unregistered rule: %v", err)
	}
}
