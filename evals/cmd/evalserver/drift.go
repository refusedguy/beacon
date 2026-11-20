//go:build eval

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Проверка расхождения плана и кода. Harbor Step 3.1 Plan vs Code Drift и
// Step 3.2 Leftover Artifacts. Режим вызывается отдельно от MCP-сервера:
//
//	go run -tags=eval ./cmd/evalserver --drift-check
//
// Сканер читает только файлы репозитория. Сеть и внешние зависимости ему не
// нужны: используется только стандартная библиотека. Каждая находка печатает
// карточку, причину и путь, чтобы устаревший план был виден до PR.

const rootModuleLine = "module github.com/refusedguy/beacon"

// Маркеры незавершённой работы из Harbor Step 3.2. Слова собраны из частей,
// чтобы сканер не нашёл их в собственном исходнике.
var leftoverPattern = regexp.MustCompile(
	`\b(` + "TO" + "DO" + `|` + "FI" + "XME" + `|` + "HA" + "CK" + `|` + "XX" + "X" +
		`|` + "TEM" + "P" + `|` + "PLACE" + "HOLDER" + `)\b` +
		`|console\.log\([^)]*[Dd]ebug|print\([^)]*[Dd]ebug`,
)

var sourceExtensions = map[string]bool{
	".go": true, ".js": true, ".sh": true, ".ts": true, ".py": true,
}

type driftFinding struct {
	kind string // "plan" или "artifact"
	path string
	msg  string
}

type taskCard struct {
	id        string
	path      string
	status    string
	phase     string
	owners    []string
	confirmed bool
}

// runDriftCheck возвращает код выхода режима --drift-check.
func runDriftCheck() int {
	fmt.Println("Harbor Step 3.1/3.2: проверка дрейфа плана и кода")
	fmt.Println()

	fmt.Println("Самопроверка детектора:")
	selfTestOK := true
	for _, line := range selfTest() {
		fmt.Println("  " + line)
		if strings.HasPrefix(line, "FAIL") {
			selfTestOK = false
		}
	}
	fmt.Println()

	root, err := findRepoRootForDrift()
	if err != nil {
		fmt.Printf("Ошибка: %v\n", err)
		return 1
	}
	cards := readCards(filepath.Join(root, "docs", "tasks"))
	active := activePhase(cards)
	fmt.Printf("Корень: %s\n", root)
	fmt.Printf("Карточек: %d, активная фаза плана: %s\n\n", len(cards), active)

	findings := scanRepo(root)
	if len(findings) == 0 {
		fmt.Println("План и код согласованы: расхождений нет.")
		if !selfTestOK {
			fmt.Println("Результат: FAIL. Самопроверка детектора не прошла")
			return 1
		}
		fmt.Println("Результат: PASS")
		return 0
	}
	for _, f := range findings {
		fmt.Printf("  [%s] %s: %s\n", strings.ToUpper(f.kind), f.path, f.msg)
	}
	fmt.Printf("\nРезультат: FAIL. Найдено расхождений: %d\n", len(findings))
	return 1
}

// findRepoRootForDrift поднимается от рабочего каталога к корневому go.mod.
// У evals/go.mod другой путь модуля, поэтому требуется точное совпадение строки.
func findRepoRootForDrift() (string, error) {
	if env := strings.TrimSpace(os.Getenv("BEACON_REPO_ROOT")); env != "" {
		return filepath.Abs(env)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("рабочий каталог: %w", err)
	}
	for {
		data, err := drvReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && hasRootModule(string(data)) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("корень репозитория не найден выше %s", dir)
		}
		dir = parent
	}
}

func hasRootModule(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == rootModuleLine {
			return true
		}
	}
	return false
}

// activePhase возвращает последнюю фазу плана. Карточки прошлых волн остаются
// в docs/tasks как история и по ним дрейф не считается.
func activePhase(cards []taskCard) string {
	best := ""
	for _, c := range cards {
		if c.phase > best {
			best = c.phase
		}
	}
	return best
}

