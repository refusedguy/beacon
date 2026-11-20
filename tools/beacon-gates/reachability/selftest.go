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

func checkFixture(files map[string]string, want func([]gate.Finding) bool) bool {
	root, err := gate.Fixture(files)
	if err != nil {
		fmt.Println("self-test FAILED: fixture:", err)
		return false
	}
	defer gate.RemoveFixture(root)

	findings, err := analyse(root)
	if err != nil {
		fmt.Println("self-test FAILED: analyse:", err)
		return false
	}
	if !want(findings) {
		fmt.Printf("self-test FAILED: unexpected findings: %v\n", findings)
		return false
	}
	return true
}

func selfTestRun() int {
	ok := true

	// A planted exported adapter with no caller must be reported by the
	// production analyse with the exact symbol key and rule.
	ok = checkFixture(map[string]string{
		"internal/infra/demo/adapter.go": plantedAdapter,
	}, func(fs []gate.Finding) bool {
		return gate.HasRule(fs, ruleUnwired) && gate.HasRuleAt(fs, ruleUnwired, "internal/infra/demo/DemoAdapter", 0)
	}) && ok

	// The same adapter referenced from cmd/ is wired and stays green.
	ok = checkFixture(map[string]string{
		"internal/infra/demo/adapter.go": plantedAdapter,
		"cmd/beacon/wire.go":             wiredAdapter,
	}, func(fs []gate.Finding) bool {
		return len(fs) == 0
	}) && ok

	// A test disabled by a build tag must be reported.
	ok = checkFixture(map[string]string{
		"internal/infra/demo/adapter_test.go": ignoredTest,
	}, func(fs []gate.Finding) bool {
		return gate.HasRuleAt(fs, ruleTestIgnore, "internal/infra/demo/adapter_test.go", 0)
	}) && ok

	// A skipped test must be reported with its call site line.
	ok = checkFixture(map[string]string{
		"internal/infra/demo/adapter_test.go": skippedTest,
	}, func(fs []gate.Finding) bool {
		return gate.HasRuleAt(fs, ruleTestSkip, "internal/infra/demo/adapter_test.go", 5)
	}) && ok

	// A test in a package that does not match its directory must be reported.
	ok = checkFixture(map[string]string{
		"internal/infra/demo/adapter.go":      "package demo\n",
		"internal/infra/demo/adapter_test.go": wrongPackageTest,
	}, func(fs []gate.Finding) bool {
		return gate.HasRuleAt(fs, ruleTestPkg, "internal/infra/demo/adapter_test.go", 0)
	}) && ok

	if !ok {
		fmt.Println("self-test FAILED: 5 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 5 fixtures")
	return gate.Green
}
