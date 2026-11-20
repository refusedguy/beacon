package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/cibridge"
	"github.com/refusedguy/beacon/internal/infra/ghci"
	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// daemonIntervalEnv overrides the default pass interval without a flag.
const daemonIntervalEnv = "BEACON_DAEMON_INTERVAL"

// daemonRunner holds the command implementation so dispatch stays a one-line
// wiring change and tests can replace it like the other commands.
var daemonRunner = func(c *CLI, args []string) int { return runDaemon(c, args) }

// RegisterDaemon installs the daemon command implementation.
func RegisterDaemon(fn func(*CLI, []string) int) { daemonRunner = fn }

// runDaemon implements `beacon daemon [--interval 3m] [--once]`. The daemon is
// the only process the operator starts: every pass calls checkall, the
// orchestrator and the merged-PR closer by itself.
func runDaemon(c *CLI, args []string) int {
	defaultInterval := orchestrator.DaemonInterval
	if raw := strings.TrimSpace(os.Getenv(daemonIntervalEnv)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			fmt.Fprintf(c.errOut, "beacon daemon: неверный %s=%q\n", daemonIntervalEnv, raw)
			return 2
		}
		defaultInterval = parsed
	}

	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	interval := fs.Duration("interval", defaultInterval, "интервал прохода (по умолчанию 3m)")
	once := fs.Bool("once", false, "выполнить один проход и выйти")
	wakeTargetFlag := fs.String("wake-target", "", "sessionID OpenCode оркестратора для wake")
	if !parseFlags("daemon", args, c.errOut, fs) {
		return 2
	}
	if *interval <= 0 {
		fmt.Fprintf(c.errOut, "beacon daemon: интервал должен быть положительным\n")
		return 2
	}
	wakeTarget, err := ciWakeTarget(*wakeTargetFlag)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}

	ports, closePorts, err := makeDaemonPorts(c, wakeTarget)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}
	defer func() { _ = closePorts() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := orchestrator.DaemonRun(ctx, ports, *interval, *once); err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}
	return 0
}

// makeDaemonPorts builds the three pass actions from the real state store.
// The checkall and cibridge ports share one store handle; the orchestrator
// factory opens its own handle, so both are closed on shutdown.
func makeDaemonPorts(c *CLI, wakeTarget string) (orchestrator.DaemonPorts, func() error, error) {
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		return orchestrator.DaemonPorts{}, nil, err
	}
	cleanup := func() error { return closeStore() }

	txWriter, ok := deps.Store.(orchestrator.TxWriter)
	if !ok {
		_ = closeStore()
		return orchestrator.DaemonPorts{}, nil, fmt.Errorf("store does not support verdict writes")
	}

	watchHandler := mcp.NewWatchPRHandler(deps.Store, deps.CI, deps.Clock)
	registry := orchestrator.FuncRegistry{
		FindFn: func(_ context.Context, pr orchestrator.PR) (orchestrator.WatchRef, bool, error) {
			watches, err := deps.Store.ActiveWatches()
			if err != nil {
				return orchestrator.WatchRef{}, false, err
			}
			for _, w := range watches {
				if w.Repo == pr.Repo && w.PR == pr.Number && w.HeadSHA == pr.HeadSHA {
					// A Watch left by an older release can name a service instead
					// of a session (beacon-ci-checkall, beacon-daemon-checkall).
					// Treat it as stale and let checkall register a fresh Watch
					// with the validated --wake-target session.
					subs := make([]orchestrator.DaemonSubscriber, 0, len(w.Subscribers))
					for _, s := range w.Subscribers {
						subs = append(subs, orchestrator.DaemonSubscriber{
							ID: s.ID, Role: string(s.Role), Adapter: s.Adapter, Target: s.Target,
						})
					}
					// The daemon wake path addresses only the executor. An
					// executor subscribes asynchronously, so at registration time
					// the Watch carries just the orchestrator session. Keep such
					// an explicit orchestrator-only Watch valid here or checkall
					// would register a duplicate on every pass. It still cannot
					// receive a retry: that wake is refused downstream. Blocked
					// escalations use the separate NotifyOrchestrator path.
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
		RegisterFn: func(ctx context.Context, pr orchestrator.PR) (orchestrator.WatchRef, error) {
			out, err := watchHandler.Register(ctx, nil, mcp.WatchPRInput{
				Repo:    pr.Repo,
				PR:      pr.Number,
				HeadSHA: pr.HeadSHA,
				Role:    "orchestrator",
				Notify:  &mcp.WatchPRNotify{Adapter: "opencode", Target: wakeTarget},
			})
			if err != nil {
				return orchestrator.WatchRef{}, err
			}
			return orchestrator.WatchRef{
				ID: out.WatchID, Repo: pr.Repo, PR: pr.Number, HeadSHA: pr.HeadSHA, Revision: out.Revision,
			}, nil
		},
	}

	orchDeps, closeOrch, err := makeOrchestratorDeps(c)
	if err != nil {
		_ = closeStore()
		return orchestrator.DaemonPorts{}, nil, err
	}
	cleanup = func() error {
		orchErr := closeOrch()
		storeErr := closeStore()
		return errors.Join(orchErr, storeErr)
	}

	// cibridge owns the merged/closed PR rule from #338. Refresh publishes a
	// terminal state for those Watches, which closes them through the domain
	// transition; it leaves ordinary ready/retry Watches to the other ports.
	bridge := cibridge.New(deps.Store, &ghci.Provider{}, deps.Clock, 0)

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
		Orchestrate: func(ctx context.Context, waker orchestrator.Waker) error {
			_, err := orchestrator.Run(ctx, orchDeps.Store, orchDeps.Merger, waker, orchestrator.Options{})
			return err
		},
		CloseMerged: func(ctx context.Context) error {
			bridge.Refresh(ctx)
			return nil
		},
		Waker: orchestrator.NewDaemonWaker(func(id string) (orchestrator.DaemonWatch, error) {
			w, err := deps.Store.Watch(id)
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
				// Leave SubscriberID empty: Wake logs the refusal once and
				// returns a clear error instead of calling the prompt API.
				return view, nil
			}
			view.Adapter, view.SubscriberID = sub.Adapter, sub.ID
			return view, nil
		}),
	}, cleanup, nil
}

// hasOrchestratorSession reports whether subs carry an explicit orchestrator
// session that can receive a blocked escalation. FindFn uses it to keep a Watch
// registered while its executor is still subscribing; the daemon wake path
// itself never selects this subscriber (see DaemonWakeSubscriber).
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

// daemonGHRunner is the single gh seam used by the daemon's checkall pass.
func daemonGHRunner(c *CLI) orchestrator.GHRunner {
	return func(args ...string) ([]byte, error) {
		res := c.run(c.root, "gh", args, nil)
		if res.code == 0 {
			return res.stdout, nil
		}
		reason := strings.TrimSpace(string(res.stderr))
		if reason == "" {
			reason = strings.TrimSpace(string(res.stdout))
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), reason)
	}
}

// The daemon's Waker lives in internal/app/orchestrator. cmd only projects the
// Watch into primitive fields, because the ADR 0005 layer matrix forbids a
// direct cmd -> internal/domain import.
