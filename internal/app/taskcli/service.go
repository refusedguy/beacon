// Package taskcli содержит бизнес-логику подкоманды beacon task.
// Каталог карточек, каталог состояния и каталог ADR передаются конфигурацией,
// поэтому тесты работают во временных каталогах.
package taskcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// Config задаёт каталоги и корень проекта.
type Config struct {
	TasksDir string
	StateDir string
	ADRDir   string
	Root     string
}

// Service выполняет list, new и take.
type Service struct {
	Cfg Config
}

// NewService создаёт сервис с конфигурацией.
func NewService(cfg Config) *Service { return &Service{Cfg: cfg} }

var (
	adrRefRE  = regexp.MustCompile(`^ADR ([0-9]{4})$`)
	idrRE     = regexp.MustCompile(`^T[0-9]+\.[0-9]+$`)
	adrStatRE = regexp.MustCompile(`(?m)^Статус:[ \t]*(.*)$`)
)

type jsonCard struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Status       string   `json:"status"`
	Phase        string   `json:"phase"`
	OwnerFiles   []string `json:"ownerFiles"`
	Dependencies []string `json:"dependencies"`
	Reference    string   `json:"reference"`
	Agent        string   `json:"agent"`
	Time         string   `json:"time"`
	Title        string   `json:"title"`
}

// ListOptions задаёт фильтры и формат вывода.
type ListOptions struct {
	Statuses []string
	Phases   []string
	Agents   []string
	Files    []string
	Format   string
}

// List читает карточки, применяет фильтры и возвращает готовый вывод.
func (s *Service) List(o ListOptions) (string, error) {
	if o.Format == "" {
		o.Format = "table"
	}
	if o.Format != "table" && o.Format != "json" {
		return "", taskstore.Validationf("неизвестный формат %q", o.Format)
	}
	for _, v := range o.Statuses {
		if !oneOf(v, "todo", "in_progress", "done", "dropped") {
			return "", taskstore.Validationf("неизвестный статус %q", v)
		}
	}
	for _, v := range o.Phases {
		if !oneOf(v, "P0", "P1", "P2", "P3", "P4") {
			return "", taskstore.Validationf("неизвестная фаза %q", v)
		}
	}
	cards, err := taskstore.LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return "", err
	}
	out := make([]taskstore.Card, 0, len(cards))
	for _, c := range cards {
		if !matchSet(c.Status, o.Statuses) || !matchSet(c.Phase, o.Phases) || !matchSet(c.Agent, o.Agents) {
			continue
		}
		if len(o.Files) > 0 {
			hit := false
			for _, want := range o.Files {
				for _, owner := range c.OwnerFiles {
					if taskstore.PathsConflict(want, owner) {
						hit = true
						break
					}
				}
				if hit {
					break
				}
			}
			if !hit {
				continue
			}
		}
		out = append(out, c)
	}
	if o.Format == "json" {
		rows := make([]jsonCard, 0, len(out))
		for _, c := range out {
			owners := c.OwnerFiles
			if owners == nil {
				owners = []string{}
			}
			deps := c.Dependencies
			if deps == nil {
				deps = []string{}
			}
			rows = append(rows, jsonCard{
				ID: c.ID, Type: c.Type, Status: c.Status, Phase: c.Phase,
				OwnerFiles: owners, Dependencies: deps, Reference: c.Reference,
				Agent: c.Agent, Time: c.Time, Title: c.Title,
			})
		}
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return "", taskstore.Startupf("не собран JSON: %v", err)
		}
		return string(b) + "\n", nil
	}
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tТип\tСтатус\tФаза\tСсылка\tАгент\tВремя\tВладелец файлов\tЗаголовок")
	for _, c := range out {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.ID, c.Type, c.Status, c.Phase, c.Reference, c.Agent, c.Time,
			strings.Join(c.OwnerFiles, ", "), c.Title)
	}
	if err := tw.Flush(); err != nil {
		return "", taskstore.Startupf("не собран вывод: %v", err)
	}
	return sb.String(), nil
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func matchSet(v string, set []string) bool {
	if len(set) == 0 {
		return true
	}
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// NewOptions задаёт параметры новой карточки.
type NewOptions struct {
	Type         string
	Phase        string
	Title        string
	Reference    string
	OwnerFiles   string
	Dependencies string
	Goal         string
	Context      string
	Steps        string
	Check        string
	Acceptance   string
	DryRun       bool
}

// New проверяет флаги, ссылку и владельцев, затем создаёт карточку под замком.
func (s *Service) New(o NewOptions) (string, string, error) {
	if o.Type == "" {
		o.Type = "task"
	}
	if !oneOf(o.Type, "task", "epic") {
		return "", "", taskstore.Validationf("неизвестный тип %q", o.Type)
	}
	if !oneOf(o.Phase, "P0", "P1", "P2", "P3", "P4") {
		return "", "", taskstore.Validationf("неверная или отсутствующая фаза %q", o.Phase)
	}
	if strings.TrimSpace(o.Title) == "" {
		return "", "", taskstore.Validationf("не задан заголовок")
	}
	if strings.TrimSpace(o.Reference) == "" {
		return "", "", taskstore.Validationf("не задана ссылка")
	}
	owners := splitFlagList(o.OwnerFiles)
	if len(owners) == 0 {
		return "", "", taskstore.Validationf("не заданы владельцы файлов")
	}
	deps := strings.TrimSpace(o.Dependencies)
	if deps == "" {
		deps = "нет"
	}
	if deps != "нет" {
		for _, d := range splitFlagList(deps) {
			if !idrRE.MatchString(d) {
				return "", "", taskstore.Validationf("неверный ID зависимости %q", d)
			}
		}
	}
	o.Goal = defaultText(o.Goal)
	o.Context = defaultText(o.Context)
	o.Steps = defaultText(o.Steps)
	o.Check = defaultText(o.Check)
	o.Acceptance = defaultText(o.Acceptance)

	lockPath := taskstore.LockPath(s.Cfg.StateDir, s.Cfg.Root)
	lock, err := taskstore.AcquireLock(lockPath, s.Cfg.Root)
	if err != nil {
		return "", "", err
	}
	defer lock.Release()

	cards, err := taskstore.LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return "", "", err
	}
	if err := s.validateReference(o.Reference, o.Type, cards); err != nil {
		return "", "", err
	}
	if err := checkOwners(cards, owners); err != nil {
		return "", "", err
	}
	group, err := strconv.Atoi(strings.TrimPrefix(o.Phase, "P"))
	if err != nil {
		return "", "", taskstore.Validationf("неверная фаза %q", o.Phase)
	}
	counterPath := taskstore.CounterPath(s.Cfg.StateDir, s.Cfg.Root)
	groups, err := taskstore.ReadCounter(counterPath)
	if err != nil {
		return "", "", err
	}
	n, id := taskstore.NextID(cards, groups, group)
	filePath := taskstore.CardPath(s.Cfg.TasksDir, id)
	if o.DryRun {
		return id, filePath, nil
	}
	content := renderCard(id, o, owners, deps)
	if err := taskstore.CreateExclusive(filePath, []byte(content)); err != nil {
		return "", "", err
	}
	groups[strconv.Itoa(group)] = n
	if err := taskstore.WriteCounter(counterPath, groups); err != nil {
		return "", "", err
	}
	return id, filePath, nil
}

