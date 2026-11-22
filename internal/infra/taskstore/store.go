// Package taskstore хранит карточки задач, счётчик ID и файловый замок.
// Пакет работает только со стандартной библиотекой.
package taskstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Коды выхода из ADR 0016. Правило действует для каждой команды beacon:
// код 1 означает ошибку валидации пользовательского ввода, код 2 означает,
// что команда не смогла запуститься или выполнить системную операцию.
// Раннеры возвращают эти константы, а не числа 0, 1 и 2 напрямую.
const (
	ExitOK         = 0
	ExitValidation = 1
	ExitStartup    = 2
)

// ExitError несёт код выхода и причину. Причина печатается в stderr.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// Validationf создаёт ошибку валидации, код 1.
func Validationf(format string, a ...any) error {
	return &ExitError{Code: ExitValidation, Msg: fmt.Sprintf(format, a...)}
}

// Startupf создаёт ошибку запуска, код 2.
func Startupf(format string, a ...any) error {
	return &ExitError{Code: ExitStartup, Msg: fmt.Sprintf(format, a...)}
}

// Code возвращает код выхода для ошибки. Неожидаемая ошибка считается ошибкой запуска.
func Code(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitStartup
}

// Card это разобранная карточка. Raw сохраняет исходный текст для take.
type Card struct {
	ID           string
	Type         string
	Status       string
	Phase        string
	OwnerFiles   []string
	Dependencies []string
	Reference    string
	Agent        string
	Time         string
	// Close metadata. These fields are optional in the card markdown and are
	// written by the atomic close writer in close.go. They stay out of
	// requiredFields so existing cards parse unchanged.
	Reason      string
	DuplicateOf string
	ClosedBy    string
	ClosedAt    string
	Blocker     string
	// Compaction metadata. These fields are optional in the card markdown and
	// are written by CompactCard. They stay out of requiredFields so existing
	// cards parse unchanged. Level 0 or empty means the card is not compacted.
	CompactLevel  string
	CompactedAt   string
	CompactCommit string
	Title         string
	Goal          string
	Context       string
	Steps         string
	Check         string
	Acceptance    string
	Extra         map[string]string
	Path          string
	Raw           string
	Group         int
	Number        int
}

var (
	// Several waves produce valid card ids: T3.226, T5.1, S1.001 and UI-31.
	// Parse the common shapes and keep the exact filename as the card id.
	idRE     = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*?)([0-9]+)[.]([0-9]+)(?:-.*)?$`)
	dashIDRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)-([0-9]+)$`)
	// letterDashIDRE accepts research-family ids where a letter sits between
	// the dash and the number, for example ATX-R01 or ATX-V08. The plain
	// dashIDRE above requires the number directly after the dash, so cards
	// produced by the backlog-research waves need their own form.
	letterDashIDRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)-([A-Za-z]+)([0-9]+)$`)
	// digitDashLetterIDRE accepts split ids where a letter follows the number,
	// for example TUI-07A. letterDashIDRE above puts the letter before the
	// number (ATX-R01), so these cards need their own form.
	digitDashLetterIDRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)-([0-9]+)([A-Za-z]+)$`)
	// dotIDRE accepts an alphabetic prefix followed directly by a dot and a
	// number, for example BS.1. The idRE above requires a digit before the dot
	// (T3.123), so families like BS need their own form.
	dotIDRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)[.]([0-9]+)$`)
	// hashIDRE accepts the adaptive hash form T<g>.<n>.<base36>, for example
	// T3.17.k9x2. Two dots keep it disjoint from idRE and dotIDRE, so every
	// previously valid id still parses through its original branch.
	hashIDRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*?)([0-9]+)[.]([0-9]+)[.]([0-9a-z]+)$`)
	// exactIDRE is the single grammar for IsID and commit-linkage parsing.
	// The last alternative adds the hash form without widening any old one.
	exactIDRE  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(?:-[A-Za-z]+[0-9]+|-[0-9]+[A-Za-z]+|[.-][0-9]+|[.][0-9]+[.][0-9a-z]+)$`)
	titleRE    = regexp.MustCompile(`^#\s+[A-Za-z][A-Za-z0-9_.-]*\.\s*(.*)$`)
	headerRE   = regexp.MustCompile(`^#\s+(.*)$`)
	fieldRE    = regexp.MustCompile(`^([\p{L}][\p{L}\p{N} ]{0,40}):[ \t]*(.*)$`)
	statusLine = regexp.MustCompile(`^(Статус|Агент|Время):[ \t]*(.*?)[ \t]*$`)
)

// requiredFields is the canonical list of the 13 task card fields for this
// package. tools/beacon-gates/docsgate keeps a copy in taskFields. The two
// packages cannot share one list: layers.json has no allowed edge from
// tools/beacon-gates to internal/infra, and the nested tools/beacon-gates go.mod
// blocks a cross-module import. Keep both lists in sync; see ARDH-08.
var requiredFields = []string{
	"Тип", "Статус", "Фаза", "Владелец файлов", "Зависимости", "Ссылка",
	"Цель", "Контекст", "Шаги", "Проверка", "Приёмка", "Агент", "Время",
}

var knownFields = func() map[string]bool {
	m := make(map[string]bool, len(requiredFields))
	for _, f := range requiredFields {
		m[f] = true
	}
	return m
}()

