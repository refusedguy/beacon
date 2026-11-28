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
// the same list for the same fixture. Reusable workflow files are skipped
// because their inner jobs reach GitHub only as caller-qualified checks.
func DetectWorkflowJobNames(root string) []string {
	files, err := workflowFiles(root)
	if err != nil {
		return []string{}
	}
	seen := make(map[string]bool)
	for _, file := range files {
		content, err := readWorkflow(file)
		if err != nil {
			continue
		}
		if isReusableWorkflow(content) {
			continue
		}
		for _, name := range ParseWorkflowJobNames(content) {
			seen[name] = true
		}
	}
	return sortedNames(seen)
}

// DetectUnreportableJobNames collects names GitHub never reports as standalone
// check runs. Matrix parent jobs expand into cell names, and the inner jobs of
// a reusable workflow appear only under the caller's job name. A required
// check list must not contain any of these names.
func DetectUnreportableJobNames(root string) []string {
	files, err := workflowFiles(root)
	if err != nil {
		return []string{}
	}
	seen := make(map[string]bool)
	for _, file := range files {
		content, err := readWorkflow(file)
		if err != nil {
			continue
		}
		if isReusableWorkflow(content) {
			for _, name := range rawJobNames(content) {
				seen[name] = true
			}
			continue
		}
		for _, job := range parseWorkflowJobs(content) {
			if hasMatrix(job.body) {
				seen[job.name] = true
			}
		}
	}
	return sortedNames(seen)
}

func workflowFiles(root string) ([]string, error) {
	return filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
}

func readWorkflow(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: skipping workflow %s: %v\n", file, err)
		return "", err
	}
	return string(data), nil
}

func sortedNames(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// workflowJob holds one job key and the lines nested under it. The parser
// keeps the raw body so each check can inspect indentation on its own.
type workflowJob struct {
	name string
	body []string
}

// ParseWorkflowJobNames extracts check names from one workflow document. Only
// keys directly under jobs: are names; nested matrix and step keys sit deeper
// and are ignored. A job with strategy.matrix contributes the cell names
// GitHub reports for its include entries instead of the unreportable parent
// name. A job with a job-level continue-on-error: true is omitted because it
// never gates merge. A step with the same flag does not make the job advisory.
func ParseWorkflowJobNames(content string) []string {
	var names []string
	for _, job := range parseWorkflowJobs(content) {
		if hasContinueOnError(job.body) {
			continue
		}
		if hasMatrix(job.body) {
			names = append(names, matrixCellNames(job.name, job.body)...)
			continue
		}
		names = append(names, job.name)
	}
	return names
}

// ParseConditionalCellNames returns the matrix cell names of jobs that carry a
// job-level if gate. GitHub skips or omits those cells when the condition is
// false, so a required-check policy must treat them as conditional checks.
func ParseConditionalCellNames(content string) []string {
	var names []string
	for _, job := range parseWorkflowJobs(content) {
		if hasIfGate(job.body) {
			names = append(names, matrixCellNames(job.name, job.body)...)
		}
	}
	return names
}

func rawJobNames(content string) []string {
	var names []string
	for _, job := range parseWorkflowJobs(content) {
		names = append(names, job.name)
	}
	return names
}

// parseWorkflowJobs reads the jobs: block. The first indented key under jobs:
// sets the job indentation, so workflows using two or four spaces both work.
// A job key ends the previous job; its own lines are not part of that body.
func parseWorkflowJobs(content string) []workflowJob {
	var jobs []workflowJob
	inJobs := false
	jobIndent := -1
	current := -1
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inJobs {
			if keyOf(line) == "jobs" {
				inJobs = true
			}
			continue
		}
		if trimmed != "" && leadingSpaces(line) == 0 {
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if current >= 0 {
				jobs[current].body = append(jobs[current].body, line)
			}
			continue
		}
		indent := leadingSpaces(line)
		if jobIndent < 0 {
			jobIndent = indent
		}
		name, _, ok := strings.Cut(trimmed, ":")
		if indent == jobIndent && ok && name != "" {
			jobs = append(jobs, workflowJob{name: strings.TrimSpace(name)})
			current = len(jobs) - 1
			continue
		}
		if current >= 0 {
			jobs[current].body = append(jobs[current].body, line)
		}
	}
	return jobs
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

func keyOf(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	if leadingSpaces(line) != 0 {
		return ""
	}
	key, _, ok := strings.Cut(trimmed, ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}

// fieldIndent is the indentation shared by the keys directly under a job.
// Nested step and strategy keys sit deeper.
func fieldIndent(lines []string) int {
	min := -1
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if min < 0 || indent < min {
			min = indent
		}
	}
	return min
}

// hasContinueOnError reports whether the job body declares continue-on-error:
// true at the job's own field indentation. A step-level flag sits deeper and
// does not make the job advisory.
func hasContinueOnError(lines []string) bool {
	field := fieldIndent(lines)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || leadingSpaces(line) != field {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if ok && strings.TrimSpace(key) == "continue-on-error" &&
			strings.EqualFold(strings.TrimSpace(value), "true") {
			return true
		}
	}
	return false
}

// hasMatrix reports whether the job body declares a matrix nested under
// strategy. The matrix may follow any other strategy key, use a different key
// order or appear inline, and none of those forms hides it.
func hasMatrix(lines []string) bool {
	field := fieldIndent(lines)
	if field < 0 {
		return false
	}
	inStrategy := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if leadingSpaces(line) == field {
			inStrategy = key == "strategy"
			if inStrategy && strings.Contains(value, "matrix:") {
				return true
			}
			continue
		}
		if inStrategy && leadingSpaces(line) > field && key == "matrix" {
			return true
		}
	}
	return false
}

