package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// counterLabels maps a counter to the label key it is broken down by. An empty
// key means the counter is a plain total.
var counterLabels = map[string]string{
	CounterPollCycles:       "",
	CounterVerdictPublished: LabelVerdict,
	CounterMergeAttempts:    "",
	CounterMergeSuccess:     "",
	CounterMergeFailures:    "",
	CounterWakeDelivered:    LabelAdapter,
	CounterWakeErrors:       LabelAdapter,
	CounterWakeSkipped:      "",
	CounterRebaseRequests:   "",
	CounterDependencyWaits:  "",
	CounterBlocks:           "",
	CounterActorErrors:      "",
}

// Counters for the blocked -> fixer -> unblock path. They live outside the
// generic counterLabels registry on purpose: that registry is a closed set
// whose export is covered by a fixed-name test. These two are plain totals and
// are exported by JSON and Prometheus through extendedCounterNames below.
const (
	CounterFixersSpawned   = "fixers_spawned_total"
	CounterBlocksEscalated = "blocks_escalated_total"
)

var extendedCounterNames = []string{CounterFixersSpawned, CounterBlocksEscalated}

// CounterWakeOutcome breaks wakes down by adapter and outcome. It stays outside
// counterLabels because that registry is a closed set whose JSON shape older
// consumers rely on. The single label value is the composite "adapter:outcome"
// pair, so both dimensions survive a metrics snapshot without a second
// exporter or a change to the existing wake counters.
const (
	CounterWakeOutcome  = "wake_outcome_total"
	LabelAdapterOutcome = "adapter_outcome"
)

var outcomeCounters = map[string]string{CounterWakeOutcome: LabelAdapterOutcome}

// outcomeKey joins the two dimensions of CounterWakeOutcome into one label
// value. The colon cannot appear in an adapter name or an outcome, so the pair
// is unambiguous.
func outcomeKey(adapter, outcome string) string {
	return adapter + ":" + outcome
}

// counterLabelKey resolves the label of a counter from both registries. Unknown
// names have no label and are exported as plain totals.
func counterLabelKey(name string) string {
	if label, ok := counterLabels[name]; ok {
		return label
	}
	return outcomeCounters[name]
}

// Gauges are point-in-time values, not monotonic totals. A scrape sees them as
// beacon_active_watches and beacon_outbox_depth. They carry no labels, so
// cardinality stays fixed no matter how many Watches or sessions exist.
const (
	GaugeActiveWatches = "active_watches"
	GaugeOutboxDepth   = "outbox_depth"
)

// GaugeNames returns the known gauge names in stable order.
func GaugeNames() []string {
	return []string{GaugeActiveWatches, GaugeOutboxDepth}
}

var knownGauges = map[string]bool{GaugeActiveWatches: true, GaugeOutboxDepth: true}

// gaugeSink is implemented by *Recorder. Gauge writes go through the installed
// sink, the same seam counters use, so call sites never reach into a Metrics.
type gaugeSink interface {
	setGauge(name string, value uint64)
}

func (r *Recorder) setGauge(name string, value uint64) {
	if r != nil && r.metrics != nil {
		r.metrics.SetGauge(name, value)
	}
}

// SetGauge publishes an absolute value through the installed sink. Before the
// composition root installs a Recorder this is a no-op.
func SetGauge(name string, value uint64) {
	if s, ok := Default().(gaugeSink); ok {
		s.setGauge(name, value)
	}
}

// ActiveWatches publishes the number of live Watches.
func ActiveWatches(n uint64) { SetGauge(GaugeActiveWatches, n) }

// OutboxDepth publishes the number of entries waiting in the outbox.
func OutboxDepth(n uint64) { SetGauge(GaugeOutboxDepth, n) }

// FixerSpawned records a fixer agent spawned to clear a blocked Watch.
func FixerSpawned(watchID string) {
	s := Default()
	s.Inc(CounterFixersSpawned, "")
	fields := Fields{}
	if watchID != "" {
		fields[FieldWatchID] = watchID
	}
	s.Log(LevelInfo, "fixer_spawned", fields)
}

// BlockEscalated records a Watch escalated to blocked by an executor's
// blocked:not_mine ack. The first escalation counts; an idempotent repeat
// does not call this helper.
func BlockEscalated(watchID, reason string) {
	s := Default()
	s.Inc(CounterBlocksEscalated, "")
	fields := Fields{}
	if watchID != "" {
		fields[FieldWatchID] = watchID
	}
	if reason != "" {
		fields[FieldReason] = reason
	}
	s.Log(LevelInfo, "block_escalated", fields)
}

