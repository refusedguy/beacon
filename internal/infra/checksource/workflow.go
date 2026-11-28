package checksource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

// Workflow resolves required check names from CI workflow files under Root.
// It uses the shared parser so the names match config load. Detect is
// injectable for tests; nil uses the shared parser. Fallback names are used
// only when detection finds none; an empty fallback makes Names fail so an
// unreadable or renamed workflow cannot open the merge gate. ConditionalDetect
// is injectable for tests; nil reads job-level if gates from the workflow
// files.
type Workflow struct {
	Root              string
	Detect            func(root string) []string
	Fallback          []string
	ConditionalDetect func(root string) []string
}

var _ core.RequiredCheckSource = Workflow{}

// Names returns the sorted job names found in the workflow files on every
// call, so a rename takes effect on the next evaluation. It returns the
// fallback when no names are detected and an error when neither has a name.
func (w Workflow) Names(ctx context.Context) ([]string, error) {
	detect := w.Detect
	if detect == nil {
		detect = shared.DetectWorkflowJobNames
	}
	names := detect(w.Root)
	if len(names) > 0 {
		return names, nil
	}
	if len(w.Fallback) > 0 {
		return append([]string(nil), w.Fallback...), nil
	}
	return nil, fmt.Errorf("no required checks detected in workflows under %s", w.Root)
}

// ConditionalNames returns the required names whose workflow job declares a
// job-level if gate. GitHub reports such a job as skipped, or omits it, when
// the condition is false. It implements the optional capability policy uses
// to accept those states without opening the gate for a missing required job.
// The result is always a subset of Names, so a fallback list never gains a
// gate from an unrelated workflow file.
func (w Workflow) ConditionalNames(ctx context.Context) ([]string, error) {
	names, err := w.Names(ctx)
	if err != nil {
		return nil, err
	}
	detect := w.ConditionalDetect
	if detect == nil {
		detect = DetectConditionalJobNames
	}
	gated := detect(w.Root)
	conditional := make([]string, 0, len(gated))
	for _, name := range names {
		for _, candidate := range gated {
			if strings.EqualFold(name, candidate) {
				conditional = append(conditional, name)
				break
			}
		}
	}
	return conditional, nil
}

// DetectConditionalJobNames returns the check names that declare a job-level
// if gate in any workflow file under root. Matrix cells inherit their parent's
// gate because GitHub skips or omits the cells when the condition is false. A
// step-level if is ignored because its job still reports a check to GitHub.
func DetectConditionalJobNames(root string) []string {
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, name := range conditionalJobs(string(data)) {
			seen[name] = true
		}
		for _, name := range shared.ParseConditionalCellNames(string(data)) {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// conditionalJobs returns the jobs in one workflow document whose own field
// block has an if key.
func conditionalJobs(content string) []string {
	var out []string
	for _, job := range parseConditionalJobs(content) {
		if jobHasIfGate(job.body) {
			out = append(out, job.name)
		}
	}
	return out
}

// workflowJob holds a job key and the lines nested under it.
type workflowJob struct {
	name string
	body []string
}

// parseConditionalJobs reads the jobs: block of one workflow document. The
// first indented key under jobs: sets the job indentation, so two- and
// four-space workflows both work. A job key ends the previous job; its own
// lines are not part of that body.
func parseConditionalJobs(content string) []workflowJob {
	var jobs []workflowJob
	inJobs := false
	jobIndent := -1
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inJobs {
			if topLevelKey(line) == "jobs" {
				inJobs = true
			}
			continue
		}
		if trimmed != "" && leadingSpaces(line) == 0 {
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if jobIndent < 0 {
			jobIndent = indent
		}
		name, _, ok := strings.Cut(trimmed, ":")
		if indent == jobIndent && ok && name != "" {
			jobs = append(jobs, workflowJob{name: strings.TrimSpace(name)})
			continue
		}
		if len(jobs) > 0 {
			jobs[len(jobs)-1].body = append(jobs[len(jobs)-1].body, line)
		}
	}
	return jobs
}

// jobHasIfGate reports whether the job body declares if at the job's own field
// indentation. Keys deeper than that belong to a step or a strategy and do not
// gate the job itself.
func jobHasIfGate(lines []string) bool {
	field := -1
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if indent := leadingSpaces(line); field < 0 || indent < field {
			field = indent
		}
	}
	if field < 0 {
		return false
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || leadingSpaces(line) != field {
			continue
		}
		key, _, ok := strings.Cut(trimmed, ":")
		if ok && strings.TrimSpace(key) == "if" {
			return true
		}
	}
	return false
}

func topLevelKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || leadingSpaces(line) != 0 {
		return ""
	}
	key, _, ok := strings.Cut(trimmed, ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}
