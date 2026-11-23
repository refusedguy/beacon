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
	if hasHelpFlag(args) {
		writeCommandHelp(stdout, "task")
		return taskstore.ExitOK
	}
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
	case "defer":
		return runTaskDefer(args[1:], stdout, stderr, cfg)
	case "done":
		return runTaskDone(args[1:], stdout, stderr, cfg)
	case "needs-input":
		return runTaskNeedsInput(args[1:], stdout, stderr, cfg)
	case "why":
		return runTaskWhy(args[1:], stdout, stderr, cfg)
	case "ready":
		return runTaskReady(args[1:], stdout, stderr, cfg)
	case "wait":
		return runTaskWait(args[1:], stdout, stderr, cfg)
	case "check-launch":
		return runTaskCheckLaunch(args[1:], stdout, stderr, cfg)
	case "lease":
		return runTaskLease(args[1:], stdout, stderr, cfg)
	case "schedule":
		return runTaskSchedule(args[1:], stdout, stderr, cfg)
	case "compact":
		return runTaskCompact(args[1:], stdout, stderr, cfg)
	case "restore":
		return runTaskRestore(args[1:], stdout, stderr, cfg)
	default:
		fmt.Fprintf(stderr, "неизвестная подкоманда task %q\n%s", args[0], taskUsage)
		return taskstore.ExitValidation
	}
}

const taskUsage = "использование: beacon task list|new|take|defer|done|needs-input|why|ready|wait|check-launch|--self-test [flags]\n" +
	"               beacon task defer <ID> --until RFC3339|YYYY-MM-DD [--forever]\n" +
	"               beacon task lease acquire|release|check [flags]\n" +
	"               beacon task schedule [create|list|delete] [flags]\n" +
	"               beacon task compact (--analyze|--apply|--auto) [--format json] [--id ID] [--summary FILE] [--dry-run]\n" +
	"               beacon task restore <ID> [--level 1|2]"

// whyEdgeKinds это ребра графа карточек из ADR 0045 п6.
var whyEdgeKinds = []string{
	"depends-on", "conflicts-with", "part-of", "derived-from", "supersedes", "same-as",
}

// whyExternalKind это пятый тип ребра из ADR 0314. Он живёт отдельно от
// whyEdgeKinds: его цель не ID карточки, а внешний гейт, и он печатается
// отдельным шагом с состоянием из Watch.
const whyExternalKind = "waits-for-external"

// whyExternalRE находит внешний гейт в тексте карточки. Формат цели совпадает
// с taskstore.IsExternalGateTarget, поэтому лишние совпадения отбрасываются
// при добавлении ребра.
var whyExternalRE = regexp.MustCompile(`\bwaits-for-external[ \t]*[ :][ \t]*(gh:pr:[0-9]+|gh:run:[A-Za-z0-9_-]+|timer:[A-Za-z0-9_.-]+|human:[A-Za-z0-9_.-]+|bead:[A-Za-z0-9_-]+)`)

// whyCardID повторяет грамматику taskstore.exactIDRE без якорей: буквенный
// префикс, затем либо -<буквы><цифры> (ATX-R01, RS-CMP-06), либо точка или
// дефис и номер (T3.17, WIRING-485, BS.1, S1.001). Старый шаблон принимал
// только цифры после разделителя и не видел рёбра к ATX-R01 и RS-CMP-06.
// Кандидатов всё равно проверяет taskstore.IsID в whyEdges, поэтому здесь
// важна полнота, а не строгость.
const whyCardID = `[A-Za-z][A-Za-z0-9_-]*(?:-[A-Za-z]+[0-9]+|[.-][0-9]+)`

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

// runTaskRestore возвращает карточку исходный текст из снапшота CB-05.
// Пустой --level берёт уровень сжатия из самой карточки.
func runTaskRestore(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"level": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 || !taskstore.IsID(p.pos[0]) {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки"))
	}
	id := p.pos[0]
	cardPath := taskstore.CardPath(cfg.TasksDir, id)
	level, err := taskstore.RestoreCard(taskstore.TasksLockPath(cfg.TasksDir), cfg.Root, cardPath, p.value("level"))
	if err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "%s restored from level %s snapshot\n", id, level)
	return taskstore.ExitOK
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
	// CBX-02: глиф в списке и поле gated_by читают один и тот же предикат.
	// JSON-строка сервиса дополняется полем, которое считает gatedByFor,
	// поэтому список и why не могут разойтись на одном ребре.
	if p.value("format") == "json" {
		out = addGatedByToListJSON(out, cfg)
	}
	fmt.Fprint(stdout, out)
	return taskstore.ExitOK
}

