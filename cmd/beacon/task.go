package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/app/taskcli"
	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// TaskCommand точка входа подкоманды task. Первый аргумент может быть «task»
// или сразу имя команды, чтобы dispetch из main.go не требовал особого случая.
func TaskCommand(args []string, stdout, stderr io.Writer) int {
	cfg, err := defaultTaskConfig()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return taskstore.Code(err)
	}
	return TaskCommandWithConfig(args, stdout, stderr, cfg)
}

// TaskCommandWithConfig выполняет подкоманду task с заданными каталогами.
func TaskCommandWithConfig(args []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	if len(args) > 0 && args[0] == "task" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, taskUsage)
		return taskstore.ExitValidation
	}
	switch args[0] {
	case "--self-test", "self-test":
		if err := taskcli.SelfTest(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return taskstore.ExitValidation
		}
		return taskstore.ExitOK
	case "list":
		return runTaskList(args[1:], stdout, stderr, cfg)
	case "new":
		return runTaskNew(args[1:], stdout, stderr, cfg)
	case "take":
		return runTaskTake(args[1:], stdout, stderr, cfg)
	case "done":
		return runTaskDone(args[1:], stdout, stderr, cfg)
	case "needs-input":
		return runTaskNeedsInput(args[1:], stdout, stderr, cfg)
	case "why":
		return runTaskWhy(args[1:], stdout, stderr, cfg)
	default:
		fmt.Fprintf(stderr, "неизвестная подкоманда task %q\n%s", args[0], taskUsage)
		return taskstore.ExitValidation
	}
}

const taskUsage = "использование: beacon task list|new|take|done|needs-input|why|--self-test [flags]"

// whyEdgeKinds это ребра графа карточек из ADR 0045 п6.
var whyEdgeKinds = []string{
	"depends-on", "conflicts-with", "part-of", "derived-from", "supersedes", "same-as",
}

// whyEdgeRE находит ребро и ID карточки в строке, например
// "depends-on T3.17", "conflicts-with:T3.18" или "same-as: T3.19".
var whyEdgeRE = regexp.MustCompile(`\b(depends-on|conflicts-with|part-of|derived-from|supersedes|same-as)[ \t]*[ :][ \t]*(T[0-9]+\.[0-9]+)\b`)

type whyEdge struct {
	kind string
	to   string
}

type whyStep struct {
	id     string
	edge   string
	status string
	agent  string
}

type parsedArgs struct {
	vals  map[string][]string
	bools map[string]bool
	pos   []string
}

func (p *parsedArgs) value(name string) string {
	v := p.vals[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func parseTaskArgs(argv []string, values, bools map[string]bool) (*parsedArgs, error) {
	p := &parsedArgs{vals: map[string][]string{}, bools: map[string]bool{}}
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "--") {
			p.pos = append(p.pos, tok)
			continue
		}
		name, val, hasEq := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
		if bools[name] {
			if hasEq {
				b, err := strconv.ParseBool(val)
				if err != nil {
					return nil, taskstore.Validationf("неверное значение флага --%s", name)
				}
				p.bools[name] = b
			} else {
				p.bools[name] = true
			}
			continue
		}
		if !values[name] {
			return nil, taskstore.Validationf("неизвестный флаг --%s", name)
		}
		if !hasEq {
			if i+1 >= len(argv) {
				return nil, taskstore.Validationf("флаг --%s требует значение", name)
			}
			i++
			val = argv[i]
		}
		p.vals[name] = append(p.vals[name], val)
	}
	return p, nil
}

func failTask(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, err.Error())
	return taskstore.Code(err)
}

func runTaskList(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{
		"status": true, "phase": true, "agent": true, "file": true, "format": true,
	}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) > 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	out, err := taskcli.NewService(cfg).List(taskcli.ListOptions{
		Statuses: p.vals["status"], Phases: p.vals["phase"],
		Agents: p.vals["agent"], Files: p.vals["file"],
		Format: p.value("format"),
	})
	if err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprint(stdout, out)
	return taskstore.ExitOK
}

func runTaskNew(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{
		"type": true, "phase": true, "title": true, "reference": true,
		"owner-files": true, "dependencies": true, "goal": true, "context": true,
		"steps": true, "check": true, "acceptance": true,
	}, map[string]bool{"dry-run": true})
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) > 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	id, filePath, err := taskcli.NewService(cfg).New(taskcli.NewOptions{
		Type: p.value("type"), Phase: p.value("phase"), Title: p.value("title"),
		Reference: p.value("reference"), OwnerFiles: p.value("owner-files"),
		Dependencies: p.value("dependencies"), Goal: p.value("goal"),
		Context: p.value("context"), Steps: p.value("steps"), Check: p.value("check"),
		Acceptance: p.value("acceptance"), DryRun: p.bools["dry-run"],
	})
	if err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "%s %s\n", id, relativePath(cfg.Root, filePath))
	return taskstore.ExitOK
}