// ParseCard разбирает карточку. ID берётся из имени файла, заголовок из первой строки.
func ParseCard(filePath string, data []byte) (Card, error) {
	var card Card
	name := filepath.Base(filePath)
	id := strings.TrimSuffix(name, ".md")
	m := idRE.FindStringSubmatch(id)
	var group, number int
	var err error
	switch {
	case m != nil:
		group, err = strconv.Atoi(m[2])
		if err != nil {
			return card, Validationf("%s: неверный номер фазы в ID", name)
		}
		number, err = strconv.Atoi(m[3])
	case hashIDRE.MatchString(id):
		h := hashIDRE.FindStringSubmatch(id)
		group, err = strconv.Atoi(h[2])
		if err == nil {
			number, err = strconv.Atoi(h[3])
		}
	case dashIDRE.MatchString(id):
		d := dashIDRE.FindStringSubmatch(id)
		number, err = strconv.Atoi(d[2])
	case letterDashIDRE.MatchString(id):
		d := letterDashIDRE.FindStringSubmatch(id)
		number, err = strconv.Atoi(d[3])
	case digitDashLetterIDRE.MatchString(id):
		d := digitDashLetterIDRE.FindStringSubmatch(id)
		number, err = strconv.Atoi(d[2])
	case dotIDRE.MatchString(id):
		d := dotIDRE.FindStringSubmatch(id)
		number, err = strconv.Atoi(d[2])
	default:
		return card, Validationf("%s: имя файла не содержит поддерживаемый ID карточки", name)
	}
	if err != nil {
		return card, Validationf("%s: неверный номер задачи в ID", name)
	}
	if m != nil {
		card.ID = fmt.Sprintf("%s%s.%s", m[1], m[2], m[3])
	} else {
		card.ID = id
	}
	card.Group, card.Number = group, number
	card.Path = filePath
	card.Raw = string(data)
	card.Extra = make(map[string]string)

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if tm := titleRE.FindStringSubmatch(line); tm != nil {
			card.Title = strings.TrimSpace(tm[1])
			break
		}
		if hm := headerRE.FindStringSubmatch(line); hm != nil && card.Title == "" {
			title := strings.TrimSpace(hm[1])
			title = strings.TrimSpace(strings.TrimPrefix(title, card.ID+"."))
			card.Title = title
		}
	}

	fields := make(map[string]string)
	counts := make(map[string]int)
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		fm := fieldRE.FindStringSubmatch(line)
		if fm == nil {
			continue
		}
		key := fm[1]
		counts[key]++
		fields[key] = strings.TrimSpace(fm[2])
	}
	for _, key := range requiredFields {
		switch {
		case counts[key] == 0:
			return card, Validationf("%s: отсутствует поле %s", name, key)
		case counts[key] > 1:
			return card, Validationf("%s: поле %s повторяется", name, key)
		}
	}

	card.Type = fields["Тип"]
	card.Status = fields["Статус"]
	card.Phase = fields["Фаза"]
	card.Reference = fields["Ссылка"]
	card.Agent = fields["Агент"]
	card.Time = fields["Время"]
	card.Reason = fields["Причина"]
	card.DuplicateOf = fields["Дубликат"]
	card.ClosedBy = fields["Закрыл"]
	card.ClosedAt = fields["Время закрытия"]
	card.Blocker = fields["Блокировщик"]
	card.CompactLevel = fields["Уровень сжатия"]
	card.CompactedAt = fields["Время сжатия"]
	card.CompactCommit = fields["Хеш коммита"]
	card.OwnerFiles = splitList(fields["Владелец файлов"])
	card.Dependencies = splitList(fields["Зависимости"])
	card.Goal = fields["Цель"]
	card.Context = fields["Контекст"]
	card.Steps = fields["Шаги"]
	card.Check = fields["Проверка"]
	card.Acceptance = fields["Приёмка"]

	if !IsValidType(card.Type) {
		return card, Validationf("%s: поле Тип имеет неизвестное значение %q", name, card.Type)
	}
	if !IsValidStatus(card.Status) {
		return card, Validationf("%s: поле Статус имеет неизвестное значение %q", name, card.Status)
	}
	if !IsValidPhase(card.Phase) {
		return card, Validationf("%s: поле Фаза имеет неизвестное значение %q", name, card.Phase)
	}
	for key, value := range fields {
		if !knownFields[key] {
			card.Extra[key] = value
		}
	}
	return card, nil
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// Допустимые значения полей Тип, Статус и Фаза определены один раз и живут
// только здесь. ParseCard проверяет их через IsValidType, IsValidStatus и
// IsValidPhase. Другие подсистемы переиспользуют те же множества через Valid*
// функции, поэтому наборы статусов и фаз не расходятся между парсером, гейтом
// документации и CLI.

var (
	cardTypes    = []string{"task", "epic", "adr", "doc", "eval"}
	cardStatuses = []string{"todo", "in_progress", "blocked", "done", "dropped", "duplicate"}
	cardPhases   = []string{"P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8"}
)

// ValidTypes возвращает все значения поля Тип, которые принимает ParseCard.
// Возвращается копия. Изменение результата не меняет каноническое множество.
func ValidTypes() []string { return append([]string(nil), cardTypes...) }

// ValidStatuses возвращает все значения поля Статус, которые принимает ParseCard.
// Возвращается копия. Изменение результата не меняет каноническое множество.
func ValidStatuses() []string { return append([]string(nil), cardStatuses...) }

// ValidPhases возвращает все значения поля Фаза, которые принимает ParseCard.
// Возвращается копия. Изменение результата не меняет каноническое множество.
func ValidPhases() []string { return append([]string(nil), cardPhases...) }

// IsValidType сообщает, принимает ли ParseCard значение поля Тип.
func IsValidType(v string) bool { return oneOf(v, cardTypes...) }

// IsValidStatus сообщает, принимает ли ParseCard значение поля Статус.
func IsValidStatus(v string) bool { return oneOf(v, cardStatuses...) }

// IsValidPhase сообщает, принимает ли ParseCard значение поля Фаза.
func IsValidPhase(v string) bool { return oneOf(v, cardPhases...) }

// splitList разбирает список через запятую. Значение «нет» даёт пустой список.
func splitList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || v == "нет" {
		return []string{}
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fileAdapter is the counted markdown reader. Card reads go through it so the
// dual-read exit metric sees every access to the legacy source.
var fileAdapter FileAdapter

// LoadCards читает все карточки каталога и сортирует их по ID.
func LoadCards(tasksDir string) ([]Card, error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, Startupf("каталог карточек %s недоступен: %v", tasksDir, err)
	}
	cards := make([]Card, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		filePath := filepath.Join(tasksDir, e.Name())
		data, err := fileAdapter.ReadFile(filePath)
		if err != nil {
			return nil, Startupf("не прочитана карточка %s: %v", e.Name(), err)
		}
		card, err := ParseCard(filePath, data)
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Group != cards[j].Group {
			return cards[i].Group < cards[j].Group
		}
		if cards[i].Number != cards[j].Number {
			return cards[i].Number < cards[j].Number
		}
		return cards[i].ID < cards[j].ID
	})
	return cards, nil
}

// NormalizePath приводит путь к каноническому виду для сравнения владельцев.
func NormalizePath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for {
		next := strings.TrimSuffix(p, "/**")
		if next == p {
			break
		}
		p = next
	}
	for strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

