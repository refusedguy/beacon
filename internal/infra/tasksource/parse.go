package tasksource

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/refusedguy/beacon/pkg/core"
)

// CardStore owns one card directory. PatchStatus is its one deep operation:
// callers name a card and a status, and the store handles lookup, parsing,
// format detection and the write. Card paths and status-line rules stay inside
// this module instead of spreading across its callers.
type CardStore struct {
	Dir string
}

// card is one located card with the parsed task and the raw text. Carrying all
// three lets callers work from a single read instead of re-reading the file.
type card struct {
	path    string
	task    core.Task
	content string
	hasFM   bool
}

func (s CardStore) cards() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(s.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != s.Dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

// find locates a card by parsed ID, not by filename, and returns the text it
// was parsed from so callers need no second read.
func (s CardStore) find(id core.TaskID) (card, error) {
	paths, err := s.cards()
	if err != nil {
		return card{}, err
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		t, ok := parseCard(string(b), path)
		if !ok || t.ID != id {
			continue
		}
		_, _, hasFM := splitFrontmatter(string(b))
		return card{path: path, task: t, content: string(b), hasFM: hasFM}, nil
	}
	return card{}, os.ErrNotExist
}

// PatchStatus finds a card by id and rewrites its status. It owns the whole
// chain, so Claim, Complete and workspace marking share one implementation.
func (s CardStore) PatchStatus(ctx context.Context, id core.TaskID, status string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, err := s.find(id)
	if err != nil {
		return err
	}
	updated := rewriteStatus(c.content, status, c.hasFM)
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(c.path, []byte(updated), 0o644)
}

type stringList []string

func (l *stringList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*l = splitList(s)
		return nil
	}
	var parts []string
	if err := node.Decode(&parts); err != nil {
		return err
	}
	for _, p := range parts {
		for _, v := range splitList(p) {
			*l = append(*l, v)
		}
	}
	return nil
}

type yamlFields struct {
	ID            string     `yaml:"id"`
	TaskID        string     `yaml:"task_id"`
	Scope         stringList `yaml:"scope"`
	OwnerFiles    stringList `yaml:"owner_files"`
	Depends       stringList `yaml:"depends"`
	DependsOn     stringList `yaml:"depends_on"`
	Priority      int        `yaml:"priority"`
	Kind          string     `yaml:"kind"`
	BaseBranch    string     `yaml:"base_branch"`
	RequireReview string     `yaml:"require_review"`
	Plan          []planSpec `yaml:"plan"`
}

// planSpec is the wire shape of one child in an epic's plan array.
type planSpec struct {
	Title    string     `yaml:"title"`
	Body     string     `yaml:"body,omitempty"`
	Scope    stringList `yaml:"scope"`
	Depends  stringList `yaml:"depends"`
	Priority int        `yaml:"priority"`
}

// epicFields holds the epic-only frontmatter written by Markdown.Create.
type epicFields struct {
	Kind          string     `yaml:"kind,omitempty"`
	BaseBranch    string     `yaml:"base_branch,omitempty"`
	RequireReview string     `yaml:"require_review,omitempty"`
	Plan          []planSpec `yaml:"plan,omitempty"`
}

type taskFields struct {
	ID            string
	Scope         []string
	Depends       []string
	Priority      int
	Kind          core.TaskKind
	BaseBranch    string
	RequireReview core.ReviewMode
	Plan          []core.ChildSpec
}

func childSpecs(specs []planSpec, dir string) []core.ChildSpec {
	out := make([]core.ChildSpec, 0, len(specs))
	for _, spec := range specs {
		child := core.ChildSpec{Title: strings.TrimSpace(spec.Title), Body: strings.TrimSpace(spec.Body), Priority: spec.Priority}
		for _, p := range spec.Scope {
			child.Scope = append(child.Scope, scopeForPath(p, dir))
		}
		for _, d := range spec.Depends {
			if d = strings.TrimSpace(d); d != "" {
				child.Depends = append(child.Depends, core.TaskID(d))
			}
		}
		out = append(out, child)
	}
	return out
}

func planSpecs(specs []core.ChildSpec) []planSpec {
	out := make([]planSpec, 0, len(specs))
	for _, spec := range specs {
		out = append(out, planSpec{
			Title:    spec.Title,
			Body:     spec.Body,
			Scope:    scopeValues(spec.Scope),
			Depends:  dependencyValues(spec.Depends),
			Priority: spec.Priority,
		})
	}
	return out
}

func normalizeKind(s string) core.TaskKind {
	return core.NormalizeTaskKind(core.TaskKind(s))
}

