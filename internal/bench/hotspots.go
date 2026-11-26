package bench

import (
	"context"
	"encoding/json"
	"fmt"

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

// runSQLiteSnapshot issues the seven reads SnapshotService.Snapshot performs.
// Work is the number of rows returned across all reads. Errors are failed
// calls. The active and recent reads use the covering indexes added for those
// queries, so the set matches the indexed production shape. The dispatch read
// ListReadyTasks is gone; ListRecent and CurrentIncident are part of a snapshot.
func runSQLiteSnapshot(e *env) (int64, int64) {
	var work, errs int64
	active, err := e.store.ListActive(e.ctx)
	if err != nil {
		errs++
	} else {
		work += int64(len(active))
	}
	recent, err := e.store.ListRecent(e.ctx, app.DefaultRecentLimit)
	if err != nil {
		errs++
	} else {
		work += int64(len(recent))
	}
	events, err := e.store.ListRunEvents(e.ctx, e.ids)
	if err != nil {
		errs++
	} else {
		work += int64(len(events))
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
	queue, err := e.store.ListQueueTasks(e.ctx)
	if err != nil {
		errs++
	} else {
		work += int64(len(queue))
	}
	incident, err := e.store.CurrentIncident(e.ctx)
	if err != nil {
		errs++
	} else if incident != nil {
		work++
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

// promptFixtures builds the tasks the standalone prompt benchmark times. Some
// tasks drop the base branch and the worker override so both optional branches
// of spawnPrompt run. The fixture is built once outside the timed loop.
func promptFixtures() []core.Task {
	tasks := taskFixtures(fixtureTasks)
	for i := range tasks {
		if i%3 == 0 {
			tasks[i].BaseBranch = ""
		}
		if i%4 == 0 {
			tasks[i].WorkerPrompt = ""
		}
	}
	return tasks
}

// runPromptBuild builds one prompt per fixture task through the same exported
// entry point the dispatcher uses. Work counts prompt bytes; an empty prompt
// is an error. Both counts stay fixed across runs because the tasks are fixed.
func runPromptBuild(tasks []core.Task) (int64, int64) {
	var work, errs int64
	for i, task := range tasks {
		instructions := ""
		if i%2 == 0 {
			instructions = "Operator instructions for the worker."
		}
		prompt := app.BuildSpawnPrompt(task, core.RunID(fmt.Sprintf("run-%04d", i)), "/ws/"+string(task.ID), instructions)
		if prompt == "" {
			errs++
		}
		work += int64(len(prompt))
	}
	return work, errs
}

// SSE fanout subcases. Each monitor subscribes to both harness frames and PR
// changes; one monitor stays slow. One event is delivered at a time and the
// fast views are drained before the next, so every run performs the same work.
const (
	sseFanoutEvents = 64
	sseSmallSubs    = 8
	sseLargeSubs    = 64
)

// fanoutCounts holds the marshal, delivery and drop totals of one benchmark
// invocation. takeFanoutCounts returns them and resets the accumulator, so
// each printed report shows only the invocation it belongs to.
var fanoutCounts struct {
	marshal   int64
	delivered int64
	dropped   int64
}

func takeFanoutCounts() (int64, int64, int64) {
	out := [3]int64{fanoutCounts.marshal, fanoutCounts.delivered, fanoutCounts.dropped}
	fanoutCounts.marshal, fanoutCounts.delivered, fanoutCounts.dropped = 0, 0, 0
	return out[0], out[1], out[2]
}

// runSSEFanout mirrors one eventFrames plus changeSubscribers broadcast with
// subs monitors. The first monitor is slow: both of its channels are full, so
// every send to it drops. The other monitors drain after each event. Work is
// the number of marshals and successful deliveries; errors are drops.
func runSSEFanout(e *env, subs int) (int64, int64) {
	frames := newEventFrames(json.Marshal)
	changes := newChangeSubscribers()
	slowFrames, unsubSlowFrames := frames.subscribe()
	defer unsubSlowFrames()
	slowChanges, unsubSlowChanges := changes.subscribe()
	defer unsubSlowChanges()
	for i := 0; i < subscriberBuffer; i++ {
		slowFrames <- nil
		slowChanges <- 0
	}
	fastFrames := make([]<-chan []byte, 0, subs-1)
	fastChanges := make([]<-chan int, 0, subs-1)
	for i := 1; i < subs; i++ {
		frameView, unsubFrames := frames.subscribe()
		defer unsubFrames()
		fastFrames = append(fastFrames, frameView)
		changeView, unsubChanges := changes.subscribe()
		defer unsubChanges()
		fastChanges = append(fastChanges, changeView)
	}
	var work, errs int64
	for i := 0; i < sseFanoutEvents; i++ {
		run := e.wire[i%len(e.wire)]
		ev := core.HarnessEvent{Kind: string(core.EvPRRegistered), RunID: run.ID}
		_, frameDelivered, frameDropped := frames.deliver(ev)
		changeDelivered, changeDropped := changes.deliver(run.PRNumber)
		work += 1 + frameDelivered + changeDelivered
		errs += frameDropped + changeDropped
		fanoutCounts.marshal++
		fanoutCounts.delivered += frameDelivered + changeDelivered
		fanoutCounts.dropped += frameDropped + changeDropped
		for _, ch := range fastFrames {
			<-ch
		}
		for _, ch := range fastChanges {
			<-ch
		}
	}
	return work, errs
}

// runFanout measures the production SSE broadcasters at 8 and 64 subscribers.
// Work and error totals cover both subcases, so five alternating runs report
// identical counts.
func runFanout(e *env) (int64, int64) {
	var work, errs int64
	for _, subs := range []int{sseSmallSubs, sseLargeSubs} {
		w, err := runSSEFanout(e, subs)
		work += w
		errs += err
	}
	return work, errs
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