// hasIfGate reports whether the job body declares if at the job's own field
// indentation. A step-level if sits deeper and does not gate the job.
func hasIfGate(lines []string) bool {
	field := fieldIndent(lines)
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

// matrixCellNames renders each include entry the way GitHub names its check
// run: "<job> (<value1>, <value2>, ...)". Values keep the key order of the
// entry. A matrix without include entries yields no cells here; its parent is
// unreportable and is collected by DetectUnreportableJobNames.
func matrixCellNames(job string, lines []string) []string {
	var names []string
	for _, values := range matrixIncludeEntries(lines) {
		names = append(names, job+" ("+strings.Join(values, ", ")+")")
	}
	return names
}

// matrixIncludeEntries reads strategy.matrix.include from a job body and
// returns the scalar values of each entry in declaration order. Nested values
// are skipped because GitHub renders them differently from scalars.
func matrixIncludeEntries(lines []string) [][]string {
	matrix := strategyMatrixBlock(lines)
	if matrix == nil {
		return nil
	}
	include := nestedYAMLBlock(matrix, "include")
	if include == nil {
		return nil
	}
	var entries [][]string
	var entry []string
	for _, line := range include {
		value, listed := includeValueLine(strings.TrimSpace(line))
		if listed {
			entries = appendEntry(entries, entry)
			entry = nil
		}
		if value != "" {
			entry = append(entry, value)
		}
	}
	return appendEntry(entries, entry)
}

// strategyMatrixBlock returns the lines nested under strategy.matrix in a job
// body, or nil when the job has no matrix block.
func strategyMatrixBlock(lines []string) []string {
	field := fieldIndent(lines)
	if field < 0 {
		return nil
	}
	var strategy []string
	indent := 0
	found := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := leadingSpaces(line)
		if found {
			if level <= indent {
				break
			}
			strategy = append(strategy, line)
			continue
		}
		if level == field {
			if key, _, ok := strings.Cut(trimmed, ":"); ok && key == "strategy" {
				found = true
				indent = level
			}
		}
	}
	if !found {
		return nil
	}
	return nestedYAMLBlock(strategy, "matrix")
}

// nestedYAMLBlock returns the lines nested under key, stopping at the first
// non-comment line at the key's own indentation or shallower.
func nestedYAMLBlock(lines []string, key string) []string {
	start := -1
	indent := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, _, ok := strings.Cut(trimmed, ":")
		if ok && name == key {
			start = i + 1
			indent = leadingSpaces(line)
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if leadingSpaces(line) <= indent {
			break
		}
		out = append(out, line)
	}
	return out
}

// includeValueLine extracts a scalar include value. listed reports whether
// the line starts a new entry with a leading dash.
func includeValueLine(trimmed string) (value string, listed bool) {
	item, hasDash := strings.CutPrefix(trimmed, "- ")
	if !hasDash {
		item = trimmed
	}
	value, ok := cellValue(item)
	return value, ok && hasDash
}

// appendEntry appends a non-empty cell entry to entries.
func appendEntry(entries [][]string, entry []string) [][]string {
	if len(entry) == 0 {
		return entries
	}
	return append(entries, entry)
}

// cellValue extracts a scalar YAML value used in a matrix include entry.
func cellValue(line string) (string, bool) {
	key, raw, ok := strings.Cut(line, ":")
	if !ok || strings.TrimSpace(key) == "" {
		return "", false
	}
	value := strings.TrimSpace(raw)
	if value == "" || strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") ||
		strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
		return "", false
	}
	return strings.Trim(value, "\"'"), true
}

// isReusableWorkflow reports whether the document declares a workflow_call
// trigger. Reusable workflow jobs are not standalone checks in a caller.
func isReusableWorkflow(content string) bool {
	inOn := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if leadingSpaces(line) == 0 {
			inOn = key == "on"
			if inOn && strings.Contains(trimmed, "workflow_call") {
				return true
			}
			continue
		}
		if inOn && key == "workflow_call" {
			return true
		}
	}
	return false
}
