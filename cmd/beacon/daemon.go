package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/refusedguy/beacon/internal/app/notify"
	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/app/tasks"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/taskstore"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// daemonIntervalEnv overrides the default pass interval without a flag. It is
// kept for the deprecated in-process daemon seam; the unified loop uses the
// adaptive github.Interval unless --interval is given explicitly.
const daemonIntervalEnv = "BEACON_DAEMON_INTERVAL"

// daemonIntervalDefault is the value passed to --interval when the flag is
// absent. Zero selects the adaptive cadence from github.Interval: 10s while a
// Watch changed recently, 30s once it is quiet.
const daemonIntervalDefault = 0

// daemonIntervalDescription is the single description of that default. The
// flag help and the CLI usage text reuse it, and README.md mirrors it.
const daemonIntervalDescription = "0 = адаптивный 10s/30s"

// daemonActorErrorCount reads the actor error counter of the process. The
// reconciler reports a failed pass through observe.ActorError instead of a
// return value, so a rising counter is the observable signal of that failure.
func daemonActorErrorCount() uint64 {
	metrics := observe.MetricsOf(observe.Default())
	if metrics == nil {
		return 0
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(metrics.SnapshotJSON(), &snapshot); err != nil {
		return 0
	}
	raw, ok := snapshot[observe.CounterActorErrors]
	if !ok {
		return 0
	}
	var count uint64
	if err := json.Unmarshal(raw, &count); err != nil {
		return 0
	}
	return count
}

// daemonOnceExitCode turns the actor error counts around one pass into a
// process exit code. New actor errors mean the pass failed, so the command
// exits with code 1 instead of hiding the failure behind a zero.
func daemonOnceExitCode(before, after uint64) int {
	if after > before {
		return 1
	}
	return 0
}

// daemonRunner holds the command implementation so dispatch stays a one-line
// wiring change and tests can replace it like the other commands.
var daemonRunner = func(c *CLI, args []string) int { return runDaemon(c, args) }

// RegisterDaemon installs the daemon command implementation.
func RegisterDaemon(fn func(*CLI, []string) int) { daemonRunner = fn }

// ---------------------------------------------------------------------------
// Notify routing for daemon verdict delivery (WIRING-485)
// ---------------------------------------------------------------------------

// daemonNotifyResolver is a concrete notify.Resolver for one Watch. It maps a
// verdict event to the single session that owns the Watch. A recipient that is
// not that session, or is not a session id, has no route and delivers nothing.
type daemonNotifyResolver struct {
	subscriberID string
}

func (r daemonNotifyResolver) Resolve(_ context.Context, key notify.Key) ([]notify.Target, error) {
	if r.subscriberID == "" || key.Recipient != r.subscriberID {
		return nil, nil
	}
	if orchestrator.ValidateWakeTarget(key.Recipient) != nil {
		return nil, nil
	}
	return []notify.Target{notify.Target(key.Recipient)}, nil
}

// daemonNotifyDeliverer adapts the wake target port to notify.Deliverer. The
// payload is the identifier-only verdict text; the wake package owns transport.
type daemonNotifyDeliverer struct {
	deliver wake.TargetDeliverer
}

func (d daemonNotifyDeliverer) Deliver(ctx context.Context, n notify.Notification) error {
	return d.deliver(ctx, string(n.Target), string(n.Payload))
}

// newDaemonNotifyRouter builds the Router used by the daemon delivery path.
// Both ports are concrete: the resolver reads the Watch projection and the
// deliverer uses the wake prompt port.
func newDaemonNotifyRouter(subscriberID string, deliver wake.TargetDeliverer) *notify.Router {
	return notify.New(
		daemonNotifyResolver{subscriberID: subscriberID},
		daemonNotifyDeliverer{deliver: deliver},
	)
}

// routeDaemonEvent is the event subscriber. It hands one verdict event to the
// router and reports a delivery failure as an error. A Watch without a route
// delivers nothing.
func routeDaemonEvent(ctx context.Context, router *notify.Router, verdict, recipient, text string) error {
	report, err := router.Route(ctx, notify.Event{
		Type:      verdict,
		Recipient: recipient,
		Payload:   []byte(text),
	})
	if err != nil {
		return fmt.Errorf("beacon daemon: notify route: %w", err)
	}
	if len(report.Failures) > 0 {
		return fmt.Errorf("beacon daemon: notify delivery to %s: %w",
			report.Failures[0].Target, report.Failures[0].Err)
	}
	return nil
}

// daemonWakeRoute is the WakeRoute handed to the orchestrator. It builds one
// Router per Watch so the resolver and the deliverer see the current
// subscriber.
func daemonWakeRoute(deliver wake.TargetDeliverer) orchestrator.WakeRoute {
	return func(ctx context.Context, w orchestrator.DaemonWatch, _ uint64, verdict, text string) error {
		router := newDaemonNotifyRouter(w.SubscriberID, deliver)
		return routeDaemonEvent(ctx, router, verdict, w.SubscriberID, text)
	}
}

// daemonClaimSource reads the durable claim of the executor's obligation for
// one (watchID, revision, subscriberID). A stored claimedAt proves an earlier
// wake was already acknowledged, which matters after a daemon restart empties
// the in-memory dedupe (Q-B-04).
type daemonClaimSource interface {
	ClaimedAt(watchID string, revision uint64, subscriberID string) time.Time
}

// dbClaimSource adapts the daemon's existing store handle to daemonClaimSource.
// It opens no second connection and reads the obligation history by watchID.
type dbClaimSource struct{ db *store.DB }

func (s dbClaimSource) ClaimedAt(watchID string, revision uint64, subscriberID string) time.Time {
	if s.db == nil || watchID == "" || subscriberID == "" {
		return time.Time{}
	}
	obligations, err := s.db.WatchObligations(watchID)
	if err != nil {
		return time.Time{}
	}
	for _, o := range obligations {
		if o.Revision == revision && o.SubscriberID == subscriberID {
			return o.ClaimedAt
		}
	}
	return time.Time{}
}

// daemonArtifactVerifier is the wiring-side ArtifactVerifier for slot tokens.
// It answers whether the artifact behind a wake has appeared: a PR for the
// watch, or PLAN.md for the card linked to the watch. Both reads reuse handles
// the daemon already owns, so this adapter opens no new connection.
type daemonArtifactVerifier struct {
	db   *store.DB
	root string
}

func (v *daemonArtifactVerifier) Verified(ctx context.Context, token orchestrator.SlotToken, artifact orchestrator.SlotArtifact) (bool, error) {
	if artifact.PR > 0 {
		if v.db == nil {
			return false, nil
		}
		w, err := v.db.Watch(token.WatchID)
		if err != nil {
			return false, err
		}
		return w.PR > 0 && w.PR == artifact.PR, nil
	}
	if strings.TrimSpace(artifact.Plan) == "" || v.db == nil {
		return false, nil
	}
	card, err := linkedCardID(ctx, v.db, token.WatchID)
	if err != nil || card == "" {
		return false, err
	}
	path := filepath.Join(v.root, "docs", "tasks", card, "PLAN.md")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// daemonWakeGate is the second line of wake defence. The slot ledger opens at
// most one slot per (watchID, revision) in this process, so a repeated
// completion signal cannot open a second one. The claimedAt check is the
// durable line: a claim taken before a restart still suppresses the wake.
// Tokens issued here are kept until a daemon pass verifies their artifact and
// burns them, so a slot is never held past the work it authorized.
type daemonWakeGate struct {
	mu       sync.Mutex
	ledger   *orchestrator.SlotLedger
	claims   daemonClaimSource
	verifier orchestrator.ArtifactVerifier
	tokens   map[string]orchestrator.SlotToken
}

func newDaemonWakeGate(claims daemonClaimSource, verifier orchestrator.ArtifactVerifier) *daemonWakeGate {
	return &daemonWakeGate{
		ledger:   orchestrator.NewSlotLedger(),
		claims:   claims,
		verifier: verifier,
		tokens:   map[string]orchestrator.SlotToken{},
	}
}

// claimConsumed reports whether the executor already acknowledged this
// revision. The check is durable, so it survives a daemon restart that empties
// both the waker's in-memory dedupe and this gate's ledger.
func (g *daemonWakeGate) claimConsumed(w orchestrator.DaemonWatch, revision uint64) bool {
	return g.claims != nil && !g.claims.ClaimedAt(w.ID, revision, w.SubscriberID).IsZero()
}

// open records one slot for a successfully delivered wake. A failed delivery
// leaves the slot closed, so the next daemon pass may retry it. The token is
// retained so a later pass can verify its artifact and burn the slot.
func (g *daemonWakeGate) open(w orchestrator.DaemonWatch, revision uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	token, inc := g.ledger.Issue(orchestrator.SignalReportToOrchestrator, w.ID, revision, w.PR)
	if inc != nil {
		g.recordIncident(inc)
		return
	}
	if g.tokens == nil {
		g.tokens = map[string]orchestrator.SlotToken{}
	}
	g.tokens[token.Key()] = token
}

// isLive is the locked read used by the wake route before it opens a slot.
func (g *daemonWakeGate) isLive(w orchestrator.DaemonWatch, revision uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ledger.IsLive(w.ID, revision)
}

// burn verifies and extinguishes every live slot token. A verified artifact
// frees the slot; a missing artifact or a verifier error leaves it held, so
// the next pass tries again. A burn without a live token is a
// spawn_without_token incident recorded in observe.
func (g *daemonWakeGate) burn(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.verifier == nil {
		return
	}
	for key, token := range g.tokens {
		artifact := orchestrator.SlotArtifact{PR: token.PR}
		if token.PR <= 0 {
			artifact.Plan = "PLAN.md"
		}
		burned, inc := g.ledger.Burn(ctx, token, artifact, g.verifier)
		if inc != nil {
			g.recordIncident(inc)
			continue
		}
		if burned {
			delete(g.tokens, key)
		}
	}
}

func (g *daemonWakeGate) recordIncident(inc *orchestrator.SlotIncident) {
	observe.Event(observe.LevelWarn, "slot_incident", observe.Fields{
		"kind":     string(inc.Kind),
		"watch":    inc.WatchID,
		"revision": inc.Revision,
		"detail":   inc.Detail,
	})
}

// daemonWakeRouteWithGate wraps daemonWakeRoute with the one-slot gate. The
// plain route keeps its contract for callers that do not have a store handle.
func daemonWakeRouteWithGate(deliver wake.TargetDeliverer, gate *daemonWakeGate) orchestrator.WakeRoute {
	route := daemonWakeRoute(deliver)
	return func(ctx context.Context, w orchestrator.DaemonWatch, revision uint64, verdict, text string) error {
		if gate.claimConsumed(w, revision) || gate.isLive(w, revision) {
			return nil
		}
		if err := route(ctx, w, revision, verdict, text); err != nil {
			return err
		}
		gate.open(w, revision)
		return nil
	}
}

// ---------------------------------------------------------------------------
// Session heartbeat (WIRING-488)
// ---------------------------------------------------------------------------

// sessionHeartbeater is the slice of the durable session registry the daemon
// uses. The app-layer *session.SessionRegistry satisfies it; tests install a
// recorder to assert the Touch call without a real store.
type sessionHeartbeater interface {
	Heartbeat(id string) error
}

// daemonHeartbeatInterval is how often a running daemon marks its session
// alive in the durable registry. It is a variable so a test can shorten it.
var daemonHeartbeatInterval = 30 * time.Second

// daemonReminderInterval is how often the daemon runs a deadline reminder
// pass. It is a variable so a test can shorten it.
var daemonReminderInterval = time.Minute

// daemonHeartbeat marks one session alive. This is the single call that binds
// the daemon cycle to the durable session registry: Heartbeat moves last_seen
// through the store's Touch. An empty id or a missing port is skipped, so a
// daemon without a bound orchestrator session simply does nothing here.
func daemonHeartbeat(hb sessionHeartbeater, sessionID string) error {
	id := strings.TrimSpace(sessionID)
	if hb == nil || id == "" {
		return nil
	}
	return hb.Heartbeat(id)
}

// newDaemonSessionRegistry adapts the daemon's already-open store handle to
// the session registry. It opens no second connection and owns no schema.
func newDaemonSessionRegistry(db *store.DB) *session.SessionRegistry {
	return session.NewSessionRegistry(newSessionDBAdapter(db))
}

// ensureDaemonSession records the daemon's own session in the durable registry
// so the periodic Heartbeat has a row to touch. An existing row is left alone,
// so a session started elsewhere keeps its metadata and status.
func ensureDaemonSession(reg *session.SessionRegistry, sessionID string) error {
	id := strings.TrimSpace(sessionID)
	if reg == nil || id == "" {
		return nil
	}
	if _, found, err := reg.Resume(id); err != nil || found {
		return err
	}
	return reg.Start(id, store.SessionStatusActive, nil)
}

// daemonHeartbeatEffectiveInterval resolves the production heartbeat interval.
// A non-positive value falls back to daemonHeartbeatInterval. Keeping this as a
// pure function lets a test verify the fallback without waiting on a ticker.
func daemonHeartbeatEffectiveInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return daemonHeartbeatInterval
	}
	return interval
}