// PathsConflict сообщает, равны ли пути или один ли он начала другого по границе сегмента.
func PathsConflict(a, b string) bool {
	a, b = NormalizePath(a), NormalizePath(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

// IsID проверяет строку вида T<g>.<n>.
func IsID(s string) bool { return exactIDRE.MatchString(s) }

// CardPath возвращает путь файла карточки по ID.
func CardPath(tasksDir, id string) string { return filepath.Join(tasksDir, id+".md") }

// StateDir возвращает каталог состояния из ADR 0011.
func StateDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); v != "" {
		return filepath.Join(v, "beacon"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", Startupf("не найден домашний каталог: %v", err)
	}
	return filepath.Join(home, ".local", "state", "beacon"), nil
}

// Hash8 возвращает первые восемь символов хеша абсолютного пути проекта.
func Hash8(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(sum[:])[:8]
}

// tasksLockName — имя единственного замка каталога карточек. AUD-05 сводит
// три лока (taskstore, claim и close) к одному файлу в docs/tasks; адаптеры в
// internal/infra/mcp повторяют это имя, потому что слои не разрешают импорт
// internal/infra -> internal/infra.
const tasksLockName = ".beacon-tasks.lock"

// TasksLockPath возвращает путь общего замка каталога карточек. Все операции
// с карточками держат этот один файл в своём адаптере.
func TasksLockPath(tasksDir string) string {
	return filepath.Join(tasksDir, tasksLockName)
}

// LockPath возвращает путь того же общего замка. Карточки всегда лежат в
// docs/tasks под корнем проекта, поэтому путь вычисляется из root; stateDir
// сохранён в сигнатуре для совместимости с вызывающими.
func LockPath(stateDir, root string) string {
	return TasksLockPath(filepath.Join(root, "docs", "tasks"))
}

// CounterPath возвращает путь файла счётчика ID.
func CounterPath(stateDir, root string) string {
	return filepath.Join(stateDir, "task-counter-"+Hash8(root)+".json")
}

// Lock это захваченный файловый замок. Владение определяется блокировкой
// syscall.Flock на открытом файле, а не самим фактом существования файла.
type Lock struct {
	Path string
	f    *os.File
}

// AcquireLock захватывает замок через syscall.Flock(LOCK_EX|LOCK_NB). Файл
// служит только точкой встречи: взаимное исключение обеспечивает ядро, поэтому
// содержимое файла больше не читается для решения о перехвате замка. Это
// устраняет гонку, когда владелец создавал файл через O_EXCL, а претендент
// читал его без блокировки и мог удалить чужой замок. До 50 попыток по 100 мс.
func AcquireLock(lockPath, projectRoot string) (*Lock, error) {
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
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return nil, Startupf("не взят замок %s: %v", lockPath, err)
		}
		// Проверка: пока мы ждали блокировку, файл могли удалить и создать
		// заново. Убеждаемся, что держим замок на текущем файле пути, иначе
		// повторяем попытку на новом inode.
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
		// Содержимое пишется под блокировкой одним вызовом: другой владелец
		// не увидит половинчатую запись. Формат сохранён для совместимости.
		content := fmt.Sprintf("pid=%d\nstarted=%s\nroot=%s\n",
			os.Getpid(), time.Now().UTC().Format(time.RFC3339), projectRoot)
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
		return &Lock{Path: lockPath, f: f}, nil
	}
	return nil, Startupf("замок %s занят: исчерпано 50 попыток", lockPath)
}

// Release отпускает замок. Повторное освобождение безопасно.
// Файл удаляется только когда путь всё ещё указывает на inode этого
// дескриптора. Проверка и удаление идут под удержанным Flock: пока лок
// удержан, другой процесс не может взять его на том же inode.
func (l *Lock) Release() error {
	if l == nil || l.Path == "" {
		return nil
	}
	var removeErr error
	if l.f != nil {
		// Сначала сверяем inode, потом снимаем Flock. Иначе между UNLOCK и
		// Remove другой процесс возьмёт замок на старом inode, а Remove
		// разлинкует путь и пустит третьего владельца на новый inode.
		locked, statErr := l.f.Stat()
		if statErr == nil {
			if current, err := os.Stat(l.Path); err == nil && os.SameFile(locked, current) {
				if err := os.Remove(l.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
					removeErr = err
				}
			}
		}
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		_ = l.f.Close()
		l.f = nil
	}
	return removeErr
}

type counterFile struct {
	Groups map[string]int `json:"groups"`
}

// ReadCounter читает счётчик. Отсутствующий файл равен пустому счётчику.
func ReadCounter(counterPath string) (map[string]int, error) {
	data, err := os.ReadFile(counterPath)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]int), nil
	}
	if err != nil {
		return nil, Startupf("не прочитан счётчик %s: %v", counterPath, err)
	}
	var c counterFile
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, Startupf("счётчик %s повреждён: %v", counterPath, err)
	}
	if c.Groups == nil {
		c.Groups = make(map[string]int)
	}
	return c.Groups, nil
}

// WriteCounter пишет счётчик через временный файл и переименование.
// AUD-05 перенёс общий замок в docs/tasks, поэтому каталог состояния больше
// не создаётся как побочный эффект захвата замка: создаём его явно.
func WriteCounter(counterPath string, groups map[string]int) error {
	data, err := json.Marshal(counterFile{Groups: groups})
	if err != nil {
		return Startupf("не собран счётчик: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(counterPath), 0700); err != nil {
		return Startupf("не создан каталог состояния: %v", err)
	}
	return AtomicWrite(counterPath, data, 0600)
}

// NextID считает номер как max(счётчик, максимум по файлам группы) + 1.
func NextID(cards []Card, groups map[string]int, group int) (int, string) {
	key := strconv.Itoa(group)
	max := groups[key]
	for _, c := range cards {
		if c.Group == group && c.Number > max {
			max = c.Number
		}
	}
	n := max + 1
	return n, fmt.Sprintf("T%d.%d", group, n)
}

// Настройки адаптивной длины хеш-ID. Порог определен здесь и нигде больше:
// и формула, и выдача ID читают одно и то же значение.
const (
	DefaultCollisionThreshold = 0.25
	MinIDLength               = 3
	MaxIDLength               = 8
	IDAlphabetBase            = 36
	IDNonceAttempts           = 10
)

// IDConfig задаёт параметры выдачи хеш-ID. Zero value означает настройки по
// умолчанию, поэтому вызывающий не обязан заполнять все поля.
type IDConfig struct {
	CollisionThreshold float64
	MinLength          int
	MaxLength          int
	Base               int
	NonceAttempts      int
}

// DefaultIDConfig возвращает единственный источник значений по умолчанию.
func DefaultIDConfig() IDConfig {
	return IDConfig{
		CollisionThreshold: DefaultCollisionThreshold,
		MinLength:          MinIDLength,
		MaxLength:          MaxIDLength,
		Base:               IDAlphabetBase,
		NonceAttempts:      IDNonceAttempts,
	}
}

func (c IDConfig) normalized() IDConfig {
	d := DefaultIDConfig()
	if c.CollisionThreshold <= 0 || c.CollisionThreshold >= 1 {
		c.CollisionThreshold = d.CollisionThreshold
	}
	if c.MinLength <= 0 {
		c.MinLength = d.MinLength
	}
	if c.MaxLength <= 0 || c.MaxLength < c.MinLength {
		c.MaxLength = d.MaxLength
	}
	if c.Base < 2 {
		c.Base = d.Base
	}
	if c.NonceAttempts <= 0 {
		c.NonceAttempts = d.NonceAttempts
	}
	return c
}

// AdaptiveIDLength выбирает минимальную длину L, при которой вероятность
// коллизии по границе дня рождения для cardCount ID не превышала порог:
// 1 - exp(-N^2/(2*Base^L)) <= CollisionThreshold. Если порог не достигнут к
// максимуму, возвращается максимум.
func AdaptiveIDLength(cardCount int, cfg IDConfig) int {
	cfg = cfg.normalized()
	if cardCount < 0 {
		cardCount = 0
	}
	n := float64(cardCount)
	for L := cfg.MinLength; L <= cfg.MaxLength; L++ {
		total := math.Pow(float64(cfg.Base), float64(L))
		prob := 1.0 - math.Exp(-n*n/(2.0*total))
		if prob <= cfg.CollisionThreshold {
			return L
		}
	}
	return cfg.MaxLength
}

// base36Digest кодирует SHA-256 в нижнем регистре base36.
func base36Digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return new(big.Int).SetBytes(sum[:]).Text(36)
}

