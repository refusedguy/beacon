package taskcli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
			return Validationf("handoff: пустое поле %s", field.name)
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
	if !IsID(id) {
		return Handoff{}, Validationf("неверный ID карточки %q", id)
	}
	cards, err := LoadCards(s.Cfg.TasksDir)
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
	return Handoff{}, Validationf("карточка %s не найдена", id)
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
		return Code(err)
	}
	newCode := func(o NewOptions) int {
		_, _, err := svc.New(o)
		return Code(err)
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
	check("take на in_progress даёт код 1 без изменений", Code(takeErr) == 1 && string(rawBefore) == string(rawAfter))

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
				results <- Code(err)
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
	lockPath := LockPath(stateDir, root)
	dead := fmt.Sprintf("pid=99999999\nstarted=%s\nroot=%s\n", time.Now().UTC().Format(time.RFC3339), root)
	if err := os.WriteFile(lockPath, []byte(dead), 0600); err != nil {
		return err
	}
	lock, err := svc.acquireLock()
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
		return HandoffJournalEntry{}, Validationf("не задана задача журнала handoff")
	}
	if step == "" {
		return HandoffJournalEntry{}, Validationf("не задан шаг журнала handoff")
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
			return nil, Validationf("пустая проверка fix-checks")
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, Validationf("не заданы проверки fix-checks")
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
		return FixChecksResult{}, Validationf("не задана исходная задача fix-checks")
	}
	agent := strings.TrimSpace(r.Agent)
	if agent == "" {
		return FixChecksResult{}, Validationf("не задан агент fix-checks")
	}
	worktree := filepath.Clean(strings.TrimSpace(r.Worktree))
	if worktree == "" || worktree == "." {
		return FixChecksResult{}, Validationf("не задан worktree fix-checks")
	}
	info, err := os.Stat(worktree)
	if err != nil {
		if os.IsNotExist(err) {
			return FixChecksResult{}, Validationf("worktree %s не существует", worktree)
		}
		return FixChecksResult{}, Startupf("не прочитан worktree %s: %v", worktree, err)
	}
	if !info.IsDir() {
		return FixChecksResult{}, Validationf("worktree %s не является каталогом", worktree)
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
			return FixChecksResult{}, Validationf(
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
		return FixChecksTask{}, Validationf("не задана задача fix-checks")
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
	return FixChecksTask{}, Validationf("задача fix-checks %s не найдена", id)
}

// FixChecksTask читает одну задачу починки по ID.
func (s *Service) FixChecksTask(id string) (FixChecksTask, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return FixChecksTask{}, Validationf("не задана задача fix-checks")
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
	return FixChecksTask{}, Validationf("задача fix-checks %s не найдена", id)
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
		return nil, Validationf("не задан worktree fix-checks")
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
		return HandoffConfirmation{}, Validationf("не задана задача подтверждения handoff")
	}
	if !answer.valid() {
		return HandoffConfirmation{}, Validationf("неизвестный ответ подтверждения handoff %q", answer)
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
		return HandoffStatus{}, Validationf("не задана задача подтверждения handoff")
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