func runTaskTake(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"agent": true, "time": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки"))
	}
	agent := p.value("agent")
	if agent == "" {
		agent = strings.TrimSpace(os.Getenv("BEACON_AGENT"))
	}
	out, err := taskcli.NewService(cfg).Take(taskcli.TakeOptions{
		ID: p.pos[0], Agent: agent, Time: p.value("time"),
	})
	if err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintln(stdout, out)
	return taskstore.ExitOK
}

// runTaskWhy печатает цепочку блокировок карточки. Цепочка строится по графу
// рёбер между карточками, а не по тексту статуса.
func runTaskWhy(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, nil, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 || !taskstore.IsID(p.pos[0]) {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки вида T<g>.<n>"))
	}
	cards, err := taskstore.LoadCards(cfg.TasksDir)
	if err != nil {
		return failTask(stderr, err)
	}
	byID := make(map[string]taskstore.Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	id := p.pos[0]
	if _, ok := byID[id]; !ok {
		return failTask(stderr, taskstore.Validationf("карточка %s не найдена", id))
	}

	fmt.Fprintln(stdout, id)
	chain := whyChain(id, byID)
	if len(chain) == 0 {
		fmt.Fprintln(stdout, "  цепочка блокировок не найдена")
		return taskstore.ExitOK
	}
	for i, step := range chain {
		holder := step.agent
		if holder == "" {
			holder = "-"
		}
		fmt.Fprintf(stdout, "  %d. %s через %s: держит %s (%s)\n",
			i+1, step.id, step.edge, holder, step.status)
	}
	return taskstore.ExitOK
}

// whyChain возвращает путь от карточки к держателям её шагов. Шагом считается
// ребро к незавершённой карточке. Посещённые ID обрывают циклы.
func whyChain(start string, byID map[string]taskstore.Card) []whyStep {
	seen := map[string]bool{start: true}
	cur := start
	var chain []whyStep
	for len(chain) <= len(byID) {
		var next *whyStep
		for _, e := range whyEdges(byID[cur]) {
			other, ok := byID[e.to]
			if !ok || seen[e.to] || whyClosed(other) {
				continue
			}
			next = &whyStep{id: e.to, edge: e.kind, status: other.Status, agent: other.Agent}
			break
		}
		if next == nil {
			break
		}
		chain = append(chain, *next)
		seen[next.id] = true
		cur = next.id
	}
	return chain
}

func whyClosed(c taskstore.Card) bool {
	return c.Status == "done" || c.Status == "dropped"
}

// whyEdges читает ребра из поля Зависимости и из текста карточки. Голый ID в
// Зависимостях означает depends-on. Порядок рёбер детерминирован.
func whyEdges(c taskstore.Card) []whyEdge {
	seen := map[whyEdge]bool{}
	var edges []whyEdge
	add := func(kind, to string) {
		if !taskstore.IsID(to) || !whyKnownEdge(kind) {
			return
		}
		e := whyEdge{kind: kind, to: to}
		if !seen[e] {
			seen[e] = true
			edges = append(edges, e)
		}
	}
	for _, dep := range c.Dependencies {
		if m := whyEdgeRE.FindStringSubmatch(dep); m != nil {
			add(m[1], m[2])
			continue
		}
		add("depends-on", dep)
	}
	for _, m := range whyEdgeRE.FindAllStringSubmatch(c.Raw, -1) {
		add(m[1], m[2])
	}
	return edges
}

func whyKnownEdge(kind string) bool {
	for _, k := range whyEdgeKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// ---- ADR 0051 п4 / T3.120: отказ завершить без evidence, состояние needs_input ----
//
// Команда завершения принимает два отдельных поля: краткий итог и то, что
// проверяли. Это не completion gate с proof из T3.88: здесь нет плана фаз и
// одобренных proof-объектов, а есть обязательный текстовый ответ воркера на
// вопрос «что проверяли». Итог не подменяет доказательство, потому что это
// разные флаги и разные поля карточки.

// completionFields это два входа команды завершения.
type completionFields struct {
	summary  string
	evidence string
}

// validateCompletion отклоняет отчёт без итога или без доказательства. Проверка
// идёт до захвата замка и до чтения карточки, поэтому отказ не меняет файлы.
func validateCompletion(f completionFields) error {
	if strings.TrimSpace(f.evidence) == "" {
		return taskstore.Validationf("завершение без доказательства отклонено: задайте --evidence")
	}
	if strings.TrimSpace(f.summary) == "" {
		return taskstore.Validationf("завершение без итога отклонено: задайте --summary")
	}
	return nil
}

// runTaskDone завершает карточку только при наличии отдельного поля
// --evidence. Итог и доказательство пишутся в карточку, маркер needs_input
// снимается.
func runTaskDone(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"summary": true, "evidence": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 || !taskstore.IsID(p.pos[0]) {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки вида T<g>.<n>"))
	}
	report := completionFields{summary: p.value("summary"), evidence: p.value("evidence")}
	if err := validateCompletion(report); err != nil {
		return failTask(stderr, err)
	}
	id := p.pos[0]

	lock, err := taskstore.AcquireLock(taskstore.LockPath(cfg.StateDir, cfg.Root), cfg.Root)
	if err != nil {
		return failTask(stderr, err)
	}
	defer lock.Release()

	cards, err := taskstore.LoadCards(cfg.TasksDir)
	if err != nil {
		return failTask(stderr, err)
	}
	var target *taskstore.Card
	for i := range cards {
		if cards[i].ID == id {
			target = &cards[i]
			break
		}
	}
	if target == nil {
		return failTask(stderr, taskstore.Validationf("карточка %s не найдена", id))
	}
	if !taskActive(target.Status) {
		return failTask(stderr, taskstore.Validationf("карточка %s в статусе %s, завершение неприменимо", id, target.Status))
	}

	newText := setCardField(target.Raw, "Статус", "done")
	newText = setCardField(newText, "Итог", oneLine(report.summary))
	newText = setCardField(newText, "Доказательство", oneLine(report.evidence))
	newText = setCardField(newText, "Состояние", "")
	if err := taskstore.AtomicWrite(target.Path, []byte(newText), 0644); err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "%s done\n", id)
	return taskstore.ExitOK
}

// runTaskNeedsInput сохраняет состояние воркера, который задал вопрос и пошёл
// дальше. Статус карточки остаётся в допустимом множестве ParseCard, а
// состояние пишется отдельным полем, поэтому карточка не ломает list и take.
func runTaskNeedsInput(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"question": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 || !taskstore.IsID(p.pos[0]) {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки вида T<g>.<n>"))
	}
	question := oneLine(p.value("question"))
	if question == "" {
		return failTask(stderr, taskstore.Validationf("состояние needs_input без вопроса отклонено: задайте --question"))
	}
	id := p.pos[0]

	lock, err := taskstore.AcquireLock(taskstore.LockPath(cfg.StateDir, cfg.Root), cfg.Root)
	if err != nil {
		return failTask(stderr, err)
	}
	defer lock.Release()

	cards, err := taskstore.LoadCards(cfg.TasksDir)
	if err != nil {
		return failTask(stderr, err)
	}
	var target *taskstore.Card
	for i := range cards {
		if cards[i].ID == id {
			target = &cards[i]
			break
		}
	}
	if target == nil {
		return failTask(stderr, taskstore.Validationf("карточка %s не найдена", id))
	}
	if !taskActive(target.Status) {
		return failTask(stderr, taskstore.Validationf("карточка %s в статусе %s, needs_input неприменимо", id, target.Status))
	}

	newText := setCardField(target.Raw, "Статус", "in_progress")
	newText = setCardField(newText, "Состояние", "needs_input")
	newText = setCardField(newText, "Вопрос", question)
	if err := taskstore.AtomicWrite(target.Path, []byte(newText), 0644); err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "%s needs_input\n", id)
	return taskstore.ExitOK
}