// runDaemonHeartbeat consumes ticks until ctx is done and calls daemonHeartbeat
// on each one. The caller owns the tick source and stops it. A registry failure
// is logged and never stops the daemon cycle.
func runDaemonHeartbeat(ctx context.Context, hb sessionHeartbeater, sessionID string, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if err := daemonHeartbeat(hb, sessionID); err != nil {
				observe.ActorError("session-heartbeat", "", err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Deadline reminders (WIRING-489)
// ---------------------------------------------------------------------------

// daemonReminderStore builds the store adapter that feeds the app notifier.
// The loader reads active Watches from the daemon's existing store handle and
// keeps those whose deadline has passed. The deliverer emits one observe event
// per reminder, which is the configured log and metric sink. The card left the
// delivery channel open; the structured event is a channel that needs no new
// transport or schema.
func daemonReminderStore(db *store.DB) *taskstore.ReminderStore {
	load := func(_ context.Context, at time.Time) ([]taskstore.ReminderTask, error) {
		if db == nil {
			return nil, nil
		}
		watches, err := db.ActiveWatches()
		if err != nil {
			return nil, err
		}
		out := make([]taskstore.ReminderTask, 0, len(watches))
		for _, w := range watches {
			if w.DeadlineAt.IsZero() || w.DeadlineAt.After(at) {
				continue
			}
			out = append(out, taskstore.ReminderTask{ID: w.ID, DeadlineAt: w.DeadlineAt})
		}
		return out, nil
	}
	deliver := func(_ context.Context, r taskstore.Reminder) error {
		observe.Event(observe.LevelInfo, "deadline_reminder", observe.Fields{
			"task":     r.TaskID,
			"reason":   r.Reason,
			"at":       r.At,
			"deadline": r.DeadlineAt,
		})
		return nil
	}
	return taskstore.NewReminderStore(load, deliver)
}

// daemonReminderNotify wraps one notifier pass as a loop callback. A delivery
// failure becomes an error so the loop's reporter records it; the notifier
// still retries the reminder on its next pass.
func daemonReminderNotify(n *tasks.Notifier) orchestrator.NotifyFunc {
	return func(ctx context.Context) error {
		report, err := n.Notify(ctx)
		if err != nil {
			return err
		}
		if len(report.Failed) > 0 {
			return fmt.Errorf("beacon daemon: %d reminder deliveries failed", len(report.Failed))
		}
		return nil
	}
}

// runDaemonReminders builds the notifier over the store adapter and ticks until
// ctx is done. It reuses the store handle the daemon already opened.
func runDaemonReminders(ctx context.Context, db *store.DB, interval time.Duration) {
	notifier := tasks.NewNotifier(daemonReminderStore(db), time.Now)
	orchestrator.RunNotifyLoop(ctx, daemonReminderNotify(notifier), interval)
}

// runDaemon implements `beacon daemon`. The pass interval uses
// daemonIntervalDefault and daemonIntervalDescription, so the flag and the
// CLI usage text describe the same value. It runs the same single cibridge
// cycle `beacon mcp` uses, under the same non-blocking poller lock, so
// starting both processes cannot produce duplicate wakes.
func runDaemon(c *CLI, args []string) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	interval := fs.Duration("interval", daemonIntervalDefault, "интервал прохода ("+daemonIntervalDescription+")")
	once := fs.Bool("once", false, "выполнить один проход и выйти")
	if !parseFlags("daemon", args, c.errOut, fs) {
		return 2
	}
	if *interval < 0 {
		fmt.Fprintf(c.errOut, "beacon daemon: интервал не может быть отрицательным\n")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}

	// HD-071: a stale or unpinned skill hash is reported on every pass. The
	// finding goes to stderr and to the actor metric, so a changed skill is
	// visible in the gate instead of passing as a silent config change.
	findings, lockErr := verifySkillsLock(projectRootForConfig())
	if lockErr != nil {
		observe.ActorError("skills-lock", "", lockErr)
		fmt.Fprintf(c.errOut, "beacon daemon: skills lock: %v\n", lockErr)
	}
	for _, finding := range findings {
		observe.ActorError("skills-lock", "", fmt.Errorf("%s: %s", finding.Name, finding.Problem))
		fmt.Fprintf(c.errOut, "beacon daemon: skills lock: %s: %s\n", finding.Name, finding.Problem)
	}

	db, err := openProcessStore(dir)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}
	defer func() { _ = db.Close() }()

	// The card monitor watches task-card status through tasks.Port and appends
	// one event per status change (WIRING-487). The adapter lives in cmd because
	// the layer matrix allows cmd -> internal/app but forbids the reverse edge.
	root := projectRootForConfig()
	cardMonitor := newDaemonCardMonitor(db, root)

	// The daemon marks its bound orchestrator session alive in the durable
	// registry while it runs (WIRING-488). It reuses the store handle opened
	// above, so no second connection is created.
	sessionID := strings.TrimSpace(os.Getenv(sessionIDEnv))
	hb := newDaemonSessionRegistry(db)
	if err := ensureDaemonSession(hb, sessionID); err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: session registry: %v\n", err)
	}

	ghBatch := newGitHubBatch(ctx, c)
	deps := buildMCPDependencies(db, dir, ghBatch.provider.PointStats)
	cfg := reconcileConfig{
		dir:              dir,
		db:               db,
		deps:             deps,
		batch:            ghBatch.provider,
		counter:          ghBatch.counter,
		projectKey:       orchestratorProjectKey(),
		root:             projectRootForConfig(),
		intervalOverride: *interval,
	}

	if *once {
		if err := daemonHeartbeat(hb, sessionID); err != nil {
			fmt.Fprintf(c.errOut, "beacon daemon: heartbeat: %v\n", err)
		}
		errorsBefore := daemonActorErrorCount()
		daemonPollCards(ctx, cardMonitor, db, root)
		ReconcilePass(ctx, cfg)
		if err := daemonReminderNotify(tasks.NewNotifier(daemonReminderStore(db), time.Now))(ctx); err != nil {
			observe.ActorError("reminder", "", err)
		}
		return daemonOnceExitCode(errorsBefore, daemonActorErrorCount())
	}

	stopLoop, err := runReconciler(ctx, cfg)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}
	defer stopLoop()

	hbTicker := time.NewTicker(daemonHeartbeatEffectiveInterval(daemonHeartbeatInterval))
	defer hbTicker.Stop()
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		runDaemonHeartbeat(ctx, hb, sessionID, hbTicker.C)
	}()
	defer func() { <-hbDone }()

	cmDone := make(chan struct{})
	go func() {
		defer close(cmDone)
		runDaemonCardMonitor(ctx, cardMonitor, db, root, daemonCardPollInterval)
	}()
	defer func() { <-cmDone }()

	reminderDone := make(chan struct{})
	go func() {
		defer close(reminderDone)
		runDaemonReminders(ctx, db, daemonReminderInterval)
	}()
	defer func() { <-reminderDone }()

	<-ctx.Done()
	return 0
}

