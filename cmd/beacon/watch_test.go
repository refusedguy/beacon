package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
)

type w69Call struct {
	dir  string
	name string
	args []string
	env  []string
}

// w69CLI builds a CLI whose runner records calls and returns a canned result.
func w69CLI(t *testing.T, respond func(w69Call) runResult) (*CLI, *bytes.Buffer, *bytes.Buffer, *[]w69Call) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	calls := new([]w69Call)
	c := &CLI{
		out:    out,
		errOut: errOut,
		root:   t.TempDir(),
		run: func(dir, name string, args, env []string) runResult {
			call := w69Call{dir, name, append([]string(nil), args...), append([]string(nil), env...)}
			*calls = append(*calls, call)
			if respond == nil {
				return runResult{}
			}
			return respond(call)
		},
	}
	return c, out, errOut, calls
}

// w69Snapshot installs a snapshot function for the duration of the test.
func w69Snapshot(t *testing.T, fn func() (mcp.StateSnapshot, error)) *int {
	t.Helper()
	previous := watchSnapshot
	calls := new(int)
	watchSnapshot = func() (mcp.StateSnapshot, error) {
		*calls++
		return fn()
	}
	t.Cleanup(func() { watchSnapshot = previous })
	return calls
}

func w69SessionList(sessions ...map[string]any) runResult {
	data, _ := json.Marshal(map[string]any{"data": sessions})
	return runResult{stdout: data}
}

func w69LineContaining(s, substr string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

func w69Watch(verdict domain.Verdict, revision uint64, targets ...string) domain.Watch {
	subs := make([]domain.Subscriber, 0, len(targets))
	for _, target := range targets {
		subs = append(subs, domain.Subscriber{Target: target, Role: domain.RoleExecutor})
	}
	return domain.Watch{
		ID:          "watch-1",
		Verdict:     verdict,
		Revision:    revision,
		Status:      domain.StatusWatching,
		Subscribers: subs,
	}
}

func TestW69RendersTableWithJoin(t *testing.T) {
	snapshotCalls := w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictReady, 7, "ses-1"),
		}}, nil
	})
	c, out, errOut, calls := w69CLI(t, func(w69Call) runResult {
		return w69SessionList(map[string]any{
			"id": "ses-1", "agent": "developer", "status": map[string]any{"type": "idle"},
		})
	})

	if code := runWatch(c, nil); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
	for _, column := range []string{"WATCH", "VERDICT", "REVISION", "SUBSCRIBER SESSION", "AGENT", "OPENCODE STATE"} {
		if !strings.Contains(out.String(), column) {
			t.Fatalf("нет колонки %s в таблице: %q", column, out.String())
		}
	}
	for _, value := range []string{"watch-1", "ready", "7", "ses-1", "developer", "idle"} {
		if !strings.Contains(out.String(), value) {
			t.Fatalf("нет значения %s в таблице: %q", value, out.String())
		}
	}
	if *snapshotCalls != 1 {
		t.Fatalf("снимков %d, ожидался 1", *snapshotCalls)
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов runners %d, ожидался 1", len(*calls))
	}
	call := (*calls)[0]
	if call.name != "opencode" || len(call.args) != 2 || call.args[0] != "api" || call.args[1] != "session.list" {
		t.Fatalf("не тот вызов opencode: %+v", call)
	}
}

func TestW69ReadinessComesFromVerdictNotIdle(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictWatching, 3, "ses-1"),
		}}, nil
	})
	c, out, _, _ := w69CLI(t, func(w69Call) runResult {
		return w69SessionList(map[string]any{
			"id": "ses-1", "agent": "developer", "status": map[string]any{"type": "idle"},
		})
	})

	if code := runWatch(c, nil); code != 0 {
		t.Fatalf("код выхода %d", code)
	}
	// idle из OpenCode не превращает watching в ready.
	line := w69LineContaining(out.String(), "watch-1")
	if !strings.Contains(line, "watching") {
		t.Fatalf("вердикт watching потерян: %q", out.String())
	}
	if strings.Contains(line, "ready") {
		t.Fatalf("idle не должен давать ready: %q", line)
	}
}

func TestW69UnmatchedSubscriberStillShown(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictRetry, 5, "ses-missing"),
		}}, nil
	})
	c, out, _, _ := w69CLI(t, func(w69Call) runResult {
		return w69SessionList(map[string]any{"id": "ses-other", "status": "busy"})
	})

	if code := runWatch(c, nil); code != 0 {
		t.Fatalf("код выхода %d", code)
	}
	if !strings.Contains(out.String(), "ses-missing") {
		t.Fatalf("вотч без совпадения скрыт: %q", out.String())
	}
	line := w69LineContaining(out.String(), "ses-missing")
	if !strings.Contains(line, "-") || strings.Contains(line, "busy") {
		t.Fatalf("нет прочерков для неизвестных agent/state: %q", line)
	}
}