func taskActive(status string) bool {
	return status == "todo" || status == "in_progress"
}

// setCardField меняет значение одного поля карточки. Пустое значение удаляет
// строку. Отсутствующее поле добавляется в конец, поэтому повторный вызов не
// создаёт дублей, а ParseCard не видит неизвестные поля как ошибку.
func setCardField(raw, name, value string) string {
	cr := ""
	if strings.Contains(raw, "\r\n") {
		cr = "\r"
	}
	prefix := name + ":"
	found := false
	out := make([]string, 0, strings.Count(raw, "\n")+2)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, prefix) {
			found = true
			if value != "" {
				out = append(out, prefix+" "+value+cr)
			}
			continue
		}
		out = append(out, line+cr)
	}
	if !found && value != "" {
		out = append(out, prefix+" "+value+cr)
	}
	return strings.Join(out, "\n")
}

// oneLine сводит значение флага к одной строке, чтобы переносы в --evidence не
// разрывали поля карточки.
func oneLine(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

func relativePath(root, filePath string) string {
	if root == "" {
		return filePath
	}
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return filePath
	}
	return filepath.ToSlash(rel)
}

func defaultTaskConfig() (taskcli.Config, error) {
	root := strings.TrimSpace(os.Getenv("BEACON_ROOT"))
	if root == "" {
		root = findProjectRoot()
	}
	stateDir, err := taskstore.StateDir()
	if err != nil {
		return taskcli.Config{}, err
	}
	return taskcli.Config{
		TasksDir: filepath.Join(root, "docs", "tasks"),
		ADRDir:   filepath.Join(root, "docs", "adrs"),
		StateDir: stateDir,
		Root:     root,
	}, nil
}

// findProjectRoot поднимается от рабочего каталога к каталогу с go.mod и docs/tasks.
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		_, modErr := os.Stat(filepath.Join(dir, "go.mod"))
		_, tasksErr := os.Stat(filepath.Join(dir, "docs", "tasks"))
		if modErr == nil && tasksErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	cwd, _ := os.Getwd()
	return cwd
}
