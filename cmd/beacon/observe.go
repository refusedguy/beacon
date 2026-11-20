package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// beaconLogFileEnv overrides the path of the structured log. Without it the
// log lives next to the project's state database.
const beaconLogFileEnv = "BEACON_LOG_FILE"

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

// setupObserve installs the process-wide recorder: structured JSON to stderr
// and to BEACON_LOG_FILE (or the project state directory), plus in-memory
// counters. The returned function closes the log file. A file that cannot be
// opened is not fatal, stderr still receives every event.
func setupObserve() func() {
	min := observe.LevelInfo
	if strings.EqualFold(strings.TrimSpace(os.Getenv(beaconLogLevelEnv)), "debug") {
		min = observe.LevelDebug
	}
	path := strings.TrimSpace(os.Getenv(beaconLogFileEnv))
	if path == "" {
		if dir, err := stateDir(); err == nil && dir != "" {
			path = filepath.Join(dir, "beacon.log")
		}
	}

	outputs := []io.Writer{os.Stderr}
	if path != "" {
		if f, err := observe.OpenLogFile(path); err == nil {
			outputs = append(outputs, f)
		}
	}
	log := observe.NewLogger(min, outputs...)
	observe.SetDefault(observe.NewRecorder(log, observe.NewMetrics()))
	return func() { _ = log.Close() }
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