func TestW69WatchWithoutSubscribersIsVisible(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictKeepWorking, 9),
		}}, nil
	})
	c, out, _, _ := w69CLI(t, func(w69Call) runResult { return w69SessionList() })

	if code := runWatch(c, nil); code != 0 {
		t.Fatalf("код выхода %d", code)
	}
	if !strings.Contains(out.String(), "watch-1") || !strings.Contains(out.String(), "keep_working") {
		t.Fatalf("вотч без подписчика не показан: %q", out.String())
	}
}

func TestW69SingleSnapshotAndSingleSessionList(t *testing.T) {
	snapshotCalls := w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{}, nil
	})
	c, _, _, calls := w69CLI(t, func(w69Call) runResult { return w69SessionList() })

	// Даже с --interval опциональный флаг не запускает цикл.
	if code := runWatch(c, []string{"--interval", "5s"}); code != 0 {
		t.Fatalf("код выхода %d", code)
	}
	if *snapshotCalls != 1 {
		t.Fatalf("снимков %d, ожидался 1 (без цикла)", *snapshotCalls)
	}
	if len(*calls) != 1 {
		t.Fatalf("session.list вызовов %d, ожидался 1 (без цикла)", len(*calls))
	}
}

func TestW69SessionListFailure(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) { return mcp.StateSnapshot{}, nil })
	c, out, errOut, _ := w69CLI(t, func(w69Call) runResult {
		return runResult{code: 3, stderr: []byte("service not running\n")}
	})

	if code := runWatch(c, nil); code != 3 {
		t.Fatalf("код выхода %d, ожидался 3", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой при ошибке: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "service not running") {
		t.Fatalf("нет причины в stderr: %q", errOut.String())
	}
}

func TestW69SnapshotFailure(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{}, errSnapshot
	})
	c, out, errOut, calls := w69CLI(t, func(w69Call) runResult { return w69SessionList() })

	if code := runWatch(c, nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 || len(*calls) != 0 {
		t.Fatalf("не должно быть вывода или вызовов: out %q, calls %d", out.String(), len(*calls))
	}
	if !strings.Contains(errOut.String(), "snapshot") {
		t.Fatalf("нет ошибки снимка в stderr: %q", errOut.String())
	}
}

type w69Err string

func (e w69Err) Error() string { return string(e) }

const errSnapshot = w69Err("store closed")

func TestW69JSONFormat(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictReady, 11, "ses-1"),
		}}, nil
	})
	c, out, errOut, _ := w69CLI(t, func(w69Call) runResult {
		return w69SessionList(map[string]any{
			"id": "ses-1", "agent": "reviewer", "status": "retry",
		})
	})

	if code := runWatch(c, []string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("stdout не JSON: %q", out.String())
	}
	var rows []watchRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Verdict != "ready" || rows[0].Revision != 11 ||
		rows[0].Subscriber != "ses-1" || rows[0].Agent != "reviewer" || rows[0].OpenCodeState != "retry" {
		t.Fatalf("неверная строка: %+v", rows)
	}
}

func TestW69BadFormat(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) { return mcp.StateSnapshot{}, nil })
	c, out, errOut, _ := w69CLI(t, func(w69Call) runResult { return w69SessionList() })

	if code := runWatch(c, []string{"--format", "yaml"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "неизвестный формат") {
		t.Fatalf("нет ошибки формата: stdout %q stderr %q", out.String(), errOut.String())
	}
}

// --- T3.114 / ADR 0048 p4: beacon gc ---

var t3114Now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// t3114Restore freezes the collector clock and restores every global the gc
// tests replace. The tests run in one package and do not call t.Parallel.
func t3114Restore(t *testing.T) {
	t.Helper()
	prevNow, prevRead, prevWrite, prevAlive := gcNow, gcReadState, gcWriteState, gcProcessAlive
	t.Cleanup(func() {
		gcNow, gcReadState, gcWriteState, gcProcessAlive = prevNow, prevRead, prevWrite, prevAlive
	})
	gcNow = func() time.Time { return t3114Now }
}

// t3114Write installs an apply fake that records the exact plan it receives.
func t3114Write(t *testing.T, fail error) (*int, *[]gcItem) {
	t.Helper()
	calls := new(int)
	applied := new([]gcItem)
	gcWriteState = func(items []gcItem) ([]gcItem, error) {
		*calls++
		*applied = append([]gcItem(nil), items...)
		if fail != nil {
			return nil, fail
		}
		return items, nil
	}
	return calls, applied
}

// t3114State holds three stale records and three records that must be kept.
func t3114State() gcState {
	return gcState{
		Leases: []gcLease{
			{ID: "lease-expired", ExpiresAt: t3114Now.Add(-time.Minute)},
			{ID: "lease-live", ExpiresAt: t3114Now.Add(time.Minute)},
		},
		PIDs: []gcPID{
			{ID: "pid-dead", PID: 111},
			{ID: "pid-live", PID: 222},
		},
		Tickets: []gcTicket{
			{ID: "ticket-stale", ExpiresAt: t3114Now.Add(-time.Minute)},
			{ID: "ticket-fresh", ExpiresAt: t3114Now.Add(time.Minute)},
			{ID: "ticket-no-deadline"},
		},
	}
}

