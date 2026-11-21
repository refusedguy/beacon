package gate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Rule names reported by CheckToolContract.
const (
	ToolNameFormatRule      = "TOOL-NAME-FORMAT"
	ToolNameDuplicateRule   = "TOOL-NAME-DUPLICATE"
	ToolDescShortRule       = "TOOL-DESC-SHORT"
	ToolParamDescRule       = "TOOL-PARAM-DESC"
	ToolRequiredRule        = "TOOL-REQUIRED"
	ToolAdditionalPropsRule = "TOOL-ADDITIONAL-PROPS"
	ToolDescNearDupRule     = "TOOL-DESC-NEAR-DUP"
	ToolListInvalidRule     = "TOOL-LIST-INVALID"
)

// toolNamePattern is the name grammar recommended by the MCP specification:
// 1 to 64 ASCII letters, digits, underscores or hyphens.
var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// MinDescriptionWords is the smallest accepted tool description.
const MinDescriptionWords = 8

// NearDuplicateThreshold is the Jaccard score of word sets at which two
// descriptions count as near duplicates. It is a ranking heuristic, not a
// claim about how a language model reads the descriptions.
const NearDuplicateThreshold = 0.8

// ToolContractResult is the outcome of checking one saved tools/list response.
// ByteSize is the exact length of the input in bytes. Nothing is printed to
// stdout; callers format the result with Report.
type ToolContractResult struct {
	ByteSize int
	Findings []Finding
}

// Report returns the one-line summary for a check. It writes no output.
func (r ToolContractResult) Report() string {
	return fmt.Sprintf("tools/list: %d bytes, %d finding(s)", r.ByteSize, len(r.Findings))
}

type toolListEnvelope struct {
	Tools  []toolEntry       `json:"tools"`
	Result *toolListEnvelope `json:"result"`
}

type toolEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type inputSchemaDoc struct {
	Properties           map[string]propertySchemaDoc `json:"properties"`
	Required             []string                     `json:"required"`
	AdditionalProperties json.RawMessage              `json:"additionalProperties"`
}

type propertySchemaDoc struct {
	Description string `json:"description"`
}

// CheckToolContract validates a saved tools/list response. It accepts both a
// bare {"tools": [...]} document and a JSON-RPC result envelope. The check is
// offline: it reads the bytes it is given and never contacts a server or
// writes to stdout.
func CheckToolContract(data []byte) ToolContractResult {
	res := ToolContractResult{ByteSize: len(data)}

	var env toolListEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		res.Findings = append(res.Findings, Finding{
			Rule:    ToolListInvalidRule,
			Path:    "tools/list",
			Message: fmt.Sprintf("invalid tools/list JSON: %v", err),
		})
		return res
	}
	tools := env.Tools
	if len(tools) == 0 && env.Result != nil {
		tools = env.Result.Tools
	}

	seen := map[string]int{}
	type description struct {
		name  string
		words map[string]bool
	}
	var descriptions []description

	for i, tool := range tools {
		line := i + 1

		if !toolNamePattern.MatchString(tool.Name) {
			res.Findings = append(res.Findings, Finding{
				Rule:    ToolNameFormatRule,
				Path:    "tools/list",
				Line:    line,
				Message: fmt.Sprintf("tool %d name %q is not a 1-64 character [a-zA-Z0-9_-] name", line, tool.Name),
			})
		}
		if first, ok := seen[tool.Name]; ok {
			res.Findings = append(res.Findings, Finding{
				Rule:    ToolNameDuplicateRule,
				Path:    "tools/list",
				Line:    line,
				Message: fmt.Sprintf("tool %d repeats name %q first used by tool %d", line, tool.Name, first),
			})
		} else {
			seen[tool.Name] = line
		}

		descriptionWords := splitWords(tool.Description)
		if len(strings.Fields(tool.Description)) < MinDescriptionWords {
			res.Findings = append(res.Findings, Finding{
				Rule:    ToolDescShortRule,
				Path:    "tools/list",
				Line:    line,
				Message: fmt.Sprintf("tool %q description has %d words, minimum is %d", tool.Name, len(strings.Fields(tool.Description)), MinDescriptionWords),
			})
		}
		descriptions = append(descriptions, description{name: tool.Name, words: descriptionWords})

		if len(tool.InputSchema) == 0 {
			res.Findings = append(res.Findings, Finding{
				Rule:    ToolAdditionalPropsRule,
				Path:    "tools/list",
				Line:    line,
				Message: fmt.Sprintf("tool %q has no inputSchema with additionalProperties=false", tool.Name),
			})
			continue
		}

		var schema inputSchemaDoc
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			res.Findings = append(res.Findings, Finding{
				Rule:    ToolListInvalidRule,
				Path:    "tools/list",
				Line:    line,
				Message: fmt.Sprintf("tool %q inputSchema is invalid JSON: %v", tool.Name, err),
			})
			continue
		}

		declared := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			declared = append(declared, name)
		}
		sort.Strings(declared)

		for _, name := range declared {
			if strings.TrimSpace(schema.Properties[name].Description) == "" {
				res.Findings = append(res.Findings, Finding{
					Rule:    ToolParamDescRule,
					Path:    "tools/list",
					Line:    line,
					Message: fmt.Sprintf("tool %q parameter %q has an empty description", tool.Name, name),
				})
			}
		}
		for _, name := range schema.Required {
			if _, ok := schema.Properties[name]; !ok {
				res.Findings = append(res.Findings, Finding{
					Rule:    ToolRequiredRule,
					Path:    "tools/list",
					Line:    line,
					Message: fmt.Sprintf("tool %q requires %q, which is not a declared parameter", tool.Name, name),
				})
			}
		}
		if len(schema.Properties) == 0 {
			if !additionalPropertiesFalse(schema.AdditionalProperties) {
				res.Findings = append(res.Findings, Finding{
					Rule:    ToolAdditionalPropsRule,
					Path:    "tools/list",
					Line:    line,
					Message: fmt.Sprintf("parameterless tool %q must set additionalProperties=false", tool.Name),
				})
			}
			if len(schema.Required) > 0 {
				res.Findings = append(res.Findings, Finding{
					Rule:    ToolRequiredRule,
					Path:    "tools/list",
					Line:    line,
					Message: fmt.Sprintf("parameterless tool %q must not declare required parameters", tool.Name),
				})
			}
		}
	}

	for i := 0; i < len(descriptions); i++ {
		for j := i + 1; j < len(descriptions); j++ {
			score := jaccard(descriptions[i].words, descriptions[j].words)
			if score >= NearDuplicateThreshold {
				res.Findings = append(res.Findings, Finding{
					Rule:    ToolDescNearDupRule,
					Path:    "tools/list",
					Line:    i + 1,
					Message: fmt.Sprintf("descriptions of %q and %q are %.2f similar, threshold %.2f", descriptions[i].name, descriptions[j].name, score, NearDuplicateThreshold),
				})
			}
		}
	}

	SortFindings(res.Findings)
	return res
}

// splitWords lowers the description and returns its distinct words.
func splitWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		out[w] = true
	}
	return out
}

// jaccard scores two word sets. Two empty sets score zero because the short
// description rule already reports them.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter, union := 0, 0
	for w := range a {
		union++
		if b[w] {
			inter++
		}
	}
	for w := range b {
		if !a[w] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// additionalPropertiesFalse accepts only the JSON literal false. A missing
// value, a schema object or true all leave the input schema open.
func additionalPropertiesFalse(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "false"
}