// histogramBounds are the upper bounds of the duration histograms, in seconds.
// They cover a sub-millisecond provider call up to a five-minute daemon pass.
var histogramBounds = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30, 300}

// Histogram is a cumulative duration histogram. Every method is safe for
// concurrent use.
type Histogram struct {
	mu      sync.Mutex
	count   uint64
	sum     float64
	buckets []uint64
}

func newHistogram() *Histogram {
	return &Histogram{buckets: make([]uint64, len(histogramBounds))}
}

func (h *Histogram) observe(seconds float64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += seconds
	for i, upper := range histogramBounds {
		if seconds <= upper {
			h.buckets[i]++
		}
	}
}

func (h *Histogram) snapshot() (uint64, float64, []uint64) {
	if h == nil {
		return 0, 0, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	buckets := append([]uint64(nil), h.buckets...)
	return h.count, h.sum, buckets
}

// Metrics is a set of in-memory counters and histograms. Every method is safe
// for concurrent use. Values are per process: a daemon keeps its own totals
// for its own lifetime, and a short-lived command sees what it touched.
type Metrics struct {
	mu         sync.Mutex
	values     map[string]uint64
	labelled   map[string]map[string]uint64
	histograms map[string]*Histogram
	gauges     map[string]uint64
}

// NewMetrics builds an empty metric set with all known names present at zero.
func NewMetrics() *Metrics {
	m := &Metrics{
		values:     make(map[string]uint64),
		labelled:   make(map[string]map[string]uint64),
		histograms: make(map[string]*Histogram),
		gauges:     make(map[string]uint64),
	}
	for name, label := range counterLabels {
		m.values[name] = 0
		if label != "" {
			m.labelled[name] = make(map[string]uint64)
		}
	}
	for _, name := range extendedCounterNames {
		m.values[name] = 0
	}
	for name := range outcomeCounters {
		m.values[name] = 0
		m.labelled[name] = make(map[string]uint64)
	}
	for _, name := range GaugeNames() {
		m.gauges[name] = 0
	}
	for _, name := range HistogramNames() {
		m.histograms[name] = newHistogram()
	}
	return m
}

// Inc adds one to name. labelValue is used only by counters that declare a
// label; an unknown name is recorded as a plain total.
func (m *Metrics) Inc(name, labelValue string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	label := counterLabelKey(name)
	m.values[name]++
	if label != "" {
		if m.labelled[name] == nil {
			m.labelled[name] = make(map[string]uint64)
		}
		m.labelled[name][labelValue]++
	}
}

// SetGauge writes an absolute value for a known gauge. Unknown names are
// ignored, so a caller cannot grow the exported series without limit.
func (m *Metrics) SetGauge(name string, value uint64) {
	if m == nil || !knownGauges[name] {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gauges == nil {
		m.gauges = make(map[string]uint64)
	}
	m.gauges[name] = value
}

// Gauge returns the current value of a known gauge.
func (m *Metrics) Gauge(name string) uint64 {
	if m == nil || !knownGauges[name] {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gauges[name]
}

// Observe records one duration in seconds in the named histogram.
func (m *Metrics) Observe(name string, seconds float64) {
	if m == nil || seconds < 0 {
		return
	}
	m.mu.Lock()
	h, ok := m.histograms[name]
	if !ok {
		h = newHistogram()
		m.histograms[name] = h
	}
	m.mu.Unlock()
	h.observe(seconds)
}

// Reset returns the metric set to zero. Tests use it between cases.
func (m *Metrics) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	for name := range m.values {
		m.values[name] = 0
	}
	for name := range m.labelled {
		m.labelled[name] = make(map[string]uint64)
	}
	for name := range m.gauges {
		m.gauges[name] = 0
	}
	histograms := make([]*Histogram, 0, len(m.histograms))
	for _, h := range m.histograms {
		histograms = append(histograms, h)
	}
	m.mu.Unlock()
	for _, h := range histograms {
		h.mu.Lock()
		h.count = 0
		h.sum = 0
		for i := range h.buckets {
			h.buckets[i] = 0
		}
		h.mu.Unlock()
	}
}

// CounterNames returns the known counter names in stable order.
func CounterNames() []string {
	names := make([]string, 0, len(counterLabels))
	for name := range counterLabels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HistogramNames returns the known histogram names in stable order.
func HistogramNames() []string {
	return []string{HistogramMerge, HistogramPollCycle, HistogramRun, HistogramWake}
}

// JSON renders every metric as a JSON object. An unlabeled counter is a
// number, a labelled counter is an object from label value to count, and a
// histogram is an object with count, sum and cumulative buckets.
func (m *Metrics) JSON() []byte {
	out := make(map[string]any)
	histograms := make(map[string]any)
	if m != nil {
		m.mu.Lock()
		for _, name := range CounterNames() {
			if counterLabels[name] == "" {
				out[name] = m.values[name]
				continue
			}
			byValue := make(map[string]uint64)
			for k, v := range m.labelled[name] {
				byValue[k] = v
			}
			out[name] = byValue
		}
		for _, name := range extendedCounterNames {
			out[name] = m.values[name]
		}
		for name := range outcomeCounters {
			byValue := make(map[string]uint64)
			for k, v := range m.labelled[name] {
				byValue[k] = v
			}
			out[name] = byValue
		}
		for _, name := range GaugeNames() {
			out[name] = m.gauges[name]
		}
		names := make([]string, 0, len(m.histograms))
		for name := range m.histograms {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			count, sum, buckets := m.histograms[name].snapshot()
			byBound := make(map[string]uint64, len(buckets)+1)
			for i, upper := range histogramBounds {
				byBound[strconv.FormatFloat(upper, 'f', -1, 64)] = buckets[i]
			}
			byBound["+Inf"] = count
			histograms[name] = map[string]any{
				"count": count, "sum": sum, "buckets": byBound,
			}
		}
		m.mu.Unlock()
	}
	out["histograms"] = histograms
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return []byte("{}\n")
	}
	return append(raw, '\n')
}

// SnapshotJSON serialises the live counters and histograms as one JSON object.
// The MCP server writes these bytes to runtime_stats so that `beacon metrics`
// and `beacon inspect` report the counters of the running process instead of
// their own zeros. The format is the same as JSON, including the histograms
// object, and is always terminated by a newline.
func (m *Metrics) SnapshotJSON() []byte {
	return m.JSON()
}

// MetricsFromSnapshotJSON restores counters and histograms from bytes written
// by SnapshotJSON. Unknown names are ignored; malformed or empty input yields
// an empty metric set so a broken snapshot never blocks a CLI command. The
// returned set is ready to be rendered with JSON or Prometheus.
func MetricsFromSnapshotJSON(raw []byte) *Metrics {
	m := NewMetrics()
	if len(raw) == 0 {
		return m
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return m
	}

	for name, value := range decoded {
		if name == "histograms" || knownGauges[name] {
			continue
		}
		var scalar uint64
		if err := json.Unmarshal(value, &scalar); err == nil {
			m.values[name] = scalar
			continue
		}
		var byLabel map[string]uint64
		if err := json.Unmarshal(value, &byLabel); err == nil {
			m.labelled[name] = byLabel
		}
	}
	for _, name := range GaugeNames() {
		raw, ok := decoded[name]
		if !ok {
			continue
		}
		var value uint64
		if err := json.Unmarshal(raw, &value); err == nil {
			m.gauges[name] = value
		}
	}

	if histRaw, ok := decoded["histograms"]; ok {
		var histograms map[string]struct {
			Count   uint64            `json:"count"`
			Sum     float64           `json:"sum"`
			Buckets map[string]uint64 `json:"buckets"`
		}
		if err := json.Unmarshal(histRaw, &histograms); err == nil {
			for name, h := range histograms {
				target := m.histograms[name]
				if target == nil {
					target = newHistogram()
					m.histograms[name] = target
				}
				target.mu.Lock()
				target.count = h.Count
				target.sum = h.Sum
				for i, upper := range histogramBounds {
					key := strconv.FormatFloat(upper, 'f', -1, 64)
					if v, ok := h.Buckets[key]; ok {
						target.buckets[i] = v
					}
				}
				target.mu.Unlock()
			}
		}
	}
	return m
}

// Prometheus renders the metrics in the Prometheus text exposition format.
// Names carry the beacon_ prefix so they cannot collide with process metrics.
func (m *Metrics) Prometheus() []byte {
	var buf strings.Builder
	if m == nil {
		return []byte{}
	}
	m.mu.Lock()
	for _, name := range CounterNames() {
		label := counterLabels[name]
		fmt.Fprintf(&buf, "# HELP beacon_%s Beacon counter %s.\n", name, name)
		fmt.Fprintf(&buf, "# TYPE beacon_%s counter\n", name)
		if label == "" {
			fmt.Fprintf(&buf, "beacon_%s %d\n", name, m.values[name])
			continue
		}
		values := m.labelled[name]
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&buf, "beacon_%s{%s=\"%s\"} %d\n", name, label, escapeLabel(k), values[k])
		}
	}
	for _, name := range extendedCounterNames {
		fmt.Fprintf(&buf, "# HELP beacon_%s Beacon counter %s.\n", name, name)
		fmt.Fprintf(&buf, "# TYPE beacon_%s counter\n", name)
		fmt.Fprintf(&buf, "beacon_%s %d\n", name, m.values[name])
	}
	outcomeNames := make([]string, 0, len(outcomeCounters))
	for name := range outcomeCounters {
		outcomeNames = append(outcomeNames, name)
	}
	sort.Strings(outcomeNames)
	for _, name := range outcomeNames {
		label := outcomeCounters[name]
		fmt.Fprintf(&buf, "# HELP beacon_%s Beacon counter %s.\n", name, name)
		fmt.Fprintf(&buf, "# TYPE beacon_%s counter\n", name)
		values := m.labelled[name]
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&buf, "beacon_%s{%s=\"%s\"} %d\n", name, label, escapeLabel(k), values[k])
		}
	}
	for _, name := range GaugeNames() {
		fmt.Fprintf(&buf, "# HELP beacon_%s Beacon gauge %s.\n", name, name)
		fmt.Fprintf(&buf, "# TYPE beacon_%s gauge\n", name)
		fmt.Fprintf(&buf, "beacon_%s %d\n", name, m.gauges[name])
	}
	names := make([]string, 0, len(m.histograms))
	for name := range m.histograms {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		count, sum, buckets := m.histograms[name].snapshot()
		fmt.Fprintf(&buf, "# HELP beacon_%s Beacon duration histogram %s.\n", name, name)
		fmt.Fprintf(&buf, "# TYPE beacon_%s histogram\n", name)
		for i, upper := range histogramBounds {
			le := strconv.FormatFloat(upper, 'f', -1, 64)
			fmt.Fprintf(&buf, "beacon_%s_bucket{le=\"%s\"} %d\n", name, le, buckets[i])
		}
		fmt.Fprintf(&buf, "beacon_%s_bucket{le=\"+Inf\"} %d\n", name, count)
		fmt.Fprintf(&buf, "beacon_%s_sum %g\n", name, sum)
		fmt.Fprintf(&buf, "beacon_%s_count %d\n", name, count)
	}
	m.mu.Unlock()
	return []byte(buf.String())
}