// makeDaemonPorts builds the legacy orchestrator pass ports from one store
// handle. It is kept as a compatibility seam for callers that still assemble
// the older daemon cycle; the production daemon command now uses the unified
// ReconcileLoop and never calls CloseMerged's bridge refresh.
func makeDaemonPorts(c *CLI) (orchestrator.DaemonPorts, func() error, error) {
	dir, err := stateDir()
	if err != nil {
		return orchestrator.DaemonPorts{}, nil, err
	}
	db, err := openProcessStore(dir)
	if err != nil {
		return orchestrator.DaemonPorts{}, nil, err
	}

	txWriter, ok := any(db).(orchestrator.TxWriter)
	if !ok {
		_ = db.Close()
		return orchestrator.DaemonPorts{}, nil, fmt.Errorf("store does not support verdict writes")
	}

	registry := orchestrator.FuncRegistry{
		FindFn: func(_ context.Context, pr orchestrator.PR) (orchestrator.WatchRef, bool, error) {
			watches, err := db.LiveWatches()
			if err != nil {
				return orchestrator.WatchRef{}, false, err
			}
			for _, w := range watches {
				if w.Repo == pr.Repo && w.PR == pr.Number && w.HeadSHA == pr.HeadSHA {
					subs := make([]orchestrator.DaemonSubscriber, 0, len(w.Subscribers))
					for _, s := range w.Subscribers {
						subs = append(subs, orchestrator.DaemonSubscriber{
							ID: s.ID, Role: string(s.Role), Adapter: s.Adapter, Target: s.Target,
						})
					}
					if _, err := orchestrator.DaemonWakeSubscriber(subs); err != nil && !hasOrchestratorSession(subs) {
						continue
					}
					return orchestrator.WatchRef{
						ID: w.ID, Repo: w.Repo, PR: w.PR, HeadSHA: w.HeadSHA, Revision: w.Revision,
					}, true, nil
				}
			}
			return orchestrator.WatchRef{}, false, nil
		},
		// A Watch belongs to the subscriber that created it through
		// beacon_watch_pr. The daemon only refreshes verdicts of existing
		// Watches and never registers one on its own.
		RegisterFn: func(_ context.Context, pr orchestrator.PR) (orchestrator.WatchRef, error) {
			return orchestrator.WatchRef{}, fmt.Errorf(
				"daemon: Watch для PR %d должен создаваться агентом через beacon_watch_pr", pr.Number)
		},
	}

	orchDeps, err := orchestratorDepsFromDB(c, db)
	if err != nil {
		_ = db.Close()
		return orchestrator.DaemonPorts{}, nil, err
	}

	// Subscribe and Summary share one WorkloadStore built over the same db
	// handle the other ports use. No second SQLite handle is opened.
	workload := daemonWorkloadStore(db)

	// The wake gate issues one slot token per delivered wake. A daemon pass
	// refreshes the watches first, then burns tokens whose PR or PLAN.md has
	// appeared so a slot is not held forever after the first delivery.
	gate := newDaemonWakeGate(dbClaimSource{db: db}, &daemonArtifactVerifier{db: db, root: c.root})

	return orchestrator.DaemonPorts{
		CheckAll: func(ctx context.Context) error {
			_, err := orchestrator.CheckAll(ctx, orchestrator.Deps{
				Source:    orchestrator.NewGHSource(defaultCIRepo, daemonGHRunner(c)),
				Watches:   registry,
				Writer:    orchestrator.NewSQLVerdictWriter(txWriter),
				ConfigDir: c.root,
			})
			gate.burn(ctx)
			return err
		},
		// Subscribe enrolls the orchestrator on every active Watch before the
		// merge pass. RunWorkload is idempotent by role: a Watch that already
		// carries an orchestrator subscriber is left untouched.
		Subscribe: func(ctx context.Context) error {
			_, err := orchestrator.RunWorkload(ctx, workload)
			return err
		},
		Orchestrate: func(ctx context.Context, waker orchestrator.Waker) error {
			_, err := orchestrator.Run(ctx, orchDeps.Store, orchDeps.Merger, waker, orchestrator.Options{})
			return err
		},
		// Summary reads a fresh snapshot after orchestration and hands the
		// report to DaemonRun, which logs the same counters as `beacon workload`.
		Summary: func(ctx context.Context) (orchestrator.Workload, error) {
			watches, err := workload.ActiveWorkloadWatches(ctx)
			if err != nil {
				return orchestrator.Workload{}, err
			}
			return orchestrator.BuildWorkload(watches), nil
		},
		// The single reconciler owns terminal-state refreshes now.
		CloseMerged: func(context.Context) error { return nil },
		Waker: orchestrator.NewDaemonWakerWithBackend(func(id string) (orchestrator.DaemonWatch, error) {
			w, err := db.Watch(id)
			if err != nil {
				return orchestrator.DaemonWatch{}, err
			}
			view := orchestrator.DaemonWatch{ID: w.ID, PR: w.PR}
			subs := make([]orchestrator.DaemonSubscriber, 0, len(w.Subscribers))
			for _, s := range w.Subscribers {
				subs = append(subs, orchestrator.DaemonSubscriber{
					ID: s.ID, Role: string(s.Role), Adapter: s.Adapter, Target: s.Target,
				})
			}
			sub, err := orchestrator.DaemonWakeSubscriber(subs)
			if err != nil {
				return view, nil
			}
			view.Adapter, view.SubscriberID = sub.Adapter, sub.ID
			return view, nil
		}, daemonWakeRouteWithGate(wake.PromptTargetDeliverer(&wake.T202API{}), gate), newDaemonWakeBackend(), wake.SharedDeliveryDedup()),
	}, db.Close, nil
}

