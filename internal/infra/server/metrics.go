// Package server metrics keep the number of exported series fixed. A series is
// defined by three bounded attributes: signal, reason and adapter. A Watch, PR
// or session identifier is never a label, so a growing number of Watches cannot
// grow the metric set.
package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// LabelSignal, LabelReason and LabelAdapter are the only label keys the
	// server metrics use. There is no watchID, pr or sessionID key: a
	// high-cardinality value must not be able to leak into a series.
	LabelSignal  = "signal"
	LabelReason  = "reason"
	LabelAdapter = "adapter"

	// Other is the single bucket for any value outside a closed domain.
	Other = "other"
)

const (
	// CounterCalls counts calls. One IncCall advances it by one, so the counter
	// is measured in calls and never in Watches or PRs. The exported name is
	// beacon_call_total.
	CounterCalls = "call_total"

	// HistogramCallDuration records call latency in seconds. The exported name
	// is beacon_call_duration_seconds.
	HistogramCallDuration = "call_duration_seconds"
)

// labelDomains are closed sets of values for each label key. Every series the
// exporter can emit is one combination of these sets, preallocated in
// NewMetrics. An unknown value is normalised to Other, so the series count is
// a constant regardless of how many Watches or PRs the process sees.
var labelDomains = map[string][]string{
	LabelSignal:  {"wake", "verdict", "merge", "poll", Other},
	LabelReason:  {"ok", "retry", "error", "blocked", Other},
	LabelAdapter: {"opencode", "cursor", "claude", Other},
}

// labelKeys is the stable order in which labels are written to the text format.
var labelKeys = []string{LabelSignal, LabelReason, LabelAdapter}

// durationBounds are the upper bounds of the latency histogram, in seconds.
var durationBounds = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30, 300}

// Attributes are the only dimensions a server metric may carry. The struct has
// no Watch or PR field on purpose: a caller cannot put a high-cardinality
// identifier into a series even by mistake.
type Attributes struct {
	Signal  string
	Reason  string
	Adapter string
}

// Normalize maps each attribute onto a value from its closed domain.
func (a Attributes) Normalize() Attributes {
	return Attributes{
		Signal:  normalizeLabel(LabelSignal, a.Signal),
		Reason:  normalizeLabel(LabelReason, a.Reason),
		Adapter: normalizeLabel(LabelAdapter, a.Adapter),
	}
}

func normalizeLabel(key, value string) string {
	for _, known := range labelDomains[key] {
		if value == known {
			return value
		}
	}
	return Other
}

// seriesKey is the normalised identity of one series.
type seriesKey struct {
	signal  string
	reason  string
	adapter string
}

func (a Attributes) key() seriesKey {
	a = a.Normalize()
	return seriesKey{signal: a.Signal, reason: a.Reason, adapter: a.Adapter}
}

// histogram is a cumulative duration histogram. Every method is safe for
// concurrent use.
type histogram struct {
	mu      sync.Mutex
	count   uint64
	sum     float64
	buckets []uint64
}

func newHistogram() *histogram {
	return &histogram{buckets: make([]uint64, len(durationBounds))}
}

func (h *histogram) observe(seconds float64) {
	if h == nil || seconds < 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += seconds
	for i, upper := range durationBounds {
		if seconds <= upper {
			h.buckets[i]++
		}
	}
}

// DurationStats is a point-in-time view of one latency histogram. Sum is in
// seconds. Buckets are cumulative and aligned with durationBounds.
type DurationStats struct {
	Count   uint64
	Sum     float64
	Buckets []uint64
}

