package checksource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// Workflow resolves required check names from CI workflow files under Root.
// It parses the files directly so the source has no dependency on a sibling
// infra adapter. Detect is injectable for tests; nil uses the parser.
type Workflow struct {
	Root   string
	Detect func(root string) []string
}

var _ core.RequiredCheckSource = Workflow{}

// Names returns the sorted job names found in the workflow files.
func (w Workflow) Names(ctx context.Context) ([]string, error) {
	detect := w.Detect
	if detect == nil {
		detect = detectJobNames
	}
	return detect(w.Root), nil
}

// detectJobNames collects job names from .github/workflows/*.y*ml. It mirrors
// the parser config load uses, kept local so checksource stays independent.
func detectJobNames(root string) []string {
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
		for _, name := range parseJobNames(string(data)) {
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

// parseJobNames extracts job names from one workflow document. Only the
// two-space indented keys directly under jobs: are names; nested matrix and
// step keys sit deeper and are ignored. A job with strategy.matrix is omitted
// because GitHub reports only the expanded cell names.
func parseJobNames(content string) []string {
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

// hasMatrix reports whether a job body declares strategy.matrix.
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