// MintID выдаёт уникальный ID для одной карточки пачки. claimed содержит ID,
// уже розданные текущей пачке, но ещё не записанные на диск. Nonce входит в
// хеш, поэтому две карточки с одинаковыми видимыми полями получают разные ID.
func MintID(group, number, cardCount int, seed string, claimed map[string]bool, cfg IDConfig) (string, error) {
	cfg = cfg.normalized()
	if claimed == nil {
		claimed = make(map[string]bool)
	}
	length := AdaptiveIDLength(cardCount, cfg)
	for L := length; L <= cfg.MaxLength; L++ {
		for nonce := 0; nonce < cfg.NonceAttempts; nonce++ {
			digest := base36Digest(strconv.Itoa(group), strconv.Itoa(number), seed, strconv.Itoa(L), strconv.Itoa(nonce))
			if len(digest) < L {
				continue
			}
			id := fmt.Sprintf("T%d.%d.%s", group, number, digest[:L])
			if claimed[id] {
				continue
			}
			claimed[id] = true
			return id, nil
		}
	}
	return "", Validationf("не удалось выдать уникальный ID для группы %d", group)
}

// MintBatch выдаёт ID для пачки карточек с общим claimed. Каждая карточка
// получает свой номер, поэтому одинаковые заголовки не дают одинаковых ID.
func MintBatch(group, cardCount int, seeds []string, cfg IDConfig) ([]string, error) {
	claimed := make(map[string]bool, len(seeds))
	ids := make([]string, 0, len(seeds))
	for i, seed := range seeds {
		id, err := MintID(group, i+1, cardCount, seed, claimed, cfg)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// IDCollision это ID, который встретился в нескольких worktree с разным
// содержимым карточки.
type IDCollision struct {
	ID    string
	Roots []string
}

// CollisionReport это результат замера коллизий последовательных ID между
// ветками. Duplicates считает все ID, встречающиеся более чем в одном корне.
// Collisions оставляет только те, где содержимое карточек различается.
type CollisionReport struct {
	Worktrees  int
	Cards      int
	Duplicates int
	Collisions []IDCollision
}

// MeasureIDCollisions сканирует docs/tasks в каждом корне и считает повторы ID
// между worktree. Формат ID не меняется: T%d.%d остаётся валидным.
//
// Замер от 2026-11-22: 316 worktree с docs/tasks, 369242 записи карточек,
// 1228 уникальных ID, 1226 ID в нескольких worktree, 234 ID с разным
// содержимым. Четыре из них в формате T<g>.<n>: T3.241, T3.284, T3.393, T5.52.
// Новые ID выдаются через MintID с хеш-суффиксом T<g>.<n>.<base36>; старый
// формат T<g>.<n> продолжает читаться через NextID и не ломает exactIDRE.
func MeasureIDCollisions(roots ...string) (CollisionReport, error) {
	report := CollisionReport{}
	seenRoots := make(map[string]bool)
	owners := make(map[string]map[string]string)
	for _, root := range roots {
		tasksDir := filepath.Join(root, "docs", "tasks")
		entries, err := os.ReadDir(tasksDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return report, Startupf("не прочитан каталог карточек %s: %v", tasksDir, err)
		}
		if !seenRoots[root] {
			seenRoots[root] = true
			report.Worktrees++
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".md")
			if !IsID(id) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
			if err != nil {
				return report, Startupf("не прочитана карточка %s: %v", entry.Name(), err)
			}
			sum := sha256.Sum256(data)
			if owners[id] == nil {
				owners[id] = make(map[string]string)
			}
			owners[id][root] = hex.EncodeToString(sum[:])
			report.Cards++
		}
	}
	for id, byRoot := range owners {
		if len(byRoot) < 2 {
			continue
		}
		report.Duplicates++
		hashes := make(map[string]bool, len(byRoot))
		for _, h := range byRoot {
			hashes[h] = true
		}
		if len(hashes) < 2 {
			continue
		}
		collisionRoots := make([]string, 0, len(byRoot))
		for root := range byRoot {
			collisionRoots = append(collisionRoots, root)
		}
		sort.Strings(collisionRoots)
		report.Collisions = append(report.Collisions, IDCollision{ID: id, Roots: collisionRoots})
	}
	sort.Slice(report.Collisions, func(i, j int) bool {
		return report.Collisions[i].ID < report.Collisions[j].ID
	})
	return report, nil
}

// IDMapping это одна замена ID при слиянии деревьев. Карточка с OldID в корне
// Root получила NewID, и все ссылки в этом корне переписаны на NewID.
type IDMapping struct {
	OldID string
	NewID string
	Root  string
}

// MergeReport это результат слияния деревьев карточек. Conflicts считает ID,
// которые встретились больше чем в одном корне. Mappings содержит по одной
// записи на каждую переименованную карточку. DanglingRefs считает ссылки на
// несуществующие ID после перезаписи и должен быть нулевым.
type MergeReport struct {
	Roots        int
	Cards        int
	Conflicts    int
	Mappings     []IDMapping
	DanglingRefs int
	DryRun       bool
}

// mergeLocated это карточка вместе с её корнем и путём файла.
type mergeLocated struct {
	root string
	path string
	card Card
	data string
}

// idRoot возвращает семейство ID, стабильную часть номера. Примеры: T3.17 и
// T3.17.k9x2 дают T3, ATX-04 даёт ATX. Корни нужны, чтобы разбирать конфликты
// по семействам в устойчивом порядке.
func idRoot(id string) string {
	if m := hashIDRE.FindStringSubmatch(id); m != nil {
		return m[1] + m[2]
	}
	if m := idRE.FindStringSubmatch(id); m != nil {
		return m[1] + m[2]
	}
	if m := dashIDRE.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	if m := letterDashIDRE.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	if m := digitDashLetterIDRE.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	if m := dotIDRE.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	return id
}

func isIDTokenChar(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' || c == '_' || c == '-' || c == '.'
}

// idEndBoundary проверяет, что ID заканчивается перед позицией end. Точка
// после ID считается знаком препинания, если за ней нет продолжения ID.
// Так T3.17 в заголовке "# T3.17. Заголовок" заменяется, а префикс T3.17
// в T3.17.k9x2 не трогается.
func idEndBoundary(s string, end int) bool {
	if end >= len(s) {
		return true
	}
	if s[end] == '.' {
		if end+1 >= len(s) {
			return true
		}
		return !isIDTokenChar(s[end+1])
	}
	return !isIDTokenChar(s[end])
}

// replaceIDToken заменяет oldID только целым токеном. Части длинных ID не
// меняются, поэтому повторное слияние не удваивает хеш-суффикс.
func replaceIDToken(text, oldID, newID string) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], oldID) &&
			(i == 0 || !isIDTokenChar(text[i-1])) &&
			idEndBoundary(text, i+len(oldID)) {
			b.WriteString(newID)
			i += len(oldID)
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// countDanglingIDRefs считает ID-токены, для которых нет карточки в present.
// Токены, не проходящие IsID, игнорируются.
func countDanglingIDRefs(contents []string, present map[string]bool) int {
	total := 0
	for _, content := range contents {
		for i := 0; i < len(content); {
			if !isIDTokenChar(content[i]) {
				i++
				continue
			}
			j := i
			for j < len(content) && isIDTokenChar(content[j]) {
				j++
			}
			tok := strings.TrimRight(content[i:j], ".")
			if IsID(tok) && !present[tok] {
				total++
			}
			i = j
		}
	}
	return total
}

// MergeTrees объединяет деревья карточек из каталогов root/docs/tasks.
// Первый проход закрепляет желаемый ID за первым по порядку корнем. Второй
// проход выдаёт остальным карточкам новый ID через MintID с nonce и заносит
// замену в отчёт. Ссылки в корне переименованной карточки переписываются на
// новый ID. В режиме dryRun функция ничего не пишет на диск и только считает
// отчёт. Повторное слияние тех же деревьев не находит конфликтов и не меняет
// файлы.
func MergeTrees(roots []string, cfg IDConfig, dryRun bool) (MergeReport, error) {
	report := MergeReport{DryRun: dryRun}
	ordered := append([]string(nil), roots...)
	sort.Strings(ordered)
	report.Roots = len(ordered)

	byID := make(map[string][]mergeLocated)
	families := make(map[string]bool)
	claimed := make(map[string]bool)
	for _, root := range ordered {
		tasksDir := filepath.Join(root, "docs", "tasks")
		entries, err := os.ReadDir(tasksDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return report, Startupf("не прочитан каталог карточек %s: %v", tasksDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			filePath := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(filePath)
			if err != nil {
				return report, Startupf("не прочитана карточка %s: %v", filePath, err)
			}
			card, err := ParseCard(filePath, data)
			if err != nil {
				continue
			}
			loc := mergeLocated{root: root, path: filePath, card: card, data: string(data)}
			byID[card.ID] = append(byID[card.ID], loc)
			families[idRoot(card.ID)] = true
			claimed[card.ID] = true
			report.Cards++
		}
	}

	// Резервируем желаемый ID за первым корнем. Остальным карточкам того же
	// ID выдаём новый ID. Общий claimed не даёт двум заменам совпасть.
	renameByRoot := make(map[string]map[string]string)
	familyNames := make([]string, 0, len(families))
	for family := range families {
		familyNames = append(familyNames, family)
	}
	sort.Strings(familyNames)
	for _, family := range familyNames {
		for id, locs := range byID {
			if idRoot(id) != family || len(locs) < 2 {
				continue
			}
			sort.Slice(locs, func(i, j int) bool { return locs[i].root < locs[j].root })
			winner := locs[0].root
			hasLoser := false
			for _, loc := range locs {
				if loc.root != winner {
					hasLoser = true
					break
				}
			}
			if !hasLoser {
				continue
			}
			report.Conflicts++
			for _, loc := range locs {
				if loc.root == winner {
					continue
				}
				seed := base36Digest(loc.root, id, loc.data)
				newID, err := MintID(loc.card.Group, loc.card.Number, report.Cards, seed, claimed, cfg)
				if err != nil {
					return report, err
				}
				if renameByRoot[loc.root] == nil {
					renameByRoot[loc.root] = make(map[string]string)
				}
				renameByRoot[loc.root][id] = newID
				report.Mappings = append(report.Mappings, IDMapping{OldID: id, NewID: newID, Root: loc.root})
			}
		}
	}
	sort.Slice(report.Mappings, func(i, j int) bool {
		if report.Mappings[i].Root != report.Mappings[j].Root {
			return report.Mappings[i].Root < report.Mappings[j].Root
		}
		return report.Mappings[i].OldID < report.Mappings[j].OldID
	})

	// Итоговый набор ID по корням: проигравшие старые ID уходят, новые
	// появляются в том же корне. По этим наборам считаем висячие ссылки.
	presentByRoot := make(map[string]map[string]bool)
	for id, locs := range byID {
		for _, loc := range locs {
			if presentByRoot[loc.root] == nil {
				presentByRoot[loc.root] = make(map[string]bool)
			}
			presentByRoot[loc.root][id] = true
		}
	}
	for root, renames := range renameByRoot {
		for oldID, newID := range renames {
			delete(presentByRoot[root], oldID)
			presentByRoot[root][newID] = true
		}
	}

	finalByRoot := make(map[string][]string)
	for id, locs := range byID {
		for _, loc := range locs {
			content := loc.data
			newName := id
			if renames := renameByRoot[loc.root]; renames != nil {
				if newID, ok := renames[id]; ok {
					newName = newID
				}
				for oldID, newID := range renames {
					content = replaceIDToken(content, oldID, newID)
				}
			}
			finalByRoot[loc.root] = append(finalByRoot[loc.root], content)
			if dryRun {
				continue
			}
			if newName == id {
				if content == loc.data {
					continue
				}
				if err := AtomicWrite(loc.path, []byte(content), 0644); err != nil {
					return report, err
				}
				continue
			}
			newPath := filepath.Join(loc.root, "docs", "tasks", newName+".md")
			if err := AtomicWrite(newPath, []byte(content), 0644); err != nil {
				return report, err
			}
			if err := os.Remove(loc.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return report, Startupf("не удалён старый файл %s: %v", loc.path, err)
			}
		}
	}
	for root, contents := range finalByRoot {
		report.DanglingRefs += countDanglingIDRefs(contents, presentByRoot[root])
	}
	return report, nil
}

// CreateExclusive создаёт файл карточки. Занятый ID даёт ошибку валидации, код 1.
func CreateExclusive(filePath string, data []byte) error {
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if errors.Is(err, os.ErrExist) {
		return Validationf("ID занят: файл %s уже существует", filepath.Base(filePath))
	}
	if err != nil {
		return Startupf("не создан файл %s: %v", filePath, err)
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	_ = f.Close()
	if werr != nil {
		_ = os.Remove(filePath)
		return Startupf("не записан файл %s: %v", filePath, err)
	}
	return SyncDir(filepath.Dir(filePath))
}

// AtomicWrite пишет данные во временный файл и заменяет целевой переименованием.
func AtomicWrite(filePath string, data []byte, perm os.FileMode) error {
	return atomicWrite(filePath, data, perm, nil)
}

// atomicWrite публикует данные одним переименованием. beforePublish вызывается
// после того, как временный файл записан и синхронизирован, прямо перед rename.
// Ошибка в нём прерывает транзакцию: временный файл удаляется, а filePath
// остаётся в прежнем состоянии. Через этот хук тест имитирует прерывание и
// проверяет, что не публикуется половинчатая запись.
func atomicWrite(filePath string, data []byte, perm os.FileMode, beforePublish func() error) error {
	dir := filepath.Dir(filePath)
	f, err := os.CreateTemp(dir, ".beacon-tmp-")
	if err != nil {
		return Startupf("не создан временный файл в %s: %v", dir, err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return Startupf("не записан временный файл: %v", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return Startupf("не синхронизирован временный файл: %v", err)
	}
	if err = f.Close(); err != nil {
		return Startupf("не закрыт временный файл: %v", err)
	}
	if err = os.Chmod(tmp, perm); err != nil {
		return Startupf("не заданы права временного файла: %v", err)
	}
	if beforePublish != nil {
		if err := beforePublish(); err != nil {
			return Startupf("транзакция прервана: %v", err)
		}
	}
	if err = os.Rename(tmp, filePath); err != nil {
		return Startupf("не заменён файл %s: %v", filePath, err)
	}
	ok = true
	return SyncDir(dir)
}

// SyncDir синхронизирует каталог после переименования.
func SyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return Startupf("не открыт каталог %s: %v", dir, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return Startupf("не синхронизирован каталог %s: %v", dir, err)
	}
	return nil
}

// TaskEvent — одно событие задачи из ADR 0030 п.4. Событие хранится в том же
// файле, что и карточка, и публикуется вместе с ней одной транзакцией.
type TaskEvent struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	TaskID string    `json:"taskID"`
	At     time.Time `json:"at"`
}

// Границы блока событий внутри файла карточки. ParseCard игнорирует эти
// строки: они не начинаются с буквы и не выглядят как поле «Ключ: значение».
const (
	eventBlockBegin = "\n<!-- beacon-task-events v1\n"
	eventBlockEnd   = "<!-- end-beacon-task-events -->\n"
)

// TaskEventTx — одна транзакция: карточка задачи и событие, которые станут
// видимы только вместе. ADR 0030 п.4 требует держать очередь, события и данные
// задачи в одном месте и писать их одной транзакцией. Файловое хранилище даёт
// ту же гарантию одним atomicWrite: читатель видит либо прежнее полное
// состояние, либо новое, но никогда карточку без события или событие без
// карточки.
type TaskEventTx struct {
	Path  string
	Card  []byte
	Event TaskEvent
}

// Write проверяет транзакцию и публикует карточку вместе с событием.
// beforePublish вызывается после подготовки временного файла перед rename;
// ошибка прерывает транзакцию и оставляет Path неизменным. Событие с уже
// записанным ID не добавляется второй раз, что даёт идемпотентность по ID.
func (tx TaskEventTx) Write(beforePublish func() error) error {
	if tx.Path == "" {
		return Validationf("путь карточки не задан")
	}
	card := stripEventBlock(tx.Card)
	parsed, err := ParseCard(filepath.Base(tx.Path), card)
	if err != nil {
		return err
	}
	if tx.Event.ID == "" || tx.Event.Kind == "" {
		return Validationf("событие должно иметь ID и вид")
	}
	if tx.Event.TaskID != parsed.ID {
		return Validationf("событие %s относится к задаче %s, а карточка имеет ID %s",
			tx.Event.ID, tx.Event.TaskID, parsed.ID)
	}
	if tx.Event.At.IsZero() {
		return Validationf("событие %s не имеет времени", tx.Event.ID)
	}

	events, err := ReadTaskEvents(tx.Path)
	if err != nil {
		return err
	}
	for _, existing := range events {
		if existing.ID == tx.Event.ID {
			return nil
		}
	}
	events = append(events, tx.Event)

	block, err := encodeEventBlock(events)
	if err != nil {
		return err
	}
	return atomicWrite(tx.Path, append(card, block...), 0644, beforePublish)
}

// ReadTaskEvents читает журнал событий из файла карточки. Отсутствующий файл
// равен пустому журналу. События и карточка читаются из одного файла, поэтому
// нельзя получить событие без карточки.
func ReadTaskEvents(path string) ([]TaskEvent, error) {
	data, err := fileAdapter.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []TaskEvent{}, nil
	}
	if err != nil {
		return nil, Startupf("не прочитана карточка %s: %v", path, err)
	}
	s := string(data)
	i := strings.Index(s, eventBlockBegin)
	if i < 0 {
		return []TaskEvent{}, nil
	}
	rest := s[i+len(eventBlockBegin):]
	j := strings.Index(rest, eventBlockEnd)
	if j < 0 {
		return nil, Startupf("карточка %s: незакрытый блок событий", path)
	}
	body := strings.TrimSpace(rest[:j])
	if body == "" {
		return []TaskEvent{}, nil
	}
	events := make([]TaskEvent, 0, 2)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev TaskEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, Startupf("карточка %s: повреждено событие: %v", path, err)
		}
		events = append(events, ev)
	}
	return events, nil
}