func (h *histogram) snapshot() DurationStats {
	if h == nil {
		return DurationStats{Buckets: make([]uint64, len(durationBounds))}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return DurationStats{
		Count:   h.count,
		Sum:     h.sum,
		Buckets: append([]uint64(nil), h.buckets...),
	}
}

// Series is one exported sample: a normalised attribute set, its call count
// and its latency summary.
type Series struct {
	Attributes Attributes
	Calls      uint64
	Duration   DurationStats
}

// Metrics is a low-cardinality metric set. It holds one counter and one
// duration histogram for every combination of the closed label domains.
// Every method is safe for concurrent use.
type Metrics struct {
	mu        sync.Mutex
	calls     map[seriesKey]uint64
	durations map[seriesKey]*histogram
}

// NewMetrics builds an empty set with every possible series present at zero.
func NewMetrics() *Metrics {
	m := &Metrics{
		calls:     make(map[seriesKey]uint64),
		durations: make(map[seriesKey]*histogram),
	}
	for _, signal := range labelDomains[LabelSignal] {
		for _, reason := range labelDomains[LabelReason] {
			for _, adapter := range labelDomains[LabelAdapter] {
				key := seriesKey{signal: signal, reason: reason, adapter: adapter}
				m.calls[key] = 0
				m.durations[key] = newHistogram()
			}
		}
	}
	return m
}

func (m *Metrics) initLocked() {
	if m.calls == nil {
		m.calls = make(map[seriesKey]uint64)
	}
	if m.durations == nil {
		m.durations = make(map[seriesKey]*histogram)
	}
}

// IncCall advances the call counter for attrs by one. Unknown attribute values
// land in the Other bucket instead of creating a new series.
func (m *Metrics) IncCall(attrs Attributes) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initLocked()
	m.calls[attrs.key()]++
}

// ObserveDuration records one latency in seconds. A negative duration is
// ignored, so a clock skew cannot poison the histogram.
func (m *Metrics) ObserveDuration(attrs Attributes, d time.Duration) {
	if m == nil {
		return
	}
	m.observeSeconds(attrs, d.Seconds())
}

// ObserveSeconds records one latency already expressed in seconds.
func (m *Metrics) ObserveSeconds(attrs Attributes, seconds float64) {
	if m == nil {
		return
	}
	m.observeSeconds(attrs, seconds)
}

func (m *Metrics) observeSeconds(attrs Attributes, seconds float64) {
	if m == nil || seconds < 0 {
		return
	}
	key := attrs.key()
	m.mu.Lock()
	m.initLocked()
	h, ok := m.durations[key]
	if !ok {
		h = newHistogram()
		m.durations[key] = h
	}
	m.mu.Unlock()
	h.observe(seconds)
}

// Snapshot returns every series in a stable order. The length of the result is
// fixed by the closed label domains, never by the number of Watches or PRs.
func (m *Metrics) Snapshot() []Series {
	out := []Series{}
	if m == nil {
		return out
	}
	m.mu.Lock()
	keys := make([]seriesKey, 0, len(m.calls))
	for key := range m.calls {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].signal != keys[j].signal {
			return keys[i].signal < keys[j].signal
		}
		if keys[i].reason != keys[j].reason {
			return keys[i].reason < keys[j].reason
		}
		return keys[i].adapter < keys[j].adapter
	})
	for _, key := range keys {
		out = append(out, Series{
			Attributes: Attributes{Signal: key.signal, Reason: key.reason, Adapter: key.adapter},
			Calls:      m.calls[key],
			Duration:   m.durations[key].snapshot(),
		})
	}
	m.mu.Unlock()
	return out
}

// SeriesCount reports the number of series the exporter can emit.
func SeriesCount() int {
	n := 1
	for _, key := range labelKeys {
		n *= len(labelDomains[key])
	}
	return n
}

// Reset returns every series to zero. Tests use it between cases.
func (m *Metrics) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	for key := range m.calls {
		m.calls[key] = 0
	}
	for _, h := range m.durations {
		h.mu.Lock()
		h.count = 0
		h.sum = 0
		for i := range h.buckets {
			h.buckets[i] = 0
		}
		h.mu.Unlock()
	}
	m.mu.Unlock()
}