func readCards(dir string) []taskCard {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	cards := make([]taskCard, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := drvReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		text := string(data)
		card := taskCard{
			id:        strings.TrimSuffix(entry.Name(), ".md"),
			path:      "docs/tasks/" + entry.Name(),
			confirmed: hasPRConfirmation(text),
		}
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "Статус:"):
				card.status = strings.TrimSpace(strings.TrimPrefix(line, "Статус:"))
			case strings.HasPrefix(line, "Фаза:"):
				card.phase = strings.TrimSpace(strings.TrimPrefix(line, "Фаза:"))
			case strings.HasPrefix(line, "Владелец файлов:"):
				list := strings.TrimSpace(strings.TrimPrefix(line, "Владелец файлов:"))
				for _, part := range strings.Split(list, ",") {
					p := strings.TrimSpace(part)
					if p != "" {
						card.owners = append(card.owners, p)
					}
				}
			}
		}
		cards = append(cards, card)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].id < cards[j].id })
	return cards
}

// hasPRConfirmation читает только раздел «Подтверждение». Терминальный статус
// карточки без ссылки на PR означает, что план закрыт без доказательства.
func hasPRConfirmation(text string) bool {
	inSection := false
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "## ") {
			if inSection {
				break
			}
			inSection = s == "## Подтверждение"
			continue
		}
		if inSection && strings.Contains(s, "PR ") {
			return true
		}
	}
	return false
}

// scanRepo возвращает все расхождения плана и кода в корне root.
func scanRepo(root string) []driftFinding {
	cards := readCards(filepath.Join(root, "docs", "tasks"))
	phase := activePhase(cards)
	findings := make([]driftFinding, 0)

	for _, card := range cards {
		if card.phase != phase && card.phase != "" {
			continue
		}
		if card.status != "done" && card.status != "dropped" {
			continue
		}
		if !card.confirmed {
			findings = append(findings, driftFinding{
				kind: "plan",
				path: card.path,
				msg: fmt.Sprintf("статус %s в активной фазе %s без подтверждения PR в разделе «Подтверждение»",
					card.status, phase),
			})
		}
		if card.status != "done" {
			continue
		}
		for _, owner := range card.owners {
			if pathExists(root, owner) {
				continue
			}
			findings = append(findings, driftFinding{
				kind: "plan",
				path: card.path,
				msg:  fmt.Sprintf("статус done, но файл владельца отсутствует: %s", owner),
			})
		}
	}

	findings = append(findings, scanArtifacts(root)...)
	findings = append(findings, scanLeftovers(root)...)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].kind != findings[j].kind {
			return findings[i].kind < findings[j].kind
		}
		if findings[i].path != findings[j].path {
			return findings[i].path < findings[j].path
		}
		return findings[i].msg < findings[j].msg
	})
	return findings
}

func pathExists(root, rel string) bool {
	p := filepath.Join(root, filepath.FromSlash(strings.TrimSpace(rel)))
	if _, err := drvStat(p); err == nil {
		return true
	}
	if strings.ContainsAny(rel, "*?[") {
		matches, err := filepath.Glob(p)
		return err == nil && len(matches) > 0
	}
	return false
}

// scanArtifacts ищет песочницы и файлы, которые спайк оставляет после себя.
// Карточки спайков в docs/tasks это план, а не артефакт, поэтому docs не
// сканируется.
func scanArtifacts(root string) []driftFinding {
	out := make([]driftFinding, 0)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			case "tmp":
				if rel == "tmp" {
					out = append(out, driftFinding{
						kind: "artifact",
						path: "tmp/",
						msg:  "осталась песочница спайка tmp/ (gitignored каталог)",
					})
				}
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(rel, "docs/") {
			return nil
		}
		if artifactName(filepath.Base(path)) {
			out = append(out, driftFinding{
				kind: "artifact",
				path: rel,
				msg:  "похоже на оставшийся артефакт спайка",
			})
		}
		return nil
	})
	return out
}

// scanLeftovers ищет в исходниках маркеры незавершённой работы по Harbor
// Step 3.2. Документы не сканируются: там те же слова могут быть частью плана.
func scanLeftovers(root string) []driftFinding {
	out := make([]driftFinding, 0)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		data, readErr := drvReadFile(path)
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !leftoverPattern.MatchString(line) {
				continue
			}
			out = append(out, driftFinding{
				kind: "artifact",
				path: fmt.Sprintf("%s:%d", rel, i+1),
				msg:  "в коде остался маркер незавершённой работы",
			})
		}
		return nil
	})
	return out
}