func TestT3114DryRunListsStaleWithoutRemoving(t *testing.T) {
	t3114Restore(t)
	gcReadState = func() (gcState, error) { return t3114State(), nil }
	calls, _ := t3114Write(t, nil)
	gcProcessAlive = func(pid int) bool { return pid == 222 }
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runGC(c, nil); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
	if *calls != 0 {
		t.Fatalf("dry run вызвал удаление %d раз", *calls)
	}
	for _, id := range []string{"lease-expired", "pid-dead", "ticket-stale"} {
		if !strings.Contains(out.String(), id) {
			t.Fatalf("нет просроченной записи %s: %q", id, out.String())
		}
	}
	for _, id := range []string{"lease-live", "pid-live", "ticket-fresh", "ticket-no-deadline"} {
		if strings.Contains(out.String(), id) {
			t.Fatalf("живая запись %s попала в dry run: %q", id, out.String())
		}
	}
	if !strings.Contains(out.String(), "НАЙДЕНО: 3") {
		t.Fatalf("нет итога dry run: %q", out.String())
	}
}

func TestT3114ApplyRemovesOnlyStaleAndReports(t *testing.T) {
	t3114Restore(t)
	gcReadState = func() (gcState, error) { return t3114State(), nil }
	calls, applied := t3114Write(t, nil)
	gcProcessAlive = func(pid int) bool { return pid == 222 }
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runGC(c, []string{"--apply"}); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if *calls != 1 {
		t.Fatalf("удаление вызвано %d раз, ожидался 1", *calls)
	}
	want := map[string]bool{"lease-expired": true, "pid-dead": true, "ticket-stale": true}
	if len(*applied) != len(want) {
		t.Fatalf("на удаление передано %d записей: %+v", len(*applied), *applied)
	}
	for _, it := range *applied {
		if !want[it.ID] {
			t.Fatalf("на удаление попала лишняя запись %s (%s)", it.ID, it.Kind)
		}
		delete(want, it.ID)
	}
	if !strings.Contains(out.String(), "УДАЛЕНО: 3") {
		t.Fatalf("нет отчёта об удалении: %q", out.String())
	}
	for _, id := range []string{"lease-expired", "pid-dead", "ticket-stale"} {
		if !strings.Contains(out.String(), id) {
			t.Fatalf("нет удалённой записи %s в отчёте: %q", id, out.String())
		}
	}
}

func TestT3114JSONReport(t *testing.T) {
	t3114Restore(t)
	gcReadState = func() (gcState, error) { return t3114State(), nil }
	_, _ = t3114Write(t, nil)
	gcProcessAlive = func(pid int) bool { return pid == 222 }
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runGC(c, []string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	var res gcResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("dry run не JSON: %v, %q", err, out.String())
	}
	if res.Mode != "dry-run" || res.Removed != 0 || len(res.Items) != 3 {
		t.Fatalf("неверный dry run отчёт: %+v", res)
	}

	out.Reset()
	if code := runGC(c, []string{"--format", "json", "--apply"}); code != 0 {
		t.Fatalf("код выхода apply %d, stderr %q", code, errOut.String())
	}
	res = gcResult{}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("apply не JSON: %v, %q", err, out.String())
	}
	if res.Mode != "applied" || res.Removed != 3 || len(res.Items) != 3 {
		t.Fatalf("неверный apply отчёт: %+v", res)
	}
}

func TestT3114ExpiryBoundary(t *testing.T) {
	t3114Restore(t)
	gcProcessAlive = func(int) bool { return true }
	state := gcState{
		Leases:  []gcLease{{ID: "lease-at-expiry", ExpiresAt: t3114Now}},
		Tickets: []gcTicket{{ID: "ticket-at-expiry", ExpiresAt: t3114Now}},
	}

	items := gcStale(state, t3114Now)
	if len(items) != 2 || items[0].ID != "lease-at-expiry" || items[1].ID != "ticket-at-expiry" {
		t.Fatalf("записи на границе срока не просрочены: %+v", items)
	}
}

