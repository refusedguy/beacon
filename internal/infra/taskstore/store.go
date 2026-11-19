// Package taskstore хранит карточки задач, счётчик ID и файловый замок.
// Пакет работает только со стандартной библиотекой.
package taskstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// Коды выхода из ADR 0016.
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
	Title        string
	Goal         string
	Context      string
	Steps        string
	Check        string
	Acceptance   string
	Extra        map[string]string
	Path         string
	Raw          string
	Group        int
	Number       int
}

var (
	idRE       = regexp.MustCompile(`^T([0-9]+)\.([0-9]+)`)
	exactIDRE  = regexp.MustCompile(`^T[0-9]+\.[0-9]+$`)
	titleRE    = regexp.MustCompile(`^#\s+T[0-9]+\.[0-9]+\.\s*(.*)$`)
	headerRE   = regexp.MustCompile(`^#\s+(.*)$`)
	fieldRE    = regexp.MustCompile(`^([\p{L}][\p{L}\p{N} ]{0,40}):[ \t]*(.*)$`)
	statusLine = regexp.MustCompile(`^(Статус|Агент|Время):[ \t]*(.*?)[ \t]*$`)
)

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
	m := idRE.FindStringSubmatch(name)
	if m == nil {
		return card, Validationf("%s: имя файла не содержит ID вида T<g>.<n>", name)
	}
	group, err := strconv.Atoi(m[1])
	if err != nil {
		return card, Validationf("%s: неверный номер фазы в ID", name)
	}
	number, err := strconv.Atoi(m[2])
	if err != nil {
		return card, Validationf("%s: неверный номер задачи в ID", name)
	}
	card.ID = fmt.Sprintf("T%d.%d", group, number)
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
	card.OwnerFiles = splitList(fields["Владелец файлов"])
	card.Dependencies = splitList(fields["Зависимости"])
	card.Goal = fields["Цель"]
	card.Context = fields["Контекст"]
	card.Steps = fields["Шаги"]
	card.Check = fields["Проверка"]
	card.Acceptance = fields["Приёмка"]

	if !oneOf(card.Type, "task", "epic") {
		return card, Validationf("%s: поле Тип имеет неизвестное значение %q", name, card.Type)
	}
	if !oneOf(card.Status, "todo", "in_progress", "done", "dropped") {
		return card, Validationf("%s: поле Статус имеет неизвестное значение %q", name, card.Status)
	}
	if !oneOf(card.Phase, "P0", "P1", "P2", "P3", "P4") {
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
		data, err := os.ReadFile(filePath)
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

// LockPath возвращает путь файлового замка.
func LockPath(stateDir, root string) string {
	return filepath.Join(stateDir, "task-"+Hash8(root)+".lock")
}

// CounterPath возвращает путь файла счётчика ID.
func CounterPath(stateDir, root string) string {
	return filepath.Join(stateDir, "task-counter-"+Hash8(root)+".json")
}

// Lock это захваченный файловый замок.
type Lock struct {
	Path string
}

// AcquireLock захватывает замок через O_CREATE|O_EXCL. До 50 попыток по 100 мс.
func AcquireLock(lockPath, projectRoot string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, Startupf("не создан каталог состояния: %v", err)
	}
	emptyReads := 0
	for attempt := 0; attempt < 50; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			content := fmt.Sprintf("pid=%d\nstarted=%s\nroot=%s\n",
				os.Getpid(), time.Now().UTC().Format(time.RFC3339), projectRoot)
			_, werr := f.WriteString(content)
			if werr == nil {
				werr = f.Sync()
			}
			_ = f.Close()
			if werr != nil {
				_ = os.Remove(lockPath)
				return nil, Startupf("не записан файл замка: %v", werr)
			}
			return &Lock{Path: lockPath}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, Startupf("не взят замок %s: %v", lockPath, err)
		}
		// Файл создаётся пустым и наполняется следующими вызовами. Читатель не
		// должен принять пустой замок за остаток после падения, пока владелец
		// не успел записать PID. Несколько пустых чтений дают ему время.
		stale, undecided, rerr := lockState(lockPath)
		if rerr != nil || stale {
			_ = os.Remove(lockPath)
			emptyReads = 0
			continue
		}
		if undecided {
			emptyReads++
			if emptyReads >= 5 {
				_ = os.Remove(lockPath)
				emptyReads = 0
			}
		} else {
			emptyReads = 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, Startupf("замок %s занят: исчерпано 50 попыток", lockPath)
}

// lockState сообщает, можно ли удалить замок. undecided означает, что владелец
// ещё не записал PID и время, либо файл повреждён.
func lockState(lockPath string) (stale, undecided bool, err error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return true, false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return false, true, nil
	}
	pid := 0
	started := time.Time{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if v, ok := strings.CutPrefix(line, "pid="); ok {
			pid, _ = strconv.Atoi(strings.TrimSpace(v))
		}
		if v, ok := strings.CutPrefix(line, "started="); ok {
			started, _ = time.Parse(time.RFC3339, strings.TrimSpace(v))
		}
	}
	if pid <= 0 || started.IsZero() {
		return false, true, nil
	}
	if !processAlive(pid) || time.Since(started) > 30*time.Second {
		return true, false, nil
	}
	return false, false, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Release отпускает замок. Повторное освобождение безопасно.
func (l *Lock) Release() error {
	if l == nil || l.Path == "" {
		return nil
	}
	err := os.Remove(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
func WriteCounter(counterPath string, groups map[string]int) error {
	data, err := json.Marshal(counterFile{Groups: groups})
	if err != nil {
		return Startupf("не собран счётчик: %v", err)
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
