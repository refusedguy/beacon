package shared

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DetectWorkflowJobNames collects the required-check names from
// .github/workflows/*.y*ml under root. It is the single parser behind config
// load, beacon init and the workflow check source, so every resolver returns
// the same list for the same fixture.
func DetectWorkflowJobNames(root string) []string {
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		return []string{}
	}
	seen := make(map[string]bool)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping workflow %s: %v\n", file, err)
			continue
		}
		for _, name := range ParseWorkflowJobNames(string(data)) {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ParseWorkflowJobNames extracts job names from one workflow document. Only
// the two-space indented keys directly under jobs: are names; nested matrix
// and step keys sit deeper and are ignored. A job with strategy.matrix is
// omitted because GitHub reports only the expanded cell names. Keys such as
// fail-fast that sit beside matrix under strategy: do not hide the matrix.
func ParseWorkflowJobNames(content string) []string {
	var names []string
	inJobs := false
	var current string
	var body []string
	flush := func() {
		if current == "" || hasMatrix(body) {
			return
		}
		names = append(names, current)
	}
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "jobs:") {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
			name, _, ok := strings.Cut(strings.TrimSpace(line), ":")
			if ok && name != "" && !strings.HasPrefix(name, "#") {
				flush()
				current = name
				body = nil
				continue
			}
		}
		if current != "" {
			body = append(body, line)
		}
	}
	flush()
	return names
}

// hasMatrix reports whether a job body declares strategy.matrix. A sibling
// key such as fail-fast never ends the strategy block; only the next
// two-space job key does.
func hasMatrix(lines []string) bool {
	inStrategy := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "strategy:" {
			inStrategy = true
			continue
		}
		if inStrategy && trimmed == "matrix:" {
			return true
		}
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if inStrategy && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && trimmed != "" {
			inStrategy = false
		}
	}
	return false
}