type seriesJSON struct {
	Signal   string       `json:"signal"`
	Reason   string       `json:"reason"`
	Adapter  string       `json:"adapter"`
	Calls    uint64       `json:"calls"`
	Duration durationJSON `json:"duration"`
}

type durationJSON struct {
	Count   uint64            `json:"count"`
	Sum     float64           `json:"sum_seconds"`
	Buckets map[string]uint64 `json:"buckets"`
}

type metricsJSON struct {
	Counter   string       `json:"counter"`
	Histogram string       `json:"histogram"`
	Series    []seriesJSON `json:"series"`
}

// JSON renders every series as one object. The histogram sum is suffixed with
// seconds so a reader cannot mistake it for milliseconds.
func (m *Metrics) JSON() []byte {
	out := metricsJSON{
		Counter:   CounterCalls,
		Histogram: HistogramCallDuration,
		Series:    []seriesJSON{},
	}
	for _, s := range m.Snapshot() {
		buckets := make(map[string]uint64, len(durationBounds)+1)
		for i, upper := range durationBounds {
			buckets[strconv.FormatFloat(upper, 'f', -1, 64)] = s.Duration.Buckets[i]
		}
		buckets["+Inf"] = s.Duration.Count
		out.Series = append(out.Series, seriesJSON{
			Signal:  s.Attributes.Signal,
			Reason:  s.Attributes.Reason,
			Adapter: s.Attributes.Adapter,
			Calls:   s.Calls,
			Duration: durationJSON{
				Count:   s.Duration.Count,
				Sum:     s.Duration.Sum,
				Buckets: buckets,
			},
		})
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return []byte("{}\n")
	}
	return append(raw, '\n')
}

// Prometheus renders the metrics in the Prometheus text exposition format.
// Names carry the beacon_ prefix so they cannot collide with process metrics.
func (m *Metrics) Prometheus() []byte {
	var buf strings.Builder
	fmt.Fprintf(&buf, "# HELP beacon_%s Calls recorded by the server.\n", CounterCalls)
	fmt.Fprintf(&buf, "# TYPE beacon_%s counter\n", CounterCalls)
	fmt.Fprintf(&buf, "# HELP beacon_%s Call latency in seconds.\n", HistogramCallDuration)
	fmt.Fprintf(&buf, "# TYPE beacon_%s histogram\n", HistogramCallDuration)
	for _, s := range m.Snapshot() {
		labels := labelString(s.Attributes)
		fmt.Fprintf(&buf, "beacon_%s{%s} %d\n", CounterCalls, labels, s.Calls)
		for i, upper := range durationBounds {
			le := strconv.FormatFloat(upper, 'f', -1, 64)
			fmt.Fprintf(&buf, "beacon_%s_bucket{%s,le=%q} %d\n",
				HistogramCallDuration, labels, le, s.Duration.Buckets[i])
		}
		fmt.Fprintf(&buf, "beacon_%s_bucket{%s,le=\"+Inf\"} %d\n",
			HistogramCallDuration, labels, s.Duration.Count)
		fmt.Fprintf(&buf, "beacon_%s_sum{%s} %g\n", HistogramCallDuration, labels, s.Duration.Sum)
		fmt.Fprintf(&buf, "beacon_%s_count{%s} %d\n", HistogramCallDuration, labels, s.Duration.Count)
	}
	return []byte(buf.String())
}

func labelString(attrs Attributes) string {
	values := []string{attrs.Signal, attrs.Reason, attrs.Adapter}
	parts := make([]string, 0, len(labelKeys))
	for i, key := range labelKeys {
		// %q escapes quotes, backslashes and newlines, which is exactly the
		// Prometheus label value escaping. Values are already from a closed
		// domain, so no user string reaches this call.
		parts = append(parts, fmt.Sprintf("%s=%q", key, values[i]))
	}
	return strings.Join(parts, ",")
}