// encodeEventBlock собирает блок событий, который дописывается к тексту карточки.
func encodeEventBlock(events []TaskEvent) ([]byte, error) {
	var b strings.Builder
	b.WriteString(eventBlockBegin)
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			return nil, Startupf("не собрано событие: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteString(eventBlockEnd)
	return []byte(b.String()), nil
}

// stripEventBlock убирает блок событий из текста карточки, чтобы повторная
// запись не дублировала его. Возвращается отдельный срез, независимый от data.
func stripEventBlock(data []byte) []byte {
	s := string(data)
	i := strings.Index(s, eventBlockBegin)
	if i < 0 {
		return append([]byte(nil), data...)
	}
	j := strings.Index(s[i:], eventBlockEnd)
	if j < 0 {
		return append([]byte(nil), data...)
	}
	end := i + j + len(eventBlockEnd)
	out := s[:i] + s[end:]
	return []byte(out)
}

// ReplaceFields меняет три строки карточки при взятии задачи.
func ReplaceFields(raw, agent, when string) string {
	cr := ""
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			line = strings.TrimSuffix(line, "\r")
		} else {
			cr = ""
		}
		fm := statusLine.FindStringSubmatch(line)
		if fm == nil {
			continue
		}
		switch {
		case fm[1] == "Статус" && fm[2] == "todo":
			lines[i] = "Статус: in_progress" + cr
		case fm[1] == "Агент" && fm[2] == "нет":
			lines[i] = "Агент: " + agent + cr
		case fm[1] == "Время" && fm[2] == "нет":
			lines[i] = "Время: " + when + cr
		}
	}
	return strings.Join(lines, "\n")
}

