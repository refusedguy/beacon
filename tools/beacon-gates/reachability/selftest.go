package reachability

import (
	"fmt"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

const plantedAdapter = "package demo\n\n" +
	"// DemoAdapter is planted code with no caller.\n" +
	"type DemoAdapter struct{}\n"

const wiredAdapter = "package main\n\n" +
	"import \"github.com/refusedguy/beacon/internal/infra/demo\"\n\n" +
	"func wire() *demo.DemoAdapter { return &demo.DemoAdapter{} }\n"

const ignoredTest = "//go:build ignore\n\npackage demo\n"

const skippedTest = "package demo\n\nimport \"testing\"\n\n" +
	"func TestSkip(t *testing.T) { t.Skip(\"later\") }\n"

const wrongPackageTest = "package other\n"

func selfTestRun() int {
	list := []gate.FixtureCase{
		// A planted exported adapter with no caller must be reported by the
		// production analyse with the exact symbol key and rule.
		{
			Files: map[string]string{
				"internal/infra/demo/adapter.go": plantedAdapter,
			},
			Want: func(fs []gate.Finding) bool {
				return gate.HasRule(fs, ruleUnwired) && gate.HasRuleAt(fs, ruleUnwired, "internal/infra/demo/DemoAdapter", 0)
			},
		},
		// The same adapter referenced from cmd/ is wired and stays green.
		{
			Files: map[string]string{
				"internal/infra/demo/adapter.go": plantedAdapter,
				"cmd/beacon/wire.go":             wiredAdapter,
			},
			Want: func(fs []gate.Finding) bool {
				return len(fs) == 0
			},
		},
		// A test disabled by a build tag must be reported.
		{
			Files: map[string]string{
				"internal/infra/demo/adapter_test.go": ignoredTest,
			},
			Want: func(fs []gate.Finding) bool {
				return gate.HasRuleAt(fs, ruleTestIgnore, "internal/infra/demo/adapter_test.go", 0)
			},
		},
		// A skipped test must be reported with its call site line.
		{
			Files: map[string]string{
				"internal/infra/demo/adapter_test.go": skippedTest,
			},
			Want: func(fs []gate.Finding) bool {
				return gate.HasRuleAt(fs, ruleTestSkip, "internal/infra/demo/adapter_test.go", 5)
			},
		},
		// A test in a package that does not match its directory must be reported.
		{
			Files: map[string]string{
				"internal/infra/demo/adapter.go":      "package demo\n",
				"internal/infra/demo/adapter_test.go": wrongPackageTest,
			},
			Want: func(fs []gate.Finding) bool {
				return gate.HasRuleAt(fs, ruleTestPkg, "internal/infra/demo/adapter_test.go", 0)
			},
		},
	}

	if !gate.RunFixtures(list, analyse, gate.RemoveFixture) {
		fmt.Println("self-test FAILED: 5 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 5 fixtures")
	return gate.Green
}