func normalizeReview(s string) core.ReviewMode {
	return core.NormalizeReviewMode(core.ReviewMode(s))
}

// PlanParser is the default core.EpicParser. It numbers children in plan order.
type PlanParser struct{}

var _ core.EpicParser = PlanParser{}

func (PlanParser) Children(epic core.Task) []core.Task {
	return core.ExpandChildren(epic, "")
}

// ParseTaskFields reads a fenced YAML block and flat Scope/Depends/Priority lines.
func ParseTaskFields(body, dir, depPrefix string) taskFields {
	var out taskFields
	var yf yamlFields
	if block, ok := yamlBlock(body); ok {
		if err := yaml.Unmarshal([]byte(block), &yf); err != nil {
			slog.Warn("tasksource: malformed YAML field block", "error", err)
		} else {
			out.ID = firstNonEmpty(yf.TaskID, yf.ID)
			out.Scope = append(out.Scope, yf.Scope...)
			out.Scope = append(out.Scope, yf.OwnerFiles...)
			out.Depends = append(out.Depends, yf.Depends...)
			out.Depends = append(out.Depends, yf.DependsOn...)
			out.Priority = yf.Priority
			out.Kind = normalizeKind(yf.Kind)
			out.BaseBranch = strings.TrimSpace(yf.BaseBranch)
			out.RequireReview = normalizeReview(yf.RequireReview)
			out.Plan = childSpecs(yf.Plan, dir)
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Scope:"), strings.HasPrefix(line, "Владелец файлов:"):
			out.Scope = append(out.Scope, splitList(afterColon(line))...)
		case strings.HasPrefix(line, "Depends:"), strings.HasPrefix(line, "Зависимости:"):
			out.Depends = append(out.Depends, splitList(afterColon(line))...)
		case strings.HasPrefix(line, "Priority:"):
			if n, err := strconv.Atoi(afterColon(line)); err == nil {
				out.Priority = n
			}
		case strings.HasPrefix(line, "Kind:"):
			out.Kind = normalizeKind(afterColon(line))
		case strings.HasPrefix(line, "BaseBranch:"), strings.HasPrefix(line, "Base branch:"):
			out.BaseBranch = afterColon(line)
		case strings.HasPrefix(line, "RequireReview:"):
			out.RequireReview = normalizeReview(afterColon(line))
		case strings.HasPrefix(line, "TaskID:"):
			out.ID = afterColon(line)
		}
	}
	out.Scope = unique(out.Scope)
	out.Depends = unique(out.Depends)
	if depPrefix != "" {
		for i, d := range out.Depends {
			out.Depends[i] = normalizeDep(d, depPrefix)
		}
	}
	return out
}

func (t taskFields) Scopes(dir string) []core.ScopePath {
	var out []core.ScopePath
	for _, p := range t.Scope {
		out = append(out, scopeForPath(p, dir))
	}
	return out
}

func (t taskFields) Dependencies() []core.TaskID {
	var out []core.TaskID
	for _, d := range t.Depends {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, core.TaskID(d))
		}
	}
	return out
}

func normalizeDep(d, prefix string) string {
	d = strings.TrimSpace(strings.Trim(d, "` "))
	if d == "" || strings.Contains(d, "-") {
		return d
	}
	return prefix + "-" + d
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func afterColon(line string) string {
	if i := strings.Index(line, ":"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

func yamlBlock(body string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(body, "\r", ""), "\n")
	var out []string
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "```yaml" || trimmed == "```yml" {
				inBlock = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), len(out) > 0
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		p = strings.Trim(strings.TrimSpace(p), "` ")
		if p != "" && p != "нет" && p != "-" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func scopeForPath(p, dir string) core.ScopePath {
	p = strings.TrimSpace(strings.Trim(p, "` "))
	if strings.Contains(p, "*") {
		return core.ScopePattern(p)
	}
	if strings.HasSuffix(p, "/") || strings.HasSuffix(p, "\\") {
		return core.ScopeDir(p)
	}
	if dir != "" {
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil && info.IsDir() {
			return core.ScopeDir(p)
		}
	}
	return core.ScopeFile(p)
}

func normalizeStatus(s string) core.TaskStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "done", "готово", "closed", "merged", "выполнено":
		return core.TaskDone
	case "dropped", "отменено", "отклонено":
		return core.TaskDropped
	case "in_progress", "in progress", "в работе", "claimed":
		return core.TaskClaimed
	default:
		return core.TaskAvailable
	}
}
