package taskcli

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

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
		return Validationf("не задан воркер")
	}
	if o.DelayMS < 0 || o.Cost < 0 {
		return Validationf("задержка и стоимость не могут быть отрицательными")
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
		return HealingDecision{}, Validationf("не задана задача healing")
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