func defaultText(v string) string {
	if strings.TrimSpace(v) == "" {
		return "заполнить при работе"
	}
	return strings.TrimSpace(v)
}

func splitFlagList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func checkOwners(cards []taskstore.Card, owners []string) error {
	for _, c := range cards {
		if c.Status != "todo" && c.Status != "in_progress" {
			continue
		}
		for _, used := range c.OwnerFiles {
			for _, want := range owners {
				if taskstore.PathsConflict(used, want) {
					return taskstore.Validationf("файл %s занят карточкой %s (%s)", want, c.ID, c.Status)
				}
			}
		}
	}
	return nil
}

// ---- ADR 0036 п6 / T3.46: конфликтное планирование до запуска агентов ----
//
// Планировщик статически анализирует границы изменяемых файлов до запуска
// агентов. Если назначения пересекаются, агенты не стартуют, пока конфликт не
// снят. Это проверка до запуска, а не структурированный ответ о конфликте из
// ADR 0033: здесь конфликт предотвращается, а не разбирается после того, как
// он случился. Агент в работе изолирован по ADR 0021, поэтому общий файл в
// двух назначениях недопустим даже при разных ветках.

// LaunchAssignment — одно назначение перед запуском: имя назначения и границы
// файлов, которые оно будет менять. Путь может быть файлом или каталогом;
// границы сравниваются по сегментам.
type LaunchAssignment struct {
	Name  string
	Files []string
}

// IntersectingFiles статически сравнивает границы файлов двух назначений и
// возвращает первую пересекающуюся пару. Третий результат false, если файлы
// не пересекаются. Каталог пересекает любой путь внутри него.
func IntersectingFiles(a, b LaunchAssignment) (string, string, bool) {
	for _, fa := range a.Files {
		for _, fb := range b.Files {
			if taskstore.PathsConflict(fa, fb) {
				return fa, fb, true
			}
		}
	}
	return "", "", false
}

// CheckLaunch — проверка до запуска. Она анализирует назначения, пока агенты
// ещё не стартовали. Если границы каких-то двух назначений пересекаются,
// запуск отменяется: возвращается ошибка валидации с именами назначений и
// конфликтующими файлами. Конфликт должен быть снят до повторного запуска.
// Пустой результат означает, что агенты могут стартовать.
func CheckLaunch(assignments ...LaunchAssignment) error {
	for i := range assignments {
		for j := i + 1; j < len(assignments); j++ {
			if fa, fb, ok := IntersectingFiles(assignments[i], assignments[j]); ok {
				return taskstore.Validationf(
					"запуск отменён: назначения %s и %s пересекаются по файлам %s и %s; снимите конфликт до запуска",
					assignments[i].Name, assignments[j].Name, fa, fb)
			}
		}
	}
	return nil
}

// PlanLaunch читает карточки и применяет проверку до запуска к их агентам.
// Карточки в статусе done или dropped освобождают свои файлы. Если владельцы
// двух активных карточек пересекаются, агенты не стартуют и возвращается
// ошибка валидации.
func (s *Service) PlanLaunch(ids ...string) error {
	cards, err := taskstore.LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return err
	}
	byID := make(map[string]taskstore.Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	assignments := make([]LaunchAssignment, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSuffix(strings.TrimSpace(raw), ".md")
		if !taskstore.IsID(id) {
			return taskstore.Validationf("неверный ID карточки %q", raw)
		}
		if seen[id] {
			return taskstore.Validationf("карточка %s указана дважды", id)
		}
		seen[id] = true
		c, ok := byID[id]
		if !ok {
			return taskstore.Validationf("карточка %s не найдена", id)
		}
		if c.Status == "done" || c.Status == "dropped" {
			continue
		}
		assignments = append(assignments, LaunchAssignment{Name: id, Files: c.OwnerFiles})
	}
	return CheckLaunch(assignments...)
}

