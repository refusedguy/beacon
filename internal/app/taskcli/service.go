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
