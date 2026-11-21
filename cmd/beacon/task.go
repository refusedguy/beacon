package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app/taskcli"
	"github.com/refusedguy/beacon/internal/infra/server"
	"github.com/refusedguy/beacon/internal/infra/store"
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
	case "wait":
		return runTaskWait(args[1:], stdout, stderr, cfg)
	case "check-launch":
		return runTaskCheckLaunch(args[1:], stdout, stderr, cfg)
	case "lease":
		return runTaskLease(args[1:], stdout, stderr, cfg)
	case "schedule":
		return runTaskSchedule(args[1:], stdout, stderr, cfg)
	default:
		fmt.Fprintf(stderr, "неизвестная подкоманда task %q\n%s", args[0], taskUsage)
		return taskstore.ExitValidation
	}
}

const taskUsage = "использование: beacon task list|new|take|done|needs-input|why|wait|check-launch|--self-test [flags]\n" +
	"               beacon task lease acquire|release|check [flags]\n" +
	"               beacon task schedule [create|list|delete] [flags]"

// whyEdgeKinds это ребра графа карточек из ADR 0045 п6.
var whyEdgeKinds = []string{
	"depends-on", "conflicts-with", "part-of", "derived-from", "supersedes", "same-as",
}

// whyCardID — грамматика ID, которую понимает ParseCard. Она совпадает с
// taskstore.exactIDRE: буквенный префикс, дефис или точка и номер. Поэтому
// почему-граф видит не только T3.17, но и RS-MCP-02, WIRING-485, BS.1 и S1.001.
const whyCardID = `[A-Za-z][A-Za-z0-9_-]*[.-][0-9]+`

// whyEdgeRE находит ребро и ID карточки в строке, например
// "depends-on T3.17", "conflicts-with:RS-MCP-02" или "same-as: BS.1".
// Вторая группа — любая поддерживаемая форма ID. Окончательно кандидатов
// проверяет whyEdges через taskstore.IsID, поэтому лишние совпадения
// в граф не попадают.
var whyEdgeRE = regexp.MustCompile(`\b(depends-on|conflicts-with|part-of|derived-from|supersedes|same-as)[ \t]*[ :][ \t]*(` + whyCardID + `)\b`)

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
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки"))
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