// Handler serves the Prometheus text format over HTTP. The CLI exports the
// same text with `beacon metrics --format prometheus`. A MetricsServer mounts
// this handler on /metrics.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write(m.Prometheus())
	})
}

// MetricsPortEnv overrides the port of the metrics server. The host is always
// loopback, so a bad setting cannot expose metrics publicly. An empty value
// leaves the endpoint off; "0" lets the OS pick a free port.
const MetricsPortEnv = "BEACON_METRICS_PORT"

// MetricsServer owns a loopback HTTP listener that serves /metrics from a
// metric set. Call Close to stop it. A zero value is inert and is used when
// the caller disables the exporter by passing an empty port.
type MetricsServer struct {
	addr      string
	server    *http.Server
	closeOnce sync.Once
	closeErr  error
}

// StartMetricsServer opens a loopback listener on port and serves /metrics
// from m. An empty port disables the exporter and returns an inert server.
// Passing "0" reserves a free port, and Addr reports the one the OS chose.
// Only the port is injected: the address always binds 127.0.0.1.
func StartMetricsServer(port string, m *Metrics) (*MetricsServer, error) {
	port = strings.TrimSpace(port)
	s := &MetricsServer{}
	if port == "" {
		return s, nil
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("observe: bad metrics port %q: %w", port, err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return nil, fmt.Errorf("observe: listen metrics port %s: %w", port, err)
	}
	s.addr = listener.Addr().String()
	if m == nil {
		m = NewMetrics()
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ActorError("metrics-serve", "", err)
		}
	}()
	return s, nil
}

// Addr reports the loopback address of the listener, or an empty string when
// the server is disabled.
func (s *MetricsServer) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

// Port reports the bound port, or zero when the server is disabled.
func (s *MetricsServer) Port() int {
	if s == nil {
		return 0
	}
	_, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(port)
	return n
}

// Close stops the listener. It is safe to call more than once.
func (s *MetricsServer) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.server.Shutdown(ctx); err != nil {
			s.closeErr = err
			_ = s.server.Close()
		}
	})
	return s.closeErr
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}
