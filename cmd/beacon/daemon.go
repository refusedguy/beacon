package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/refusedguy/beacon/internal/app/notify"
	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// daemonIntervalEnv overrides the default pass interval without a flag. It is
// kept for the deprecated in-process daemon seam; the unified loop uses the
// adaptive github.Interval unless --interval is given explicitly.
const daemonIntervalEnv = "BEACON_DAEMON_INTERVAL"

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
	return session.NewSessionRegistry(db)
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

// runDaemonHeartbeat ticks until ctx is done and calls daemonHeartbeat on each
// tick. A registry failure is logged and never stops the daemon cycle. An
// interval that is not positive falls back to daemonHeartbeatInterval.
func runDaemonHeartbeat(ctx context.Context, hb sessionHeartbeater, sessionID string, interval time.Duration) {
	if interval <= 0 {
		interval = daemonHeartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := daemonHeartbeat(hb, sessionID); err != nil {
				observe.ActorError("session-heartbeat", "", err)
			}
		}
	}
}

// runDaemon implements `beacon daemon [--interval 3m] [--once]`. It runs the
// same single cibridge cycle `beacon mcp` uses, under the same non-blocking
// poller lock, so starting both processes cannot produce duplicate wakes.
func runDaemon(c *CLI, args []string) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	interval := fs.Duration("interval", 0, "интервал прохода (0 = адаптивный 10s/30s)")
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
		ReconcilePass(ctx, cfg)
		return 0
	}

	stopLoop, err := runReconciler(ctx, cfg)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}
	defer stopLoop()

	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		runDaemonHeartbeat(ctx, hb, sessionID, daemonHeartbeatInterval)
	}()
	defer func() { <-hbDone }()

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

	return orchestrator.DaemonPorts{
		CheckAll: func(ctx context.Context) error {
			_, err := orchestrator.CheckAll(ctx, orchestrator.Deps{
				Source:    orchestrator.NewGHSource(defaultCIRepo, daemonGHRunner(c)),
				Watches:   registry,
				Writer:    orchestrator.NewSQLVerdictWriter(txWriter),
				ConfigDir: c.root,
			})
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
		Waker: orchestrator.NewDaemonWakerWithRoute(func(id string) (orchestrator.DaemonWatch, error) {
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
		}, daemonWakeRoute(wake.PromptTargetDeliverer(&wake.T202API{}))),
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