func TestT3114BadFormat(t *testing.T) {
	t3114Restore(t)
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runGC(c, []string{"--format", "yaml"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "неизвестный формат") {
		t.Fatalf("нет ошибки формата: stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestT3114ReadFailure(t *testing.T) {
	t3114Restore(t)
	gcReadState = func() (gcState, error) { return gcState{}, errSnapshot }
	calls, _ := t3114Write(t, nil)
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runGC(c, []string{"--apply"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if *calls != 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "чтение состояния") {
		t.Fatalf("ожидалась ошибка чтения без удаления: calls %d stdout %q stderr %q", *calls, out.String(), errOut.String())
	}
}

func TestT3114ApplyFailure(t *testing.T) {
	t3114Restore(t)
	gcReadState = func() (gcState, error) { return t3114State(), nil }
	calls, _ := t3114Write(t, errSnapshot)
	gcProcessAlive = func(pid int) bool { return pid == 222 }
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runGC(c, []string{"--apply"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if *calls != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "store closed") {
		t.Fatalf("ожидалась ошибка удаления: calls %d stdout %q stderr %q", *calls, out.String(), errOut.String())
	}
}

func TestT3114RegisterWiring(t *testing.T) {
	t3114Restore(t)
	prevRunner := gcRunner
	t.Cleanup(func() { gcRunner = prevRunner })

	RegisterGCStores(
		func() (gcState, error) { return t3114State(), nil },
		func(items []gcItem) ([]gcItem, error) { return items, nil },
	)
	ran := false
	RegisterGC(func(*CLI, []string) int { ran = true; return 7 })
	c, _, _, _ := w69CLI(t, nil)

	if code := gcRunner(c, nil); code != 7 || !ran {
		t.Fatalf("RegisterGC не подключил команду: код %d ran %v", code, ran)
	}
	if state, err := gcReadState(); err != nil || len(state.Leases) != 2 {
		t.Fatalf("RegisterGCStores не подключил чтение: %+v, %v", state, err)
	}
}

// --- T3.125 / ADR 0056 p4: батчинг событий в одно пробуждение ---

var w3125Base = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestWakeBatchCollapsesBurst(t *testing.T) {
	events := []wakeEvent{
		{At: w3125Base, Target: "ses-1", Text: "ready@7"},
		{At: w3125Base.Add(30 * time.Millisecond), Target: "ses-1", Text: "ready@8"},
		{At: w3125Base.Add(150 * time.Millisecond), Target: "ses-1", Text: "ready@9"},
	}
	batches := batchWakeEvents(events, wakeBatchWindow)
	if len(batches) != 1 {
		t.Fatalf("батчей %d, ожидался 1: %+v", len(batches), batches)
	}
	if batches[0].Target != "ses-1" || len(batches[0].Texts) != 3 {
		t.Fatalf("неверный батч: %+v", batches[0])
	}
	for i, want := range []string{"ready@7", "ready@8", "ready@9"} {
		if batches[0].Texts[i] != want {
			t.Fatalf("текст %d = %q, хотим %q", i, batches[0].Texts[i], want)
		}
	}
}

func TestWakeBatchWindowOpensNewBatch(t *testing.T) {
	events := []wakeEvent{
		{At: w3125Base, Target: "ses-1", Text: "a"},
		{At: w3125Base.Add(199 * time.Millisecond), Target: "ses-1", Text: "b"},
		{At: w3125Base.Add(200 * time.Millisecond), Target: "ses-1", Text: "c"},
		{At: w3125Base.Add(350 * time.Millisecond), Target: "ses-1", Text: "d"},
	}
	batches := batchWakeEvents(events, wakeBatchWindow)
	if len(batches) != 2 {
		t.Fatalf("батчей %d, ожидалось 2: %+v", len(batches), batches)
	}
	if len(batches[0].Texts) != 2 || len(batches[1].Texts) != 2 {
		t.Fatalf("размеры батчей %d и %d, хотим 2 и 2: %+v", len(batches[0].Texts), len(batches[1].Texts), batches)
	}
	if batches[0].Texts[0] != "a" || batches[0].Texts[1] != "b" ||
		batches[1].Texts[0] != "c" || batches[1].Texts[1] != "d" {
		t.Fatalf("порядок событий в батчах: %+v", batches)
	}
}

func TestWakeBatchSeparatesTargets(t *testing.T) {
	events := []wakeEvent{
		{At: w3125Base, Target: "ses-1", Text: "a"},
		{At: w3125Base.Add(10 * time.Millisecond), Target: "ses-2", Text: "b"},
		{At: w3125Base.Add(20 * time.Millisecond), Target: "ses-1", Text: "c"},
	}
	batches := batchWakeEvents(events, wakeBatchWindow)
	if len(batches) != 2 {
		t.Fatalf("батчей %d, ожидалось 2: %+v", len(batches), batches)
	}
	if batches[0].Target != "ses-1" || batches[1].Target != "ses-2" {
		t.Fatalf("порядок батчей (по времени открытия): %+v", batches)
	}
	if len(batches[0].Texts) != 2 || len(batches[1].Texts) != 1 {
		t.Fatalf("размеры батчей: %+v", batches)
	}
}

func TestWakeBatchIsNotWakeBetween(t *testing.T) {
	// T3.80 разносит пробуждения во времени. T3.125 уменьшает кратность:
	// 50 событий в окне дают ровно один wake без случайной задержки.
	events := make([]wakeEvent, 0, 50)
	for i := 0; i < 50; i++ {
		events = append(events, wakeEvent{
			At:     w3125Base.Add(time.Duration(i) * time.Millisecond),
			Target: "ses-1",
			Text:   fmt.Sprintf("line-%d", i),
		})
	}
	batches := batchWakeEvents(events, wakeBatchWindow)
	if len(batches) != 1 || len(batches[0].Texts) != 50 {
		t.Fatalf("ожидался 1 батч из 50 событий: %d батчей, %+v", len(batches), batches)
	}
	second := batchWakeEvents([]wakeEvent{{At: w3125Base.Add(250 * time.Millisecond), Target: "ses-1", Text: "late"}}, wakeBatchWindow)
	if len(second) != 1 || second[0].Texts[0] != "late" {
		t.Fatalf("событие после окна не открыло новый wake: %+v", second)
	}
}

func TestEmitWakeBatchesSendsOncePerBatch(t *testing.T) {
	batches := []wakeBatch{
		{Target: "ses-1", Texts: []string{"a", "b"}},
		{Target: "ses-2", Texts: []string{"c"}},
	}
	var sends []string
	err := emitWakeBatches(batches, func(target, text string) error {
		sends = append(sends, target+"|"+text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sends) != 2 {
		t.Fatalf("wake вызовов %d, ожидалось 2: %q", len(sends), sends)
	}
	if sends[0] != "ses-1|a; b" || sends[1] != "ses-2|c" {
		t.Fatalf("wake вызовы: %q", sends)
	}
}

func TestEmitWakeBatchesStopsOnError(t *testing.T) {
	batches := []wakeBatch{
		{Target: "ses-1", Texts: []string{"a"}},
		{Target: "ses-2", Texts: []string{"b"}},
	}
	calls := 0
	err := emitWakeBatches(batches, func(string, string) error {
		calls++
		return errors.New("send failed")
	})
	if err == nil {
		t.Fatal("ожидалась ошибка отправки")
	}
	if calls != 1 {
		t.Fatalf("send вызовов %d, ожидался 1", calls)
	}
}

func TestRunWatchBatchedWakeGroupsBySession(t *testing.T) {
	rows := []watchRow{
		{Watch: "watch-1", Verdict: "ready", Revision: 7, Subscriber: "ses-1"},
		{Watch: "watch-1", Verdict: "ready", Revision: 8, Subscriber: "ses-1"},
		{Watch: "watch-2", Verdict: "ready", Revision: 3, Subscriber: "ses-2"},
		{Watch: "watch-2", Verdict: "ready", Revision: 3},
	}
	var sends []string
	err := runWatchBatchedWake(rows, w3125Base, func(target, text string) error {
		sends = append(sends, target+"|"+text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sends) != 2 {
		t.Fatalf("wake вызовов %d, ожидалось 2: %q", len(sends), sends)
	}
	found := make(map[string]string)
	for _, s := range sends {
		parts := strings.SplitN(s, "|", 2)
		found[parts[0]] = parts[1]
	}
	if found["ses-1"] != "ready@7; ready@8" || found["ses-2"] != "ready@3" {
		t.Fatalf("wake содержимое: %+v", found)
	}
	if _, ok := found[""]; ok {
		t.Fatalf("wake для строки без подписчика: %+v", found)
	}
}

func TestRunWatchWakeFlagEmitsOneWakePerSession(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictReady, 7, "ses-1", "ses-1", "ses-2"),
		}}, nil
	})
	c, out, errOut, _ := w69CLI(t, func(w69Call) runResult { return w69SessionList() })
	if code := runWatch(c, []string{"--wake"}); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if n := strings.Count(out.String(), "wake session=ses-1 "); n != 1 {
		t.Fatalf("ses-1 разбужен %d раз, ожидался 1: %q", n, out.String())
	}
	if n := strings.Count(out.String(), "wake session=ses-2 "); n != 1 {
		t.Fatalf("ses-2 разбужен %d раз, ожидался 1: %q", n, out.String())
	}
}

func TestRunWatchNoWakeWithoutFlag(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictReady, 7, "ses-1"),
		}}, nil
	})
	c, out, _, _ := w69CLI(t, func(w69Call) runResult { return w69SessionList() })
	if code := runWatch(c, nil); code != 0 {
		t.Fatalf("код выхода %d", code)
	}
	if strings.Contains(out.String(), "wake session=") {
		t.Fatalf("wake без флага: %q", out.String())
	}
}

