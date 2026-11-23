package taskcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/hookrun"
)

// ---- AN-06: точки расширения на жизненном цикле карточки ----
//
// Сценарии лежат в Config.HooksDir и не имеют отношения к .githooks:
// pre-commit и pre-push остаются нетронутыми. Имя сценария до первой точки
// совпадает с событием, например claimed.sh или closed. Сценарии одного
// события запускаются в порядке сортировки имён, поэтому порядок вызовов
// детерминирован. Один вызов события даёт ровно одну запись в журнал.
// Ошибка сценария пишется в журнал и не меняет статус карточки.

// HookEvent — событие жизненного цикла карточки.
type HookEvent string

const (
	HookClaimed   HookEvent = "claimed"
	HookUnblocked HookEvent = "unblocked"
	HookClosed    HookEvent = "closed"
)

// HookLogEntry — одна запись о вызове сценариев события.
type HookLogEntry struct {
	Event  string   `json:"event"`
	CardID string   `json:"card_id"`
	Time   string   `json:"time"`
	Hooks  []string `json:"hooks"`
	Error  string   `json:"error,omitempty"`
}

// hookPayload уходит сценарию на stdin.
type hookPayload struct {
	Event  string `json:"event"`
	CardID string `json:"card_id"`
	Time   string `json:"time"`
}

// hookRunTimeout ограничивает режимы Sync и Gate. Notify его не использует.
const hookRunTimeout = 5 * time.Second

// hookModeForEvent выбирает режим запуска для события карточки. Claimed и
// closed ждут завершения сценария, unblocked передаёт его вывод по строкам.
func hookModeForEvent(event HookEvent) hookrun.Mode {
	switch event {
	case HookUnblocked:
		return hookrun.Gate
	default:
		return hookrun.Sync
	}
}

// hookFiles возвращает сценарии события в порядке имён.
func hookFiles(dir string, event HookEvent) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	want := string(event)
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		base := name
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		if base == want {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// RunHooks вызывает сценарии события и пишет одну запись в журнал. Возвращаемая
// ошибка относится только к журналу, сбой сценария в неё не входит.
func (s *Service) RunHooks(event HookEvent, cardID, when string) error {
	if event == HookUnblocked {
		if err := s.RegisterGateWaiter(context.Background(), "gate:unblocked", cardID); err != nil {
			return err
		}
	}
	dir := strings.TrimSpace(s.Cfg.HooksDir)
	if dir == "" {
		return nil
	}
	names, err := hookFiles(dir, event)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(names) == 0 {
		return nil
	}
	body, err := json.Marshal(hookPayload{Event: string(event), CardID: cardID, Time: when})
	if err != nil {
		return err
	}
	entry := HookLogEntry{Event: string(event), CardID: cardID, Time: when, Hooks: []string{}}
	mode := hookModeForEvent(event)
	for _, name := range names {
		entry.Hooks = append(entry.Hooks, name)
		res := hookrun.Execute(hookrun.OSRunner{}, mode, hookrun.Spec{
			Path:    filepath.Join(dir, name),
			Stdin:   string(body),
			Timeout: hookRunTimeout,
		})
		out, runErr := res.Output, res.Err
		if runErr == nil {
			continue
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = runErr.Error()
		}
		if entry.Error != "" {
			entry.Error += "; "
		}
		entry.Error += name + ": " + msg
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Cfg.StateDir, "hooks.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", line)
	return err
}

// HookLog возвращает записи о вызовах хуков в порядке записи.
func (s *Service) HookLog() ([]HookLogEntry, error) {
	raw, err := os.ReadFile(filepath.Join(s.Cfg.StateDir, "hooks.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []HookLogEntry
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var entry HookLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// appendDeferWakeLog пишет журнал под отдельным замком. Сбой журнала не меняет
// результат прохода: карточки уже записаны, а список остаётся доступным.
func (s *Service) appendDeferWakeLog(entries []DeferWakeLogEntry) {
	repo := newDeferWakeRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return
	}
	defer unlock()
	st, err := repo.load()
	if err != nil {
		return
	}
	st.Journal = append(st.Journal, entries...)
	_ = repo.save(st)
}

// DeferWakeLog возвращает записи журнала в порядке записи.
func (s *Service) DeferWakeLog() ([]DeferWakeLogEntry, error) {
	st, err := newDeferWakeRepo(s.Cfg).load()
	if err != nil {
		return nil, err
	}
	return append([]DeferWakeLogEntry(nil), st.Journal...), nil
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
		return Validationf("не задана причина precheck")
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

// appendRecheckReport writes one report line and increments the recheck
// counter. Both happen even when individual card writes failed.
func (s *Service) appendRecheckReport(rep RecheckReport, now time.Time) error {
	when := now.UTC()
	if when.IsZero() {
		when = time.Now().UTC()
	}
	entry := RecheckReportEntry{
		Time:      when.Format(time.RFC3339),
		Rechecked: rep.Rechecked,
		Changed:   rep.Changed,
		Failed:    rep.Failed,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Cfg.StateDir, recheckReportName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(f, "%s\n", line)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}

	countPath := filepath.Join(s.Cfg.StateDir, recheckCountName)
	count := 0
	if raw, err := os.ReadFile(countPath); err == nil {
		count, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	return os.WriteFile(countPath, []byte(strconv.Itoa(count+1)+"\n"), 0600)
}

// RecheckLog returns the report lines in the order they were written.
func (s *Service) RecheckLog() ([]RecheckReportEntry, error) {
	raw, err := os.ReadFile(filepath.Join(s.Cfg.StateDir, recheckReportName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []RecheckReportEntry
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var entry RecheckReportEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// RecheckCount returns the number of recheck passes recorded so far.
func (s *Service) RecheckCount() (int, error) {
	raw, err := os.ReadFile(filepath.Join(s.Cfg.StateDir, recheckCountName))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}
