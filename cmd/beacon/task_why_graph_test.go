package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// TestWhyEdgeREAllCardIDForms проверяет, что ребро находится для всех форм ID,
// которые понимает ParseCard: T3.17, RS-MCP-02, WIRING-485, BS.1 и S1.001.
func TestWhyEdgeREAllCardIDForms(t *testing.T) {
	cases := []struct {
		line, kind, id string
	}{
		{"depends-on T3.17", "depends-on", "T3.17"},
		{"conflicts-with:T3.18", "conflicts-with", "T3.18"},
		{"same-as: T3.19", "same-as", "T3.19"},
		{"depends-on RS-MCP-02", "depends-on", "RS-MCP-02"},
		{"part-of WIRING-485", "part-of", "WIRING-485"},
		{"derived-from BS.1", "derived-from", "BS.1"},
		{"supersedes S1.001", "supersedes", "S1.001"},
		{"conflicts-with:UI-31", "conflicts-with", "UI-31"},
		{"part-of Q-A-03", "part-of", "Q-A-03"},
	}
	for _, tc := range cases {
		m := whyEdgeRE.FindStringSubmatch(tc.line)
		if m == nil {
			t.Errorf("ребро не найдено в %q", tc.line)
			continue
		}
		if m[1] != tc.kind || m[2] != tc.id {
			t.Errorf("%q: получено (%q, %q), ждали (%q, %q)", tc.line, m[1], m[2], tc.kind, tc.id)
		}
	}
	for _, line := range []string{"depends-on простое слово", "blocked BS.1", "depends-on 12345"} {
		if whyEdgeRE.MatchString(line) {
			t.Errorf("ложное срабатывание на %q", line)
		}
	}
}

// TestTaskWhyMixedGraphChain строит цепочку из карточек разных семейств.
// Каждое следующее ребро записано то в Зависимостях, то в тексте, чтобы
// проверить разбор рёбер из Raw и обход графа.
func TestTaskWhyMixedGraphChain(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T9.1", "todo", "", "нет", "Начало. depends-on RS-MCP-02")
	writeWhyCard(t, dir, "RS-MCP-02", "in_progress", "rs-owner", "нет", "conflicts-with WIRING-485")
	writeWhyCard(t, dir, "WIRING-485", "in_progress", "wiring-owner", "BS.1", "Ссылка на вхождение")
	writeWhyCard(t, dir, "BS.1", "in_progress", "bs-owner", "нет", "part-of S1.001")
	writeWhyCard(t, dir, "S1.001", "in_progress", "s1-owner", "нет", "Конец цепочки")

	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T9.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"1. RS-MCP-02 через depends-on: держит rs-owner (in_progress)",
		"2. WIRING-485 через conflicts-with: держит wiring-owner (in_progress)",
		"3. BS.1 через depends-on: держит bs-owner (in_progress)",
		"4. S1.001 через part-of: держит s1-owner (in_progress)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет %q: %q", want, out)
		}
	}
}

// TestTaskWhyMixedGraphDependentsAndCycle проверяет обратные рёбра на
// смешанном графе и то, что обрыв циклов в whyChain не сломан.
func TestTaskWhyMixedGraphDependentsAndCycle(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T9.1", "todo", "", "нет", "depends-on RS-MCP-02")
	writeWhyCard(t, dir, "RS-MCP-02", "in_progress", "rs-owner", "нет", "conflicts-with WIRING-485")
	writeWhyCard(t, dir, "WIRING-485", "in_progress", "wiring-owner", "BS.1", "Ещё depends-on S1.001")
	writeWhyCard(t, dir, "BS.1", "done", "bs-owner", "нет", "нет")
	writeWhyCard(t, dir, "S1.001", "in_progress", "s1-owner", "нет", "нет")
	// Замкнутый цикл из двух карточек разных семейств.
	writeWhyCard(t, dir, "RS-CMP-01", "todo", "", "нет", "depends-on WIRING-486")
	writeWhyCard(t, dir, "WIRING-486", "in_progress", "cycle-owner", "нет", "depends-on RS-CMP-01")

	cards, err := taskstore.LoadCards(dir)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]taskstore.Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}

	deps := whyDependents("WIRING-485", byID)
	if len(deps) != 1 || deps[0].id != "RS-MCP-02" || deps[0].edge != "conflicts-with" {
		t.Fatalf("кто зависит от WIRING-485: %+v", deps)
	}
	deps = whyDependents("BS.1", byID)
	if len(deps) != 1 || deps[0].id != "WIRING-485" || deps[0].edge != "depends-on" {
		t.Fatalf("кто зависит от BS.1: %+v", deps)
	}
	deps = whyDependents("S1.001", byID)
	if len(deps) != 1 || deps[0].id != "WIRING-485" {
		t.Fatalf("кто зависит от S1.001: %+v", deps)
	}
	if got := whyDependents("T9.1", byID); len(got) != 0 {
		t.Fatalf("у T9.1 нет зависимых, получено %+v", got)
	}

	// BS.1 закрыта, поэтому whyChain пропускает её и идёт по второму ребру
	// из текста карточки WIRING-485 к S1.001.
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T9.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"1. RS-MCP-02 через depends-on",
		"2. WIRING-485 через conflicts-with",
		"3. S1.001 через depends-on",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет %q: %q", want, out)
		}
	}
	if strings.Contains(out, "BS.1") {
		t.Errorf("закрытая BS.1 попала в цепочку: %q", out)
	}

	// Цикл RS-CMP-01 <-> WIRING-486 должен посетить каждую карточку один раз.
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "why", "RS-CMP-01"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("цикл: код %d, stderr %q", code, stderr.String())
	}
	out = stdout.String()
	if !strings.Contains(out, "1. WIRING-486 через depends-on") {
		t.Errorf("цикл не начался: %q", out)
	}
	if strings.Contains(out, "2.") || strings.Count(out, "RS-CMP-01") != 1 {
		t.Errorf("цикл не оборван: %q", out)
	}
}
