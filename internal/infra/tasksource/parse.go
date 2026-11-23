package tasksource

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/refusedguy/beacon/pkg/core"
)

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
	ID         string     `yaml:"id"`
	TaskID     string     `yaml:"task_id"`
	Scope      stringList `yaml:"scope"`
	OwnerFiles stringList `yaml:"owner_files"`
	Depends    stringList `yaml:"depends"`
	DependsOn  stringList `yaml:"depends_on"`
	Priority   int        `yaml:"priority"`
}

type taskFields struct {
	ID       string
	Scope    []string
	Depends  []string
	Priority int
}

// ParseTaskFields reads a fenced YAML block and flat Scope/Depends/Priority lines.
func ParseTaskFields(body, dir, depPrefix string) taskFields {
	var out taskFields
	var yf yamlFields
	if block, ok := yamlBlock(body); ok {
		if err := yaml.Unmarshal([]byte(block), &yf); err == nil {
			out.ID = firstNonEmpty(yf.TaskID, yf.ID)
			out.Scope = append(out.Scope, yf.Scope...)
			out.Scope = append(out.Scope, yf.OwnerFiles...)
			out.Depends = append(out.Depends, yf.Depends...)
			out.Depends = append(out.Depends, yf.DependsOn...)
			out.Priority = yf.Priority
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
	_ = dir
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