// ---- ADR 0056 п5 / T3.126: wait --after SEQ ----
//
// Команда wait возобновляет чтение журнала задач строго после позиции SEQ и
// печатает только события с seq больше указанной. SEQ это аргумент CLI и
// позиция в журнале задач Beacon, а не курсор протокола MCP из ADR 0025: там
// курсор непрозрачен и отдаётся подписчику через протокол, здесь число задаёт
// вызывающий и не требует опроса, потому что новая позиция берётся из журнала.
func runTaskWait(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"after": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) > 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	raw := p.value("after")
	if raw == "" {
		return failTask(stderr, taskstore.Validationf("команда wait требует --after SEQ"))
	}
	after, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || after < 0 {
		return failTask(stderr, taskstore.Validationf("неверная позиция --after %q: нужно неотрицательное число", raw))
	}

	db, err := store.Open(filepath.Join(cfg.StateDir, mcpStateDBName))
	if err != nil {
		return failTask(stderr, err)
	}
	defer db.Close()

	events, _, err := db.LoadEvents()
	if err != nil {
		return failTask(stderr, err)
	}
	for _, e := range events {
		if e.Seq <= after {
			continue
		}
		fmt.Fprintf(stdout, "%d %s %s\n", e.Seq, e.WatchID, string(e.Payload))
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

// whyDependents возвращает карточки, которые прямо зависят от id. Это обратные
// рёбра графа X -> id, прочитанного whyEdges. Порядок детерминирован по ID,
// каждая карточка попадает в результат один раз. Обхода нет, поэтому циклы
// здесь невозможны; обрыв циклов при прямом ходе остаётся в whyChain.
func whyDependents(id string, byID map[string]taskstore.Card) []whyStep {
	ids := make([]string, 0, len(byID))
	for key := range byID {
		ids = append(ids, key)
	}
	sort.Strings(ids)
	var out []whyStep
	for _, key := range ids {
		c := byID[key]
		for _, e := range whyEdges(c) {
			if e.to != id {
				continue
			}
			out = append(out, whyStep{
				id:     c.ID,
				edge:   e.kind,
				status: c.Status,
				agent:  c.Agent,
			})
			break
		}
	}
	return out
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
// разные флаги и разные поля карточки. Попытка закрыться без доказательства
// не закрывает карточку, а переводит её в needs_input.

// completionFields это два входа команды завершения.
type completionFields struct {
	summary  string
	evidence string
}

// runTaskDone завершает карточку только при наличии отдельного поля
// --evidence. Без доказательства карточка не закрывается: она уходит в
// needs_input с вопросом, который нужно закрыть перед повторной попыткой.
func runTaskDone(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"summary": true, "evidence": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 || !taskstore.IsID(p.pos[0]) {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки вида T<g>.<n>"))
	}
	report := completionFields{summary: p.value("summary"), evidence: p.value("evidence")}
	hasEvidence := strings.TrimSpace(report.evidence) != ""
	if hasEvidence && strings.TrimSpace(report.summary) == "" {
		return failTask(stderr, taskstore.Validationf("завершение без итога отклонено: задайте --summary"))
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

	if !hasEvidence {
		reason := "завершение без доказательства: задайте --evidence"
		newText := needsInputText(target.Raw, reason)
		if err := taskstore.AtomicWrite(target.Path, []byte(newText), 0644); err != nil {
			return failTask(stderr, err)
		}
		return failTask(stderr, taskstore.Validationf("завершение без доказательства отклонено: карточка %s переведена в needs_input, задайте --evidence", id))
	}

	newText := setCardField(target.Raw, "Статус", "done")
	newText = setCardField(newText, "Итог", oneLine(report.summary))
	newText = setCardField(newText, "Доказательство", oneLine(report.evidence))
	newText = setCardField(newText, "Состояние", "")
	newText = setCardField(newText, "Вопрос", "")
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

	newText := needsInputText(target.Raw, question)
	if err := taskstore.AtomicWrite(target.Path, []byte(newText), 0644); err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "%s needs_input\n", id)
	return taskstore.ExitOK
}

func taskActive(status string) bool {
	return status == "todo" || status == "in_progress"
}

// needsInputText переводит карточку в состояние ожидания ввода. Статус
// остаётся в допустимом множестве ParseCard, поэтому карточка не ломает list
// и take. Причина пишется отдельным полем Вопрос.
func needsInputText(raw, reason string) string {
	newText := setCardField(raw, "Статус", "in_progress")
	newText = setCardField(newText, "Состояние", "needs_input")
	newText = setCardField(newText, "Вопрос", reason)
	return newText
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

// runTaskCheckLaunch — быстрая проверка до запуска волны сабентов. Команда не
// трогает карточки и не запускает агентов: она только читает OwnerFiles и
// отклоняет план, если границы двух задач пересекаются. Глобы в OwnerFiles
// сравниваются тем же кодом, что и аренды, через doublestar в слое app.
func runTaskCheckLaunch(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, nil, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) == 0 {
		return failTask(stderr, taskstore.Validationf("нужны ID карточек: beacon task check-launch T1 T2 T3"))
	}
	svc := taskcli.NewService(cfg)
	if err := svc.PlanLaunch(p.pos...); err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "запуск разрешён: %d карточек без пересечений по файлам\n", len(p.pos))
	return taskstore.ExitOK
}

// leaseExitBlocked отделяет отказ guard от ошибок стора. Хук пропускает любой
// код, кроме этого: отсутствующий стор или сбой бинарника не должны мешать
// коммиту.
const leaseExitBlocked = 3

const leaseUsage = "использование: beacon task lease acquire --agent A (--glob G | --worktree W)...\n" +
	"               beacon task lease release --agent A\n" +
	"               beacon task lease check --agent A (--file F | --worktree W)... [--bypass]"

// runTaskLease — advisory-аренды файлов и ворктри. Команда живёт внутри task,
// потому что каталог и dispatch верхнего уровня не входят в OwnerFiles волны.
func runTaskLease(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return taskstore.ExitValidation
	}
	switch argv[0] {
	case "acquire":
		return runTaskLeaseAcquire(argv[1:], stdout, stderr, cfg)
	case "release":
		return runTaskLeaseRelease(argv[1:], stdout, stderr, cfg)
	case "check":
		return runTaskLeaseCheck(argv[1:], stdout, stderr, cfg)
	default:
		fmt.Fprintf(stderr, "неизвестная подкоманда lease %q\n%s\n", argv[0], leaseUsage)
		return taskstore.ExitValidation
	}
}

