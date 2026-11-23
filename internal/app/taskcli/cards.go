package taskcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// cardLock — та часть файлового замка, которая нужна сервису.
type cardLock interface {
	Release() error
}

// acquireLock берёт общий замок карточек. С интервалом по умолчанию вызов
// идёт через taskstore, поэтому production-путь не меняется. Другой интервал
// включает локальную реализацию с тем же flock, проверкой inode и 50 попытками.
func (s *Service) acquireLock() (cardLock, error) {
	path := LockPath(s.Cfg.StateDir, s.Cfg.Root)
	interval := s.Cfg.LockRetryInterval
	if interval <= 0 || interval == DefaultLockRetryInterval {
		return AcquireLock(path, s.Cfg.Root)
	}
	return acquireLockWithInterval(path, s.Cfg.Root, interval)
}

// flockLock удерживает файловый замок через открытый дескриптор.
type flockLock struct {
	path string
	f    *os.File
}

// Release отпускает flock и удаляет файл замка. Повторное освобождение
// безопасно, как и у taskstore.Lock.
func (l *flockLock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	if l.f != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		_ = l.f.Close()
		l.f = nil
	}
	err := os.Remove(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// acquireLockWithInterval повторяет логику taskstore.AcquireLock, но ждёт
// заданный интервал между попытками. Замок остаётся тем же: flock на открытом
// файле, проверка current inode и 50 попыток. Проверка конкуренции не слабеет.
func acquireLockWithInterval(lockPath, root string, retry time.Duration) (cardLock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, Startupf("не создан каталог состояния: %v", err)
	}
	for attempt := 0; attempt < 50; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, Startupf("не открыт замок %s: %v", lockPath, err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
				time.Sleep(retry)
				continue
			}
			return nil, Startupf("не взят замок %s: %v", lockPath, err)
		}
		locked, err := f.Stat()
		if err != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			return nil, Startupf("не прочитан файл замка %s: %v", lockPath, err)
		}
		current, err := os.Stat(lockPath)
		if err != nil || !os.SameFile(locked, current) {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			continue
		}
		content := fmt.Sprintf("pid=%d\nstarted=%s\nroot=%s\n",
			os.Getpid(), time.Now().UTC().Format(time.RFC3339), root)
		if err := f.Truncate(0); err != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			return nil, Startupf("не очищен файл замка %s: %v", lockPath, err)
		}
		if _, err := f.WriteAt([]byte(content), 0); err != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			return nil, Startupf("не записан файл замка: %v", err)
		}
		if err := f.Sync(); err != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			return nil, Startupf("не синхронизирован файл замка: %v", err)
		}
		return &flockLock{path: lockPath, f: f}, nil
	}
	return nil, Startupf("замок %s занят: исчерпано 50 попыток", lockPath)
}