// ---- HD-022 / ADR 0295: связь feat/fix с milestone ----
//
// Для Beacon milestone это ссылка на ADR и на карточку. Коммит feat или fix
// обязан указать обе ссылки либо обоснование исключения. Проверка чистая:
// она работает с переданными данными и портами. Чтение файлов и сетевые
// вызовы остаются за пределами проверки.

// Linkage содержит данные коммита, нужные для проверки связи. Subject
// отделяет проверенный коммит от пустого состояния: пустой Subject и пустые
// остальные поля дают LinkageEmpty.
type Linkage struct {
	Subject       string
	ADRRef        string
	CardRef       string
	Justification string
}

// LinkageVerdict это исход проверки связи с milestone.
type LinkageVerdict string

const (
	// LinkageLinked: ссылка на ADR и карточку есть.
	LinkageLinked LinkageVerdict = "linked"
	// LinkageExempted: ссылки нет, но есть обоснование исключения.
	LinkageExempted LinkageVerdict = "exempted"
	// LinkageMissing: нет ни ссылки, ни обоснования. Такой коммит даёт warning.
	LinkageMissing LinkageVerdict = "missing"
	// LinkageEmpty: данные для проверки не переданы.
	LinkageEmpty LinkageVerdict = "empty"
)

// LinkagePorts это порты проверки связи. Оба поля это чистые предикаты,
// которые решают, считается ли ссылка действительной. Вызывающий подставляет
// порты, а проверка не делает I/O. Nil-порт принимает любую непустую ссылку.
type LinkagePorts struct {
	ValidADR  func(ref string) bool
	ValidCard func(ref string) bool
}

