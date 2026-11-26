package bench

import (
	"context"
	"encoding/json"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/internal/infra/sqlite"
	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
	"github.com/refusedguy/beacon/pkg/core/redact"
)

// env holds the seeded fixtures used by every pass. It is built once outside
// the timed loop so the numbers measure the hot paths, not fixture setup.
type env struct {
	ctx     context.Context
	store   *sqlite.Store
	runs    []*core.Run
	ids     []string
	cards   []string
	wire    []*core.Run
	texts   []secretText
	journal *opJournal
	fanout  *fanoutBench
	cancel  context.CancelFunc
}

// runSQLiteSnapshot reads the same six queries a status snapshot issues. Work
// is the number of rows returned across all queries. Errors are failed calls.
func runSQLiteSnapshot(e *env) (int64, int64) {
	var work, errs int64
	active, err := e.store.ListActive(e.ctx)
	if err != nil {
		errs++
	} else {
		work += int64(len(active))
	}
	queue, err := e.store.ListQueueTasks(e.ctx)
	if err != nil {
		errs++
	} else {
		work += int64(len(queue))
	}
	decisions, err := e.store.ListDecisions(e.ctx, e.ids, app.DefaultDecisionsLimit, app.DefaultOrphanDecisionsLimit)
	if err != nil {
		errs++
	} else {
		work += int64(len(decisions))
	}
	counts, err := e.store.DecisionCounts(e.ctx, e.ids)
	if err != nil {
		errs++
	} else {
		work += int64(len(counts))
	}
	events, err := e.store.ListRunEvents(e.ctx, e.ids)
	if err != nil {
		errs++
	} else {
		work += int64(len(events))
	}
	ready, err := e.store.ListReadyTasks(e.ctx)
	if err != nil {
		errs++
	} else {
		work += int64(len(ready))
	}
	return work, errs
}

// runCardParse parses every fixture card the way a task source refresh does.
// Work counts parsed scope, dependency and plan entries.
func runCardParse(e *env) (int64, int64) {
	var work, errs int64
	for _, card := range e.cards {
		fields := tasksource.ParseTaskFields(card, "docs/tasks", "")
		if fields.ID == "" {
			errs++
		}
		work += int64(len(fields.Scope) + len(fields.Depends) + len(fields.Plan))
	}
	return work, errs
}

// runWireEncode builds and encodes the three loopback payload shapes the
// daemon writes most often. Work counts encoded bytes.
func runWireEncode(e *env) (int64, int64) {
	var work, errs int64
	wires := core.NewWireRuns(e.wire)
	b, err := json.Marshal(wires)
	if err != nil {
		errs++
	}
	work += int64(len(b))
	event := core.NewWireEvent(core.HarnessEvent{Kind: string(core.EvPRRegistered), RunID: e.wire[0].ID})
	b, err = json.Marshal(event)
	if err != nil {
		errs++
	}
	work += int64(len(b))
	result := core.NewWireControlResult(core.ControlApprove, e.wire[0].ID)
	b, err = json.Marshal(result)
	if err != nil {
		errs++
	}
	work += int64(len(b))
	return work, errs
}

// runRedact applies the full rule chain to every fixture text. Work counts
// output bytes. A text that should contain a secret but is returned unchanged
// counts as an error.
func runRedact(e *env) (int64, int64) {
	var work, errs int64
	for _, item := range e.texts {
		out := redact.Redact(item.value)
		if item.redacted && out == item.value {
			errs++
		}
		work += int64(len(out))
	}
	return work, errs
}