func (s *Service) validateReference(ref, typ string, cards []taskstore.Card) error {
	if m := adrRefRE.FindStringSubmatch(ref); m != nil {
		ok, err := s.adrAccepted(m[1])
		if err != nil {
			return err
		}
		if !ok {
			return taskstore.Validationf("ссылка %s: ADR не найден или не принят", ref)
		}
		return nil
	}
	if idrRE.MatchString(ref) {
		if typ == "epic" {
			return taskstore.Validationf("эпик обязан ссылаться на ADR, получено %s", ref)
		}
		for _, c := range cards {
			if c.ID != ref || c.Type != "epic" {
				continue
			}
			m := adrRefRE.FindStringSubmatch(c.Reference)
			if m == nil {
				return taskstore.Validationf("ссылка %s: у эпика нет ссылки на ADR", ref)
			}
			ok, err := s.adrAccepted(m[1])
			if err != nil {
				return err
			}
			if !ok {
				return taskstore.Validationf("ссылка %s: ADR эпика не принят", ref)
			}
			return nil
		}
		return taskstore.Validationf("ссылка %s: эпик не найден", ref)
	}
	return taskstore.Validationf("неизвестная ссылка %q", ref)
}

func (s *Service) adrAccepted(num string) (bool, error) {
	matches, err := filepath.Glob(filepath.Join(s.Cfg.ADRDir, num+"-*.md"))
	if err != nil {
		return false, taskstore.Startupf("не найден ADR %s: %v", num, err)
	}
	if len(matches) == 0 {
		return false, nil
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return false, taskstore.Startupf("не прочитан ADR %s: %v", num, err)
	}
	m := adrStatRE.FindStringSubmatch(string(data))
	if m == nil {
		return false, nil
	}
	status := strings.TrimSpace(m[1])
	return status == "accepted" || status == "default", nil
}

func renderCard(id string, o NewOptions, owners []string, deps string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s. %s\n", id, o.Title)
	fmt.Fprintf(&b, "Статус: todo\n")
	fmt.Fprintf(&b, "Фаза: %s\n", o.Phase)
	fmt.Fprintf(&b, "Тип: %s\n", o.Type)
	fmt.Fprintf(&b, "Владелец файлов: %s\n", strings.Join(owners, ", "))
	fmt.Fprintf(&b, "Зависимости: %s\n", deps)
	fmt.Fprintf(&b, "Ссылка: %s\n", o.Reference)
	fmt.Fprintf(&b, "Агент: нет\n")
	fmt.Fprintf(&b, "Время: нет\n")
	fmt.Fprintf(&b, "Цель: %s\n", o.Goal)
	fmt.Fprintf(&b, "Контекст: %s\n", o.Context)
	fmt.Fprintf(&b, "Шаги: %s\n", o.Steps)
	fmt.Fprintf(&b, "Проверка: %s\n", o.Check)
	fmt.Fprintf(&b, "Приёмка: %s\n", o.Acceptance)
	return b.String()
}

// TakeOptions задаёт параметры взятия карточки.
type TakeOptions struct {
	ID    string
	Agent string
	Time  string
}

// Take меняет статус карточки на in_progress и записывает агента и время.
func (s *Service) Take(o TakeOptions) (string, error) {
	id := strings.TrimSpace(o.ID)
	id = strings.TrimSuffix(id, ".md")
	if !taskstore.IsID(id) {
		return "", taskstore.Validationf("неверный ID карточки %q", o.ID)
	}
	if strings.TrimSpace(o.Agent) == "" {
		return "", taskstore.Validationf("не задан агент")
	}
	when := strings.TrimSpace(o.Time)
	if when == "" {
		when = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := time.Parse(time.RFC3339, when); err != nil {
		return "", taskstore.Validationf("неверное время %q: %v", o.Time, err)
	}

	lock, err := taskstore.AcquireLock(taskstore.LockPath(s.Cfg.StateDir, s.Cfg.Root), s.Cfg.Root)
	if err != nil {
		return "", err
	}
	defer lock.Release()

	cards, err := taskstore.LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return "", err
	}
	var target *taskstore.Card
	for i := range cards {
		if cards[i].ID == id {
			target = &cards[i]
			break
		}
	}
	if target == nil {
		return "", taskstore.Validationf("карточка %s не найдена", id)
	}
	if target.Status != "todo" {
		return "", taskstore.Validationf("карточка %s уже взята агентом %s в %s", id, target.Agent, target.Time)
	}
	newText := taskstore.ReplaceFields(target.Raw, o.Agent, when)
	if err := taskstore.AtomicWrite(target.Path, []byte(newText), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s in_progress %s %s", id, o.Agent, when), nil
}

// ---- ADR 0031 §4 / Q-report 2.3: цепочка фолбэка агентов при 429 ----
//
// Rule and chain live in the CLI manager. The MCP core never sees executor
// names or the 429 rule, so the ADR 0006 and ADR 0024 boundary holds. The
// external job id is the task card id and is never rewritten on a handover.

// Executors are the fixed rungs of the CLI-manager fallback chain.
const (
	ExecutorClaudeCode = "claude-code"
	ExecutorHermes     = "hermes"
	ExecutorGemini     = "gemini"
)

// FallbackChain is the order a job walks when an executor answers 429.
var FallbackChain = []string{ExecutorClaudeCode, ExecutorHermes, ExecutorGemini}

// RateLimited is the 429 signal from an executor. It is the only event that
// triggers routing. Status is 429 so a caller can raise it from its own
// transport error without importing the CLI package.
type RateLimited struct {
	Executor string
	Message  string
}

func (e *RateLimited) Error() string {
	if e == nil || e.Message == "" {
		return "executor answered 429"
	}
	return e.Message
}

// Status makes RateLimited legible to generic transport-error handling.
func (e *RateLimited) Status() int { return 429 }

// statusCoder is the narrow shape of an error that carries an HTTP status.
type statusCoder interface {
	Status() int
}

// IsRateLimit reports whether err is a 429 from an executor. Plain errors and
// 5xx responses are not routing signals and must not move a job.
func IsRateLimit(err error) bool {
	if err == nil {
		return false
	}
	var rl *RateLimited
	if errors.As(err, &rl) {
		return true
	}
	var sc statusCoder
	return errors.As(err, &sc) && sc.Status() == 429
}

// Job is one CLI-managed dispatch. ID is the external job id and never changes
// when the executor changes; keeping it stable is the point of the fallback.
type Job struct {
	ID       string
	Executor string
	Tried    []string
}

// NewJob starts a job on the first executor of the chain.
func NewJob(id string) (Job, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Job{}, taskstore.Validationf("не задан внешний ID задачи")
	}
	return Job{ID: id, Executor: FallbackChain[0]}, nil
}