var (
	adrRefRE = regexp.MustCompile(`^ADR ([0-9]{4})$`)
	// The ID grammar lives in taskstore.IsID and is reached through the
	// taskcli facade. Cards come from several waves: T3.226, T5.1, S1.001,
	// ATX-R01 and RS-CMP-06. A local regex rejected the new forms, so the
	// parsers now share taskstore's contract instead.
	adrStatRE = regexp.MustCompile(`(?m)^Статус:[ \t]*(.*)$`)
	// cardStatusLineRE matches the three fields Take writes. The taskstore
	// copy is private to that package, so taskcli keeps its own here.
	cardStatusLineRE = regexp.MustCompile(`^(Статус|Агент|Время):[ \t]*(.*?)[ \t]*$`)
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
		return "", Validationf("beacon task list: неверное значение --format %q, ожидается table или json", o.Format)
	}
	for _, v := range o.Statuses {
		if !taskstore.IsValidStatus(v) {
			return "", Validationf("неизвестный статус %q", v)
		}
	}
	for _, v := range o.Phases {
		if !slices.Contains([]string{"P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8"}, v) {
			return "", Validationf("неизвестная фаза %q", v)
		}
	}
	// CBX-05: ready-чтение сначала просыпает датированные отложки. Ошибка
	// отдельной записи не роняет список, она даёт одну строку предупреждения
	// и запись в журнал.
	wake, err := s.SweepExpiredDefers(DeferWakeOptions{})
	if err != nil {
		return "", err
	}
	warning := ""
	if len(wake.Failed) > 0 {
		warning = fmt.Sprintf("предупреждение: пробуждение по дате не удалось для %d карточки, см. журнал\n", len(wake.Failed))
	}
	var cards []Card
	if len(o.Statuses) == 1 && o.Statuses[0] == "todo" {
		cards, err = s.taskStore().ListTodo()
	} else {
		cards, err = LoadCards(s.Cfg.TasksDir)
	}
	if err != nil {
		return "", err
	}
	out := make([]Card, 0, len(cards))
	for _, c := range cards {
		if !matchSet(c.Status, o.Statuses) || !matchSet(c.Phase, o.Phases) || !matchSet(c.Agent, o.Agents) {
			continue
		}
		if len(o.Files) > 0 {
			hit := false
			for _, want := range o.Files {
				for _, owner := range c.OwnerFiles {
					if PathsConflict(want, owner) {
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
			return "", Startupf("не собран JSON: %v", err)
		}
		return string(b) + "\n" + warning, nil
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
		return "", Startupf("не собран вывод: %v", err)
	}
	return sb.String() + warning, nil
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
	if !slices.Contains([]string{"task", "epic"}, o.Type) {
		return "", "", Validationf("неизвестный тип %q", o.Type)
	}
	if !slices.Contains([]string{"P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8"}, o.Phase) {
		return "", "", Validationf("неверная или отсутствующая фаза %q", o.Phase)
	}
	if strings.TrimSpace(o.Title) == "" {
		return "", "", Validationf("не задан заголовок")
	}
	if strings.TrimSpace(o.Reference) == "" {
		return "", "", Validationf("не задана ссылка")
	}
	owners := splitFlagList(o.OwnerFiles)
	if len(owners) == 0 {
		return "", "", Validationf("не заданы владельцы файлов")
	}
	deps := strings.TrimSpace(o.Dependencies)
	if deps == "" {
		deps = "нет"
	}
	if deps != "нет" {
		for _, d := range splitFlagList(deps) {
			if !IsID(d) {
				return "", "", Validationf("неверный ID зависимости %q", d)
			}
		}
	}
	o.Goal = defaultText(o.Goal)
	o.Context = defaultText(o.Context)
	o.Steps = defaultText(o.Steps)
	o.Check = defaultText(o.Check)
	o.Acceptance = defaultText(o.Acceptance)

	lock, err := s.acquireLock()
	if err != nil {
		return "", "", err
	}
	defer lock.Release()

	cards, err := LoadCards(s.Cfg.TasksDir)
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
		return "", "", Validationf("неверная фаза %q", o.Phase)
	}
	counterPath := CounterPath(s.Cfg.StateDir, s.Cfg.Root)
	groups, err := ReadCounter(counterPath)
	if err != nil {
		return "", "", err
	}
	n, id := NextID(cards, groups, group)
	filePath := CardPath(s.Cfg.TasksDir, id)
	if o.DryRun {
		return id, filePath, nil
	}
	content := renderCard(id, o, owners, deps)
	if err := CreateExclusive(filePath, []byte(content)); err != nil {
		return "", "", err
	}
	groups[strconv.Itoa(group)] = n
	if err := WriteCounter(counterPath, groups); err != nil {
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

func checkOwners(cards []Card, owners []string) error {
	for _, c := range cards {
		if c.Status != "todo" && c.Status != "in_progress" {
			continue
		}
		for _, used := range c.OwnerFiles {
			for _, want := range owners {
				if PathsConflict(used, want) {
					return Validationf("файл %s занят карточкой %s (%s)", want, c.ID, c.Status)
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

// LaunchAssignment — одно назначение перед запуском: имя назначения, границы
// файлов, которые оно будет менять, и статус карточки. Путь может быть файлом
// или каталогом; границы сравниваются по сегментам. Статус «in_progress»
// означает, что агент уже работает с этими файлами: такое назначение не может
// быть кандидатом на новый запуск и блокирует пересекающихся кандидатов.
type LaunchAssignment struct {
	Name   string
	Files  []string
	Status string
}

// isInProgress сообщает, занято ли назначение активным агентом.
func (a LaunchAssignment) isInProgress() bool { return a.Status == "in_progress" }

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
				return Validationf(
					"запуск отменён: назначения %s и %s пересекаются по файлам %s и %s; снимите конфликт до запуска",
					assignments[i].Name, assignments[j].Name, fa, fb)
			}
		}
	}
	return nil
}

// PreflightResult — уже наблюдённое решение PreflightGuard. Сервис сам не читает
// GitHub и не открывает БД: вызывающий передаёт готовый ответ, полученный на
// границе. Поля совпадают с github.PreflightDecision, но не создают зависимости
// app → infra.
type PreflightResult struct {
	Allowed bool
	Reason  string
}

// AdmissionDecision — единый ответ гейта запуска для автоспавнера. Allowed
// означает, что все проверки пройдены; при отказе в Reason лежит причина.
type AdmissionDecision struct {
	Allowed bool
	Reason  string
}

const (
	// AdmissionAllowed — причина разрешённого запуска.
	AdmissionAllowed = "запуск допущен"
	// AdmissionSlotsExhausted — все слоты заняты, свободного нет.
	AdmissionSlotsExhausted = "slots_exhausted"
	// AdmissionQuotaExhausted — причина отказа, если PreflightGuard не дал причину.
	AdmissionQuotaExhausted = "quota_exhausted"
)

// AdmitLaunch объединяет проверки допуска в один вызов. Порядок проверок:
// квота PreflightGuard, свободные слоты, пересечение файлов с активными
// in_progress. Первый отказ завершает решение, поэтому запуск не доходит ни до
// старта агента, ни до чтения карточек внутри сервиса.
func (s *Service) AdmitLaunch(preflight PreflightResult, slotsUsed, slotsTotal int, assignments ...LaunchAssignment) AdmissionDecision {
	if !preflight.Allowed {
		reason := preflight.Reason
		if reason == "" {
			reason = AdmissionQuotaExhausted
		}
		return AdmissionDecision{Reason: reason}
	}
	if slotsTotal <= 0 || slotsUsed >= slotsTotal {
		return AdmissionDecision{Reason: AdmissionSlotsExhausted}
	}
	if err := checkAdmissionConflicts(assignments); err != nil {
		return AdmissionDecision{Reason: err.Error()}
	}
	return AdmissionDecision{Allowed: true, Reason: AdmissionAllowed}
}

// checkAdmissionConflicts проверяет назначения перед запуском. Пересечение
// кандидата с активной in_progress карточкой отменяет запуск с отдельной
// причиной; пересечение двух кандидатов остаётся проверкой CheckLaunch. Два
// уже работающих назначения решением гейта не являются и пропускаются.
func checkAdmissionConflicts(assignments []LaunchAssignment) error {
	for i := range assignments {
		for j := i + 1; j < len(assignments); j++ {
			fa, fb, ok := IntersectingFiles(assignments[i], assignments[j])
			if !ok {
				continue
			}
			activeI, activeJ := assignments[i].isInProgress(), assignments[j].isInProgress()
			if activeI && activeJ {
				continue
			}
			if activeI || activeJ {
				active, candidate := assignments[i], assignments[j]
				if activeJ {
					active, candidate = assignments[j], assignments[i]
				}
				return Validationf(
					"запуск отменён: карточка %s пересекается по файлам %s и %s с активной in_progress карточкой %s",
					candidate.Name, fa, fb, active.Name)
			}
			return Validationf(
				"запуск отменён: назначения %s и %s пересекаются по файлам %s и %s; снимите конфликт до запуска",
				assignments[i].Name, assignments[j].Name, fa, fb)
		}
	}
	return nil
}

// PlanLaunch читает карточки и применяет проверку до запуска к их агентам.
// Карточки в статусе done или dropped освобождают свои файлы. Если владельцы
// двух активных карточек пересекаются, агенты не стартуют и возвращается
// ошибка валидации.
func (s *Service) PlanLaunch(ids ...string) error {
	cards, err := LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return err
	}
	byID := make(map[string]Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	assignments := make([]LaunchAssignment, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id := strings.TrimSuffix(strings.TrimSpace(raw), ".md")
		if !IsID(id) {
			return Validationf("неверный ID карточки %q", raw)
		}
		if seen[id] {
			return Validationf("карточка %s указана дважды", id)
		}
		seen[id] = true
		c, ok := byID[id]
		if !ok {
			return Validationf("карточка %s не найдена", id)
		}
		if c.Status == "done" || c.Status == "dropped" {
			continue
		}
		assignments = append(assignments, LaunchAssignment{Name: id, Files: c.OwnerFiles, Status: c.Status})
	}
	return CheckLaunch(assignments...)
}

func (s *Service) validateReference(ref, typ string, cards []Card) error {
	if m := adrRefRE.FindStringSubmatch(ref); m != nil {
		ok, err := s.adrAccepted(m[1])
		if err != nil {
			return err
		}
		if !ok {
			return Validationf("ссылка %s: ADR не найден или не принят", ref)
		}
		return nil
	}
	if IsID(ref) {
		if typ == "epic" {
			return Validationf("эпик обязан ссылаться на ADR, получено %s", ref)
		}
		for _, c := range cards {
			if c.ID != ref || c.Type != "epic" {
				continue
			}
			m := adrRefRE.FindStringSubmatch(c.Reference)
			if m == nil {
				return Validationf("ссылка %s: у эпика нет ссылки на ADR", ref)
			}
			ok, err := s.adrAccepted(m[1])
			if err != nil {
				return err
			}
			if !ok {
				return Validationf("ссылка %s: ADR эпика не принят", ref)
			}
			return nil
		}
		return Validationf("ссылка %s: эпик не найден", ref)
	}
	return Validationf("неизвестная ссылка %q", ref)
}

func (s *Service) adrAccepted(num string) (bool, error) {
	matches, err := filepath.Glob(filepath.Join(s.Cfg.ADRDir, num+"-*.md"))
	if err != nil {
		return false, Startupf("не найден ADR %s: %v", num, err)
	}
	if len(matches) == 0 {
		return false, nil
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return false, Startupf("не прочитан ADR %s: %v", num, err)
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
	if !IsID(id) {
		return "", Validationf("неверный ID карточки %q", o.ID)
	}
	if strings.TrimSpace(o.Agent) == "" {
		return "", Validationf("не задан агент")
	}
	when := strings.TrimSpace(o.Time)
	if when == "" {
		when = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := time.Parse(time.RFC3339, when); err != nil {
		return "", Validationf("неверное время %q: %v", o.Time, err)
	}

	// CBX-05: карточку с истёкшей датой отложки нельзя взять как blocked.
	// Sweep работает под своим замком до того, как take возьмёт общий замок.
	if _, err := s.SweepExpiredDefers(DeferWakeOptions{}); err != nil {
		return "", err
	}

	if _, err := s.taskStore().Claim(id, o.Agent, when); err != nil {
		return "", err
	}
	// AN-06: хуки идут после успешной записи карточки. Их сбой не откатывает
	// карточку и не меняет её статус.
	_ = s.RunHooks(HookClaimed, id, when)
	return fmt.Sprintf("%s in_progress %s %s", id, o.Agent, when), nil
}

// ---- MEM-06: TTL-reaper для карточек in_progress ----
//
// LeaseTTL внизу файла защищает только glob-аренды. Карточка in_progress
// живёт отдельно. Если процесс воркера умер, её статус остаётся in_progress
// навсегда и слот не освобождается. ReapStaleCards возвращает такие карточки
// в todo. Карточки с живым Watch reaper пропускает. Проверку жизни приносит
// функция-аргумент, поэтому сервис не знает про store и card_links.

// CardLeaseTTL — возраст, после которого карточка in_progress считается
// оставленной. Значение отделено от LeaseTTL, чтобы смена срока аренды glob-ов
// не меняла срок жизни карточек.
const CardLeaseTTL = 30 * time.Minute

// ReapOptions задаёт внедряемые часы, TTL и порты проверки живой работы.
// Пустое Now означает time.Now().UTC(), нулевой TTL означает CardLeaseTTL.
// Отрицательный TTL отклоняется общим валидатором запроса.
// Нулевой порт означает отсутствие жизни, так тесты моделируют мёртвый воркер.
type ReapOptions struct {
	Now   time.Time
	TTL   time.Duration // возраст, после которого карточка считается оставленной
	Agent string        // подпись агента, который запустил reaper, для журнала
	// IDs — фильтр карточек. Пустой список означает все карточки.
	// Длина исходного списка проверяется до дедупликации.
	IDs []string
	// WatchAlive сообщает, что карточка всё ещё связана с живым Watch.
	WatchAlive func(cardID string) (bool, error)
	// ProcessAlive сообщает, что у карточки жив процесс воркера.
	ProcessAlive func(cardID string) (bool, error)
}

// ReapResult — результат одного прохода reaper.
type ReapResult struct {
	Scanned int      // число карточек in_progress, рассмотренных в проходе
	Reaped  []string // ID карточек, переведённых в todo
	Skipped []string // ID карточек, у которых есть живой Watch или процесс
}

// ReaperLogEntry — одна запись журнала: факт возврата карточки в слот.
type ReaperLogEntry struct {
	Time    time.Time `json:"time"`
	CardID  string    `json:"cardID"`
	Agent   string    `json:"agent"`
	TakenAt string    `json:"takenAt"`
	Reason  string    `json:"reason"`
}

// reaperState хранит журнал reaper в каталоге состояния репозитория.
type reaperState struct {
	Journal []ReaperLogEntry `json:"journal"`
}

// stateMsgForm holds the passive-participle endings shared by the read, parse
// and write errors of one fileState. Russian participles agree with the
// subject in gender and number, so each subject shape keeps its own triple.
type stateMsgForm struct {
	read  string
	parse string
	write string
}

var (
	msgMasculine = stateMsgForm{"прочитан", "разобран", "собран"}
	msgFeminine  = stateMsgForm{"прочитана", "разобрана", "собрана"}
	msgNeuter    = stateMsgForm{"прочитано", "разобрано", "собрано"}
	msgPlural    = stateMsgForm{"прочитаны", "разобраны", "собраны"}
)

// fileRepoSpec carries the file names and the Russian message pieces for one
// fileState repository. The form stays separate from the subject so the
// participle case survives an edit to either.
type fileRepoSpec struct {
	file        string
	lockFile    string
	subject     string       // nominative subject for read, parse and write
	lockSubject string       // genitive subject after "замок"
	form        stateMsgForm // Russian message form
}

// fileRepoSpecs is the single table of settings for the seven fileState repos.
var fileRepoSpecs = struct {
	confirmation, fixChecks, handoffJournal, precheck, reservation, reaper, deferWake fileRepoSpec
}{
	confirmation:   fileRepoSpec{"handoff-confirmations.json", "handoff-confirmations.lock", "подтверждения handoff", "подтверждений handoff", msgPlural},
	fixChecks:      fileRepoSpec{"fix-checks.json", "fix-checks.lock", "состояние fix-checks", "fix-checks", msgNeuter},
	handoffJournal: fileRepoSpec{"handoff-journal.json", "handoff-journal.lock", "журнал handoff", "журнала handoff", msgMasculine},
	precheck:       fileRepoSpec{"precheck.json", "precheck.lock", "журнал precheck", "precheck", msgMasculine},
	reservation:    fileRepoSpec{"file-reservations.json", "file-reservations.lock", "аренда файлов", "аренды", msgFeminine},
	reaper:         fileRepoSpec{"reaper-journal.json", "reaper-journal.lock", "журнал reaper", "журнала reaper", msgMasculine},
	deferWake:      fileRepoSpec{"defer-wake-journal.json", "defer-wake-journal.lock", "журнал пробуждения", "журнала пробуждения", msgMasculine},
}

// newFileRepo builds a fileState from cfg and spec. All nine path and error
// fields come from the spec, so a new repo only needs one table row.
func newFileRepo[T any](cfg Config, spec fileRepoSpec) fileState[T] {
	return fileState[T]{
		stateDir:    cfg.StateDir,
		root:        cfg.Root,
		file:        spec.file,
		lockFile:    spec.lockFile,
		readErr:     "не " + spec.form.read + " " + spec.subject + ": %v",
		parseErr:    "не " + spec.form.parse + " " + spec.subject + ": %v",
		writeErr:    "не " + spec.form.write + " " + spec.subject + ": %v",
		lockOpenErr: "не открыт замок " + spec.lockSubject + ": %v",
		lockTakeErr: "не взят замок " + spec.lockSubject + ": %v",
	}
}

// reaperRepo owns reaper-journal.json. The private flock keeps two concurrent
// reaper passes from writing the same card twice.
type reaperRepo struct {
	fileState[reaperState]
}

func newReaperRepo(cfg Config) reaperRepo {
	return reaperRepo{newFileRepo[reaperState](cfg, fileRepoSpecs.reaper)}
}

// ---- CBX-15: общий контракт запроса освобождения аренды ----
//
// CLI, MCP и daemon собирают ReclaimRequest и вызывают prepareReclaim.
// Валидация и счёт времени живут в одной точке, поэтому отказ и срез
// совпадают для всех путей вызова.

const (
	// MaxReclaimIDs — предел исходного списка ID до дедупликации.
	MaxReclaimIDs = 20
	// MaxReclaimAgentBytes — предел длины исполнителя из-за VARCHAR(255).
	MaxReclaimAgentBytes = 255
)

// ReclaimFieldError называет поле запроса, которое не прошло проверку.
// Ошибка оборачивает Validationf, поэтому код выхода остаётся ошибкой
// пользовательского ввода.
type ReclaimFieldError struct {
	Field string
	err   error
}

func (e *ReclaimFieldError) Error() string { return e.err.Error() }
func (e *ReclaimFieldError) Unwrap() error { return e.err }

func reclaimFieldError(field, format string, a ...any) error {
	return &ReclaimFieldError{Field: field, err: Validationf(format, a...)}
}

// ReclaimRequest — общий контракт запроса reclaim. Валидация не читает данные.
type ReclaimRequest struct {
	Agent     string        // исполнитель: непустой после обрезки, не длиннее 255 байт
	OlderThan time.Duration // возраст: неотрицательный, ноль означает CardLeaseTTL
	IDs       []string      // исходный фильтр ID: не более 20 элементов, без пустых
}

// ValidateReclaimRequest проверяет запрос и не читает карточки. Первая
// обнаруженная ошибка содержит имя поля. Cap считается по исходному списку
// до дедупликации.
func ValidateReclaimRequest(r ReclaimRequest) error {
	if strings.TrimSpace(r.Agent) == "" {
		return reclaimFieldError("agent", "не задан исполнитель reclaim")
	}
	if len(r.Agent) > MaxReclaimAgentBytes {
		return reclaimFieldError("agent", "исполнитель длиннее %d байт: %d", MaxReclaimAgentBytes, len(r.Agent))
	}
	if r.OlderThan < 0 {
		return reclaimFieldError("older_than", "возраст не может быть отрицательным: %s", r.OlderThan)
	}
	if len(r.IDs) > MaxReclaimIDs {
		return reclaimFieldError("ids", "запрошено %d ID, максимум %d до дедупликации", len(r.IDs), MaxReclaimIDs)
	}
	for _, id := range r.IDs {
		if strings.TrimSpace(id) == "" {
			return reclaimFieldError("ids", "пустой элемент фильтра ID")
		}
	}
	return nil
}

// reclaimPlan — проверенный запрос, готовый к чтению карточек. Cutoff
// вычисляется один раз и используется во всех путях вызова.
type reclaimPlan struct {
	Now    time.Time
	Cutoff time.Time
	Agent  string
	IDs    map[string]bool // nil означает отсутствие фильтра
}

// prepareReclaim валидирует запрос и строит план. Карточки не читаются.
func prepareReclaim(r ReclaimRequest, now time.Time) (reclaimPlan, error) {
	if err := ValidateReclaimRequest(r); err != nil {
		return reclaimPlan{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	age := r.OlderThan
	if age == 0 {
		age = CardLeaseTTL
	}
	plan := reclaimPlan{
		Now:    now,
		Cutoff: now.Add(-age),
		Agent:  r.Agent,
	}
	if len(r.IDs) > 0 {
		plan.IDs = make(map[string]bool, len(r.IDs))
		for _, id := range r.IDs {
			plan.IDs[strings.TrimSpace(id)] = true
		}
	}
	return plan, nil
}

// reclaimRequestFromOptions преобразует options сервиса в общий запрос.
func reclaimRequestFromOptions(o ReapOptions) ReclaimRequest {
	return ReclaimRequest{Agent: o.Agent, OlderThan: o.TTL, IDs: o.IDs}
}

func (p reclaimPlan) wants(id string) bool {
	return p.IDs == nil || p.IDs[id]
}

// ReapStaleCards переводит в todo карточки in_progress, у которых истёк TTL и
// нет живого Watch или процесса. Проход идемпотентен: после возврата карточка
// имеет статус todo и больше не попадает в выборку, поэтому повторный вызов
// не пишет вторую запись в журнал.
func (s *Service) ReapStaleCards(o ReapOptions) (ReapResult, error) {
	plan, err := prepareReclaim(reclaimRequestFromOptions(o), o.Now)
	if err != nil {
		return ReapResult{}, err
	}

	lock, err := s.acquireLock()
	if err != nil {
		return ReapResult{}, err
	}
	defer lock.Release()

	cards, err := s.loadReapCards(s.Cfg.TasksDir)
	if err != nil {
		return ReapResult{}, err
	}

	out := ReapResult{Reaped: []string{}, Skipped: []string{}}
	var freed []ReaperLogEntry
	for _, c := range cards {
		if c.Status != "in_progress" || !plan.wants(c.ID) {
			continue
		}
		out.Scanned++
		takenAt, err := time.Parse(time.RFC3339, strings.TrimSpace(c.Time))
		if err != nil || !plan.Cutoff.After(takenAt) {
			out.Skipped = append(out.Skipped, c.ID)
			continue
		}
		if alive, err := liveness(o.WatchAlive, c.ID); err != nil {
			return out, err
		} else if alive {
			out.Skipped = append(out.Skipped, c.ID)
			continue
		}
		if alive, err := liveness(o.ProcessAlive, c.ID); err != nil {
			return out, err
		} else if alive {
			out.Skipped = append(out.Skipped, c.ID)
			continue
		}

		if err := AtomicWrite(c.Path, []byte(releaseCardFields(c.Raw)), 0644); err != nil {
			return out, err
		}
		out.Reaped = append(out.Reaped, c.ID)
		freed = append(freed, ReaperLogEntry{
			Time:    plan.Now,
			CardID:  c.ID,
			Agent:   plan.Agent,
			TakenAt: c.Time,
			Reason: fmt.Sprintf("карточка %s освобождена reaper: старше %s, живого Watch и процесса нет",
				c.ID, plan.Now.Sub(plan.Cutoff)),
		})
	}

	if len(freed) > 0 {
		repo := newReaperRepo(s.Cfg)
		unlock, err := repo.lock()
		if err != nil {
			return out, err
		}
		defer unlock()
		st, err := repo.load()
		if err != nil {
			return out, err
		}
		st.Journal = append(st.Journal, freed...)
		if err := repo.save(st); err != nil {
			return out, err
		}
	}
	return out, nil
}

// liveness вызывает порт проверки жизни. Отсутствие порта означает, что
// жизни нет. В продакшене порт подключает адаптер MEM-04.
func liveness(port func(string) (bool, error), cardID string) (bool, error) {
	if port == nil {
		return false, nil
	}
	return port(cardID)
}

// releaseCardFields возвращает три поля Take в исходное состояние. Остальные
// строки карточки не меняются, поэтому JSON и метаданные сохраняются.
func releaseCardFields(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			line = strings.TrimSuffix(line, "\r")
		}
		fm := cardStatusLineRE.FindStringSubmatch(line)
		if fm == nil {
			continue
		}
		switch fm[1] {
		case "Статус":
			lines[i] = "Статус: todo" + cr
		case "Агент":
			lines[i] = "Агент: нет" + cr
		case "Время":
			lines[i] = "Время: нет" + cr
		}
	}
	return strings.Join(lines, "\n")
}

// ReaperLog возвращает записи журнала в порядке записи.
func (s *Service) ReaperLog() ([]ReaperLogEntry, error) {
	st, err := newReaperRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	return append([]ReaperLogEntry(nil), st.Journal...), nil
}

// ---- CBX-05: ленивое пробуждение датированных отложек ----
//
// Отложка живёт в поле «Дата отложки» карточки. В beacon нет отдельного
// статуса deferred, поэтому отложенная карточка получает статус blocked.
// Дата в прошлом возвращает её в todo перед ready-чтением и перед take.
// Запись без даты ("бессрочно") никогда не просыпается.

const deferUntilField = "Дата отложки"

// deferForever помечает отложку без даты. Такая карточка остаётся blocked
// при любом числе проходов sweep.
const deferForever = "бессрочно"

// DeferOptions задаёт карточку и срок отложки. Forever пишет отложку без даты.
type DeferOptions struct {
	ID      string
	Until   string
	Forever bool
}

// Defer пишет срок отложки и переводит карточку в blocked. Дата принимается
// в RFC3339 или в форме YYYY-MM-DD. Вторая форма означает полночь UTC.
func (s *Service) Defer(o DeferOptions) (string, error) {
	id := strings.TrimSuffix(strings.TrimSpace(o.ID), ".md")
	if !IsID(id) {
		return "", Validationf("неверный ID карточки %q", o.ID)
	}
	until := deferForever
	if !o.Forever {
		when, err := parseDeferUntil(o.Until)
		if err != nil {
			return "", err
		}
		if when.IsZero() {
			return "", Validationf("нужна дата отложки или флаг --forever")
		}
		until = when.Format(time.RFC3339)
	}

	lock, err := s.acquireLock()
	if err != nil {
		return "", err
	}
	defer lock.Release()

	cards, err := LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return "", err
	}
	var target *Card
	for i := range cards {
		if cards[i].ID == id {
			target = &cards[i]
			break
		}
	}
	if target == nil {
		return "", Validationf("карточка %s не найдена", id)
	}
	if slices.Contains([]string{"done", "dropped", "duplicate"}, target.Status) {
		return "", Validationf("карточка %s закрыта со статусом %s", id, target.Status)
	}
	text := rewriteDeferFields(target.Raw, "blocked", until)
	if err := AtomicWrite(target.Path, []byte(text), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s deferred %s", id, until), nil
}

// parseDeferUntil разбирает значение поля «Дата отложки». Пустое значение и
// "бессрочно" дают нулевое время, то есть отложку без авто-пробуждения.
func parseDeferUntil(value string) (time.Time, error) {
	v := strings.TrimSpace(value)
	if v == "" || v == deferForever {
		return time.Time{}, nil
	}
	if when, err := time.Parse(time.RFC3339, v); err == nil {
		return when.UTC(), nil
	}
	when, err := time.Parse("2006-01-02", v)
	if err != nil {
		return time.Time{}, Validationf("неверная дата отложки %q: ожидается RFC3339, YYYY-MM-DD или %s", value, deferForever)
	}
	return when.UTC(), nil
}

// rewriteDeferFields меняет статус и срок отложки одной правкой текста. Остальные
// поля и переводы строк, включая CRLF, сохраняются.
func rewriteDeferFields(raw, status, until string) string {
	lines := strings.Split(raw, "\n")
	hasStatus, hasDate := false, false
	for _, line := range lines {
		name, _, found := strings.Cut(strings.TrimSuffix(line, "\r"), ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(name) {
		case "Статус":
			hasStatus = true
		case deferUntilField:
			hasDate = true
		}
	}

	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			line = strings.TrimSuffix(line, "\r")
		}
		name, _, found := strings.Cut(line, ":")
		replaced := false
		if found {
			switch strings.TrimSpace(name) {
			case "Статус":
				out = append(out, "Статус: "+status+cr)
				replaced = true
				if !hasDate {
					out = append(out, deferUntilField+": "+until+cr)
					hasDate = true
				}
			case deferUntilField:
				out = append(out, deferUntilField+": "+until+cr)
				replaced = true
			}
		}
		if !replaced {
			out = append(out, line+cr)
		}
	}
	if !hasStatus {
		out = append([]string{"Статус: " + status}, out...)
	}
	if !hasDate {
		out = append(out, deferUntilField+": "+until)
	}
	return strings.Join(out, "\n")
}

// DeferWakeOptions задаёт часы и операции чтения и записи для одного прохода
// пробуждения. Пустые поля берутся из Config или из обычных файловых вызовов.
type DeferWakeOptions struct {
	Now       time.Time
	ReadCard  func(path string) ([]byte, error)
	WriteCard func(path string, data []byte) error
}

// DeferWakeReport считает карточки одного прохода. Failed содержит ID, чью
// запись не удалось выполнить. Такие карточки остаются отложенными.
type DeferWakeReport struct {
	Scanned int
	Woken   []string
	Skipped []string
	Failed  []string
}

// DeferWakeLogEntry фиксирует одно действие пробуждения или одну ошибку записи.
type DeferWakeLogEntry struct {
	Time   time.Time `json:"time"`
	CardID string    `json:"cardID"`
	Action string    `json:"action"`
	Reason string    `json:"reason"`
}

type deferWakeState struct {
	Journal []DeferWakeLogEntry `json:"journal"`
}

type deferWakeRepo struct {
	fileState[deferWakeState]
}

func newDeferWakeRepo(cfg Config) deferWakeRepo {
	return deferWakeRepo{newFileRepo[deferWakeState](cfg, fileRepoSpecs.deferWake)}
}

// SweepExpiredDefers возвращает в todo карточки с истёкшей датой отложки.
// Перед записью он читает файл заново и сравнивает статус и дату со снимком.
// Карточка, которую успели изменить между снимком и записью, пропускается.
// Ошибки отдельных записей попадают в журнал и не прерывают проход.
func (s *Service) SweepExpiredDefers(o DeferWakeOptions) (DeferWakeReport, error) {
	rep := DeferWakeReport{Woken: []string{}, Skipped: []string{}, Failed: []string{}}

	now := o.Now
	if now.IsZero() && s.Cfg.DeferWakeNow != nil {
		now = s.Cfg.DeferWakeNow()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	read := o.ReadCard
	if read == nil {
		read = s.Cfg.DeferWakeReadCard
	}
	if read == nil {
		read = os.ReadFile
	}
	write := o.WriteCard
	if write == nil {
		write = s.Cfg.DeferWakeWriteCard
	}
	if write == nil {
		write = func(path string, data []byte) error { return AtomicWrite(path, data, 0644) }
	}

	lock, err := s.acquireLock()
	if err != nil {
		return rep, err
	}
	defer lock.Release()

	cards, err := LoadCards(s.Cfg.TasksDir)
	if err != nil {
		return rep, err
	}
	var entries []DeferWakeLogEntry
	for _, c := range cards {
		until, err := parseDeferUntil(c.Extra[deferUntilField])
		if err != nil || until.IsZero() {
			continue
		}
		rep.Scanned++
		if now.Before(until) {
			rep.Skipped = append(rep.Skipped, c.ID)
			continue
		}

		current, err := read(c.Path)
		if err != nil {
			rep.Failed = append(rep.Failed, c.ID)
			entries = append(entries, DeferWakeLogEntry{
				Time: now, CardID: c.ID, Action: "error", Reason: err.Error(),
			})
			continue
		}
		fresh, err := taskstore.ParseCard(c.Path, current)
		if err != nil {
			rep.Failed = append(rep.Failed, c.ID)
			entries = append(entries, DeferWakeLogEntry{
				Time: now, CardID: c.ID, Action: "error", Reason: err.Error(),
			})
			continue
		}
		freshUntil, err := parseDeferUntil(fresh.Extra[deferUntilField])
		if err != nil || freshUntil.IsZero() || !freshUntil.Equal(until) || fresh.Status != c.Status {
			rep.Skipped = append(rep.Skipped, c.ID)
			continue
		}

		text := rewriteDeferFields(string(current), "todo", "")
		if err := write(c.Path, []byte(text)); err != nil {
			rep.Failed = append(rep.Failed, c.ID)
			entries = append(entries, DeferWakeLogEntry{
				Time: now, CardID: c.ID, Action: "error", Reason: err.Error(),
			})
			continue
		}
		rep.Woken = append(rep.Woken, c.ID)
		entries = append(entries, DeferWakeLogEntry{
			Time: now, CardID: c.ID, Action: "wake",
			Reason: fmt.Sprintf("дата отложки %s истекла", until.Format(time.RFC3339)),
		})
	}

	if len(entries) > 0 {
		s.appendDeferWakeLog(entries)
	}
	return rep, nil
}
