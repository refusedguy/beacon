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

// Diff is the local change set used to describe a PR. Base and Head name the
// two refs and Patch holds the unified diff. An empty Patch means there is
// nothing to describe, so the card source stays authoritative.
type Diff struct {
	Base  string
	Head  string
	Patch string
}

// DiffSource reads the diff between two local refs. It is a port: cmd passes
// the git process call through a seam, so the app layer holds no exec
// capability (ADR 0005).
type DiffSource interface {
	Diff(ctx context.Context, base, head string) (Diff, error)
}

// PRText is one generated title and body proposal.
type PRText struct {
	Title string
	Body  string
}

// Model is one endpoint in the generation chain. CountTokens reports the
// prompt size in this endpoint's own accounting and Window is the largest
// prompt it accepts. Generate is called only after the prompt fits the window,
// so an oversized diff moves to the next model instead of overflowing one.
// cmd supplies the network adapter (ADR 0005).
type Model interface {
	Name() string
	Window() int
	CountTokens(prompt string) int
	Generate(ctx context.Context, prompt string) (PRText, error)
}

// DiffPrompt renders the model input. The diff is the source of what changed;
// card fields only supply project vocabulary. Nothing is truncated here:
// token counting decides which model can take the whole prompt.
func DiffPrompt(d Diff, card Card) string {
	var b strings.Builder
	b.WriteString("Generate a pull request title and body from the diff.\n")
	if title := strings.TrimSpace(card.Title); title != "" {
		fmt.Fprintf(&b, "Context title: %s\n", title)
	}
	if body := strings.TrimSpace(card.Body); body != "" {
		fmt.Fprintf(&b, "Context: %s\n", body)
	}
	fmt.Fprintf(&b, "Base: %s\nHead: %s\n", d.Base, d.Head)
	b.WriteString("Diff:\n")
	b.WriteString(d.Patch)
	return b.String()
}

// GenerateFromDiff walks the model chain in order and returns the first
// successful proposal. A model is skipped when its token count exceeds its
// window, so no model receives an oversized request. False means no model
// accepted the prompt or every one failed; the caller then uses the card
// fallback and still produces text.
func GenerateFromDiff(ctx context.Context, prompt string, models []Model) (PRText, bool) {
	for _, m := range models {
		if m == nil || strings.TrimSpace(m.Name()) == "" || m.Window() <= 0 {
			continue
		}
		if m.CountTokens(prompt) > m.Window() {
			continue
		}
		text, err := m.Generate(ctx, prompt)
		if err != nil {
			continue
		}
		title := strings.TrimSpace(text.Title)
		if title == "" {
			continue
		}
		return PRText{Title: title, Body: strings.TrimSpace(text.Body)}, true
	}
	return PRText{}, false
}