// NextOnRateLimit advances the job one step along the chain. The external id
// is returned unchanged. ok is false when the chain is exhausted; in that
// case the job itself is unchanged, so the executor is stable too.
func NextOnRateLimit(j Job) (Job, bool) {
	i := chainIndex(j.Executor)
	if i < 0 || i+1 >= len(FallbackChain) {
		return j, false
	}
	tried := append(append([]string{}, j.Tried...), j.Executor)
	return Job{ID: j.ID, Executor: FallbackChain[i+1], Tried: tried}, true
}

func chainIndex(agent string) int {
	for i, name := range FallbackChain {
		if name == agent {
			return i
		}
	}
	return -1
}

// RouteResult is the observable outcome of one routing step.
type RouteResult struct {
	JobID     string // external job id, unchanged by the handover
	From      string
	To        string
	Status    string // "in_progress <executor>", or the old status when exhausted
	Exhausted bool
}

// RouteRateLimit applies the fallback rule to a task card. On a 429 it moves
// the executor one step forward, keeps the external job id (the card ID), and
// writes the new executor into the card so status shows who runs it now. A
// card whose executor is outside the chain starts from the first rung.
func (s *Service) RouteRateLimit(id string, upstream error) (RouteResult, error) {
	if !IsRateLimit(upstream) {
		return RouteResult{}, taskstore.Validationf("фолбэк вызывается только по 429, получено: %v", upstream)
	}
	id = strings.TrimSuffix(strings.TrimSpace(id), ".md")
	if !taskstore.IsID(id) {
		return RouteResult{}, taskstore.Validationf("неверный ID карточки %q", id)
	}

	lock, err := taskstore.AcquireLock(taskstore.LockPath(s.Cfg.StateDir, s.Cfg.Root), s.Cfg.Root)
	if err != nil {
		return RouteResult{}, err
	}
	defer lock.Release()

	cards, err := taskstore.LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return RouteResult{}, err
	}
	var target *taskstore.Card
	for i := range cards {
		if cards[i].ID == id {
			target = &cards[i]
			break
		}
	}
	if target == nil {
		return RouteResult{}, taskstore.Validationf("карточка %s не найдена", id)
	}
	if target.Status != "todo" && target.Status != "in_progress" {
		return RouteResult{}, taskstore.Validationf("карточка %s в статусе %s, фолбэк неприменим", id, target.Status)
	}

	from := target.Agent
	if chainIndex(from) < 0 {
		from = FallbackChain[0]
	}
	job := Job{ID: target.ID, Executor: from}
	next, ok := NextOnRateLimit(job)
	if !ok {
		return RouteResult{JobID: target.ID, From: from, To: from,
			Status: target.Status + " " + from, Exhausted: true}, nil
	}

	newText := setCardExecutor(target.Raw, next.Executor, from)
	if err := taskstore.AtomicWrite(target.Path, []byte(newText), 0644); err != nil {
		return RouteResult{}, err
	}
	return RouteResult{JobID: target.ID, From: from, To: next.Executor,
		Status: "in_progress " + next.Executor}, nil
}

// setCardExecutor writes the current executor into the status fields of a
// card. Агент stays the machine-readable field; Исполнитель and Фолбэк make
// the handover readable in status. Fields are updated in place and inserted
// once, so repeated fallbacks never duplicate them.
func setCardExecutor(raw, executor, from string) string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines)+2)
	hasExecutor, hasFallback := false, false
	for _, line := range lines {
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			line = strings.TrimSuffix(line, "\r")
		}
		switch {
		case strings.HasPrefix(line, "Агент:"):
			out = append(out, "Агент: "+executor+cr)
		case strings.HasPrefix(line, "Исполнитель:"):
			out = append(out, "Исполнитель: "+executor+cr)
			hasExecutor = true
		case strings.HasPrefix(line, "Фолбэк:"):
			out = append(out, fmt.Sprintf("Фолбэк: %s → %s (429)%s", from, executor, cr))
			hasFallback = true
		case strings.HasPrefix(line, "Статус:"):
			out = append(out, "Статус: in_progress"+cr)
		default:
			out = append(out, line+cr)
		}
	}
	if !hasExecutor {
		out = insertAfter(out, "Время:", "Исполнитель: "+executor)
	}
	if !hasFallback {
		out = insertAfter(out, "Исполнитель:", fmt.Sprintf("Фолбэк: %s → %s (429)", from, executor))
	}
	return strings.Join(out, "\n")
}