func TestRunWatchWakeJSONKeepsStdoutValid(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictReady, 7, "ses-1"),
		}}, nil
	})
	c, out, errOut, _ := w69CLI(t, func(w69Call) runResult { return w69SessionList() })
	if code := runWatch(c, []string{"--format", "json", "--wake"}); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("stdout не JSON: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "wake session=ses-1 ") {
		t.Fatalf("wake не в stderr: %q", errOut.String())
	}
}

// --- T3.141 / ADR 0060 p2: beacon watch --live ---

// w3141Source is the stand-in for the opencode client. It feeds a script of
// event channels: closing a channel is an SSE disconnect, and after the script
// runs out Events returns a channel that blocks until the lifetime ends.
type w3141Source struct {
	sessions    []opencodeSession
	sessionErr  error
	channels    []chan liveEvent
	sessionCall int
	eventsCall  int
}

func (s *w3141Source) SessionList() ([]opencodeSession, error) {
	s.sessionCall++
	if s.sessionErr != nil {
		return nil, s.sessionErr
	}
	return s.sessions, nil
}

func (s *w3141Source) Events(ctx context.Context) <-chan liveEvent {
	n := s.eventsCall
	s.eventsCall++
	if n < len(s.channels) {
		return s.channels[n]
	}
	ch := make(chan liveEvent)
	return ch
}

// w3141Session builds one session.list row for the stand-in.
func w3141Session(id, agent, state string) opencodeSession {
	status, _ := json.Marshal(map[string]string{"type": state})
	return opencodeSession{ID: id, Agent: agent, Status: status}
}

