package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// The tracing wrappers sit on the command seams that already exist for tests.
// They are pure wiring: with BEACON_OTEL_ENABLED unset the tracer is a no-op
// and a command behaves exactly as before. Deeper per-tick spans need hooks
// inside the daemon, cibridge and wake packages, outside this file's scope.
func init() {
	daemon := daemonRunner
	RegisterDaemon(func(c *CLI, args []string) int {
		return tracedCommand("beacon.daemon.tick", "daemon", func() int { return daemon(c, args) })
	})

	orchestrate := orchestratorRunner
	RegisterOrchestrator(func(c *CLI, args []string) int {
		return tracedCommand("beacon.orchestrator.pass", "orchestrator", func() int { return orchestrate(c, args) })
	})

	ci := ciRunner
	RegisterCI(func(c *CLI, args []string) int {
		return tracedCommand("beacon.ci.refresh", "cibridge", func() int { return ci(c, args) })
	})

	deliver := deliverRunner
	RegisterDeliver(func(c *CLI, args []string) int {
		return tracedCommand("beacon.wake.deliver", "wake", func() int { return deliver(c, args) })
	})
}

func tracedCommand(name, component string, fn func() int) int {
	_, end := observe.Current().Start(context.Background(), name,
		observe.String("process.executable.name", "beacon"),
		observe.String("beacon.component", component))
	defer end()
	return fn()
}

// beaconLogFormatEnv selects the slog handler: json or the default text.
const beaconLogFormatEnv = "BEACON_LOG_FORMAT"

// beaconLogLevelEnv selects the minimum level: debug, info, warn or error.
const beaconLogLevelEnv = "BEACON_LOG_LEVEL"

// metricsRunner is the dispatch seam for `beacon metrics`, mirroring the other
// commands so tests can replace it.
var metricsRunner = func(c *CLI, args []string) int { return runMetrics(c, args) }

// RegisterMetrics installs the metrics command implementation.
func RegisterMetrics(fn func(*CLI, []string) int) { metricsRunner = fn }

// runMetrics exports the in-process counters as JSON or Prometheus text. The
// counters belong to the process that recorded them, so a short-lived command
// sees the counters it touched and a daemon sees its own running totals.
func runMetrics(c *CLI, args []string) int {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	format := fs.String("format", "json", "формат вывода: json или prometheus")
	if !parseFlags("metrics", args, c.errOut, fs) {
		return 2
	}
	metrics := observe.MetricsOf(observe.Default())
	if metrics == nil {
		metrics = observe.NewMetrics()
	}
	switch *format {
	case "json":
		_, _ = c.out.Write(metrics.JSON())
	case "prometheus":
		_, _ = c.out.Write(metrics.Prometheus())
	default:
		fmt.Fprintf(c.errOut, "beacon metrics: неизвестный формат %q\n", *format)
		return 2
	}
	return 0
}

// setupObserve installs a slog logger and the in-memory counters. Each process
// gets its own file under <project-state>/logs, and beacon.latest.log points at
// it. stderr is reserved for setup failures: normal records go only to the
// file, so an operator never sees the same line twice.
func setupObserve() func() {
	rawLevel := os.Getenv(beaconLogLevelEnv)
	level, ok := observe.ParseLevel(rawLevel)
	if !ok {
		fmt.Fprintf(os.Stderr, "beacon: неверный %s=%q, используется info\n", beaconLogLevelEnv, rawLevel)
	}
	format := os.Getenv(beaconLogFormatEnv)

	// Every legacy log.Printf call in the cycle goes to the same file. Nothing
	// in the daemon writes to stderr: a restart keeps all records on disk.
	previousLogOutput := log.Writer()
	install := func(w io.Writer) func() {
		logger := observe.NewLogger(format, level, w)
		observe.SetDefault(observe.NewRecorder(logger, observe.NewMetrics()))
		log.SetOutput(observe.NewLogWriter(logger))
		return func() {
			observe.FlushCycle()
			log.SetOutput(previousLogOutput)
			observe.SetDefault(nil)
		}
	}

	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "beacon: каталог состояния: %v\n", err)
		return install(io.Discard)
	}
	runLog, err := observe.OpenRunLog(dir, time.Now(), os.Getpid())
	if err != nil {
		fmt.Fprintf(os.Stderr, "beacon: лог: %v\n", err)
		return install(io.Discard)
	}
	cleanup := install(runLog.Writer())
	return func() {
		cleanup()
		_ = runLog.Close()
	}
}

// observingDeliveryTarget wraps the live delivery port so every wake the CLI
// sends is counted and logged. The adapter is the one the port reports; the
// default covers the opencode target until a port carries a richer name.
type observingDeliveryTarget struct {
	next    deliveryTarget
	adapter string
}

func (t observingDeliveryTarget) Prompt(sessionID, text string) error {
	if t.next == nil {
		err := fmt.Errorf("delivery: target is nil")
		observe.WakeFailed(t.adapter, sessionID, err)
		return err
	}
	err := t.next.Prompt(sessionID, text)
	if err != nil {
		observe.WakeFailed(t.adapter, sessionID, err)
		return err
	}
	if t.adapter == "" {
		observe.WakeDelivered("opencode", sessionID)
	} else {
		observe.WakeDelivered(t.adapter, sessionID)
	}
	return nil
}