// insertAfter places line after the last field with the given prefix. If the
// prefix is absent the line is appended.
func insertAfter(lines []string, prefix, line string) []string {
	at := -1
	for i, existing := range lines {
		if strings.HasPrefix(existing, prefix) {
			at = i
		}
	}
	if at < 0 {
		return append(lines, line)
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at+1]...)
	out = append(out, line)
	return append(out, lines[at+1:]...)
}

// ---- ADR 0032 §1 / T3.25: структурированная передача преемнику ----
//
// Handoff несёт пять обязательных полей вместо отсылки к полной истории чата.
// Тип определён в слое app, а не в domain: матрица слоёв не разрешает ребро
// internal/app -> internal/domain, поэтому app держит зеркало контракта с
// теми же именами полей и JSON-ключами, что и domain.Handoff.

// Handoff is the five-field structured transfer filled from a task card. The
// successor reads these fields instead of the whole chat history.
type Handoff struct {
	Intent           string `json:"intent"`
	Evidence         string `json:"evidence"`
	PatchPlan        string `json:"patch_plan"`
	VerificationPlan string `json:"verification_plan"`
	RiskNotes        string `json:"risk_notes"`
}

// Validate reports whether all five handoff fields are filled, naming the
// first empty one.
func (h Handoff) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"intent", h.Intent},
		{"evidence", h.Evidence},
		{"patch_plan", h.PatchPlan},
		{"verification_plan", h.VerificationPlan},
		{"risk_notes", h.RiskNotes},
	} {
		if strings.TrimSpace(field.value) == "" {
			return taskstore.Validationf("handoff: пустое поле %s", field.name)
		}
	}
	return nil
}

// BuildHandoff читает карточку и заполняет пять полей передачи из её тела:
// Цель -> intent, Контекст -> evidence, Шаги -> patch_plan,
// Проверка -> verification_plan, Приёмка -> risk_notes. Преемник читает
// полученную передачу вместо всей истории чата.
func (s *Service) BuildHandoff(id string) (Handoff, error) {
	id = strings.TrimSuffix(strings.TrimSpace(id), ".md")
	if !taskstore.IsID(id) {
		return Handoff{}, taskstore.Validationf("неверный ID карточки %q", id)
	}
	cards, err := taskstore.LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return Handoff{}, err
	}
	for _, c := range cards {
		if c.ID != id {
			continue
		}
		h := Handoff{
			Intent:           c.Goal,
			Evidence:         c.Context,
			PatchPlan:        c.Steps,
			VerificationPlan: c.Check,
			RiskNotes:        c.Acceptance,
		}
		if err := h.Validate(); err != nil {
			return Handoff{}, err
		}
		return h, nil
	}
	return Handoff{}, taskstore.Validationf("карточка %s не найдена", id)
}

// ---- ADR 0037 п2 / T3.48: возврат wait_result по событию, не по таймауту ----
//
// wait_result возвращается в момент события или границы одобрения. Интервал
// опроса только обновляет наблюдаемое состояние и подсказку pollAfterMs. Он
// никогда не служит причиной возврата. Лестница ADR 0017 не меняется: исход
// доставки по-прежнему определяет подтверждение, повтор и эскалацию, а возврат
// не переписывает маркеры лестницы.

// WaitEventKind это вид сигнала, который видит ожидание. Только result и
// approval заканчивают ожидание. Тик опроса и рабочее обновление оставляют его
// в работе.
type WaitEventKind string

const (
	WaitEventResult      WaitEventKind = "result"
	WaitEventApproval    WaitEventKind = "approval"
	WaitEventTick        WaitEventKind = "tick"
	WaitEventWatch       WaitEventKind = "watch"
	WaitEventSourceEnded WaitEventKind = "source-ended"
)

// WaitEvent это одно наблюдаемое ожиданием событие.
type WaitEvent struct {
	Kind     WaitEventKind
	WatchID  string
	Revision uint64
	Status   string
	Verdict  string
}

// WaitState это состояние, которое ожидание накопило до возврата.
type WaitState struct {
	WatchID  string
	Revision uint64
	Status   string
	Verdict  string
}

// WaitOutcome это то, с чем ожидание возвращается. Cause называет событие,
// закончившее ожидание, и никогда не бывает тиком опроса. PollAfterMs это
// подсказка, а не дедлайн и не причина возврата.
type WaitOutcome struct {
	WatchID     string        `json:"watchID"`
	Cause       WaitEventKind `json:"cause"`
	Revision    uint64        `json:"revision"`
	Status      string        `json:"status"`
	Verdict     string        `json:"verdict"`
	PollAfterMs int64         `json:"pollAfterMs"`
}

// Consume применяет одно событие к состоянию. Возврат (true) происходит только
// на result или approval. Тик опроса и рабочее обновление обновляют наблюдаемое
// состояние и возвращают false, поэтому вызывающий продолжает ждать.
// Тик не меняет Cause и не заканчивает ожидание.
func (st *WaitState) Consume(ev WaitEvent) (WaitOutcome, bool) {
	switch ev.Kind {
	case WaitEventResult, WaitEventApproval:
		return WaitOutcome{
			WatchID:  ev.WatchID,
			Cause:    ev.Kind,
			Revision: ev.Revision,
			Status:   ev.Status,
			Verdict:  ev.Verdict,
		}, true
	case WaitEventTick, WaitEventWatch:
		if ev.WatchID != "" {
			st.WatchID = ev.WatchID
		}
		if ev.Revision != 0 {
			st.Revision = ev.Revision
		}
		if ev.Status != "" {
			st.Status = ev.Status
		}
		if ev.Verdict != "" {
			st.Verdict = ev.Verdict
		}
	}
	return WaitOutcome{}, false
}