// runJournal exercises a mix of fresh operations and duplicate operation ids.
// Work counts completed calls.
func runJournal(e *env) (int64, int64) {
	var work, errs int64
	for i := 0; i < 64; i++ {
		id := "op-" + string(rune('a'+i%26)) + "-" + itoa(i%16)
		if _, owner := e.journal.claim(id); owner {
			e.journal.finish(id, nil, nil)
		}
		if _, ok := e.journal.cached(id); !ok {
			errs++
		}
		work++
	}
	return work, errs
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// runFanout pushes events through the fanout mirror. Two views drain every
// event and the third stays full, so the slow-view drop is measured too. Work
// counts attempted deliveries (events times views). Errors count drops on the
// slow view.
func runFanout(e *env) (int64, int64) {
	const events = 64
	slowBefore := len(e.fanout.views[2])
	// Send one event at a time and drain both fast views before the next send.
	// Flooding src would fill their 16-slot buffers and the production drop
	// policy would discard events the reader is still waiting for.
	for i := 0; i < events; i++ {
		e.fanout.src <- i
		<-e.fanout.views[0]
		<-e.fanout.views[1]
	}
	dropped := events - (len(e.fanout.views[2]) - slowBefore)
	return events * int64(len(e.fanout.views)), int64(dropped)
}

// fakeSnapshotStore carries rows read once from SQLite so the assembly phase
// can be timed without paying for SQL again. It implements app.SnapshotStore.
// Queue and incident reads are intentionally absent, which matches the deep
// fixture: it seeds no task cards and no base incident.
type fakeSnapshotStore struct {
	active    []*core.Run
	recent    []*core.Run
	events    []app.RunEvent
	decisions []app.Decision
	counts    []app.DecisionCount
}

func (f fakeSnapshotStore) ListActive(context.Context) ([]*core.Run, error) {
	return f.active, nil
}

func (f fakeSnapshotStore) ListRecent(context.Context, int) ([]*core.Run, error) {
	return f.recent, nil
}

func (f fakeSnapshotStore) ListRunEvents(context.Context, []string) ([]app.RunEvent, error) {
	return f.events, nil
}

func (f fakeSnapshotStore) ListDecisions(context.Context, []string, int, int) ([]app.Decision, error) {
	return f.decisions, nil
}

func (f fakeSnapshotStore) DecisionCounts(context.Context, []string) ([]app.DecisionCount, error) {
	return f.counts, nil
}

// readAssemblyRows pulls the rows SnapshotService reads once from the real
// store. The returned ids are the active and recent run ids in the same order,
// so the reads phase and the assembly phase work on the same set.
func readAssemblyRows(ctx context.Context, store app.SnapshotStore) (fakeSnapshotStore, []string, error) {
	var out fakeSnapshotStore
	var err error
	if out.active, err = store.ListActive(ctx); err != nil {
		return out, nil, err
	}
	if out.recent, err = store.ListRecent(ctx, app.DefaultRecentLimit); err != nil {
		return out, nil, err
	}
	ids := make([]string, 0, len(out.active)+len(out.recent))
	for _, run := range out.active {
		ids = append(ids, string(run.ID))
	}
	for _, run := range out.recent {
		ids = append(ids, string(run.ID))
	}
	if out.events, err = store.ListRunEvents(ctx, ids); err != nil {
		return out, nil, err
	}
	if out.decisions, err = store.ListDecisions(ctx, ids, app.DefaultDecisionsLimit, app.DefaultOrphanDecisionsLimit); err != nil {
		return out, nil, err
	}
	if out.counts, err = store.DecisionCounts(ctx, ids); err != nil {
		return out, nil, err
	}
	return out, ids, nil
}

// snapshotAssembly is one full snapshot assembly result. Work counts the runs
// and history entries put into the snapshot, body is the encoded JSON and errs
// is one when assembly or encoding fails.
type snapshotAssembly struct {
	work int64
	errs int64
	body []byte
}

// assembleSnapshot runs the work the SQLite benchmark omits. It builds the
// operator snapshot through SnapshotService.Snapshot and encodes it with
// json.Marshal, the call printJSON uses for `beacon status --snapshot`
// (cmd/beacon/main.go:504). A fake store holding pre-read rows keeps SQL reads
// out of the timed phase.
func assembleSnapshot(ctx context.Context, svc app.SnapshotService) snapshotAssembly {
	snap, err := svc.Snapshot(ctx)
	if err != nil {
		return snapshotAssembly{errs: 1}
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return snapshotAssembly{errs: 1}
	}
	return snapshotAssembly{work: snapshotWork(snap), body: body}
}

// snapshotWork counts the objects a snapshot assembles. A change that drops
// history from the output cannot hide behind a flat timing number.
func snapshotWork(snap *app.Snapshot) int64 {
	work := int64(len(snap.Active) + len(snap.Recent) + len(snap.Queue))
	for _, group := range [][]app.RunSnapshot{snap.Active, snap.Recent} {
		for _, run := range group {
			work += int64(len(run.Timeline) + len(run.Decisions) + len(run.Checks))
		}
	}
	return work
}
