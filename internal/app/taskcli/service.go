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

	"github.com/bmatcuk/doublestar/v4"
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
	adrRefRE = regexp.MustCompile(`^ADR ([0-9]{4})$`)
	// Cards come from several waves: T3.226, T5.1, S1.001 and UI-31. All of
	// them are valid identifiers, so the lister must not reject a new wave
	// just because its prefix differs from the original T<n>.<n> form.
	idrRE     = regexp.MustCompile(`^[A-Za-z]+[0-9]*[.-][0-9]+$`)
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
		if !oneOf(v, "P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8") {
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
	if !oneOf(o.Phase, "P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8") {
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
// не пересекаются. Каталог пересекает любой путь внутри него, а glob через
// doublestar пересекает всё, что ему соответствует. Один и тот же файл может
// быть записан точным путём в одной карточке и шаблоном в другой, поэтому
// сравнение симметрично.
func IntersectingFiles(a, b LaunchAssignment) (string, string, bool) {
	for _, fa := range a.Files {
		for _, fb := range b.Files {
			if patternsConflict(fa, fb) {
				return fa, fb, true
			}
		}
	}
	return "", "", false
}

// patternsConflict сравнивает две границы как glob-ы в обе стороны. Точный путь
// не содержит метасимволов, поэтому для пары «файл и шаблон» срабатывает вызов,
// где шаблон стоит первым. Для двух шаблонов проверка консервативна: совпадение
// одного шаблона с буквальным видом другого считается пересечением.
func patternsConflict(a, b string) bool {
	return globMatches(a, b) || globMatches(b, a)
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

	repo := newRateLimitRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return RouteResult{}, err
	}
	defer unlock()

	target, err := repo.load(id)
	if err != nil {
		return RouteResult{}, err
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
	if err := repo.save(*target, newText); err != nil {
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

// LoadWorkerHistory читает историю. Отсутствующий файл это пустая история.
func (s *Service) LoadWorkerHistory() (WorkerHistory, error) {
	return newWorkerHistoryRepo(s.Cfg).load()
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
	repo := newWorkerHistoryRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return err
	}
	defer unlock()
	h, err := repo.load()
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
	return repo.save(h)
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

// ---- ADR 0039 п2 / T3.61: CI healing с общим почасовым потолком ----
//
// Один наблюдатель на сервис классифицирует упавшие проверки и решает,
// запускать ли ещё одну починку. Слепой ретрай запрещён пунктом 1 ADR 0039:
// только класс transient получает ограниченное число попыток. Почасовой
// потолок считается на весь сервис, а не на каждого из агентов, поэтому
// запуски лежат в одном файле состояния под общим замком. Исчерпание попыток
// или потолка возвращает action park с полным текстом ошибки, а не новый круг
// починок.

// CheckClass — класс упавшей проверки. Починка разрешена только transient.
type CheckClass string

const (
	CheckClassTransient     CheckClass = "transient"
	CheckClassDeterministic CheckClass = "deterministic"
	CheckClassUnknown       CheckClass = "unknown"
)

// CheckFailure — одна упавшая проверка. Output сохраняется целиком, чтобы
// парковка по пункту 1 ADR 0039 получила полный текст ошибки.
type CheckFailure struct {
	Name   string `json:"name"`
	Output string `json:"output"`
}

// Healable сообщает, имеет ли класс смысл чинить ретраем.
func (c CheckClass) Healable() bool { return c == CheckClassTransient }

var (
	transientSignals = []string{
		"timeout", "timed out", "etimedout", "connection reset", "econnreset",
		"socket hang up", "rate limit", "429", "502", "503", "504",
		"service unavailable", "temporarily unavailable", "connection refused",
	}
	deterministicSignals = []string{
		"compilation error", "build failed", "test failed", "tests failed",
		"--- fail", "assertion", "expected", "lint error", "type error",
		"typecheck", "panic:", "syntax error",
	}
)

func containsSignal(output string, signals []string) bool {
	lower := strings.ToLower(output)
	for _, s := range signals {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// ClassifyCheck относит проверку к классу по её выводу. Временные ошибки
// инфраструктуры дают transient, ошибки кода и тестов дают deterministic,
// всё остальное остаётся unknown и не ретраится.
func ClassifyCheck(f CheckFailure) CheckClass {
	if containsSignal(f.Output, transientSignals) {
		return CheckClassTransient
	}
	if containsSignal(f.Output, deterministicSignals) {
		return CheckClassDeterministic
	}
	return CheckClassUnknown
}

// HealingAction — решение наблюдателя: запустить починку или запарковать.
type HealingAction string

const (
	HealingRetry HealingAction = "retry"
	HealingPark  HealingAction = "park"
)

// Ограничения сервиса, а не отдельного агента. Cap считается по всем задачам.
const (
	MaxHealingAttempts = 2
	HourlyLaunchCap    = 20
)

// HealingDecision — наблюдаемое решение одной проверки.
// Поля глубины зеркалят модель domain.Descent. Матрица слоёв (ADR 0005)
// запрещает app импортировать internal/domain, поэтому значения хранятся
// примитивами; пустые значения не меняют существующие решения.
type HealingDecision struct {
	Action     HealingAction `json:"action"`
	Class      CheckClass    `json:"class"`
	Attempt    int           `json:"attempt"`
	Reason     string        `json:"reason"`
	CapSpent   int           `json:"capSpent"`
	CapLimit   int           `json:"capLimit"`
	Layer      string        `json:"layer,omitempty"`
	Hypothesis string        `json:"hypothesis,omitempty"`
	Falsifier  string        `json:"falsifier,omitempty"`
	Evidence   string        `json:"evidence,omitempty"`
	Steps      int           `json:"steps,omitempty"`
}

// HealingLogEntry — одна запись журнала. Класс, номер попытки и расход
// потолка пишутся по каждому решению, включая парковку.
type HealingLogEntry struct {
	Time     time.Time     `json:"time"`
	TaskID   string        `json:"taskId"`
	Check    string        `json:"check"`
	Class    CheckClass    `json:"class"`
	Attempt  int           `json:"attempt"`
	Action   HealingAction `json:"action"`
	CapSpent int           `json:"capSpent"`
	Reason   string        `json:"reason"`
	Output   string        `json:"output"`
}

// ciHealingState — общее состояние наблюдателя в каталоге состояния репозитория.
type ciHealingState struct {
	Launches []time.Time       `json:"launches"`
	Attempts map[string]int    `json:"attempts"`
	Journal  []HealingLogEntry `json:"journal"`
}

func pruneLaunches(st *ciHealingState, now time.Time) {
	cutoff := now.Add(-time.Hour)
	kept := st.Launches[:0]
	for _, t := range st.Launches {
		if !t.Before(cutoff) {
			kept = append(kept, t)
		}
	}
	st.Launches = kept
}

func healingKey(taskID, check string) string { return taskID + "\x00" + check }

// HealingLog возвращает журнал решений в порядке записи.
func (s *Service) HealingLog() ([]HealingLogEntry, error) {
	st, err := newHealingRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	return append([]HealingLogEntry(nil), st.Journal...), nil
}

// CapSpent возвращает число запусков агента за последний час по всему сервису.
func (s *Service) CapSpent(now time.Time) (int, error) {
	st, err := newHealingRepo(s.Cfg).load()
	if err != nil {
		return 0, err
	}
	pruneLaunches(&st, now)
	return len(st.Launches), nil
}

// HealingAttempts возвращает число попыток, уже использованных парой задача/проверка.
func (s *Service) HealingAttempts(taskID, check string) (int, error) {
	st, err := newHealingRepo(s.Cfg).load()
	if err != nil {
		return 0, err
	}
	return st.Attempts[healingKey(taskID, check)], nil
}

// DecideHealing классифицирует упавшую проверку и под общим замком решает,
// запускать ли починку. now приходит извне, чтобы потолок был проверяемым;
// нулевое время означает текущее. Ретрай получает только transient, не более
// MaxHealingAttempts попыток на пару задача/проверка и не более
// HourlyLaunchCap запусков на сервис за час. Исчерпание любого предела даёт
// action park с полным текстом ошибки в журнале, а не новый круг починок.
func (s *Service) DecideHealing(taskID string, f CheckFailure, now time.Time) (HealingDecision, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return HealingDecision{}, taskstore.Validationf("не задана задача healing")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	class := ClassifyCheck(f)
	decision := HealingDecision{Class: class, CapLimit: HourlyLaunchCap}

	repo := newHealingRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return HealingDecision{}, err
	}
	defer unlock()

	st, err := repo.load()
	if err != nil {
		return HealingDecision{}, err
	}
	pruneLaunches(&st, now)

	key := healingKey(taskID, f.Name)
	attempts := st.Attempts[key]
	decision.CapSpent = len(st.Launches)

	switch {
	case !class.Healable():
		decision.Action = HealingPark
		decision.Attempt = attempts
		decision.Reason = fmt.Sprintf("проверка %s класса %s не чинится ретраем", f.Name, class)
	case attempts >= MaxHealingAttempts:
		decision.Action = HealingPark
		decision.Attempt = attempts
		decision.Reason = fmt.Sprintf("истощены попытки починки %s: %d из %d", f.Name, attempts, MaxHealingAttempts)
	case decision.CapSpent >= HourlyLaunchCap:
		decision.Action = HealingPark
		decision.Attempt = attempts
		decision.Reason = fmt.Sprintf("истощён почасовой потолок запусков сервиса: %d из %d", decision.CapSpent, HourlyLaunchCap)
	default:
		decision.Action = HealingRetry
		decision.Attempt = attempts + 1
		decision.Reason = fmt.Sprintf("запуск починки %s, попытка %d из %d", f.Name, decision.Attempt, MaxHealingAttempts)
		st.Attempts[key] = decision.Attempt
		st.Launches = append(st.Launches, now)
		decision.CapSpent = len(st.Launches)
	}

	st.Journal = append(st.Journal, HealingLogEntry{
		Time: now.UTC(), TaskID: taskID, Check: f.Name, Class: class,
		Attempt: decision.Attempt, Action: decision.Action,
		CapSpent: decision.CapSpent, Reason: decision.Reason, Output: f.Output,
	})
	if err := repo.save(st); err != nil {
		return HealingDecision{}, err
	}
	return decision, nil
}

// ---- ADR 0040 п6 / T3.71: advisory-аренда файловых glob-ов ----
//
// Агент берёт аренду на glob-ы до правки и снимает её после. Guard проверяет
// файлы коммита на границе коммита и блокирует только аренды других агентов.
// Это advisory-блокировка ресурса, а не блокировка файла операционной системой:
// аренда не мешает писать файл, она мешает закоммитить чужой файл. Это также не
// планирование конфликта до запуска из T3.46: здесь решение принимается в момент
// записи. Снятие аренды и явный обход guard пишутся в журнал, поэтому обход не
// остаётся незаметным.

// ReservationEvent задаёт вид события журнала аренды.
type ReservationEvent string

const (
	ReservationAcquire ReservationEvent = "acquire"
	ReservationRelease ReservationEvent = "release"
	ReservationBlocked ReservationEvent = "blocked"
	ReservationBypass  ReservationEvent = "bypass"
)

// FileReservation хранит аренду glob-ов одного агента. Повторная аренда того же
// агента заменяет предыдущий набор, а не складывает с ним.
type FileReservation struct {
	Agent string    `json:"agent"`
	Globs []string  `json:"globs"`
	Time  time.Time `json:"time"`
}

// ReservationLogEntry хранит одну запись журнала аренды. Снятие и обход guard
// фиксируются всегда; блокировка тоже пишется, чтобы отказ был виден.
type ReservationLogEntry struct {
	Time   time.Time        `json:"time"`
	Kind   ReservationEvent `json:"kind"`
	Agent  string           `json:"agent"`
	Globs  []string         `json:"globs,omitempty"`
	Files  []string         `json:"files,omitempty"`
	Owners []string         `json:"owners,omitempty"`
	Reason string           `json:"reason"`
}

// GuardDecision описывает наблюдаемый результат проверки коммита. Blocked означает,
// что файлы задевают чужую аренду; Bypassed означает, что такой коммит всё же
// пропущен по явному обходу и это записано в журнал.
type GuardDecision struct {
	Agent    string   `json:"agent"`
	Allowed  bool     `json:"allowed"`
	Blocked  bool     `json:"blocked"`
	Bypassed bool     `json:"bypassed"`
	Files    []string `json:"files"`
	Owners   []string `json:"owners"`
	Reason   string   `json:"reason"`
}

// fileReservationState хранит аренды и журнал в каталоге состояния репозитория.
type fileReservationState struct {
	Reservations []FileReservation     `json:"reservations"`
	Journal      []ReservationLogEntry `json:"journal"`
}

// globMatches сообщает, задевает ли файл glob аренды. Нормализация путей и
// прямой конфликт каталога остаются здесь, а синтаксис glob задаёт doublestar:
// '*' и '?' не переходят границу сегмента, '**' переходит, а '**/' означает
// ноль или несколько целых сегментов.
func globMatches(glob, file string) bool {
	g := strings.ReplaceAll(strings.TrimSpace(glob), "\\", "/")
	f := strings.ReplaceAll(strings.TrimSpace(file), "\\", "/")
	g, f = strings.TrimSuffix(g, "/"), strings.TrimSuffix(f, "/")
	if g == "" || f == "" {
		return false
	}
	if taskstore.PathsConflict(g, f) {
		return true
	}
	ok, err := doublestar.Match(g, f)
	return err == nil && ok
}

func validateReservationGlobs(globs []string) ([]string, error) {
	out := make([]string, 0, len(globs))
	seen := make(map[string]bool, len(globs))
	for _, raw := range globs {
		g := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
		g = strings.TrimSuffix(g, "/")
		if g == "" {
			return nil, taskstore.Validationf("пустой glob аренды")
		}
		if !doublestar.ValidatePattern(g) {
			return nil, taskstore.Validationf("неверный glob аренды %q", raw)
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	if len(out) == 0 {
		return nil, taskstore.Validationf("не заданы glob-ы аренды")
	}
	return out, nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// ReserveFiles берёт аренду на glob-ы до правки. Аренда advisory: она не
// проверяет чужие аренды и не мешает писать файл, а только регистрирует заявку.
// Конфликт проверяется на границе коммита в CheckCommit.
func (s *Service) ReserveFiles(agent string, globs []string, now time.Time) error {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return taskstore.Validationf("не задан агент аренды")
	}
	normalized, err := validateReservationGlobs(globs)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	repo := newReservationRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return err
	}
	defer unlock()

	st, err := repo.load()
	if err != nil {
		return err
	}
	kept := make([]FileReservation, 0, len(st.Reservations)+1)
	for _, r := range st.Reservations {
		if r.Agent != agent {
			kept = append(kept, r)
		}
	}
	st.Reservations = append(kept, FileReservation{Agent: agent, Globs: normalized, Time: now.UTC()})
	st.Journal = append(st.Journal, ReservationLogEntry{
		Time: now.UTC(), Kind: ReservationAcquire, Agent: agent, Globs: normalized,
		Reason: fmt.Sprintf("агент %s арендовал %d glob-ов", agent, len(normalized)),
	})
	return repo.save(st)
}

// ReleaseFiles снимает аренду агента. Снятие пишется в журнал вместе с glob-ами,
// которые были уже взяты, даже если список пуст.
func (s *Service) ReleaseFiles(agent string, now time.Time) error {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return taskstore.Validationf("не задан агент аренды")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	repo := newReservationRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return err
	}
	defer unlock()

	st, err := repo.load()
	if err != nil {
		return err
	}
	kept := make([]FileReservation, 0, len(st.Reservations))
	var released []string
	for _, r := range st.Reservations {
		if r.Agent == agent {
			released = append(released, r.Globs...)
			continue
		}
		kept = append(kept, r)
	}
	st.Reservations = kept
	st.Journal = append(st.Journal, ReservationLogEntry{
		Time: now.UTC(), Kind: ReservationRelease, Agent: agent, Globs: released,
		Reason: fmt.Sprintf("агент %s снял аренду", agent),
	})
	return repo.save(st)
}

// CheckCommit проверяет файлы коммита против аренд других агентов. Своя аренда
// коммит не блокирует. При запрете возвращается Blocked с файлами и владельцами;
// явный обход через bypass=true пропускает коммит и пишется в журнал.
func (s *Service) CheckCommit(agent string, files []string, bypass bool, now time.Time) (GuardDecision, error) {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return GuardDecision{}, taskstore.Validationf("не задан агент коммита")
	}
	if len(files) == 0 {
		return GuardDecision{}, taskstore.Validationf("не заданы файлы коммита")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	repo := newReservationRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return GuardDecision{}, err
	}
	defer unlock()

	st, err := repo.load()
	if err != nil {
		return GuardDecision{}, err
	}
	decision := GuardDecision{Agent: agent, Allowed: true, Reason: "аренд других агентов не найдено"}
	for _, file := range files {
		f := strings.TrimSpace(file)
		if f == "" {
			continue
		}
		for _, r := range st.Reservations {
			if r.Agent == agent || now.Sub(r.Time) > LeaseTTL {
				continue
			}
			for _, g := range r.Globs {
				if !globMatches(g, f) {
					continue
				}
				decision.Blocked = true
				decision.Files = appendUnique(decision.Files, f)
				decision.Owners = appendUnique(decision.Owners, r.Agent)
			}
		}
	}
	if !decision.Blocked {
		return decision, nil
	}
	decision.Allowed = false
	decision.Reason = fmt.Sprintf("коммит агента %s заблокирован: файлы %s арендованы %s",
		agent, strings.Join(decision.Files, ", "), strings.Join(decision.Owners, ", "))
	kind := ReservationBlocked
	if bypass {
		decision.Allowed = true
		decision.Bypassed = true
		decision.Reason = fmt.Sprintf("guard обойден агентом %s: файлы %s арендованы %s",
			agent, strings.Join(decision.Files, ", "), strings.Join(decision.Owners, ", "))
		kind = ReservationBypass
	}
	st.Journal = append(st.Journal, ReservationLogEntry{
		Time: now.UTC(), Kind: kind, Agent: agent,
		Files: decision.Files, Owners: decision.Owners, Reason: decision.Reason,
	})
	if err := repo.save(st); err != nil {
		return GuardDecision{}, err
	}
	return decision, nil
}

// Reservations возвращает текущие аренды в порядке записи.
func (s *Service) Reservations() ([]FileReservation, error) {
	st, err := newReservationRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	return append([]FileReservation(nil), st.Reservations...), nil
}

// ReservationLog возвращает журнал аренды в порядке записи.
func (s *Service) ReservationLog() ([]ReservationLogEntry, error) {
	st, err := newReservationRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	return append([]ReservationLogEntry(nil), st.Journal...), nil
}

// LeaseTTL — срок жизни аренды без подтверждения. Аренда старше этого срока
// считается оставленной: агент мог закончить или упасть, и его glob не должен
// блокировать коммиты навсегда. Проверка времени живёт на границе чтения,
// журнал аренды при этом сохраняется целиком.
const LeaseTTL = 30 * time.Minute

// WorktreeGlob возвращает glob, покрывающий всё дерево ворктри. Путь может
// быть относительным; разделители приводятся к прямому слешу для doublestar.
func WorktreeGlob(worktree string) string {
	p := strings.ReplaceAll(strings.TrimSpace(worktree), "\\", "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}
	return p + "/**"
}

// ReserveLeases регистрирует под одной арендой файловые glob-ы и целые ворктри.
// Повторная аренда того же агента заменяет предыдущий набор, как в ReserveFiles.
func (s *Service) ReserveLeases(agent string, globs, worktrees []string, now time.Time) error {
	all := make([]string, 0, len(globs)+len(worktrees))
	all = append(all, globs...)
	for _, worktree := range worktrees {
		g := WorktreeGlob(worktree)
		if g == "" {
			return taskstore.Validationf("пустой путь ворктри аренды")
		}
		all = append(all, g)
	}
	return s.ReserveFiles(agent, all, now)
}

// ---- ADR 0041 п6 / T3.82: журнал причин отказа precheck ----
//
// Причина отказа precheck пишется в отдельный файл состояния под тем же замком
// flock, что и другие журналы сервиса. Сигнатура RecordPrecheckFailure
// совпадает с портом mcp.PrecheckJournal по примитивам, поэтому слой app
// реализует порт, не импортируя infra.

// PrecheckLogEntry — одна запись журнала. Исход фиксирован как precheck_failure.
type PrecheckLogEntry struct {
	Time    time.Time `json:"time"`
	Outcome string    `json:"outcome"`
	Reason  string    `json:"reason"`
}

type precheckState struct {
	Journal []PrecheckLogEntry `json:"journal"`
}

// RecordPrecheckFailure пишет причину отказа precheck в журнал. Пустая причина
// отклоняется: журнал существует, чтобы причина была понятной.
func (s *Service) RecordPrecheckFailure(reason string, at time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return taskstore.Validationf("не задана причина precheck")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	repo := newPrecheckRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := repo.load()
	if err != nil {
		return err
	}
	st.Journal = append(st.Journal, PrecheckLogEntry{
		Time: at.UTC(), Outcome: "precheck_failure", Reason: reason,
	})
	return repo.save(st)
}

// PrecheckLog возвращает журнал причин precheck в порядке записи.
func (s *Service) PrecheckLog() ([]PrecheckLogEntry, error) {
	st, err := newPrecheckRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	return append([]PrecheckLogEntry(nil), st.Journal...), nil
}

// ---- ADR 0044 п4 / T3.99: bounded handoff journal с recovery на старте ----
//
// После каждого шага сервис атомарно дописывает факт в журнал handoff с жёстким
// лимитом размера. Самые старые записи отбрасываются при переполнении, а на
// старте процесс читает журнал и подставляет блок восстановления. Журнал хранит
// только факты о пройденных шагах и не заменяет структурированную передачу
// T3.25 с её пятью полями intent, evidence, patch_plan, verification_plan и
// risk_notes, ни секции продолжения T3.107 must_not_redo, must_preserve и
// working_set. Ограничение размера и блок восстановления это новое состояние
// между передачами, которого в T3.25 нет.

// HandoffJournalEntry — одна запись журнала: факт о пройденном шаге передачи.
// Seq монотонен и не переиспользуется при отбрасывании старых записей.
type HandoffJournalEntry struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	TaskID string    `json:"taskId"`
	Step   string    `json:"step"`
	Note   string    `json:"note"`
}

// HandoffRecovery — блок восстановления, который процесс подставляет на старте,
// прочитав журнал. Entries и Steps идут в порядке записи.
type HandoffRecovery struct {
	HasJournal bool                  `json:"hasJournal"`
	Count      int                   `json:"count"`
	LastSeq    uint64                `json:"lastSeq"`
	Steps      []string              `json:"steps"`
	Entries    []HandoffJournalEntry `json:"entries"`
}

// Жёсткие лимиты журнала. При переполнении отбрасываются самые старые записи,
// пока не выполнены оба условия: число записей и суммарный размер в байтах.
const (
	MaxHandoffJournalEntries = 16
	MaxHandoffJournalBytes   = 64 * 1024
)

type handoffJournalState struct {
	Entries []HandoffJournalEntry `json:"entries"`
}

func handoffJournalBytes(entries []HandoffJournalEntry) int {
	data, err := json.Marshal(handoffJournalState{Entries: entries})
	if err != nil {
		return 0
	}
	return len(data)
}

// pruneHandoffJournal отбрасывает самые старые записи, пока не выполнены оба
// лимита. Всегда остаётся хотя бы одна запись, чтобы новая запись не терялась
// из-за собственного размера.
func pruneHandoffJournal(entries []HandoffJournalEntry) []HandoffJournalEntry {
	for len(entries) > MaxHandoffJournalEntries {
		entries = entries[1:]
	}
	for len(entries) > 1 && handoffJournalBytes(entries) > MaxHandoffJournalBytes {
		entries = entries[1:]
	}
	return entries
}

// AppendHandoffStep атомарно дописывает факт о пройденном шаге в журнал и
// возвращает записанную запись. Seq растёт монотонно и не сбрасывается при
// отбрасывании старых записей. Пустая задача или шаг отклоняется.
func (s *Service) AppendHandoffStep(taskID, step, note string, at time.Time) (HandoffJournalEntry, error) {
	taskID = strings.TrimSpace(taskID)
	step = strings.TrimSpace(step)
	if taskID == "" {
		return HandoffJournalEntry{}, taskstore.Validationf("не задана задача журнала handoff")
	}
	if step == "" {
		return HandoffJournalEntry{}, taskstore.Validationf("не задан шаг журнала handoff")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	repo := newHandoffJournalRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return HandoffJournalEntry{}, err
	}
	defer unlock()

	st, err := repo.load()
	if err != nil {
		return HandoffJournalEntry{}, err
	}
	var seq uint64 = 1
	if n := len(st.Entries); n > 0 {
		seq = st.Entries[n-1].Seq + 1
	}
	entry := HandoffJournalEntry{
		Seq: seq, Time: at.UTC(), TaskID: taskID, Step: step, Note: strings.TrimSpace(note),
	}
	st.Entries = append(st.Entries, entry)
	st.Entries = pruneHandoffJournal(st.Entries)
	if err := repo.save(st); err != nil {
		return HandoffJournalEntry{}, err
	}
	return entry, nil
}

// HandoffJournal возвращает записи журнала в порядке записи.
func (s *Service) HandoffJournal() ([]HandoffJournalEntry, error) {
	st, err := newHandoffJournalRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	return append([]HandoffJournalEntry(nil), st.Entries...), nil
}

// RecoverHandoff читает журнал на старте и подставляет блок восстановления.
// Пустой журнал даёт HasJournal=false, нулевой LastSeq и пустые срезы. Блок
// несёт только факты о пройденных шагах; BuildHandoff остаётся отдельным
// контрактом структурированной передачи из пяти полей.
func (s *Service) RecoverHandoff() (HandoffRecovery, error) {
	st, err := newHandoffJournalRepo(s.Cfg).load()
	if err != nil {
		return HandoffRecovery{}, err
	}
	out := HandoffRecovery{
		HasJournal: len(st.Entries) > 0,
		Count:      len(st.Entries),
		Steps:      make([]string, 0, len(st.Entries)),
		Entries:    append([]HandoffJournalEntry(nil), st.Entries...),
	}
	if n := len(st.Entries); n > 0 {
		out.LastSeq = st.Entries[n-1].Seq
	}
	for _, e := range st.Entries {
		out.Steps = append(out.Steps, e.Step)
	}
	return out, nil
}

// ---- ADR 0059 п4 / T3.137: /fix-checks как отдельная задача на том же worktree ----
//
// После красной проверки агенту ставится отдельная задача «починить красные
// проверки» в уже существующем worktree. Worktree не пересоздаётся, агент не
// перезапускается: сервис только регистрирует задачу и сохраняет того же
// исполнителя и то же рабочее пространство. Это продолжение работы в той же
// изоляции ADR 0021, а не новая аренда и не новый worktree.

// FixChecksStatus — состояние задачи починки проверок.
type FixChecksStatus string

const (
	FixChecksPending FixChecksStatus = "pending"
	FixChecksDone    FixChecksStatus = "done"
)

// FixChecksTask — отдельная задача «починить красные проверки». Key это ключ
// идемпотентности: повторный запрос с тем же источником, worktree, агентом и
// набором проверок возвращает эту задачу, а не создаёт вторую.
type FixChecksTask struct {
	ID           string          `json:"id"`
	Key          string          `json:"key"`
	SourceTaskID string          `json:"sourceTaskId"`
	Worktree     string          `json:"worktree"`
	Agent        string          `json:"agent"`
	Checks       []string        `json:"checks"`
	Status       FixChecksStatus `json:"status"`
	CreatedAt    time.Time       `json:"createdAt"`
}

// FixChecksRequest задаёт исходную задачу, существующий worktree, того же
// исполнителя и список упавших проверок. Now приходит извне, чтобы время было
// проверяемым; нулевое время означает текущее.
type FixChecksRequest struct {
	SourceTaskID string
	Worktree     string
	Agent        string
	Checks       []string
	Now          time.Time
}

// FixChecksResult — наблюдаемый исход постановки задачи. Created=false означает,
// что задача уже была создана ранее и возвращена без дубликата.
type FixChecksResult struct {
	Task    FixChecksTask `json:"task"`
	Created bool          `json:"created"`
	Reason  string        `json:"reason"`
}

// fixChecksState хранит отдельные задачи починки в каталоге состояния репозитория.
type fixChecksState struct {
	Sequence int             `json:"sequence"`
	Tasks    []FixChecksTask `json:"tasks"`
}

// normalizeFixChecks убирает пустые и повторные проверки и сортирует список,
// чтобы порядок перечисления не создавал вторую задачу.
func normalizeFixChecks(checks []string) ([]string, error) {
	out := make([]string, 0, len(checks))
	seen := make(map[string]bool, len(checks))
	for _, raw := range checks {
		c := strings.TrimSpace(raw)
		if c == "" {
			return nil, taskstore.Validationf("пустая проверка fix-checks")
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, taskstore.Validationf("не заданы проверки fix-checks")
	}
	sort.Strings(out)
	return out, nil
}

func fixChecksKey(source, worktree, agent string, checks []string) string {
	return strings.Join([]string{source, worktree, agent, strings.Join(checks, "\x00")}, "\x00")
}

// RequestFixChecks ставит отдельную задачу починки в уже существующем worktree.
// Повтор того же запроса возвращает прежнюю задачу без дубликата. Активная
// задача другого агента в том же worktree отклоняется: продолжение не должно
// перезапускать исполнителя. Новый worktree сервис не создаёт.
func (s *Service) RequestFixChecks(r FixChecksRequest) (FixChecksResult, error) {
	source := strings.TrimSpace(r.SourceTaskID)
	if source == "" {
		return FixChecksResult{}, taskstore.Validationf("не задана исходная задача fix-checks")
	}
	agent := strings.TrimSpace(r.Agent)
	if agent == "" {
		return FixChecksResult{}, taskstore.Validationf("не задан агент fix-checks")
	}
	worktree := filepath.Clean(strings.TrimSpace(r.Worktree))
	if worktree == "" || worktree == "." {
		return FixChecksResult{}, taskstore.Validationf("не задан worktree fix-checks")
	}
	info, err := os.Stat(worktree)
	if err != nil {
		if os.IsNotExist(err) {
			return FixChecksResult{}, taskstore.Validationf("worktree %s не существует", worktree)
		}
		return FixChecksResult{}, taskstore.Startupf("не прочитан worktree %s: %v", worktree, err)
	}
	if !info.IsDir() {
		return FixChecksResult{}, taskstore.Validationf("worktree %s не является каталогом", worktree)
	}
	checks, err := normalizeFixChecks(r.Checks)
	if err != nil {
		return FixChecksResult{}, err
	}
	if r.Now.IsZero() {
		r.Now = time.Now().UTC()
	}

	repo := newFixChecksRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return FixChecksResult{}, err
	}
	defer unlock()

	st, err := repo.load()
	if err != nil {
		return FixChecksResult{}, err
	}
	key := fixChecksKey(source, worktree, agent, checks)
	for _, t := range st.Tasks {
		if t.Key == key {
			return FixChecksResult{Task: t, Created: false,
				Reason: fmt.Sprintf("задача %s уже поставлена в worktree %s", t.ID, t.Worktree)}, nil
		}
	}
	for _, t := range st.Tasks {
		if t.Worktree == worktree && t.Status == FixChecksPending && t.Agent != agent {
			return FixChecksResult{}, taskstore.Validationf(
				"в worktree %s уже работает агент %s по задаче %s; перезапуск агента запрещён",
				worktree, t.Agent, t.ID)
		}
	}

	st.Sequence++
	task := FixChecksTask{
		ID:           fmt.Sprintf("fix-%d", st.Sequence),
		Key:          key,
		SourceTaskID: source,
		Worktree:     worktree,
		Agent:        agent,
		Checks:       checks,
		Status:       FixChecksPending,
		CreatedAt:    r.Now.UTC(),
	}
	st.Tasks = append(st.Tasks, task)
	if err := repo.save(st); err != nil {
		return FixChecksResult{}, err
	}
	return FixChecksResult{Task: task, Created: true,
		Reason: fmt.Sprintf("задача %s поставлена агенту %s в worktree %s", task.ID, agent, worktree)}, nil
}

// CompleteFixChecks помечает задачу починки выполненной. Повторный вызов для
// уже завершённой задачи возвращает её без изменений.
func (s *Service) CompleteFixChecks(id string, now time.Time) (FixChecksTask, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return FixChecksTask{}, taskstore.Validationf("не задана задача fix-checks")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	repo := newFixChecksRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return FixChecksTask{}, err
	}
	defer unlock()

	st, err := repo.load()
	if err != nil {
		return FixChecksTask{}, err
	}
	for i := range st.Tasks {
		if st.Tasks[i].ID != id {
			continue
		}
		if st.Tasks[i].Status != FixChecksDone {
			st.Tasks[i].Status = FixChecksDone
			if err := repo.save(st); err != nil {
				return FixChecksTask{}, err
			}
		}
		return st.Tasks[i], nil
	}
	return FixChecksTask{}, taskstore.Validationf("задача fix-checks %s не найдена", id)
}

// FixChecksTask читает одну задачу починки по ID.
func (s *Service) FixChecksTask(id string) (FixChecksTask, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return FixChecksTask{}, taskstore.Validationf("не задана задача fix-checks")
	}
	st, err := newFixChecksRepo(s.Cfg).load()
	if err != nil {
		return FixChecksTask{}, err
	}
	for _, t := range st.Tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return FixChecksTask{}, taskstore.Validationf("задача fix-checks %s не найдена", id)
}

// FixChecksTasks возвращает все задачи починки в порядке постановки.
func (s *Service) FixChecksTasks() ([]FixChecksTask, error) {
	st, err := newFixChecksRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	return append([]FixChecksTask(nil), st.Tasks...), nil
}

// FixChecksForWorktree возвращает задачи конкретного worktree. Пустой путь это
// ошибка: задача всегда привязана к существующему рабочему пространству.
func (s *Service) FixChecksForWorktree(worktree string) ([]FixChecksTask, error) {
	worktree = filepath.Clean(strings.TrimSpace(worktree))
	if worktree == "" || worktree == "." {
		return nil, taskstore.Validationf("не задан worktree fix-checks")
	}
	st, err := newFixChecksRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	out := make([]FixChecksTask, 0, len(st.Tasks))
	for _, t := range st.Tasks {
		if t.Worktree == worktree {
			out = append(out, t)
		}
	}
	return out, nil
}

// ---- ADR 0063 фича 5 / T3.157: подтверждение continued после handoff ----
//
// Передача сессии считается состоявшейся только после проверки факта: получатель
// продолжил диалог. Возможны три ответа: continued, inconclusive, not-continued.
// Отправка самой заметки (BuildHandoff, AppendHandoffStep) handoff не завершает:
// пока нет ответа continued, передача остаётся неподтверждённой. Ответы
// inconclusive и not-continued не отбрасываются: они остаются в журнале и видны
// через HandoffStatus. Это проверка факта, а не resume по чекпоинту ADR 0062 и не
// курсор ADR 0025.

// HandoffAnswer — ответ проверки факта продолжения сессии после handoff.
type HandoffAnswer string

const (
	HandoffContinued    HandoffAnswer = "continued"
	HandoffInconclusive HandoffAnswer = "inconclusive"
	HandoffNotContinued HandoffAnswer = "not-continued"
)

func (a HandoffAnswer) valid() bool {
	switch a {
	case HandoffContinued, HandoffInconclusive, HandoffNotContinued:
		return true
	}
	return false
}

// HandoffConfirmation — один записанный ответ проверки продолжения передачи.
type HandoffConfirmation struct {
	TaskID string        `json:"taskId"`
	Answer HandoffAnswer `json:"answer"`
	At     time.Time     `json:"at"`
}

// HandoffStatus — наблюдаемое состояние подтверждения одной передачи. Answer
// это последний записанный ответ, Completed true только когда он continued.
// Answers хранит все ответы в порядке записи, поэтому inconclusive и
// not-continued не скрываются.
type HandoffStatus struct {
	TaskID    string                `json:"taskId"`
	Answer    HandoffAnswer         `json:"answer"`
	Completed bool                  `json:"completed"`
	Answers   []HandoffConfirmation `json:"answers"`
}

type handoffConfirmationState struct {
	Entries []HandoffConfirmation `json:"entries"`
}

// RecordHandoffAnswer записывает ответ проверки факта продолжения. Допустимы
// только три ответа ADR 0063; неизвестный ответ и пустая задача отклоняются.
func (s *Service) RecordHandoffAnswer(taskID string, answer HandoffAnswer, at time.Time) (HandoffConfirmation, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return HandoffConfirmation{}, taskstore.Validationf("не задана задача подтверждения handoff")
	}
	if !answer.valid() {
		return HandoffConfirmation{}, taskstore.Validationf("неизвестный ответ подтверждения handoff %q", answer)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	repo := newConfirmationRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return HandoffConfirmation{}, err
	}
	defer unlock()
	st, err := repo.load()
	if err != nil {
		return HandoffConfirmation{}, err
	}
	entry := HandoffConfirmation{TaskID: taskID, Answer: answer, At: at.UTC()}
	st.Entries = append(st.Entries, entry)
	if err := repo.save(st); err != nil {
		return HandoffConfirmation{}, err
	}
	return entry, nil
}

// HandoffStatus возвращает состояние подтверждения задачи. Completed true только
// при последнем ответе continued; без ответов Completed false. Все ответы видны
// в Answers, включая inconclusive и not-continued.
func (s *Service) HandoffStatus(taskID string) (HandoffStatus, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return HandoffStatus{}, taskstore.Validationf("не задана задача подтверждения handoff")
	}
	st, err := newConfirmationRepo(s.Cfg).load()
	if err != nil {
		return HandoffStatus{}, err
	}
	out := HandoffStatus{TaskID: taskID, Answers: make([]HandoffConfirmation, 0)}
	for _, e := range st.Entries {
		if e.TaskID != taskID {
			continue
		}
		out.Answers = append(out.Answers, e)
		out.Answer = e.Answer
	}
	out.Completed = out.Answer == HandoffContinued
	return out, nil
}

// ---- HD-022 / ADR 0295: связь feat/fix с milestone ----
//
// Проверка связи живёт в taskstore и остаётся чистой. Этот метод только
// разбирает текст коммита и подставляет порты. Для коммитов вне feat/fix
// связь не требуется, поэтому исход LinkageEmpty.

// CheckCommitLinkage разбирает текст коммита и проверяет связь с milestone.
// Порты передаёт вызывающий, чтобы проверка не делала I/O.
func (s *Service) CheckCommitLinkage(message string, ports taskstore.LinkagePorts) taskstore.LinkageVerdict {
	l := taskstore.ParseLinkage(message)
	if !taskstore.RequiresLinkage(l.Subject) {
		return taskstore.LinkageEmpty
	}
	return taskstore.CheckLinkage(l, ports)
}
