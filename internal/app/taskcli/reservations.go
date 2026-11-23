package taskcli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

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
	if PathsConflict(g, f) {
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
			return nil, Validationf("пустой glob аренды")
		}
		if !doublestar.ValidatePattern(g) {
			return nil, Validationf("неверный glob аренды %q", raw)
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	if len(out) == 0 {
		return nil, Validationf("не заданы glob-ы аренды")
	}
	return out, nil
}

// ReserveFiles берёт аренду на glob-ы до правки. Аренда advisory: она не
// проверяет чужие аренды и не мешает писать файл, а только регистрирует заявку.
// Конфликт проверяется на границе коммита в CheckCommit.
func (s *Service) ReserveFiles(agent string, globs []string, now time.Time) error {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return Validationf("не задан агент аренды")
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
		return Validationf("не задан агент аренды")
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
		return GuardDecision{}, Validationf("не задан агент коммита")
	}
	if len(files) == 0 {
		return GuardDecision{}, Validationf("не заданы файлы коммита")
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
				if !slices.Contains(decision.Files, f) {
					decision.Files = append(decision.Files, f)
				}
				if !slices.Contains(decision.Owners, r.Agent) {
					decision.Owners = append(decision.Owners, r.Agent)
				}
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
			return Validationf("пустой путь ворктри аренды")
		}
		all = append(all, g)
	}
	return s.ReserveFiles(agent, all, now)
}

// ---- HD-022 / ADR 0295: связь feat/fix с milestone ----
//
// Проверка связи живёт в taskstore и остаётся чистой. Этот метод только
// разбирает текст коммита и подставляет порты. Для коммитов вне feat/fix
// связь не требуется, поэтому исход LinkageEmpty.

// CheckCommitLinkage разбирает текст коммита и проверяет связь с milestone.
// Порты передаёт вызывающий, чтобы проверка не делала I/O.
func (s *Service) CheckCommitLinkage(message string, ports LinkagePorts) LinkageVerdict {
	l := ParseLinkage(message)
	if !RequiresLinkage(l.Subject) {
		return LinkageEmpty
	}
	return CheckLinkage(l, ports)
}