func artifactName(base string) bool {
	lower := strings.ToLower(base)
	for _, suffix := range []string{".orig", ".rej", ".bak", ".tmp", "~"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	stem := strings.TrimSuffix(lower, filepath.Ext(lower))
	for _, prefix := range []string{"scratch", "spike-", "debug-"} {
		if strings.HasPrefix(stem, prefix) {
			return true
		}
	}
	return strings.HasSuffix(stem, "-spike")
}

// selfTest прогоняет детектор на двух временных фикстурах: с дрейфом и без.
// Так проверка доказывает, что она ловит именно устаревший план и артефакт,
// а не просто всегда возвращает PASS.
func selfTest() []string {
	out := make([]string, 0, 4)
	root, err := os.MkdirTemp("", "beacon-drift-selftest-")
	if err != nil {
		return []string{"FAIL создать временный каталог: " + err.Error()}
	}
	defer func() { _ = drvRemoveAll(root) }()

	staleCard := "# HD-999. Устаревший план\nСтатус: done\nФаза: P8\n" +
		"Владелец файлов: internal/missing/hd999.go\n"
	if err := writeFixture(root, "docs/tasks/HD-999.md", staleCard); err != nil {
		return []string{"FAIL записать фикстуру карточки: " + err.Error()}
	}
	if err := writeFixture(root, "tmp/spike-demo/leftover.go", "package main\n"); err != nil {
		return []string{"FAIL записать фикстуру артефакта: " + err.Error()}
	}
	markerLine := "// " + "TO" + "DO" + ": убрать перед мержем\n"
	if err := writeFixture(root, "internal/marker/leftover.go", markerLine); err != nil {
		return []string{"FAIL записать фикстуру маркера: " + err.Error()}
	}

	hasPlan, hasArtifact, hasMarker := false, false, false
	for _, finding := range scanRepo(root) {
		if finding.kind == "plan" && strings.Contains(finding.path, "HD-999") {
			hasPlan = true
		}
		if finding.kind == "artifact" && strings.Contains(finding.path, "tmp/") {
			hasArtifact = true
		}
		if finding.kind == "artifact" && strings.Contains(finding.path, "leftover.go:1") {
			hasMarker = true
		}
	}
	if hasPlan {
		out = append(out, "PASS устаревший план обнаружен: done без подтверждения и с отсутствующим файлом")
	} else {
		out = append(out, "FAIL устаревший план не обнаружен")
	}
	if hasArtifact {
		out = append(out, "PASS остаток спайка обнаружен: tmp/spike-demo/")
	} else {
		out = append(out, "FAIL остаток спайка не обнаружен")
	}
	if hasMarker {
		out = append(out, "PASS маркер незавершённой работы обнаружен")
	} else {
		out = append(out, "FAIL маркер незавершённой работы не обнаружен")
	}

	cleanRoot, err := os.MkdirTemp("", "beacon-drift-clean-")
	if err != nil {
		return append(out, "FAIL создать чистую фикстуру: "+err.Error())
	}
	defer func() { _ = drvRemoveAll(cleanRoot) }()
	cleanCard := "# HD-998. Актуальный план\nСтатус: done\nФаза: P8\n" +
		"Владелец файлов: internal/clean/hd998.go\n\n" +
		"## Подтверждение\n- PR #1 MERGED: 2026-11-20 (squash).\n"
	if err := writeFixture(cleanRoot, "docs/tasks/HD-998.md", cleanCard); err != nil {
		return append(out, "FAIL записать чистую карточку: "+err.Error())
	}
	if err := writeFixture(cleanRoot, "internal/clean/hd998.go", "package clean\n"); err != nil {
		return append(out, "FAIL записать файл владельца: "+err.Error())
	}
	if len(scanRepo(cleanRoot)) == 0 {
		out = append(out, "PASS чистая фикстура не даёт ложных срабатываний")
	} else {
		out = append(out, "FAIL чистая фикстура дала ложное срабатывание")
	}
	return out
}

func writeFixture(root, rel, content string) error {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := drvMkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return drvWriteFile(p, []byte(content), 0o644)
}