// AwaitResult читает события, пока не придёт result или approval, и возвращает
// соответствующий исход. Тики опроса ожидание не заканчивают. Закрытый канал
// без завершающего события даёт исход без побочных действий, чтобы мёртвый
// источник не подвешивал вызывающего. Это не таймаут опроса. pollAfterMs
// копируется в исход как подсказка и не используется как причина возврата.
func AwaitResult(events <-chan WaitEvent, pollAfterMs int64) WaitOutcome {
	var st WaitState
	for ev := range events {
		if out, done := st.Consume(ev); done {
			out.PollAfterMs = pollAfterMs
			return out
		}
	}
	return WaitOutcome{
		WatchID:     st.WatchID,
		Cause:       WaitEventSourceEnded,
		Revision:    st.Revision,
		Status:      "watching",
		Verdict:     "watching",
		PollAfterMs: pollAfterMs,
	}
}

// WaitLadderPosition это положение лестницы ADR 0017, которое ожидание обязано
// оставить без изменений.
type WaitLadderPosition struct {
	Outcome    string
	Attempts   int
	EvidenceAt time.Time
	Stopped    bool
}

// LadderRetries содержит ветвление ADR 0017 по исходу доставки. Повтор есть
// только у transport-failed. posted и indeterminate повторов не имеют.
func LadderRetries(outcome string) bool { return outcome == "transport-failed" }

// AwaitWithLadder ждёт завершающего события и возвращает положение лестницы
// таким же, каким его получил. Исход доставки по-прежнему выбирает
// подтверждение, повтор и эскалацию через LadderRetries. Меняется только момент
// возврата управления, а не маркеры лестницы (ADR 0037 п2).
func AwaitWithLadder(events <-chan WaitEvent, pos WaitLadderPosition, pollAfterMs int64) (WaitOutcome, WaitLadderPosition) {
	return AwaitResult(events, pollAfterMs), pos
}

