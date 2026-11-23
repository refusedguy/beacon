package gate

import "strings"

// LayerSelfTestCases returns the self-test cases shared by both layer gates.
// Each binary appends its own cases. Layers-ast adds a LAYER-DIVERGENCE case.
func LayerSelfTestCases() []FixtureCase {
	base := LayerFixtureFiles()
	return []FixtureCase{
		// Clean tree: all four matrix permissions are used.
		{
			Files: base,
			Want: func(fs []Finding) bool {
				return len(fs) == 0
			},
		},
		// Planted violation: domain imports infra. go list sees the edge and the
		// source line is located from the package directory.
		{
			Files: With(base, map[string]string{
				"internal/domain/plant.go": "package domain\n\n" +
					"import _ \"" + LayerFixtureModule + "/internal/infra\"\n",
			}),
			Want: func(fs []Finding) bool {
				return HasRuleAt(fs, "LAYER-FORBIDDEN-EDGE", "internal/domain/plant.go", 3)
			},
		},
		// A package with no matrix row is a finding.
		{
			Files: With(base, map[string]string{
				"internal/newlayer/x.go": "package newlayer\n",
			}),
			Want: func(fs []Finding) bool {
				return HasRuleAt(fs, "LAYER-NO-RULE", "internal/newlayer", 1)
			},
		},
		// Unused permission: app stops importing infra, so the app -> infra row
		// in the matrix is left with no supporting edge.
		{
			Files: With(base, map[string]string{
				"internal/app/app.go": "package app\n",
			}),
			Want: func(fs []Finding) bool {
				return len(fs) == 1 && strings.Contains(fs[0].Message, "internal/app -> internal/infra")
			},
		},
	}
}