// daemonWorkloadStore adapts the daemon's already-open store handle to the
// WorkloadStore port. It reuses the db passed in, so a daemon pass never opens
// a second handle to the same SQLite file or starts a verdict-source poller.
// The projection from a stored Watch stays here in the composition root;
// neither cmd nor the app package names a domain type (ADR 0005 layer matrix).
func daemonWorkloadStore(db *store.DB) orchestrator.WorkloadStore {
	// A nil clock falls back to the wall clock in domain.NewFanout, and a nil
	// CI provider is allowed because RunWorkload always supplies headSHA.
	handler := mcp.NewWatchPRHandler(db, nil, nil)
	return &workloadStore{
		activeFn: func(context.Context) ([]orchestrator.WorkloadWatch, error) {
			all, err := db.ActiveWatches()
			if err != nil {
				return nil, err
			}
			out := make([]orchestrator.WorkloadWatch, 0, len(all))
			for _, w := range all {
				view := orchestrator.WorkloadWatch{
					ID: w.ID, Repo: w.Repo, PR: w.PR, HeadSHA: w.HeadSHA,
					Project: w.Repo, Verdict: string(w.Verdict),
				}
				for _, s := range w.Subscribers {
					view.Subscribers = append(view.Subscribers, orchestrator.WorkloadSubscriber{
						ID: s.ID, Role: string(s.Role),
					})
				}
				out = append(out, view)
			}
			return out, nil
		},
		register: func(ctx context.Context, w orchestrator.WorkloadWatch, sub orchestrator.WorkloadSubscriber) error {
			// RunWorkload only ever enrolls the orchestrator, so the role is a
			// constant here and cmd needs no domain import (layer matrix).
			if sub.Role != orchestrator.RoleOrchestrator {
				return fmt.Errorf("beacon daemon: unsupported subscriber role %q", sub.Role)
			}
			_, err := handler.Register(ctx, nil, mcp.WatchPRInput{
				Repo:    w.Repo,
				PR:      w.PR,
				HeadSHA: w.HeadSHA,
				Role:    "orchestrator",
				Notify:  &mcp.WatchPRNotify{Adapter: "opencode", Target: sub.ID},
			})
			return err
		},
	}
}

