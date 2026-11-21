package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// The tracing wrappers sit on the command seams that already exist for tests.
// They are pure wiring: with BEACON_OTEL_ENABLED unset the tracer is a no-op
// and a command behaves exactly as before. Deeper per-tick spans need hooks
// inside the daemon, cibridge and wake packages, outside this file's scope.
func init() {
	daemon := daemonRunner
	RegisterDaemon(func(c *CLI, args []string) int {
		return tracedCommand("beacon.daemon.tick", "daemon", func() int { return daemon(c, stripOTLPFlags(args)) })
	})

	orchestrate := orchestratorRunner
	RegisterOrchestrator(func(c *CLI, args []string) int {
		return tracedCommand("beacon.orchestrator.pass", "orchestrator", func() int {
			return orchestrate(c, stripOTLPFlags(args))
		})
	})

	ci := ciRunner
	RegisterCI(func(c *CLI, args []string) int {
		return tracedCommand("beacon.ci.refresh", "cibridge", func() int { return ci(c, stripOTLPFlags(args)) })
	})

	deliver := deliverRunner
	RegisterDeliver(func(c *CLI, args []string) int {
		return tracedCommand("beacon.wake.deliver", "wake", func() int { return deliver(c, stripOTLPFlags(args)) })
	})
}

// stripOTLPFlags removes the OTLP knobs before a traced command parses its own
// flags. The knobs are consumed by setupObserve, which runs first in main.
func stripOTLPFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--otel-exporter="),
			strings.HasPrefix(a, "--otel-endpoint="),
			strings.HasPrefix(a, "--otel-protocol="):
			continue
		case a == "--otel-exporter", a == "--otel-endpoint", a == "--otel-protocol":
			if i+1 < len(args) {
				i++
			}
			continue
		default:
			out = append(out, a)
		}
	}
	return out
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

// metricsServerStart is the seam that opens the optional /metrics endpoint.
// Tests replace it so setupObserve never binds a real port. Only a port is
// injected; cmd/beacon never names net/http.
var metricsServerStart = func(port string, m *observe.Metrics) (func(), error) {
	srv, err := observe.StartMetricsServer(port, m)
	if err != nil {
		return nil, err
	}
	return func() { _ = srv.Close() }, nil
}

// startMetricsEndpoint serves the process metrics at /metrics on loopback for
// a long-lived command when BEACON_METRICS_PORT is set. An unset port leaves
// the command socket-free, and a bad port is reported but never fatal.
func startMetricsEndpoint(m *observe.Metrics, args []string) func() {
	noop := func() {}
	if len(args) == 0 || !isServerCommand(args[0]) {
		return noop
	}
	port := strings.TrimSpace(os.Getenv(observe.MetricsPortEnv))
	if port == "" {
		return noop
	}
	stop, err := metricsServerStart(port, m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "beacon: /metrics: %v\n", err)
		return noop
	}
	return stop
}

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

// otlpConfigFromArgs layers the OTLP command flags over the environment. The
// env variables are the supported path for every command; a caller that owns
// its own flag set can pass these flags through and get the same config. No
// flag value contacts a collector here, so enabling OTLP never needs a live
// endpoint at parse time.
func otlpConfigFromArgs(args []string) observe.Config {
	cfg := observe.ConfigFromEnv()
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--otel-exporter="):
			cfg.Exporter = strings.TrimSpace(strings.TrimPrefix(a, "--otel-exporter="))
		case a == "--otel-exporter" && i+1 < len(args):
			i++
			cfg.Exporter = strings.TrimSpace(args[i])
		case strings.HasPrefix(a, "--otel-endpoint="):
			cfg.Endpoint = strings.TrimSpace(strings.TrimPrefix(a, "--otel-endpoint="))
		case a == "--otel-endpoint" && i+1 < len(args):
			i++
			cfg.Endpoint = strings.TrimSpace(args[i])
		case strings.HasPrefix(a, "--otel-protocol="):
			cfg.Protocol = cmdProtocol(strings.TrimPrefix(a, "--otel-protocol="))
		case a == "--otel-protocol" && i+1 < len(args):
			i++
			cfg.Protocol = cmdProtocol(args[i])
		}
	}
	return cfg
}

func cmdProtocol(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case observe.ProtocolHTTPJSON, "json":
		return observe.ProtocolHTTPJSON
	case observe.ProtocolGRPC:
		return observe.ProtocolGRPC
	default:
		return observe.ProtocolHTTPProtobuf
	}
}

// installOTLPExporter swaps in the OTLP tracer only when tracing is enabled,
// the exporter is named and an endpoint is present. Every other combination
// leaves the local tracer from package init in place.
func installOTLPExporter(args []string) {
	cfg := otlpConfigFromArgs(args)
	if !cfg.Enabled || !strings.EqualFold(strings.TrimSpace(cfg.Exporter), "otlp") ||
		strings.TrimSpace(cfg.Endpoint) == "" {
		return
	}
	observe.SetTracer(observe.NewTracer(cfg))
}

// setupObserve installs a slog logger and the in-memory counters. A long-lived
// server command (mcp or daemon) opens the rotating JSONL server log and moves
// both latest links to it. Every other command opens a fresh timestamped CLI
// log and leaves those links alone, so a one-shot command cannot hide the
// server log from an operator following beacon.mcp.latest.log.
func setupObserve(args ...string) func() {
	installOTLPExporter(args)
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
		metrics := observe.NewMetrics()
		observe.SetDefault(observe.NewRecorder(logger, metrics))
		log.SetOutput(observe.NewLogWriter(logger))
		stopMetrics := startMetricsEndpoint(metrics, args)
		return func() {
			stopMetrics()
			observe.FlushCycle()
			_ = observe.Shutdown(context.Background())
			log.SetOutput(previousLogOutput)
			observe.SetDefault(nil)
		}
	}

	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "beacon: каталог состояния: %v\n", err)
		return install(io.Discard)
	}
	var runLog *observe.RunLog
	if len(args) > 0 && isServerCommand(args[0]) {
		runLog, err = observe.OpenServerLog(dir)
	} else {
		runLog, err = observe.OpenCLILog(dir)
	}
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

// isServerCommand reports whether a command owns the long-lived server log.
// Only mcp and daemon move the latest links; short commands never do.
func isServerCommand(name string) bool {
	switch strings.TrimSpace(name) {
	case "mcp", "daemon":
		return true
	default:
		return false
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
