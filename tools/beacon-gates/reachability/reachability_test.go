package reachability

import (
	"go/token"
	"os"
	"testing"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func analyseFixture(t *testing.T, files map[string]string) []gate.Finding {
	t.Helper()
	root, err := gate.Fixture(files)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	findings, err := analyse(root)
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func TestAnalyseUnwired(t *testing.T) {
	fs := analyseFixture(t, map[string]string{
		"internal/infra/demo/adapter.go": plantedAdapter,
	})
	if !gate.HasRuleAt(fs, ruleUnwired, "internal/infra/demo/DemoAdapter", 0) {
		t.Fatalf("want REACH-UNWIRED on DemoAdapter, got %v", fs)
	}
}

func TestAnalyseWiredFromCmd(t *testing.T) {
	fs := analyseFixture(t, map[string]string{
		"internal/infra/demo/adapter.go": plantedAdapter,
		"cmd/beacon/wire.go":             wiredAdapter,
	})
	if len(fs) != 0 {
		t.Fatalf("wired adapter should be green, got %v", fs)
	}
}

func TestAnalyseWiredTransitively(t *testing.T) {
	fs := analyseFixture(t, map[string]string{
		"internal/infra/demo/adapter.go": "package demo\n\ntype DemoAdapter struct{}\n",
		"internal/app/use/use.go": "package use\n\n" +
			"import \"github.com/refusedguy/beacon/internal/infra/demo\"\n\n" +
			"func Use() *demo.DemoAdapter { return &demo.DemoAdapter{} }\n",
		"cmd/beacon/wire.go": "package main\n\n" +
			"import \"github.com/refusedguy/beacon/internal/app/use\"\n\n" +
			"func wire() { _ = use.Use() }\n",
	})
	if len(fs) != 0 {
		t.Fatalf("adapter wired through app should be green, got %v", fs)
	}
}

func TestAnalyseTestHygiene(t *testing.T) {
	fs := analyseFixture(t, map[string]string{
		"internal/infra/demo/adapter.go":      "package demo\n",
		"internal/infra/demo/ignored_test.go": ignoredTest,
		"internal/infra/demo/skipped_test.go": skippedTest,
		"internal/infra/demo/wrong_test.go":   wrongPackageTest,
	})
	if !gate.HasRuleAt(fs, ruleTestIgnore, "internal/infra/demo/ignored_test.go", 0) {
		t.Errorf("missing REACH-TEST-IGNORE: %v", fs)
	}
	if !gate.HasRuleAt(fs, ruleTestSkip, "internal/infra/demo/skipped_test.go", 5) {
		t.Errorf("missing REACH-TEST-SKIP: %v", fs)
	}
	if !gate.HasRuleAt(fs, ruleTestPkg, "internal/infra/demo/wrong_test.go", 0) {
		t.Errorf("missing REACH-TEST-PKG: %v", fs)
	}
}

func TestIsTarget(t *testing.T) {
	cases := []struct {
		pkg, name string
		want      bool
	}{
		{"internal/infra/mcp", "WakeHandler", true},
		{"internal/app/prcreate", "NewService", true},
		{"internal/infra/github", "Provider", true},
		{"internal/infra/mcp", "NewServer", false},
		{"internal/infra/mcp", "newHandlerOptions", false},
		{"internal/domain", "Clock", false},
		{"cmd/beacon", "CLI", false},
		{"internal/infra/observe", "ProviderCall", false},
	}
	for _, tc := range cases {
		if got := isTarget(tc.pkg, tc.name, token.FUNC); got != tc.want {
			t.Errorf("isTarget(%q, %q) = %v, want %v", tc.pkg, tc.name, got, tc.want)
		}
	}
	if isTarget("internal/infra/github", "CodeCIUnknownProvider", token.CONST) {
		t.Error("constants must not be targets")
	}
}

func TestLoadReachabilityExceptions(t *testing.T) {
	ex, err := gate.LoadReachabilityExceptions()
	if err != nil {
		t.Fatal(err)
	}
	if ex.BaselineLimit != 0 {
		t.Errorf("baseline limit = %d, want 0", ex.BaselineLimit)
	}
}