// w3141Harness owns the loop goroutine and the replaced seams of one test.
type w3141Harness struct {
	out      *bytes.Buffer
	errOut   *bytes.Buffer
	calls    *[]w69Call
	rendered <-chan struct{}
	done     <-chan int
	cancel   context.CancelFunc
	waited   bool
}

// w3141Start installs the stand-in, starts runWatch --live in a goroutine and
// wraps watchSnapshot so tests can wait for each refresh deterministically.
// Call w69Snapshot before w3141Start to set the snapshot contents.
func w3141Start(t *testing.T, src *w3141Source, args ...string) *w3141Harness {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, out, errOut, calls := w69CLI(t, nil)
	prevSnap, prevSrc, prevCtx, prevDelay := watchSnapshot, watchLiveSource, watchLiveContext, watchReconnectDelay
	prevLiveRunner := watchLiveRunner
	watchLiveRunner = runWatchLive
	watchReconnectDelay = 0

	rendered := make(chan struct{}, 64)
	watchSnapshot = func() (mcp.StateSnapshot, error) {
		select {
		case rendered <- struct{}{}:
		default:
		}
		return prevSnap()
	}
	ctx, cancel := context.WithCancel(context.Background())
	watchLiveContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	SetLiveSource(src)

	done := make(chan int, 1)
	go func() { done <- runWatch(c, append([]string{"--live"}, args...)) }()

	h := &w3141Harness{
		out: out, errOut: errOut, calls: calls,
		rendered: rendered, done: done, cancel: cancel,
	}
	t.Cleanup(func() {
		cancel()
		SetLiveSource(prevSrc)
		watchLiveRunner = prevLiveRunner
		watchLiveContext = prevCtx
		watchSnapshot = prevSnap
		watchReconnectDelay = prevDelay
	})
	return h
}

// w3141Renders waits for n snapshot reads, so the loop has printed n tables.
func (h *w3141Harness) w3141Renders(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-h.rendered:
		case <-time.After(2 * time.Second):
			t.Fatalf("не дождались рендера %d из %d, stderr %q", i+1, n, h.errOut.String())
		}
	}
}

// w3141Stop cancels the lifetime, then returns the command exit code.
func (h *w3141Harness) w3141Stop(t *testing.T) int {
	t.Helper()
	if !h.waited {
		h.waited = true
		h.cancel()
		select {
		case code := <-h.done:
			return code
		case <-time.After(2 * time.Second):
			t.Fatal("живой цикл не остановился по context")
		}
	}
	return -1
}

func TestW3141LiveRendersSevenColumnJoinTable(t *testing.T) {
	snapshots := w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictReady, 7, "ses-1"),
		}}, nil
	})
	events := make(chan liveEvent, 2)
	events <- liveEvent{Type: "session.status"}
	events <- liveEvent{Type: "session.execution.started"}
	close(events)
	src := &w3141Source{
		sessions: []opencodeSession{w3141Session("ses-1", "developer", "idle")},
		channels: []chan liveEvent{events},
	}
	h := w3141Start(t, src)

	// connect + two events + reconnect after the channel closes.
	h.w3141Renders(t, 4)
	if code := h.w3141Stop(t); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, h.errOut.String())
	}
	if h.errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", h.errOut.String())
	}
	for _, column := range []string{
		"WATCH", "VERDICT", "REVISION", "SESSION", "AGENT", "OPENCODE STATE", "ATTENTION",
	} {
		if !strings.Contains(h.out.String(), column) {
			t.Fatalf("нет колонки %s: %q", column, h.out.String())
		}
	}
	for _, value := range []string{"watch-1", "ready", "7", "ses-1", "developer", "idle", "-"} {
		if !strings.Contains(h.out.String(), value) {
			t.Fatalf("нет значения %s: %q", value, h.out.String())
		}
	}
	if *snapshots != 4 {
		t.Fatalf("снимков %d, ожидалось 4", *snapshots)
	}
	if src.sessionCall != 4 {
		t.Fatalf("session.list вызовов %d, ожидалось 4", src.sessionCall)
	}
	if src.eventsCall != 2 {
		t.Fatalf("подписок %d, ожидалось 2 (подключение + reconnect)", src.eventsCall)
	}
	if len(*h.calls) != 0 {
		t.Fatalf("живой контур вызвал внешний runner %d раз", len(*h.calls))
	}
}

func TestW3141LiveVerdictComesFromBeaconNotIdle(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictWatching, 3, "ses-1"),
		}}, nil
	})
	events := make(chan liveEvent, 1)
	events <- liveEvent{Type: "session.status"}
	close(events)
	src := &w3141Source{
		sessions: []opencodeSession{w3141Session("ses-1", "developer", "idle")},
		channels: []chan liveEvent{events},
	}
	h := w3141Start(t, src)
	h.w3141Renders(t, 2)
	if code := h.w3141Stop(t); code != 0 {
		t.Fatalf("код выхода %d", code)
	}

	line := w69LineContaining(h.out.String(), "watch-1")
	if !strings.Contains(line, "watching") || strings.Contains(line, "ready") {
		t.Fatalf("idle из OpenCode изменил вердикт: %q", line)
	}
}