// CheckLinkage проверяет связь коммита с milestone. Пустое состояние даёт
// LinkageEmpty. Ссылка на ADR и карточку даёт LinkageLinked. Когда ссылки нет,
// но есть обоснование, исход LinkageExempted. Иначе LinkageMissing.
func CheckLinkage(l Linkage, ports LinkagePorts) LinkageVerdict {
	if strings.TrimSpace(l.Subject) == "" &&
		strings.TrimSpace(l.ADRRef) == "" &&
		strings.TrimSpace(l.CardRef) == "" &&
		strings.TrimSpace(l.Justification) == "" {
		return LinkageEmpty
	}
	adr := strings.TrimSpace(l.ADRRef)
	card := strings.TrimSpace(l.CardRef)
	adrOK := adr != "" && (ports.ValidADR == nil || ports.ValidADR(adr))
	cardOK := card != "" && (ports.ValidCard == nil || ports.ValidCard(card))
	if adrOK && cardOK {
		return LinkageLinked
	}
	if strings.TrimSpace(l.Justification) != "" {
		return LinkageExempted
	}
	return LinkageMissing
}

var (
	linkageADRRE = regexp.MustCompile(`(?i)\bADR[\s-]*([0-9]{4})\b`)
	// ParseLinkage reads card refs from commit text. Use the exactIDRE grammar
	// so the parser accepts the same ID shapes as card files, including
	// ATX-R01 and RS-CMP-06. Word boundaries keep refs apart from other text.
	linkageCardRE = regexp.MustCompile(`\b` + strings.TrimSuffix(strings.TrimPrefix(exactIDRE.String(), "^"), "$") + `\b`)
	linkageJustRE = regexp.MustCompile(`(?im)^[ \t]*(Обоснование|Исключение|Justification|Exception)[ \t]*:[ \t]*(.+)$`)
	commitTypeRE  = regexp.MustCompile(`^([a-z]+)(?:\([^)]*\))?[!]?:`)
)

