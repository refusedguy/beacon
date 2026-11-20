package observe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// counterLabels maps a counter to the label key it is broken down by. An empty
// key means the counter is a plain total.
var counterLabels = map[string]string{
	CounterPollCycles:       "",
	CounterVerdictPublished: LabelVerdict,
	CounterMergeAttempt:     "",
	CounterMergeSuccess:     "",
	CounterWakeDelivered:    LabelAdapter,
	CounterWakeFailed:       "",
	CounterRebaseRequested:  "",
}

// Metrics is a set of in-memory counters. Every method is safe for concurrent
// use. Counters are per process: a daemon keeps its own totals for its own
// lifetime, and a short-lived command sees the counters it touched.
type Metrics struct {
	mu       sync.Mutex
	values   map[string]uint64
	labelled map[string]map[string]uint64
}

// NewMetrics builds an empty counter set with all known counters present at
// zero.
func NewMetrics() *Metrics {
	m := &Metrics{
		values:   make(map[string]uint64),
		labelled: make(map[string]map[string]uint64),
	}
	for name, label := range counterLabels {
		m.values[name] = 0
		if label != "" {
			m.labelled[name] = make(map[string]uint64)
		}
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
	label, known := counterLabels[name]
	if !known {
		label = ""
	}
	m.values[name]++
	if label != "" {
		if m.labelled[name] == nil {
			m.labelled[name] = make(map[string]uint64)
		}
		m.labelled[name][labelValue]++
	}
}

// Reset returns the counter set to zero. Tests use it between cases.
func (m *Metrics) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name := range m.values {
		m.values[name] = 0
	}
	for name := range m.labelled {
		m.labelled[name] = make(map[string]uint64)
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

// JSON renders every counter as a JSON object. An unlabeled counter is a
// number; a labelled counter is an object from label value to count.
func (m *Metrics) JSON() []byte {
	out := make(map[string]any)
	if m == nil {
		raw, _ := json.MarshalIndent(out, "", "  ")
		return append(raw, '\n')
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return []byte("{}\n")
	}
	return append(raw, '\n')
}

// Prometheus renders the counters in the Prometheus text exposition format.
// Counter names carry the beacon_ prefix so they cannot collide with process
// metrics.
func (m *Metrics) Prometheus() []byte {
	var buf strings.Builder
	if m == nil {
		return []byte{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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
	return []byte(buf.String())
}

// Handler serves the Prometheus text format over HTTP. It is optional: the
// CLI exports the same text with `beacon metrics --format prometheus`.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write(m.Prometheus())
	})
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}