// SelfTest прогоняет кейсы из раздела 7 спецификации с посаженными нарушениями.
func SelfTest(w io.Writer) error {
	root, err := os.MkdirTemp("", "beacon-selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	tasksDir := filepath.Join(root, "docs", "tasks")
	adrDir := filepath.Join(root, "docs", "adrs")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{tasksDir, adrDir, stateDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
	}
	cfg := Config{TasksDir: tasksDir, StateDir: stateDir, ADRDir: adrDir, Root: root}
	svc := NewService(cfg)

	write := func(p, text string) error { return os.WriteFile(p, []byte(text), 0644) }
	baseCard := func(id, status, typ, ref, owners, agent, when string) string {
		return fmt.Sprintf("# %s. Тестовая карточка\nСтатус: %s\nФаза: P2\nТип: %s\n"+
			"Владелец файлов: %s\nЗависимости: нет\nСсылка: %s\nАгент: %s\nВремя: %s\n"+
			"Цель: тест\nКонтекст: тест\nШаги: тест\nПроверка: тест\nПриёмка: тест\n",
			id, status, typ, owners, ref, agent, when)
	}
	if err := write(filepath.Join(adrDir, "0001-accepted.md"), "Статус: accepted\n"); err != nil {
		return err
	}
	if err := write(filepath.Join(adrDir, "0002-open.md"), "Статус: open\n"); err != nil {
		return err
	}
	if err := write(filepath.Join(tasksDir, "T2.1.md"),
		baseCard("T2.1", "todo", "task", "ADR 0001", "internal/infra/mcp/server.go", "нет", "нет")); err != nil {
		return err
	}

	goodOpts := func() NewOptions {
		return NewOptions{
			Type: "task", Phase: "P2", Title: "Новая карточка", Reference: "ADR 0001",
			OwnerFiles: "internal/app/new/one.go",
		}
	}
	failed := 0
	check := func(name string, cond bool) {
		if cond {
			fmt.Fprintf(w, "%s: ok\n", name)
		} else {
			fmt.Fprintf(w, "%s: FAIL\n", name)
			failed++
		}
	}
	listCode := func() int {
		_, err := svc.List(ListOptions{Format: "json"})
		return taskstore.Code(err)
	}
	newCode := func(o NewOptions) int {
		_, _, err := svc.New(o)
		return taskstore.Code(err)
	}

	// Парсер должен упасть на карточке без поля Статус.
	broken := filepath.Join(tasksDir, "T2.2.md")
	if err := write(broken, strings.Replace(
		baseCard("T2.2", "todo", "task", "ADR 0001", "internal/app/bad.go", "нет", "нет"),
		"Статус: todo\n", "", 1)); err != nil {
		return err
	}
	check("list без Статуса даёт код 1", listCode() == 1)
	os.Remove(broken)

	check("new с ADR 0099 даёт код 1", func() bool {
		o := goodOpts()
		o.Reference = "ADR 0099"
		return newCode(o) == 1
	}())
	check("new с ссылкой T9.9 даёт код 1", func() bool {
		o := goodOpts()
		o.Reference = "T9.9"
		return newCode(o) == 1
	}())
	check("new с ADR open даёт код 1", func() bool {
		o := goodOpts()
		o.Reference = "ADR 0002"
		return newCode(o) == 1
	}())
	check("epic со ссылкой на задачу даёт код 1", func() bool {
		o := goodOpts()
		o.Type = "epic"
		o.Reference = "T2.1"
		return newCode(o) == 1
	}())
	check("new с занятым файлом даёт код 1", func() bool {
		o := goodOpts()
		o.OwnerFiles = "internal/infra/mcp/server.go"
		return newCode(o) == 1
	}())
	check("new с файлом внутри занятого каталога даёт код 1", func() bool {
		if err := os.Remove(filepath.Join(tasksDir, "T2.1.md")); err != nil {
			return false
		}
		if err := write(filepath.Join(tasksDir, "T2.1.md"),
			baseCard("T2.1", "in_progress", "task", "ADR 0001", "internal/infra/mcp/", "agent-1", "2026-11-19T14:03:21Z")); err != nil {
			return false
		}
		o := goodOpts()
		o.OwnerFiles = "internal/infra/mcp/server.go"
		return newCode(o) == 1
	}())

	// Два новых подряд получают разные ID; счётчик не переиспользует удалённый номер.
	id1, _, err1 := svc.New(goodOpts())
	o2 := goodOpts()
	o2.OwnerFiles = "internal/app/new/two.go"
	id2, _, err2 := svc.New(o2)
	check("два new дают разные ID", err1 == nil && err2 == nil && id1 != id2)
	check("счётчик не переиспользует удалённый номер", func() bool {
		if err := os.Remove(filepath.Join(tasksDir, id2+".md")); err != nil {
			return false
		}
		o := goodOpts()
		o.OwnerFiles = "internal/app/new/three.go"
		id3, _, err := svc.New(o)
		return err == nil && id3 != id2
	}())

	// take на карточке in_progress отклоняется, карточка не меняется.
	inProgress := filepath.Join(tasksDir, "T2.9.md")
	if err := write(inProgress, baseCard("T2.9", "in_progress", "task", "ADR 0001",
		"internal/app/nine.go", "agent-x", "2026-11-19T14:03:21Z")); err != nil {
		return err
	}
	rawBefore, _ := os.ReadFile(inProgress)
	_, takeErr := svc.Take(TakeOptions{ID: "T2.9", Agent: "agent-y", Time: "2026-11-19T15:00:00Z"})
	rawAfter, _ := os.ReadFile(inProgress)
	check("take на in_progress даёт код 1 без изменений", taskstore.Code(takeErr) == 1 && string(rawBefore) == string(rawAfter))

	// Два take одной карточки: ровно один успех.
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("T4.%d", i+1)
		cardPath := filepath.Join(tasksDir, id+".md")
		if err := write(cardPath, baseCard(id, "todo", "task", "ADR 0001",
			fmt.Sprintf("internal/app/race%d.go", i), "нет", "нет")); err != nil {
			return err
		}
		results := make(chan int, 2)
		for j := 0; j < 2; j++ {
			go func(n int) {
				_, err := svc.Take(TakeOptions{ID: id, Agent: fmt.Sprintf("agent-%d", n), Time: "2026-11-19T14:03:21Z"})
				results <- taskstore.Code(err)
			}(j)
		}
		a, b := <-results, <-results
		if !((a == 0 && b == 1) || (a == 1 && b == 0)) {
			check(fmt.Sprintf("гонка take, повтор %d", i+1), false)
			return fmt.Errorf("гонка take дала коды %d и %d", a, b)
		}
	}
	check("гонка take: 20 повторов, ровно один успех", true)

	// Остаток замка с мёртвым PID удаляется.
	lockPath := taskstore.LockPath(stateDir, root)
	dead := fmt.Sprintf("pid=99999999\nstarted=%s\nroot=%s\n", time.Now().UTC().Format(time.RFC3339), root)
	if err := os.WriteFile(lockPath, []byte(dead), 0600); err != nil {
		return err
	}
	lock, err := taskstore.AcquireLock(lockPath, root)
	check("замок с мёртвым PID перехвачен", err == nil)
	if err == nil {
		_ = lock.Release()
	}

	if failed > 0 {
		return fmt.Errorf("self-test: %d кейсов не прошли", failed)
	}
	fmt.Fprintf(w, "self-test passed: %d cases\n", 11)
	return nil
}

// ---- ADR 0038 п3 / T3.55: ранжирование воркеров по истории ----
//
// Capability matching из T3.47 сужает множество кандидатов, затем ранжирование
// упорядочивает выживших по репозиторий-локальной истории прохождения,
// задержки и стоимости. Имя сессии остаётся ключом доставки по ADR 0034, но не
// участвует в выборе: при равенстве метрик сохраняется порядок входа.

// WorkerMetrics это репозиторий-локальная история одного воркера. Passes и
// Failures дают историю прохождения, а DelayMS и Cost накапливаются по Samples
// наблюдениям и дают средние задержку и стоимость.
type WorkerMetrics struct {
	Passes   int   `json:"passes"`
	Failures int   `json:"failures"`
	DelayMS  int64 `json:"delayMs"`
	Cost     int64 `json:"cost"`
	Samples  int   `json:"samples"`
}

// PassRate возвращает долю успешных прохождений. Без наблюдений доля равна нулю.
func (m WorkerMetrics) PassRate() float64 {
	total := m.Passes + m.Failures
	if total <= 0 {
		return 0
	}
	return float64(m.Passes) / float64(total)
}

// AvgDelayMS возвращает среднюю задержку одного наблюдения.
func (m WorkerMetrics) AvgDelayMS() int64 {
	if m.Samples <= 0 {
		return 0
	}
	return m.DelayMS / int64(m.Samples)
}

// AvgCost возвращает среднюю стоимость одного наблюдения.
func (m WorkerMetrics) AvgCost() int64 {
	if m.Samples <= 0 {
		return 0
	}
	return m.Cost / int64(m.Samples)
}

// WorkerHistory это история воркеров в каталоге состояния репозитория.
type WorkerHistory map[string]WorkerMetrics