// addGatedByToListJSON добавляет поле gated_by в каждую строку JSON-списка.
// Исходный вывод сервиса не меняется: при ошибке разбора он возвращается как есть.
func addGatedByToListJSON(out string, cfg taskcli.Config) string {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return out
	}
	cards, err := taskstore.LoadCards(cfg.TasksDir)
	if err != nil {
		return out
	}
	byID := make(map[string]taskstore.Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	for i := range rows {
		var id string
		if err := json.Unmarshal(rows[i]["id"], &id); err != nil || id == "" {
			continue
		}
		gates := gatedByFor(id, byID)
		if gates == nil {
			gates = []taskstore.GateRef{}
		}
		encoded, err := json.Marshal(gates)
		if err != nil {
			continue
		}
		rows[i]["gated_by"] = encoded
	}
	encoded, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return out
	}
	return string(encoded) + "\n"
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

// runTaskDefer откладывает карточку до даты или на неопределённый срок.
func runTaskDefer(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"until": true}, map[string]bool{"forever": true})
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки"))
	}
	out, err := taskcli.NewService(cfg).Defer(taskcli.DeferOptions{
		ID: p.pos[0], Until: p.value("until"), Forever: p.bools["forever"],
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
	p, err := parseTaskArgs(argv, map[string]bool{"format": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 || !taskstore.IsID(p.pos[0]) {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки"))
	}
	format := p.value("format")
	if format != "" && format != "json" {
		return failTask(stderr, taskstore.Validationf("beacon task why: неверное значение --format %q, ожидается json", format))
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

	chain := whyChain(id, byID)
	gated := gatedByFor(id, byID)
	if format == "json" {
		return writeWhyJSON(stdout, id, chain, gated)
	}

	fmt.Fprintln(stdout, id)
	fmt.Fprintf(stdout, "  gated_by: %s\n", formatGateRefs(gated))
	gates := whyExternalGates(byID[id])
	if len(chain) == 0 && len(gates) == 0 {
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
	for i, target := range gates {
		fmt.Fprintf(stdout, "  гейт %d. %s через %s: внешнее состояние %s (Watch)\n",
			i+1, target, whyExternalKind, whyExternalState(target, cfg))
	}
	return taskstore.ExitOK
}

// gatedByFor строит набор гейтов карточки через единственный предикат
// taskstore.GatesHolding. Рёбра берутся из тех же полей, что и в whyChain,
// поэтому глиф списка, заголовок, why и gated_by видят один граф.
func gatedByFor(id string, byID map[string]taskstore.Card) []taskstore.GateRef {
	subject, ok := byID[id]
	if !ok {
		return nil
	}
	deps := make([]taskstore.GateDep, 0)
	for _, e := range whyEdges(subject) {
		target, ok := byID[e.to]
		if !ok {
			continue
		}
		deps = append(deps, taskstore.GateDep{Kind: e.kind, Target: target})
	}
	return taskstore.GatesHolding(subject, deps)
}

// formatGateRefs печатает набор гейтов в текстовом заголовке.
func formatGateRefs(refs []taskstore.GateRef) string {
	if len(refs) == 0 {
		return "пусто"
	}
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		part := ref.ID + " (" + ref.Type + ")"
		if ref.Reason != "" {
			part += ": " + ref.Reason
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

type whyJSONStep struct {
	ID     string `json:"id"`
	Edge   string `json:"edge"`
	Status string `json:"status"`
	Agent  string `json:"agent"`
}

type whyJSON struct {
	ID      string              `json:"id"`
	GatedBy []taskstore.GateRef `json:"gated_by"`
	Chain   []whyJSONStep       `json:"chain"`
}

func writeWhyJSON(stdout io.Writer, id string, chain []whyStep, gated []taskstore.GateRef) int {
	if gated == nil {
		gated = []taskstore.GateRef{}
	}
	steps := make([]whyJSONStep, 0, len(chain))
	for _, s := range chain {
		steps = append(steps, whyJSONStep{ID: s.id, Edge: s.edge, Status: s.status, Agent: s.agent})
	}
	b, err := json.MarshalIndent(whyJSON{ID: id, GatedBy: gated, Chain: steps}, "", "  ")
	if err != nil {
		return taskstore.ExitStartup
	}
	fmt.Fprintln(stdout, string(b))
	return taskstore.ExitOK
}

// ---- CBX-16: объяснение ready-очереди ----
//
// readyExplainView отдаёт оператору причины готовности вместо одного булева
// признака. ready содержит карточки без открытых блокировщиков. blocked
// содержит карточки с хотя бы одним открытым блокировщиком. Три списка
// зависимостей разделяют закрытые, закреплённые и открытые цели блокирующих
// рёбер.

type readyDep struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

type readyExplainEntry struct {
	ID                 string     `json:"id"`
	ResolvedBlockers   []readyDep `json:"resolvedBlockers"`
	PinnedDependencies []readyDep `json:"pinnedDependencies"`
	OpenDependencies   []readyDep `json:"openDependencies"`
}

type readyExplainView struct {
	Ready   []readyExplainEntry `json:"ready"`
	Blocked []readyExplainEntry `json:"blocked"`
}

// buildReadyExplain читает карточки один раз и строит объяснение ready-очереди.
// Рёбра приходят из того же whyEdges, что и why, поэтому объяснение и gated_by
// видят один граф. В объяснение попадают только блокирующие типы рёбер:
// parent-child (part-of) и related отсекаются через taskstore.IsGateEdgeKind.
// Неизвестная цель ребра пропускается, не обрывая построение остальной очереди.
func buildReadyExplain(tasksDir string) (readyExplainView, error) {
	cards, err := taskstore.LoadCards(tasksDir)
	if err != nil {
		return readyExplainView{}, err
	}
	byID := make(map[string]taskstore.Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}

	view := readyExplainView{
		Ready:   []readyExplainEntry{},
		Blocked: []readyExplainEntry{},
	}
	for _, c := range cards {
		// В ready-очередь попадают только карточки, которые ещё можно взять:
		// закрытые и закреплённые держит SubjectCanBeGated, а in_progress уже
		// работает агент. Карточки в статусе blocked остаются видимыми.
		if c.Status == "in_progress" || !taskstore.SubjectCanBeGated(c) {
			continue
		}
		entry := readyExplainEntry{
			ID:                 c.ID,
			ResolvedBlockers:   []readyDep{},
			PinnedDependencies: []readyDep{},
			OpenDependencies:   []readyDep{},
		}
		seen := map[string]bool{}
		for _, e := range whyEdges(c) {
			if !taskstore.IsGateEdgeKind(e.kind) || seen[e.to] {
				continue
			}
			seen[e.to] = true
			target, ok := byID[e.to]
			if !ok {
				continue
			}
			dep := readyDep{
				ID:     target.ID,
				Type:   taskstore.GateKind(target),
				Reason: taskstore.GateReason(readyGateDescription(target)),
			}
			switch {
			case whyClosed(target):
				entry.ResolvedBlockers = append(entry.ResolvedBlockers, dep)
			case taskstore.IsPinned(target):
				entry.PinnedDependencies = append(entry.PinnedDependencies, dep)
			default:
				entry.OpenDependencies = append(entry.OpenDependencies, dep)
			}
		}
		sortReadyDeps(entry.ResolvedBlockers)
		sortReadyDeps(entry.PinnedDependencies)
		sortReadyDeps(entry.OpenDependencies)
		if len(entry.OpenDependencies) == 0 {
			view.Ready = append(view.Ready, entry)
		} else {
			view.Blocked = append(view.Blocked, entry)
		}
	}
	sort.Slice(view.Ready, func(i, j int) bool { return view.Ready[i].ID < view.Ready[j].ID })
	sort.Slice(view.Blocked, func(i, j int) bool { return view.Blocked[i].ID < view.Blocked[j].ID })
	return view, nil
}

// readyGateDescription возвращает текст, из которого GateReason читает причину
// гейта. Поле description в Extra имеет приоритет над Context, как и в движке
// gated_by, поэтому оба пути видят одну причину.
func readyGateDescription(c taskstore.Card) string {
	if c.Extra != nil {
		if v := strings.TrimSpace(c.Extra["description"]); v != "" {
			return v
		}
	}
	return c.Context
}

func sortReadyDeps(deps []readyDep) {
	sort.Slice(deps, func(i, j int) bool { return deps[i].ID < deps[j].ID })
}

func readyDepIDs(deps []readyDep) string {
	if len(deps) == 0 {
		return "-"
	}
	ids := make([]string, 0, len(deps))
	for _, d := range deps {
		ids = append(ids, d.ID)
	}
	return strings.Join(ids, ",")
}

// runTaskReady печатает объяснение ready-очереди. JSON содержит оба раздела и
// все три списка зависимостей для каждой записи, поэтому повторный вызов даёт
// побайтово тот же результат.
func runTaskReady(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"format": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) > 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	format := p.value("format")
	if format != "" && format != "table" && format != "json" {
		return failTask(stderr, taskstore.Validationf("beacon task ready: неверное значение --format %q, ожидается table или json", format))
	}
	view, err := buildReadyExplain(cfg.TasksDir)
	if err != nil {
		return failTask(stderr, err)
	}
	if format == "json" {
		b, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return failTask(stderr, taskstore.Startupf("не собран JSON ready: %v", err))
		}
		fmt.Fprintln(stdout, string(b))
		return taskstore.ExitOK
	}
	for _, e := range view.Ready {
		fmt.Fprintf(stdout, "READY %s resolved=%s pinned=%s open=%s\n",
			e.ID, readyDepIDs(e.ResolvedBlockers), readyDepIDs(e.PinnedDependencies), readyDepIDs(e.OpenDependencies))
	}
	for _, e := range view.Blocked {
		fmt.Fprintf(stdout, "BLOCKED %s resolved=%s pinned=%s open=%s\n",
			e.ID, readyDepIDs(e.ResolvedBlockers), readyDepIDs(e.PinnedDependencies), readyDepIDs(e.OpenDependencies))
	}
	return taskstore.ExitOK
}

// whyExternalGates возвращает уникальные цели внешних гейтов карточки в
// порядке появления. Ребра читаются из тех же полей, что и внутренние.
func whyExternalGates(c taskstore.Card) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range whyEdges(c) {
		if e.kind != whyExternalKind || seen[e.to] {
			continue
		}
		seen[e.to] = true
		out = append(out, e.to)
	}
	return out
}

// whyExternalState читает состояние внешнего гейта из Watch. Это чтение, а не
// опрос: новый цикл опроса не запускается. Для gh:pr ребро соответствует
// Watch с тем же номером PR, для gh:run — Watch с совпадающим HeadSHA или Issue.
func whyExternalState(target string, cfg taskcli.Config) string {
	db, err := store.OpenProduction(filepath.Join(cfg.StateDir, mcpStateDBName))
	if err != nil {
		return "нет данных Watch"
	}
	defer db.Close()
	watches, err := db.AllWatches()
	if err != nil {
		return "нет данных Watch"
	}
	for _, w := range watches {
		switch {
		case strings.HasPrefix(target, "gh:pr:"):
			n, err := strconv.Atoi(strings.TrimPrefix(target, "gh:pr:"))
			if err == nil && w.PR == n {
				return string(w.Verdict)
			}
		case strings.HasPrefix(target, "gh:run:"):
			id := strings.TrimPrefix(target, "gh:run:")
			if w.HeadSHA == id || w.Issue == id {
				return string(w.Verdict)
			}
		}
	}
	return "нет данных Watch"
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

	// This read loads stored event payloads. OpenProduction keeps the same
	// redacting guarantee the long-lived MCP process has (SE-03).
	db, err := store.OpenProduction(filepath.Join(cfg.StateDir, mcpStateDBName))
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
	return c.Status == "done" || c.Status == "dropped" || c.Status == "duplicate"
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
	for _, m := range whyExternalRE.FindAllStringSubmatch(c.Raw, -1) {
		if taskstore.IsExternalGateTarget(m[1]) {
			e := whyEdge{kind: whyExternalKind, to: m[1]}
			if !seen[e] {
				seen[e] = true
				edges = append(edges, e)
			}
		}
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

// completionFields это входы команды завершения. reason и duplicateOf несут
// причину закрытия и карточку-дубль; оба поля необязательны и не меняют rule
// об обязательном доказательстве.
type completionFields struct {
	summary     string
	evidence    string
	reason      string
	duplicateOf string
}

// runTaskDone завершает карточку только при наличии отдельного поля
// --evidence. Без доказательства карточка не закрывается: она уходит в
// needs_input с вопросом, который нужно закрыть перед повторной попыткой.
func runTaskDone(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{
		"summary": true, "evidence": true, "reason": true,
		"duplicate-of": true, "duplicateOf": true,
	}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 || !taskstore.IsID(p.pos[0]) {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки вида T<g>.<n>"))
	}
	duplicateOf := p.value("duplicate-of")
	if duplicateOf == "" {
		duplicateOf = p.value("duplicateOf")
	}
	report := completionFields{
		summary:     p.value("summary"),
		evidence:    p.value("evidence"),
		reason:      oneLine(p.value("reason")),
		duplicateOf: strings.TrimSpace(duplicateOf),
	}
	if report.duplicateOf != "" && !taskstore.IsID(report.duplicateOf) {
		return failTask(stderr, taskstore.Validationf("--duplicate-of: неверный ID карточки %q", report.duplicateOf))
	}
	hasEvidence := strings.TrimSpace(report.evidence) != ""
	if hasEvidence && strings.TrimSpace(report.summary) == "" {
		return failTask(stderr, taskstore.Validationf("завершение без итога отклонено: задайте --summary"))
	}
	id := p.pos[0]
	lockPath := taskstore.LockPath(cfg.StateDir, cfg.Root)

	// Замок держится только на подготовке карточки. CloseCard берёт тот же
	// замок сам, поэтому его нельзя вызывать с уже захваченным.
	var cardPath string
	err = func() error {
		lock, err := taskstore.AcquireLock(lockPath, cfg.Root)
		if err != nil {
			return err
		}
		defer lock.Release()

		cards, err := taskstore.LoadCards(cfg.TasksDir)
		if err != nil {
			return err
		}
		var target *taskstore.Card
		for i := range cards {
			if cards[i].ID == id {
				target = &cards[i]
				break
			}
		}
		if target == nil {
			return taskstore.Validationf("карточка %s не найдена", id)
		}
		if !taskActive(target.Status) {
			return taskstore.Validationf("карточка %s в статусе %s, завершение неприменимо", id, target.Status)
		}

		if !hasEvidence {
			reason := "завершение без доказательства: задайте --evidence"
			newText := needsInputText(target.Raw, reason)
			if err := taskstore.AtomicWrite(target.Path, []byte(newText), 0644); err != nil {
				return err
			}
			return taskstore.Validationf("завершение без доказательства отклонено: карточка %s переведена в needs_input, задайте --evidence", id)
		}

		// CloseCard управляет полями закрытия, поэтому здесь пишутся только
		// итог, доказательство и снятие маркера ожидания ввода.
		newText := setCardField(target.Raw, "Итог", oneLine(report.summary))
		newText = setCardField(newText, "Доказательство", oneLine(report.evidence))
		newText = setCardField(newText, "Состояние", "")
		newText = setCardField(newText, "Вопрос", "")
		if err := taskstore.AtomicWrite(target.Path, []byte(newText), 0644); err != nil {
			return err
		}
		cardPath = target.Path
		return nil
	}()
	if err != nil {
		return failTask(stderr, err)
	}

	// Канонические поля закрытия пишет CloseCard из taskstore. Флаг
	// --duplicate-of попадает в «Дубликат», а не в «Дублирует».
	closeOpts := taskstore.CloseCardOptions{
		Status:      "done",
		Reason:      report.reason,
		DuplicateOf: report.duplicateOf,
	}
	if err := taskstore.CloseCard(lockPath, cfg.Root, cardPath, closeOpts); err != nil {
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
	root := projectRootFromEnv()
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

// ---- CB-06: компактинг без ключа внешней модели ----
//
// Агент работает без ключа: analyze только читает карточки и отдаёт JSON со
// списком кандидатов, apply принимает готовое саммари из файла и проходит
// проверки размера и снапшота. Ни один из этих режимов не обращается к сети:
// в файле нет ни net/http, ни os/exec, ни вызова внешней модели. Режим auto
// оставлен необязательным и без ключа завершается кодом валидации, не делая
// сетевого вызова.
const compactUsage = "использование: beacon task compact (--analyze | --apply | --auto) [--format json] [--id ID] [--summary FILE] [--summary-text TEXT] [--root DIR] [--max-age-days N] [--dry-run]\n"

var (
	// Файловые возможности держатся в переменных, чтобы cmd/beacon не называл
	// os.ReadFile, os.WriteFile и os.MkdirAll напрямую (ADR 0005).
	compactReadFile  = os.ReadFile
	compactWriteFile = os.WriteFile
	compactMkdirAll  = os.MkdirAll
	compactGetenv    = os.Getenv
	compactNow       = time.Now
)

// compactTextFields — четыре текстовых поля, по размеру которых решается,
// стоит ли сжимать карточку. Поля идентификатора, типов, связей и статуса не
// входят в список: сжатие не меняет граф и ID карточки (CB-05).
var compactTextFields = []string{"Контекст", "Шаги", "Проверка", "Приёмка"}

type compactCandidate struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ClosedAt  string `json:"closed_at,omitempty"`
	SizeBytes int    `json:"size_bytes"`
	Reason    string `json:"reason"`
}

type compactResult struct {
	Mode       string `json:"mode"`
	Applied    bool   `json:"applied"`
	ID         string `json:"id,omitempty"`
	SizeBefore int    `json:"size_before,omitempty"`
	SizeAfter  int    `json:"size_after,omitempty"`
	Snapshot   string `json:"snapshot,omitempty"`
}

// compactSnapshot хранит исходный текст карточки до перезаписи. Text и Fields
// сохраняются побайтово, чтобы restore вернул исходный текст без потерь.
type compactSnapshot struct {
	CardID    string            `json:"card_id"`
	CreatedAt string            `json:"created_at"`
	Schema    int               `json:"schema"`
	Text      string            `json:"text"`
	Fields    map[string]string `json:"fields"`
}

// runTaskCompact — точка входа безключевого компактинга. Проверка взаимной
// исключительности режимов идёт до любого чтения файлов, поэтому неверный
// вызов не меняет карточки и не создаёт снапшоты.
func runTaskCompact(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv,
		map[string]bool{
			"format": true, "id": true, "summary": true,
			"summary-text": true, "root": true, "max-age-days": true,
		},
		map[string]bool{"analyze": true, "apply": true, "auto": true, "dry-run": true})
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) > 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	modes := 0
	for _, mode := range []string{"analyze", "apply", "auto"} {
		if p.bools[mode] {
			modes++
		}
	}
	if modes != 1 {
		fmt.Fprint(stderr, compactUsage)
		return failTask(stderr, taskstore.Validationf("compact: нужен ровно один режим --analyze, --apply или --auto"))
	}
	if format := strings.TrimSpace(p.value("format")); format != "" && format != "json" {
		return failTask(stderr, taskstore.Validationf("compact: неизвестный формат %q", format))
	}

	switch {
	case p.bools["analyze"]:
		return runCompactAnalyze(stdout, stderr, cfg, p)
	case p.bools["apply"]:
		return runCompactApply(stdout, stderr, cfg, p)
	default:
		return runCompactAuto(stderr, p)
	}
}

// runCompactAnalyze печатает список кандидатов в JSON. Режим только читает
// карточки: он не пишет снапшоты, не меняет файлы и не зовёт внешнюю модель.
func runCompactAnalyze(stdout, stderr io.Writer, cfg taskcli.Config, p *parsedArgs) int {
	dir := cfg.TasksDir
	if root := strings.TrimSpace(p.value("root")); root != "" {
		dir = filepath.Join(root, "docs", "tasks")
	}
	maxAge := 30
	if raw := strings.TrimSpace(p.value("max-age-days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return failTask(stderr, taskstore.Validationf("compact: неверный --max-age-days %q", raw))
		}
		maxAge = n
	}
	cards, err := taskstore.LoadCards(dir)
	if err != nil {
		return failTask(stderr, err)
	}
	now := compactNow()
	candidates := make([]compactCandidate, 0)
	for _, c := range cards {
		if candidate, ok := compactIsCandidate(c, maxAge, now); ok {
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	b, err := json.MarshalIndent(candidates, "", "  ")
	if err != nil {
		return failTask(stderr, taskstore.Startupf("compact: не собран JSON кандидатов: %v", err))
	}
	fmt.Fprintln(stdout, string(b))
	return taskstore.ExitOK
}

// compactIsCandidate выбирает закрытую карточку с корректным временем
// закрытия и выдержавшую порог по дням. Поле уровня сжатия убирает уже
// обработанные карточки из повторной выдачи.
func compactIsCandidate(c taskstore.Card, maxAgeDays int, now time.Time) (compactCandidate, bool) {
	if c.Status != "done" && c.Status != "dropped" && c.Status != "duplicate" {
		return compactCandidate{}, false
	}
	if strings.TrimSpace(c.Extra["Уровень сжатия"]) != "" {
		return compactCandidate{}, false
	}
	closedAt := strings.TrimSpace(c.ClosedAt)
	if closedAt == "" {
		return compactCandidate{}, false
	}
	closed, err := time.Parse(time.RFC3339, closedAt)
	if err != nil {
		return compactCandidate{}, false
	}
	age := now.Sub(closed)
	if age < time.Duration(maxAgeDays)*24*time.Hour {
		return compactCandidate{}, false
	}
	return compactCandidate{
		ID: c.ID, Status: c.Status, ClosedAt: closedAt,
		SizeBytes: compactTextSize(c.Raw),
		Reason:    "закрытая карточка старше порога сжатия",
	}, true
}

// runCompactApply принимает саммари из файла или флага и меняет карточку только
// после того, как снапшот записан, а новый текст оказался короче исходного.
func runCompactApply(stdout, stderr io.Writer, cfg taskcli.Config, p *parsedArgs) int {
	id := strings.TrimSpace(p.value("id"))
	if !taskstore.IsID(id) {
		return failTask(stderr, taskstore.Validationf("compact: нужен корректный --id карточки"))
	}
	summaryPath := strings.TrimSpace(p.value("summary"))
	summaryText := p.value("summary-text")
	if summaryPath == "" && strings.TrimSpace(summaryText) == "" {
		return failTask(stderr, taskstore.Validationf("compact: нужен --summary FILE или --summary-text TEXT"))
	}
	if summaryPath != "" && strings.TrimSpace(summaryText) != "" {
		return failTask(stderr, taskstore.Validationf("compact: указаны и --summary, и --summary-text"))
	}
	if summaryPath != "" {
		data, err := compactReadFile(summaryPath)
		if err != nil {
			return failTask(stderr, taskstore.Startupf("compact: не прочитан файл саммари %s: %v", summaryPath, err))
		}
		summaryText = string(data)
	}
	summaryText = oneLine(summaryText)
	if summaryText == "" {
		return failTask(stderr, taskstore.Validationf("compact: саммари пустое"))
	}

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
		return failTask(stderr, taskstore.Validationf("compact: карточка %s не найдена", id))
	}

	now := compactNow()
	newText, err := compactSummaryText(target.Raw, summaryText, now)
	if err != nil {
		return failTask(stderr, err)
	}
	if p.bools["dry-run"] {
		fmt.Fprintf(stdout, "%s dry-run: %d -> %d байт четырёх полей, записи нет\n", id, compactTextSize(target.Raw), compactTextSize(newText))
		return taskstore.ExitOK
	}

	snapshotPath, err := compactSaveSnapshot(cfg, *target, now)
	if err != nil {
		return failTask(stderr, err)
	}
	if err := compactWriteFile(target.Path, []byte(newText), 0644); err != nil {
		return failTask(stderr, taskstore.Startupf("compact: не записана карточка %s: %v", target.Path, err))
	}
	result := compactResult{
		Mode: "apply", Applied: true, ID: id,
		SizeBefore: compactTextSize(target.Raw),
		SizeAfter:  compactTextSize(newText),
		Snapshot:   relativePath(cfg.Root, snapshotPath),
	}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return failTask(stderr, taskstore.Startupf("compact: не собран JSON результата: %v", err))
	}
	fmt.Fprintln(stdout, string(b))
	return taskstore.ExitOK
}

// runCompactAuto оставляет внешнюю модель необязательной. Без ключа команда
// возвращает код валидации и не делает ни одного сетевого вызова; агенту
// предложено использовать analyze или apply.
func runCompactAuto(stderr io.Writer, p *parsedArgs) int {
	id := strings.TrimSpace(p.value("id"))
	if p.bools["dry-run"] || id != "" {
		return failTask(stderr, taskstore.Validationf("compact: --auto не поддерживает --dry-run и --id; используйте --analyze или --apply"))
	}
	key := strings.TrimSpace(compactGetenv("BEACON_MODEL_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(compactGetenv("OPENAI_API_KEY"))
	}
	if key == "" {
		return failTask(stderr, taskstore.Validationf("compact: --auto требует ключ внешней модели; без ключа используйте --analyze или --apply"))
	}
	return failTask(stderr, taskstore.Validationf("compact: внешняя модель недоступна в этой сборке; используйте --analyze или --apply"))
}

// compactSummaryText заменяет четыре текстовых поля коротким саммари и
// добавляет метаданные уровня сжатия. Композиция чистая: вызывающий сам
// решает, когда писать результат на диск.
func compactSummaryText(raw, summary string, now time.Time) (string, error) {
	before := compactTextSize(raw)
	out := raw
	for i, name := range compactTextFields {
		value := ""
		switch i {
		case 0:
			value = "Саммари: " + summary
		default:
			value = "См. снапшот"
		}
		out = compactSetTextField(out, name, value)
	}
	if after := compactTextSize(out); after >= before {
		return "", taskstore.Validationf("compact: саммари не короче оригинала: %d >= %d байт, карточка не изменена", after, before)
	}
	out = setCardField(out, "Уровень сжатия", "1")
	out = setCardField(out, "Время сжатия", now.UTC().Format(time.RFC3339))
	return out, nil
}

// compactSaveSnapshot пишет исходный текст карточки до перезаписи. Снимок
// лежит в state dir, а не в tasks dir, поэтому не попадает в LoadCards и не
// меняет число карточек в каталоге.
func compactSaveSnapshot(cfg taskcli.Config, c taskstore.Card, now time.Time) (string, error) {
	fields := make(map[string]string, len(compactTextFields))
	for _, name := range compactTextFields {
		fields[name] = compactTextField(c.Raw, name)
	}
	snap := compactSnapshot{
		CardID: c.ID, CreatedAt: now.UTC().Format(time.RFC3339),
		Schema: 1, Text: c.Raw, Fields: fields,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", taskstore.Startupf("compact: не собран снапшот: %v", err)
	}
	path := filepath.Join(cfg.StateDir, "compact-snapshots", c.ID+".json")
	if err := compactMkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", taskstore.Startupf("compact: не создан каталог снапшотов: %v", err)
	}
	if err := compactWriteFile(path, append(data, '\n'), 0600); err != nil {
		return "", taskstore.Startupf("compact: не записан снапшот %s: %v", path, err)
	}
	return path, nil
}

// compactTextField читает одно текстовое поле из текста карточки.
func compactTextField(raw, name string) string {
	prefix := name + ":"
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// compactSetTextField меняет значение поля на месте. Отсутствующее поле не
// добавляется: карточка с укороченным текстом обязана разбираться так же, как
// до сжатия.
func compactSetTextField(raw, name, value string) string {
	cr := ""
	if strings.Contains(raw, "\r\n") {
		cr = "\r"
	}
	prefix := name + ":"
	out := make([]string, 0, strings.Count(raw, "\n")+1)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, prefix) {
			if value != "" {
				out = append(out, prefix+" "+value+cr)
			}
			continue
		}
		out = append(out, line+cr)
	}
	return strings.Join(out, "\n")
}

// compactTextSize считает суммарный размер четырёх текстовых полей. Именно
// это число сравнивается до и после сжатия по правилу CB-05.
func compactTextSize(raw string) int {
	n := 0
	for _, name := range compactTextFields {
		n += len(compactTextField(raw, name))
	}
	return n
}
