package main

import (
	"bytes"
	"encoding/json"
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

func t3114AlivePID() func(int) bool { return func(pid int) bool { return pid == 222 } }

func TestT3114DryRunListsStaleWithoutRemoving(t *testing.T) {
	t3114Restore(t)
	gcReadState = func() (gcState, error) { return t3114State(), nil }
	calls, _ := t3114Write(t, nil)
	gcProcessAlive = t3114AlivePID()
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
	gcProcessAlive = t3114AlivePID()
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
	gcProcessAlive = t3114AlivePID()
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
	gcProcessAlive = t3114AlivePID()
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
