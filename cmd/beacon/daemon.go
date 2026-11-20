package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
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
	db, err := openProcessStore(dir)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}
	defer func() { _ = db.Close() }()

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
		ReconcilePass(ctx, cfg)
		return 0
	}

	stopLoop, err := runReconciler(ctx, cfg)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon daemon: %v\n", err)
		return 2
	}
	defer stopLoop()
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
		// The single reconciler owns terminal-state refreshes now.
		CloseMerged: func(context.Context) error { return nil },
		Waker: orchestrator.NewDaemonWaker(func(id string) (orchestrator.DaemonWatch, error) {
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
		}),
	}, db.Close, nil
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