func TestW3141LiveIntervalRefreshesWithoutEvents(t *testing.T) {
	snapshots := w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{}, nil
	})
	src := &w3141Source{}
	h := w3141Start(t, src, "--interval", "20ms")
	h.w3141Renders(t, 3)
	if code := h.w3141Stop(t); code != 0 {
		t.Fatalf("код выхода %d", code)
	}
	if *snapshots < 3 || src.sessionCall < 3 {
		t.Fatalf("перечитываний: снимков %d, session.list %d, ожидалось минимум 3",
			*snapshots, src.sessionCall)
	}
	if src.eventsCall != 1 {
		t.Fatalf("подписок %d, интервал не должен создавать новые", src.eventsCall)
	}
}

func TestW3141LiveReconnectRereadsSnapshotWithoutReplay(t *testing.T) {
	first := make(chan liveEvent, 1)
	first <- liveEvent{Type: "session.status"}
	close(first)
	second := make(chan liveEvent, 1)
	second <- liveEvent{Type: "session.status"}
	close(second)
	snapshots := w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictReady, 7, "ses-1"),
		}}, nil
	})
	src := &w3141Source{
		sessions: []opencodeSession{w3141Session("ses-1", "developer", "idle")},
		channels: []chan liveEvent{first, second},
	}
	h := w3141Start(t, src)

	// connect, event, reconnect with a fresh snapshot, event, reconnect.
	h.w3141Renders(t, 5)
	if code := h.w3141Stop(t); code != 0 {
		t.Fatalf("код выхода %d", code)
	}
	if src.eventsCall != 3 {
		t.Fatalf("подписок %d, ожидалось 3", src.eventsCall)
	}
	if *snapshots != 5 || src.sessionCall != 5 {
		t.Fatalf("перечитываний: снимков %d, session.list %d, ожидалось 5",
			*snapshots, src.sessionCall)
	}
}

func TestW3141LiveNoSourceFailsClosed(t *testing.T) {
	prev, prevRunner := watchLiveSource, watchLiveRunner
	SetLiveSource(nil)
	watchLiveRunner = runWatchLive
	t.Cleanup(func() { SetLiveSource(prev); watchLiveRunner = prevRunner })
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runWatch(c, []string{"--live"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "не подключён") {
		t.Fatalf("нет ошибки источника: stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestW3141LiveSnapshotFailureFailsClosed(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{}, errSnapshot
	})
	prev, prevRunner := watchLiveSource, watchLiveRunner
	t.Cleanup(func() { SetLiveSource(prev); watchLiveRunner = prevRunner })
	SetLiveSource(&w3141Source{})
	watchLiveRunner = runWatchLive
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runWatch(c, []string{"--live"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "snapshot") {
		t.Fatalf("ожидалась ошибка снимка: stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestW3141MarkAttentionUsesVerdictAndRequests(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	requests := []attentionRequest{
		{Agent: "reviewer", TaskID: "watch-2", Reason: "question", RequestedAt: base.Add(time.Second)},
		{Agent: "developer", TaskID: "watch-1", Reason: "approval", RequestedAt: base},
	}
	rows := []watchRow{
		{Watch: "watch-1", Verdict: "watching", Agent: "developer"},
		{Watch: "watch-2", Verdict: "ready", Agent: "reviewer"},
		{Watch: "watch-3", Verdict: "needs_input", Agent: "builder"},
		{Watch: "watch-4", Verdict: "ready", Agent: "unknown"},
	}
	markAttention(rows, requests)
	want := map[string]string{
		"watch-1": attentionYes,
		"watch-2": attentionYes,
		"watch-3": attentionYes,
		"watch-4": "",
	}
	for _, r := range rows {
		if r.Attention != want[r.Watch] {
			t.Fatalf("внимание %s = %q, хотим %q", r.Watch, r.Attention, want[r.Watch])
		}
	}
	// Ровно то же решение без запросов: вердикт needs_input всё ещё виден.
	again := []watchRow{{Watch: "watch-3", Verdict: "needs_input"}}
	markAttention(again, nil)
	if again[0].Attention != attentionYes {
		t.Fatalf("needs_input без запросов не отмечен: %+v", again[0])
	}
}

func TestW3141LiveJSONIncludesAttention(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictNeedsInput, 4, "ses-1"),
		}}, nil
	})
	src := &w3141Source{sessions: []opencodeSession{w3141Session("ses-1", "developer", "retry")}}
	h := w3141Start(t, src, "--format", "json")
	h.w3141Renders(t, 1)
	if code := h.w3141Stop(t); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, h.errOut.String())
	}
	if !json.Valid(h.out.Bytes()) {
		t.Fatalf("stdout не JSON: %q", h.out.String())
	}
	var rows []watchRow
	if err := json.Unmarshal(h.out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Attention != attentionYes || rows[0].Verdict != "needs_input" {
		t.Fatalf("неверная строка: %+v", rows)
	}
}

// T5.58: the live JOIN carries the ADR 0094 registry axes.