func leaseAgent(stderr io.Writer, p *parsedArgs) (string, bool) {
	agent := strings.TrimSpace(p.value("agent"))
	if agent == "" {
		fmt.Fprintln(stderr, "нужен --agent")
		return "", false
	}
	return agent, true
}

func runTaskLeaseAcquire(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{
		"agent": true, "glob": true, "worktree": true,
	}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	agent, ok := leaseAgent(stderr, p)
	if !ok {
		return taskstore.ExitValidation
	}
	globs, worktrees := p.vals["glob"], p.vals["worktree"]
	if len(globs) == 0 && len(worktrees) == 0 {
		fmt.Fprintln(stderr, "нужен хотя бы один --glob или --worktree")
		return taskstore.ExitValidation
	}
	svc := taskcli.NewService(cfg)
	if err := svc.ReserveLeases(agent, globs, worktrees, time.Time{}); err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "аренда установлена: агент=%s glob-ов=%d ворктри=%d\n",
		agent, len(globs), len(worktrees))
	return taskstore.ExitOK
}

func runTaskLeaseRelease(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"agent": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	agent, ok := leaseAgent(stderr, p)
	if !ok {
		return taskstore.ExitValidation
	}
	svc := taskcli.NewService(cfg)
	if err := svc.ReleaseFiles(agent, time.Time{}); err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "аренда снята: агент=%s\n", agent)
	return taskstore.ExitOK
}

func runTaskLeaseCheck(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{
		"agent": true, "file": true, "worktree": true,
	}, map[string]bool{"bypass": true})
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	agent, ok := leaseAgent(stderr, p)
	if !ok {
		return taskstore.ExitValidation
	}
	files := append([]string(nil), p.vals["file"]...)
	files = append(files, p.vals["worktree"]...)
	if len(files) == 0 {
		fmt.Fprintln(stderr, "нужен хотя бы один --file или --worktree")
		return taskstore.ExitValidation
	}
	svc := taskcli.NewService(cfg)
	decision, err := svc.CheckCommit(agent, files, p.bools["bypass"], time.Time{})
	if err != nil {
		return failTask(stderr, err)
	}
	if decision.Blocked {
		fmt.Fprintln(stdout, decision.Reason)
		return leaseExitBlocked
	}
	fmt.Fprintf(stdout, "аренда проверена: агент=%s файлов=%d, блокировок нет\n", agent, len(files))
	return taskstore.ExitOK
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

// ---- ADR 0189 / ADR 0192 / BS.32: управление расписаниями ----
//
// CLI не открывает SQLite. Он говорит с внутренним API через порт
// ScheduleStore; пока BS.33 не влит, порт реализован JSON-файлом в state dir.
// Тесты подменяют эту точку входа моковым стором.

var scheduleStoreFor = func(cfg taskcli.Config) (server.ScheduleStore, error) {
	return server.NewFileScheduleStore(filepath.Join(cfg.StateDir, "schedules.json"))
}

func scheduleService(cfg taskcli.Config) (*server.ScheduleService, error) {
	store, err := scheduleStoreFor(cfg)
	if err != nil {
		return nil, err
	}
	return server.NewScheduleService(store)
}

// failSchedule maps a validation error from the internal API to exit code 1.
// Store and configuration failures keep the generic startup code.
func failSchedule(stderr io.Writer, err error) int {
	if server.IsValidation(err) {
		return failTask(stderr, taskstore.Validationf("%s", err.Error()))
	}
	return failTask(stderr, err)
}

const scheduleUsage = "использование: beacon task schedule [--id S] [--task-id T --kind interval|cron|mark --expr E] [--timezone Z]\n" +
	"               beacon task schedule list [--format json]\n" +
	"               beacon task schedule delete (--id S | S)\n" +
	"               пауза: beacon task schedule --id S --enabled=false; возобновление: --enabled=true"

func runTaskSchedule(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, scheduleUsage)
		return taskstore.ExitValidation
	}
	switch argv[0] {
	case "list":
		return runScheduleList(argv[1:], stdout, stderr, cfg)
	case "delete":
		return runScheduleDelete(argv[1:], stdout, stderr, cfg)
	default:
		return runSchedulePut(argv, stdout, stderr, cfg)
	}
}

