package prcreate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// FileCardSource reads a task card from <dir>/docs/tasks/<id>.md through an
// injected reader. cmd supplies os.ReadFile through a seam, so the app layer
// holds no file capability (ADR 0005).
type FileCardSource struct {
	Dir      string
	ReadFile func(string) ([]byte, error)
}

// Card implements CardSource. The first Markdown heading becomes the PR title;
// the remaining lines become the body. Both are trimmed, so a card with only a
// heading yields an empty body.
func (f FileCardSource) Card(_ context.Context, id string) (Card, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Card{}, fail(CodeCardFailed, "пустой id карточки")
	}
	if f.ReadFile == nil {
		return Card{}, fail(CodeCardFailed, "читатель карточек не подключён")
	}
	dir := f.Dir
	if dir == "" {
		dir = "."
	}
	path := filepath.Join(dir, "docs", "tasks", filepath.Base(id)+".md")
	raw, err := f.ReadFile(path)
	if err != nil {
		return Card{}, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	title := ""
	bodyStart := 0
	for i, line := range lines {
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			bodyStart = i + 1
			break
		}
	}
	if title == "" {
		return Card{}, fail(CodeCardFailed, "в карточке %s нет заголовка", id)
	}
	body := strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))
	return Card{ID: id, Title: title, Body: body}, nil
}