func TestT558JoinWatchesCarriesRegistryAxes(t *testing.T) {
	snap := mcp.StateSnapshot{
		ActiveWatches: []domain.Watch{
			w69Watch(domain.VerdictReady, 7, "ses-1", "ses-unknown"),
		},
		Registry: []mcp.RegistryEntry{
			{
				ID:           "ses-1",
				Role:         domain.RoleExecutor,
				Capabilities: []domain.Capability{"watch", "ci-retry"},
				Config:       "cfg-1",
				Status:       mcp.RegistryActive,
			},
		},
	}

	rows := joinWatches(snap, nil)
	if len(rows) != 2 {
		t.Fatalf("строк %d, ожидалось 2", len(rows))
	}
	if rows[0].Role != string(domain.RoleExecutor) ||
		strings.Join(rows[0].Capabilities, ",") != "watch,ci-retry" ||
		rows[0].Config != "cfg-1" {
		t.Fatalf("строка с реестром = %+v", rows[0])
	}
	// A subscriber without a registry row stays visible with empty axes.
	if rows[1].Role != "" || len(rows[1].Capabilities) != 0 || rows[1].Config != "" {
		t.Fatalf("строка без реестра = %+v", rows[1])
	}
}

func TestT558JoinWatchesWithoutRegistryKeepsPreviousShape(t *testing.T) {
	snap := mcp.StateSnapshot{ActiveWatches: []domain.Watch{w69Watch(domain.VerdictReady, 7, "ses-1")}}
	rows := joinWatches(snap, nil)
	if len(rows) != 1 || rows[0].Role != "" || len(rows[0].Capabilities) != 0 || rows[0].Config != "" {
		t.Fatalf("снимок без реестра дал строки %+v", rows)
	}
}

func TestT558LiveTableShowsRegistryColumns(t *testing.T) {
	rows := []watchRow{
		{
			Watch: "watch-1", Verdict: "ready", Revision: 7, Subscriber: "ses-1",
			Role: string(domain.RoleExecutor), Capabilities: []string{"watch", "ci-retry"}, Config: "cfg-1",
		},
		{Watch: "watch-2", Verdict: "ready", Revision: 3, Subscriber: "ses-2"},
	}
	var out bytes.Buffer
	renderLiveTable(&out, rows)
	text := out.String()

	for _, column := range []string{"ROLE", "CAPS", "CONFIG"} {
		if !strings.Contains(text, column) {
			t.Fatalf("нет колонки %s: %q", column, text)
		}
	}
	matched := w69LineContaining(text, "watch-1")
	if !strings.Contains(matched, string(domain.RoleExecutor)) ||
		!strings.Contains(matched, "watch,ci-retry") || !strings.Contains(matched, "cfg-1") {
		t.Fatalf("строка с реестром = %q", matched)
	}
	unmatched := strings.Fields(w69LineContaining(text, "watch-2"))
	if len(unmatched) != 10 {
		t.Fatalf("строка без реестра имеет %d полей, ожидалось 10: %v", len(unmatched), unmatched)
	}
	for i := 7; i < 10; i++ {
		if unmatched[i] != "-" {
			t.Fatalf("поле %d строки без реестра = %q, ожидался dash: %v", i, unmatched[i], unmatched)
		}
	}
}

func TestT558JSONCarriesRegistryAxes(t *testing.T) {
	rows := []watchRow{{
		Watch: "watch-1", Verdict: "ready", Revision: 7, Subscriber: "ses-1",
		Role: string(domain.RoleExecutor), Capabilities: []string{"watch"}, Config: "cfg-1",
	}}
	var out bytes.Buffer
	if err := renderWatchJSON(&out, rows); err != nil {
		t.Fatalf("json: %v", err)
	}
	var decoded []watchRow
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != 1 || decoded[0].Role != string(domain.RoleExecutor) ||
		strings.Join(decoded[0].Capabilities, ",") != "watch" || decoded[0].Config != "cfg-1" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestT558LiveReconnectRereadsRegistryColumns(t *testing.T) {
	w69Snapshot(t, func() (mcp.StateSnapshot, error) {
		return mcp.StateSnapshot{
			ActiveWatches: []domain.Watch{w69Watch(domain.VerdictReady, 7, "ses-1")},
			Registry: []mcp.RegistryEntry{{
				ID: "ses-1", Role: domain.RoleExecutor,
				Capabilities: []domain.Capability{"watch"}, Config: "cfg-1",
			}},
		}, nil
	})
	events := make(chan liveEvent, 1)
	events <- liveEvent{Type: "session.status"}
	close(events)
	src := &w3141Source{
		sessions: []opencodeSession{w3141Session("ses-1", "developer", "idle")},
		channels: []chan liveEvent{events},
	}
	h := w3141Start(t, src)

	// Подключение, событие и reconnect после обрыва SSE: три перечитывания.
	h.w3141Renders(t, 3)
	if code := h.w3141Stop(t); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, h.errOut.String())
	}
	text := h.out.String()
	for _, column := range []string{"ROLE", "CAPS", "CONFIG", string(domain.RoleExecutor), "watch", "cfg-1"} {
		if !strings.Contains(text, column) {
			t.Fatalf("нет %s после reconnect: %q", column, text)
		}
	}
	if strings.Count(text, "cfg-1") < 2 {
		t.Fatalf("таблица не перечитана после обрыва SSE: %q", text)
	}
}