// ParseLinkage разбирает текст коммита на поля связи. Subject это первая
// строка сообщения. ADR и карточка берутся из первых совпадений, обоснование
// из строки с соответствующим заголовком. Функция чистая и не делает I/O.
func ParseLinkage(message string) Linkage {
	normalized := strings.ReplaceAll(message, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	l := Linkage{Subject: strings.TrimSpace(lines[0])}
	if m := linkageADRRE.FindStringSubmatch(normalized); m != nil {
		l.ADRRef = "ADR " + m[1]
	}
	for _, m := range linkageCardRE.FindAllString(normalized, -1) {
		if strings.HasPrefix(strings.ToUpper(m), "ADR") {
			continue
		}
		l.CardRef = m
		break
	}
	if m := linkageJustRE.FindStringSubmatch(normalized); m != nil {
		l.Justification = strings.TrimSpace(m[2])
	}
	return l
}

// RequiresLinkage сообщает, требует ли subject коммита связи с milestone.
// Требование действует только для типов feat и fix.
func RequiresLinkage(subject string) bool {
	m := commitTypeRE.FindStringSubmatch(strings.TrimSpace(subject))
	return m != nil && (m[1] == "feat" || m[1] == "fix")
}

// CB-05. Обратимое сжатие закрытых карточек. Снапшот исходного текста пишется
// до публикации нового текста карточки, поэтому при любой ошибке записи
// карточка остаётся прежней, а restore возвращает побайтово исходный текст.

// Уровни сжатия. Уровень 2 требует уже выполненного уровня 1.
const (
	CompactLevelNone = "0"
	CompactLevel1    = "1"
	CompactLevel2    = "2"
)

// DefaultCompactMinAgeDays это порог возраста закрытой карточки по умолчанию.
const DefaultCompactMinAgeDays = 30

// compactMarker подставляется в поля, которые не несут саммари. Одно поле
// несёт весь текст сжатия, остальные три помечаются этим коротким маркером.
const compactMarker = "-"

// CompactableFields это четыре текстовых поля, размер которых сравнивается при
// сжатии. Заголовок, ID, статус, зависимости и метаданные не изменяются.
var CompactableFields = []string{"Контекст", "Шаги", "Проверка", "Приёмка"}

var compactFieldOrder = []string{
	"Уровень сжатия", "Время сжатия", "Хеш коммита",
}

// CompactOptions задаёт параметры операции сжатия. Now и MinAgeDays нужны
// тестам: нулевое Now значит текущее время, неположительное MinAgeDays равно
// порог по умолчанию.
type CompactOptions struct {
	Level      string
	Summary    string
	CommitHash string
	Now        time.Time
	MinAgeDays int
}

// CompactResult считает размер исходных четырёх полей и размер после сжатия.
type CompactResult struct {
	OriginalBytes int
	CompactBytes  int
	SnapshotPath  string
}

// CardSnapshot хранит исходный текст карточки до перезаписи.
type CardSnapshot struct {
	ID        string `json:"id"`
	Level     string `json:"level"`
	Raw       string `json:"raw"`
	CreatedAt string `json:"created_at"`
	Commit    string `json:"commit,omitempty"`
}

// CompactRefusalComment даёт текст отказа, когда сжатие не уменьшает размер.
func CompactRefusalComment(originalBytes, compactBytes int) string {
	return fmt.Sprintf("сжатие отклонено: новый текст %d байт не короче исходного %d байт", compactBytes, originalBytes)
}

// CheckCompactEligibility проверяет, что карточку можно сжать. Требования:
// закрытый статус, заполненное Время закрытия, порог по дням и нужный уровень.
func CheckCompactEligibility(card Card, o CompactOptions) error {
	if !IsCloseStatus(card.Status) {
		return Validationf("beacon task compact: карточка %s не закрыта (Статус: %s)", card.ID, card.Status)
	}
	closedRaw := strings.TrimSpace(card.ClosedAt)
	if closedRaw == "" {
		return Validationf("beacon task compact: у карточки %s нет поля Время закрытия", card.ID)
	}
	closed, err := time.Parse(time.RFC3339, closedRaw)
	if err != nil {
		return Validationf("beacon task compact: неверное Время закрытия у %s: %v", card.ID, err)
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	minDays := o.MinAgeDays
	if minDays <= 0 {
		minDays = DefaultCompactMinAgeDays
	}
	if now.Sub(closed) < time.Duration(minDays)*24*time.Hour {
		return Validationf("beacon task compact: карточка %s закрыта менее %d дней назад", card.ID, minDays)
	}
	switch o.Level {
	case CompactLevel1:
	case CompactLevel2:
		if card.CompactLevel != CompactLevel1 {
			return Validationf("beacon task compact: уровень 2 требует сначала уровень 1 (текущий %q)", card.CompactLevel)
		}
	default:
		return Validationf("beacon task compact: неверный уровень сжатия %q", o.Level)
	}
	return nil
}

// extractFields собирает значения полей верхнего уровня без I/O.
func extractFields(raw string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		fm := fieldRE.FindStringSubmatch(line)
		if fm != nil {
			fields[fm[1]] = strings.TrimSpace(fm[2])
		}
	}
	return fields
}

// setManagedFields заменяет значения управляемых полей на месте. Строки с
// пустым значением удаляются, отсутствующие поля вставляются перед первым
// разделом «## ». Порядок строк и переводы строк вне полей сохраняются.
func setManagedFields(raw string, order []string, values map[string]string) string {
	cr := ""
	if strings.Contains(raw, "\r\n") {
		cr = "\r"
	}
	lines := strings.Split(raw, "\n")
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(lines)+len(order))
	insertAt := -1
	for _, line := range lines {
		if insertAt < 0 && strings.HasPrefix(line, "## ") {
			insertAt = len(out)
		}
		lineCR := ""
		body := line
		if strings.HasSuffix(body, "\r") {
			lineCR = "\r"
			body = strings.TrimSuffix(body, "\r")
		}
		colon := strings.IndexByte(body, ':')
		if colon < 0 {
			out = append(out, line)
			continue
		}
		key := strings.TrimSpace(body[:colon])
		value, managed := values[key]
		if !managed {
			out = append(out, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if value == "" {
			continue
		}
		out = append(out, key+": "+value+lineCR)
	}
	missing := make([]string, 0, len(order))
	for _, key := range order {
		if !seen[key] && values[key] != "" {
			missing = append(missing, key+": "+values[key]+cr)
		}
	}
	if len(missing) > 0 {
		if insertAt < 0 {
			insertAt = len(out)
		}
		tail := append([]string(nil), out[insertAt:]...)
		out = append(out[:insertAt:insertAt], missing...)
		out = append(out, tail...)
	}
	return strings.Join(out, "\n")
}

// ApplyCompact строит новый текст карточки без I/O. Отказ из-за размера
// возвращает исходный текст и ошибку: вызывающий не публикует изменение.
func ApplyCompact(raw string, o CompactOptions) (string, CompactResult, error) {
	res := CompactResult{}
	fields := extractFields(raw)
	for _, key := range CompactableFields {
		res.OriginalBytes += len(fields[key])
	}
	summary := strings.TrimSpace(o.Summary)
	if summary == "" {
		return "", res, Validationf("beacon task compact: нужно непустое саммари (--summary)")
	}
	res.CompactBytes = len(summary)
	for i := 1; i < len(CompactableFields); i++ {
		res.CompactBytes += len(compactMarker)
	}
	if res.CompactBytes >= res.OriginalBytes {
		return "", res, Validationf("beacon task compact: %s", CompactRefusalComment(res.OriginalBytes, res.CompactBytes))
	}
	when := o.Now
	if when.IsZero() {
		when = time.Now().UTC()
	}
	values := make(map[string]string, len(CompactableFields)+len(compactFieldOrder))
	for i, key := range CompactableFields {
		if i == 0 {
			values[key] = summary
		} else {
			values[key] = compactMarker
		}
	}
	values["Уровень сжатия"] = o.Level
	values["Время сжатия"] = when.UTC().Format(time.RFC3339)
	values["Хеш коммита"] = strings.TrimSpace(o.CommitHash)
	newRaw := setManagedFields(raw, compactFieldOrder, values)
	return newRaw, res, nil
}

const compactSnapshotDir = ".beacon-snapshots"

// SnapshotPath возвращает путь снапшота карточки для заданного уровня.
func SnapshotPath(tasksDir, id, level string) string {
	return filepath.Join(tasksDir, compactSnapshotDir, id+"."+level+".json")
}

func writeSnapshotData(path string, snap CardSnapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return Startupf("не кодирован снапшот: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Startupf("не создан каталог снапшотов: %v", err)
	}
	return atomicWrite(path, append(data, '\n'), 0600, nil)
}

// ReadSnapshot читает исходный текст карточки из снапшота уровня.
func ReadSnapshot(tasksDir, id, level string) (string, error) {
	path := SnapshotPath(tasksDir, id, level)
	data, err := fileAdapter.ReadFile(path)
	if err != nil {
		return "", Startupf("не прочитан снапшот %s: %v", path, err)
	}
	var snap CardSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return "", Startupf("неверный снапшот %s: %v", path, err)
	}
	if snap.Raw == "" {
		return "", Validationf("пустой снапшот %s", path)
	}
	return snap.Raw, nil
}

// CompactCard сжимает карточку. Снапшот пишется хуком beforePublish прямо
// перед переименованием, поэтому ошибка архивации оставляет карточку нетронутой.
func CompactCard(lockPath, projectRoot, cardPath string, o CompactOptions) (CompactResult, error) {
	lock, err := AcquireLock(lockPath, projectRoot)
	if err != nil {
		return CompactResult{}, err
	}
	defer lock.Release()

	data, err := fileAdapter.ReadFile(cardPath)
	if err != nil {
		return CompactResult{}, Startupf("не прочитана карточка %s: %v", cardPath, err)
	}
	card, err := ParseCard(cardPath, data)
	if err != nil {
		return CompactResult{}, err
	}
	if err := CheckCompactEligibility(card, o); err != nil {
		return CompactResult{}, err
	}
	newText, res, err := ApplyCompact(string(data), o)
	if err != nil {
		return res, err
	}
	when := o.Now
	if when.IsZero() {
		when = time.Now().UTC()
	}
	snap := CardSnapshot{
		ID: card.ID, Level: o.Level, Raw: string(data),
		CreatedAt: when.UTC().Format(time.RFC3339), Commit: strings.TrimSpace(o.CommitHash),
	}
	snapPath := SnapshotPath(filepath.Dir(cardPath), card.ID, o.Level)
	if err := atomicWrite(cardPath, []byte(newText), 0644, func() error {
		return writeSnapshotData(snapPath, snap)
	}); err != nil {
		return res, err
	}
	res.SnapshotPath = snapPath
	return res, nil
}

// RestoreCard возвращает карточку текст из снапшота. Пустой level берёт уровень
// из самой карточки. Возвращается использованный уровень.
func RestoreCard(lockPath, projectRoot, cardPath, level string) (string, error) {
	lock, err := AcquireLock(lockPath, projectRoot)
	if err != nil {
		return "", err
	}
	defer lock.Release()

	data, err := fileAdapter.ReadFile(cardPath)
	if err != nil {
		return "", Startupf("не прочитана карточка %s: %v", cardPath, err)
	}
	card, err := ParseCard(cardPath, data)
	if err != nil {
		return "", err
	}
	use := strings.TrimSpace(level)
	if use == "" {
		use = card.CompactLevel
	}
	if use == "" || use == CompactLevelNone {
		return "", Validationf("beacon task restore: карточка %s не сжата", card.ID)
	}
	raw, err := ReadSnapshot(filepath.Dir(cardPath), card.ID, use)
	if err != nil {
		return "", err
	}
	if err := AtomicWrite(cardPath, []byte(raw), 0644); err != nil {
		return "", err
	}
	return use, nil
}
