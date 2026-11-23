// Package tasksource adapts local markdown cards and GitHub issues to app.TaskSource.
package tasksource

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
	"gopkg.in/yaml.v3"
)

type Markdown struct {
	Dir string
}

func NewMarkdown(dir string) *Markdown { return &Markdown{Dir: dir} }

func (m *Markdown) ListAvailable(ctx context.Context) ([]core.Task, error) {
	_ = ctx
	paths, err := m.cards()
	if err != nil {
		return nil, err
	}
	var out []core.Task
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		t, ok := parseCard(string(b), path)
		if !ok || t.Status != core.TaskAvailable {
			continue
		}
		out = append(out, t)
	}
	core.SortTasks(out)
	return out, nil
}

func (m *Markdown) cards() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(m.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != m.Dir && strings.HasPrefix(d.Name(), ".") {
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

type frontmatter struct {
	ID        string     `yaml:"id"`
	Title     string     `yaml:"title"`
	Status    string     `yaml:"status"`
	Scope     stringList `yaml:"scope"`
	Depends   stringList `yaml:"depends"`
	DependsOn stringList `yaml:"depends_on"`
	Priority  int        `yaml:"priority"`
	SourceRef string     `yaml:"source_ref"`
	Phase     string     `yaml:"phase"`
}

func splitFrontmatter(s string) (yamlText, body string, ok bool) {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", "", false
}

func parseCard(content, path string) (core.Task, bool) {
	dir := filepath.Dir(path)
	fmText, body, hasFM := splitFrontmatter(content)
	var t core.Task
	if hasFM {
		var fm frontmatter
		if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
			return t, false
		}
		t.ID = core.TaskID(strings.TrimSpace(fm.ID))
		t.Title = strings.TrimSpace(fm.Title)
		t.Status = normalizeStatus(fm.Status)
		t.Priority = fm.Priority
		for _, p := range fm.Scope {
			t.Scope = append(t.Scope, scopeForPath(p, dir))
		}
		for _, d := range append(fm.Depends, fm.DependsOn...) {
			if d = strings.TrimSpace(d); d != "" {
				t.DependsOn = append(t.DependsOn, core.TaskID(d))
			}
		}
		if fm.Phase != "" {
			body = "Phase: " + fm.Phase + "\n" + body
		}
		if fm.SourceRef != "" {
			body = "SourceRef: " + fm.SourceRef + "\n" + body
		}
		t.Body = strings.TrimSpace(body)
		if t.ID == "" {
			t.ID, t.Title = headingID(content)
		}
	} else {
		t = parseLegacy(content, dir)
	}
	t.SourceRef = path
	return t, t.ID != ""
}

func headingID(s string) (core.TaskID, string) {
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		head := strings.TrimPrefix(line, "# ")
		if i := strings.Index(head, "."); i > 0 {
			return core.TaskID(strings.TrimSpace(head[:i])), strings.TrimSpace(head[i+1:])
		}
	}
	return "", ""
}

func parseLegacy(content, dir string) core.Task {
	t := core.Task{}
	t.ID, t.Title = headingID(content)
	var extra []string
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Статус:"):
			t.Status = normalizeStatus(strings.TrimPrefix(line, "Статус:"))
		case strings.HasPrefix(line, "Владелец файлов:"):
			for _, p := range splitList(strings.TrimPrefix(line, "Владелец файлов:")) {
				t.Scope = append(t.Scope, scopeForPath(p, dir))
			}
		case strings.HasPrefix(line, "Зависимости:"):
			for _, d := range splitList(strings.TrimPrefix(line, "Зависимости:")) {
				t.DependsOn = append(t.DependsOn, core.TaskID(d))
			}
		case strings.HasPrefix(line, "Фаза:"), strings.HasPrefix(line, "Ссылка:"):
			extra = append(extra, line)
		}
	}
	t.Body = strings.Join(extra, "\n")
	return t
}

func (m *Markdown) findCard(id core.TaskID) (string, bool) {
	paths, err := m.cards()
	if err != nil {
		return "", false
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		t, ok := parseCard(string(b), path)
		if ok && t.ID == id {
			return path, true
		}
	}
	return "", false
}

func (m *Markdown) rewriteStatus(ctx context.Context, id core.TaskID, status string) error {
	_ = ctx
	path, ok := m.findCard(id)
	if !ok {
		return os.ErrNotExist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n")
	inFM := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	closeAt := -1
	if inFM {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				closeAt = i
				break
			}
		}
	}
	updated := false
	for i, line := range lines {
		if inFM && closeAt >= 0 && i > closeAt {
			break
		}
		trim := strings.TrimSpace(line)
		if inFM && i > 0 && i < closeAt {
			if strings.HasPrefix(trim, "status:") {
				lines[i] = "status: " + status
				updated = true
				break
			}
			continue
		}
		if strings.HasPrefix(trim, "Статус:") {
			lines[i] = "Статус: " + status
			updated = true
		}
		if updated {
			break
		}
	}
	if !updated {
		if inFM && closeAt >= 0 {
			lines = append(lines[:closeAt], append([]string{"status: " + status}, lines[closeAt:]...)...)
		} else {
			lines = append([]string{"Статус: " + status}, lines...)
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func (m *Markdown) Claim(ctx context.Context, id core.TaskID) error {
	return m.rewriteStatus(ctx, id, "in_progress")
}

func (m *Markdown) Complete(ctx context.Context, id core.TaskID) error {
	return m.rewriteStatus(ctx, id, "done")
}