// hasOrchestratorSession reports whether subs carry an explicit orchestrator
// session that can receive a blocked escalation.
func hasOrchestratorSession(subs []orchestrator.DaemonSubscriber) bool {
	for _, s := range subs {
		if s.Role != orchestrator.RoleOrchestrator {
			continue
		}
		if orchestrator.ValidateWakeTarget(s.ID) != nil {
			continue
		}
		if s.Target != "" && orchestrator.ValidateWakeTarget(s.Target) != nil {
			continue
		}
		return true
	}
	return false
}

// daemonGHRunner is the single gh seam used by the legacy checkall pass.
func daemonGHRunner(c *CLI) orchestrator.GHRunner {
	return func(args ...string) ([]byte, error) {
		res := c.run(c.root, "gh", args, nil)
		if res.code == 0 {
			return res.stdout, nil
		}
		reason := string(res.stderr)
		if reason == "" {
			reason = string(res.stdout)
		}
		return nil, ghError("gh", runResult{stderr: []byte(reason), code: res.code})
	}
}

// daemonCardPort is the composition-root adapter for tasks.Port. It reads card
// status from the task-card files under <root>/docs/tasks and journal events
// from the state DB the daemon already holds open. The adapter lives in cmd on
// purpose: the layer matrix allows cmd -> internal/app and cmd -> internal/infra
// but forbids internal/infra -> internal/app (ADR 0005).
type daemonCardPort struct {
	db       *store.DB
	tasksDir string
	readFile func(string) ([]byte, error)
}