// runSchedulePut создаёт расписание или меняет его состояние. Наличие --id
// означает обновление: --enabled=false ставит задачу на паузу, не удаляя её.
func runSchedulePut(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv,
		map[string]bool{"id": true, "task-id": true, "kind": true, "expr": true, "timezone": true},
		map[string]bool{"enabled": true})
	if err != nil {
		return failTask(stderr, err)
	}
	forceCreate := false
	if len(p.pos) > 0 {
		switch p.pos[0] {
		case "create", "add":
			forceCreate = true
		case "pause", "resume":
			// Ключевое слово-псевдоним обновления, дальше всё задано флагами.
		default:
			return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
		}
	}
	svc, err := scheduleService(cfg)
	if err != nil {
		return failTask(stderr, err)
	}
	ctx := context.Background()

	if id := strings.TrimSpace(p.value("id")); id != "" && !forceCreate {
		enabled := false
		if v, set := p.bools["enabled"]; set {
			enabled = v
		}
		if enabled {
			got, err := svc.Resume(ctx, id)
			if err != nil {
				return failSchedule(stderr, err)
			}
			fmt.Fprintf(stdout, "%s resumed\n", got.ID)
			return taskstore.ExitOK
		}
		got, err := svc.Pause(ctx, id)
		if err != nil {
			return failSchedule(stderr, err)
		}
		fmt.Fprintf(stdout, "%s paused\n", got.ID)
		return taskstore.ExitOK
	}

	taskID := strings.TrimSpace(p.value("task-id"))
	if taskID == "" {
		return failTask(stderr, taskstore.Validationf("нужен --task-id"))
	}
	kind := server.Kind(strings.TrimSpace(p.value("kind")))
	if kind == "" {
		return failTask(stderr, taskstore.Validationf("нужен --kind: interval, cron или mark"))
	}
	expr := strings.TrimSpace(p.value("expr"))
	if expr == "" {
		return failTask(stderr, taskstore.Validationf("нужен --expr"))
	}
	got, err := svc.Schedule(ctx, server.ScheduleCreate{
		ID: p.value("id"), TaskID: taskID, Kind: kind, Expr: expr, Timezone: p.value("timezone"),
	})
	if err != nil {
		return failSchedule(stderr, err)
	}
	fmt.Fprintf(stdout, "%s scheduled\n", got.ID)
	return taskstore.ExitOK
}

func runScheduleList(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"format": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) > 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	format := p.value("format")
	if format != "" && format != "table" && format != "json" {
		return failTask(stderr, taskstore.Validationf("неизвестный формат %q", format))
	}
	svc, err := scheduleService(cfg)
	if err != nil {
		return failTask(stderr, err)
	}
	items, err := svc.List(context.Background())
	if err != nil {
		return failSchedule(stderr, err)
	}
	if format == "json" {
		b, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return failTask(stderr, taskstore.Startupf("не собран JSON расписаний: %v", err))
		}
		fmt.Fprintln(stdout, string(b))
		return taskstore.ExitOK
	}
	fmt.Fprintln(stdout, "ID\tЗАДАЧА\tВИД\tВЫРАЖЕНИЕ\tВКЛЮЧЕНО\tПОЯС")
	for _, s := range items {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%t\t%s\n",
			s.ID, s.TaskID, s.Kind, s.Expr, s.Enabled, s.Timezone)
	}
	return taskstore.ExitOK
}

func runScheduleDelete(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"id": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	id := strings.TrimSpace(p.value("id"))
	if id == "" && len(p.pos) == 1 {
		id = strings.TrimSpace(p.pos[0])
	}
	if id == "" {
		return failTask(stderr, taskstore.Validationf("нужен --id или позиционный ID расписания"))
	}
	if len(p.pos) > 0 && strings.TrimSpace(p.pos[0]) != id {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	svc, err := scheduleService(cfg)
	if err != nil {
		return failTask(stderr, err)
	}
	if err := svc.Delete(context.Background(), id); err != nil {
		return failSchedule(stderr, err)
	}
	fmt.Fprintf(stdout, "%s deleted\n", id)
	return taskstore.ExitOK
}