// WorkerCandidate это один кандидат после capability matching из T3.47. Key —
// непрозрачный ключ доставки, Capabilities — объявленные возможности, а Metrics
// — снимок истории. Ни Key, ни имя в выборе не участвуют.
type WorkerCandidate struct {
	Key          string        `json:"key"`
	Capabilities []string      `json:"capabilities"`
	Metrics      WorkerMetrics `json:"metrics"`
}

// WorkerOutcome это одно наблюдение, которое обновляет историю воркера.
type WorkerOutcome struct {
	Worker  string
	Passed  bool
	DelayMS int64
	Cost    int64
}

// declaresAll повторяет контракт T3.47: кандидат должен объявлять все
// затребованные возможности и может объявлять больше.
func declaresAll(declared, required []string) bool {
	set := make(map[string]struct{}, len(declared))
	for _, c := range declared {
		set[c] = struct{}{}
	}
	for _, c := range required {
		if _, ok := set[c]; !ok {
			return false
		}
	}
	return true
}

// RankWorkers возвращает кандидатов, упорядоченных по истории: сначала большая
// доля прохождений, затем меньшая задержка, затем меньшая стоимость. При
// полном равенстве метрик сохраняется порядок входа, поэтому имя не влияет на
// результат.
func RankWorkers(candidates []WorkerCandidate) []WorkerCandidate {
	out := append([]WorkerCandidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Metrics, out[j].Metrics
		if ar, br := a.PassRate(), b.PassRate(); ar != br {
			return ar > br
		}
		if ad, bd := a.AvgDelayMS(), b.AvgDelayMS(); ad != bd {
			return ad < bd
		}
		if ac, bc := a.AvgCost(), b.AvgCost(); ac != bc {
			return ac < bc
		}
		return false
	})
	return out
}

// matchingWorkers сужает множество по возможностям до ранжирования. Пустой
// набор возможностей не выбирает никого, как в T3.47.
func matchingWorkers(candidates []WorkerCandidate, required []string) []WorkerCandidate {
	if len(required) == 0 {
		return nil
	}
	out := make([]WorkerCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Key == "" {
			continue
		}
		if declaresAll(c.Capabilities, required) {
			out = append(out, c)
		}
	}
	return out
}

// SelectWorker сначала сужает множество по возможностям, затем выбирает
// лучшего по истории. Второй результат false, когда подходящих кандидатов нет.
func SelectWorker(candidates []WorkerCandidate, required []string) (WorkerCandidate, bool) {
	narrowed := matchingWorkers(candidates, required)
	if len(narrowed) == 0 {
		return WorkerCandidate{}, false
	}
	return RankWorkers(narrowed)[0], true
}

// workerHistoryPath задаёт файл репозиторий-локальной истории в state dir.
func (s *Service) workerHistoryPath() string {
	return filepath.Join(s.Cfg.StateDir, "worker-history.json")
}

// LoadWorkerHistory читает историю. Отсутствующий файл это пустая история.
func (s *Service) LoadWorkerHistory() (WorkerHistory, error) {
	data, err := os.ReadFile(s.workerHistoryPath())
	if os.IsNotExist(err) {
		return WorkerHistory{}, nil
	}
	if err != nil {
		return nil, taskstore.Startupf("не прочитана история воркеров: %v", err)
	}
	var h WorkerHistory
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, taskstore.Startupf("не разобрана история воркеров: %v", err)
	}
	if h == nil {
		h = WorkerHistory{}
	}
	return h, nil
}

// RecordWorkerOutcome добавляет одно наблюдение в историю под общим замком.
func (s *Service) RecordWorkerOutcome(o WorkerOutcome) error {
	worker := strings.TrimSpace(o.Worker)
	if worker == "" {
		return taskstore.Validationf("не задан воркер")
	}
	if o.DelayMS < 0 || o.Cost < 0 {
		return taskstore.Validationf("задержка и стоимость не могут быть отрицательными")
	}
	lock, err := taskstore.AcquireLock(taskstore.LockPath(s.Cfg.StateDir, s.Cfg.Root), s.Cfg.Root)
	if err != nil {
		return err
	}
	defer lock.Release()
	h, err := s.LoadWorkerHistory()
	if err != nil {
		return err
	}
	m := h[worker]
	if o.Passed {
		m.Passes++
	} else {
		m.Failures++
	}
	m.DelayMS += o.DelayMS
	m.Cost += o.Cost
	m.Samples++
	h[worker] = m
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return taskstore.Startupf("не собрана история воркеров: %v", err)
	}
	return taskstore.AtomicWrite(s.workerHistoryPath(), append(data, '\n'), 0644)
}

// AttachHistory подставляет метрики из истории в кандидатов по их ключу.
// Кандидаты без истории получают нулевые метрики и остаются в списке.
func (s *Service) AttachHistory(candidates []WorkerCandidate) ([]WorkerCandidate, error) {
	h, err := s.LoadWorkerHistory()
	if err != nil {
		return nil, err
	}
	out := append([]WorkerCandidate(nil), candidates...)
	for i := range out {
		if m, ok := h[out[i].Key]; ok {
			out[i].Metrics = m
		}
	}
	return out, nil
}

// SelectWorkerByHistory присоединяет историю и выбирает воркера по возможностям
// и истории. Это вход для адаптера, который уже сузил круг по T3.47.
func (s *Service) SelectWorkerByHistory(candidates []WorkerCandidate, required []string) (WorkerCandidate, bool, error) {
	withHistory, err := s.AttachHistory(candidates)
	if err != nil {
		return WorkerCandidate{}, false, err
	}
	sel, ok := SelectWorker(withHistory, required)
	return sel, ok, nil
}