// Card implements tasks.Port. A missing file means the card is absent; a parse
// failure is an error so a malformed card is visible instead of silently
// treated as a blank baseline.
func (p *daemonCardPort) Card(_ context.Context, id string) (tasks.Card, bool, error) {
	path := taskstore.CardPath(p.tasksDir, filepath.Base(strings.TrimSpace(id)))
	read := p.readFile
	if read == nil {
		read = os.ReadFile
	}
	raw, err := read(path)
	if os.IsNotExist(err) {
		return tasks.Card{}, false, nil
	}
	if err != nil {
		return tasks.Card{}, false, fmt.Errorf("card monitor: read %s: %w", path, err)
	}
	card, err := taskstore.ParseCard(path, raw)
	if err != nil {
		return tasks.Card{}, false, fmt.Errorf("card monitor: parse %s: %w", path, err)
	}
	return tasks.Card{ID: card.ID, Status: card.Status}, true, nil
}

// EventsAfter implements tasks.Port over the scoped event log. Store seq values
// are positive int64, so the uint64 cursor narrows safely for a real journal.
// The store already returns ascending seq greater than cursor; limit caps the
// page after that filter.
func (p *daemonCardPort) EventsAfter(_ context.Context, id string, cursor uint64, limit int) ([]tasks.RecordedEvent, error) {
	out := make([]tasks.RecordedEvent, 0)
	if p.db == nil {
		return out, nil
	}
	events, err := p.db.ReadAfter(strings.TrimSpace(id), int64(cursor))
	if err != nil {
		return nil, fmt.Errorf("card monitor: events for %s: %w", id, err)
	}
	for _, ev := range events {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, tasks.RecordedEvent{Seq: uint64(ev.Seq), Type: ev.Type})
	}
	return out, nil
}

// Serialize implements tasks.Port. The monitor never encodes bytes itself.
func (p *daemonCardPort) Serialize(change tasks.Change) ([]byte, error) {
	return json.Marshal(change)
}

// newDaemonCardMonitor builds one Monitor over the daemon's store handle. It is
// created once per process so the status baseline and replay cursor survive
// across passes.
func newDaemonCardMonitor(db *store.DB, root string) *tasks.Monitor {
	return tasks.NewMonitor(&daemonCardPort{
		db:       db,
		tasksDir: filepath.Join(root, "docs", "tasks"),
	})
}

// daemonPollCards subscribes the current task cards and polls each once. A
// status change is appended to the event log under the card id, so one change
// produces exactly one event. Subscribe is idempotent and Poll leaves state
// untouched on error, so a restart or a repeat never duplicates an event or
// resets the baseline.
func daemonPollCards(ctx context.Context, monitor *tasks.Monitor, db *store.DB, root string) {
	cards, err := taskstore.LoadCards(filepath.Join(root, "docs", "tasks"))
	if err != nil {
		observe.ActorError("card-monitor", "", err)
		return
	}
	for _, card := range cards {
		id := strings.TrimSpace(card.ID)
		if id == "" {
			continue
		}
		if err := monitor.Subscribe(id); err != nil {
			observe.ActorError("card-monitor", id, err)
			continue
		}
		result, err := monitor.Poll(ctx, id)
		if err != nil {
			observe.ActorError("card-monitor", id, err)
			continue
		}
		for _, change := range result.Changes {
			if _, err := db.Append(id, change.Type, change.Payload); err != nil {
				observe.ActorError("card-monitor", id, err)
			}
		}
	}
}

// daemonCardPollInterval is the pass cadence of the card monitor. It is a
// variable so a test can shorten it without waiting the full interval.
var daemonCardPollInterval = 30 * time.Second

// runDaemonCardMonitor ticks until ctx is done and polls the cards on each
// tick. A monitor failure is logged and never stops the daemon cycle.
func runDaemonCardMonitor(ctx context.Context, monitor *tasks.Monitor, db *store.DB, root string, interval time.Duration) {
	if interval <= 0 {
		interval = daemonCardPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			daemonPollCards(ctx, monitor, db, root)
		}
	}
}
