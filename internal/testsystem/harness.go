// Package testsystem is a scenario harness for Beacon's eval and regression
// tests. It drives stub executors over a fixed command sequence and returns a
// fully deterministic event trace.
//
// The harness performs no external I/O. It never opens a network connection,
// starts a process, reads the clock, the environment or the filesystem. Every
// value in a Result comes from the Scenario (name, seed and steps) alone. That
// makes a trace safe to record as a golden fingerprint and to replay inside an
// eval: the same seed always yields the same events, on any Go version.
//
// A typical eval imports the package, pulls a ready scenario from NewLibrary
// and asserts on the fingerprint:
//
//	scenario, ok := testsystem.NewLibrary().Get("watch-lifecycle")
//	if !ok { t.Fatal("missing scenario") }
//	got := testsystem.Run(scenario)
//	if got.Fingerprint() != testsystem.Record(scenario).Fingerprint() {
//		t.Fatal("stub trace drifted")
//	}
//
// Regression tests pin a seed with Record and later call Check/Replay. A
// fingerprint change means either the harness or the scenario changed.
package testsystem

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Command is one step of a Scenario. Kind names the stub action (for example
// "watch_pr" or "ack") and Fields are opaque arguments that the runner echoes
// into the resulting Event. Nothing in Command is executed against a real
// system.
type Command struct {
	Kind   string
	Fields map[string]string
}

// Scenario is a named, ordered sequence of commands together with the seed
// that drives the stub executor. Seed is part of the scenario's identity: two
// scenarios with different seeds produce different traces.
type Scenario struct {
	Name  string
	Seed  uint64
	Steps []Command
}

// Event is a deterministic observation produced by the stub runner. Seq is the
// zero-based step index, Kind is the command kind with a ".done" suffix,
// Receipt is a seed-derived stand-in for a real identifier (PR number, watch
// id, delivery id) and Fields echoes the command fields plus the seed and step
// index.
type Event struct {
	Seq     int
	Kind    string
	Receipt string
	Fields  map[string]string
}

// Result is the complete trace of one scenario run. It contains no clocks,
// hosts or other ambient state.
type Result struct {
	Scenario string
	Seed     uint64
	Events   []Event
}

// Runner executes scenarios against stub executors. It is stateless, so a
// zero Runner is safe to share. All randomness comes from the scenario seed
// via a pure integer generator, never from math/rand or the clock.
type Runner struct{}

// Run drives every step of s through the stub executor and returns its trace.
// Unknown command kinds are stubbed like any other; they never trigger a real
// call and never produce an error.
func (Runner) Run(s Scenario) Result {
	rng := newPRNG(s.Seed)
	events := make([]Event, 0, len(s.Steps))
	for i, step := range s.Steps {
		kind := step.Kind
		if kind == "" {
			kind = "step"
		}
		fields := cloneFields(step.Fields)
		fields["seed"] = fmt.Sprintf("%016x", s.Seed)
		fields["step"] = strconv.Itoa(i)
		events = append(events, Event{
			Seq:     i,
			Kind:    kind + ".done",
			Receipt: fmt.Sprintf("%016x", rng.next()),
			Fields:  fields,
		})
	}
	return Result{Scenario: s.Name, Seed: s.Seed, Events: events}
}

// Run is the package-level convenience entry point used by evals.
func Run(s Scenario) Result { return (Runner{}).Run(s) }

// String returns the canonical, sorted text form of e. Map keys are sorted so
// the text is stable no matter how Go iterates the map.
func (e Event) String() string {
	return fmt.Sprintf("%d|%s|%s|%s", e.Seq, e.Kind, e.Receipt, encodeFields(e.Fields))
}

// String returns the canonical text form of r. This is the exact input hashed
// by Fingerprint, exposed so a mismatch can be inspected.
func (r Result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "scenario=%s seed=%016x\n", r.Scenario, r.Seed)
	for _, e := range r.Events {
		b.WriteString(e.String())
		b.WriteByte('\n')
	}
	return b.String()
}

// Fingerprint is a stable SHA-256 digest of the canonical trace. It is the
// value pinned by a Regression and the value evals assert on.
func (r Result) Fingerprint() string {
	sum := sha256.Sum256([]byte(r.String()))
	return hex.EncodeToString(sum[:])
}

// Regression is a seed pinned to an expected trace. Record creates one; Check
// re-runs it and fails if the fingerprint drifted. Replay returns the full
// trace for callers that want to compare more than the digest.
type Regression struct {
	Scenario    string
	Seed        uint64
	Steps       []Command
	Fingerprint string
}

// Record runs s once and pins its seed and fingerprint. The steps are copied
// so later mutation of the caller's scenario cannot change the regression.
func Record(s Scenario) Regression {
	result := Run(s)
	return Regression{
		Scenario:    s.Name,
		Seed:        s.Seed,
		Steps:       cloneSteps(s.Steps),
		Fingerprint: result.Fingerprint(),
	}
}

// ScenarioValue rebuilds the scenario captured by the regression.
func (g Regression) ScenarioValue() Scenario {
	return Scenario{Name: g.Scenario, Seed: g.Seed, Steps: cloneSteps(g.Steps)}
}

// Replay re-runs the pinned scenario through the stub executor.
func (g Regression) Replay() Result { return Run(g.ScenarioValue()) }

// Check reports an error when a replay no longer matches the pinned seed and
// fingerprint. It is the assertion used by seed regression tests.
func (g Regression) Check() error {
	got := g.Replay()
	if got.Fingerprint() != g.Fingerprint {
		return fmt.Errorf(
			"testsystem: regression %q seed %016x: fingerprint changed: got %s, want %s",
			g.Scenario, g.Seed, got.Fingerprint(), g.Fingerprint,
		)
	}
	return nil
}

// Registry holds named scenarios so evals can reuse ready-made fixtures
// instead of rebuilding command sequences. The zero value is ready to use.
type Registry struct {
	scenarios map[string]Scenario
	order     []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{scenarios: make(map[string]Scenario)} }

// Add stores s under its Name. Adding an existing name replaces the scenario
// without changing its position in Names.
func (r *Registry) Add(s Scenario) {
	if r.scenarios == nil {
		r.scenarios = make(map[string]Scenario)
	}
	if _, exists := r.scenarios[s.Name]; !exists {
		r.order = append(r.order, s.Name)
	}
	r.scenarios[s.Name] = s
}

// Get returns the scenario registered under name.
func (r *Registry) Get(name string) (Scenario, bool) {
	s, ok := r.scenarios[name]
	return s, ok
}

// Names returns the registered names in insertion order.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// Run replays the named scenario through the stub executor.
func (r *Registry) Run(name string) (Result, bool) {
	s, ok := r.Get(name)
	if !ok {
		return Result{}, false
	}
	return Run(s), true
}

// RunAll replays every registered scenario in insertion order.
func (r *Registry) RunAll() []Result {
	out := make([]Result, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, Run(r.scenarios[name]))
	}
	return out
}

// NewLibrary returns a registry preloaded with ready stub scenarios that evals
// can reuse as-is. These are deterministic fixtures, not live integrations:
// each one models a common Beacon watch lifecycle entirely through the stub
// executor, with no GitHub or provider access.
func NewLibrary() *Registry {
	r := NewRegistry()
	r.Add(Scenario{
		Name: "watch-lifecycle",
		Seed: 0x57415443485f3031,
		Steps: []Command{
			{Kind: "watch_pr", Fields: map[string]string{"repo": "refusedguy/beacon", "pr": "73"}},
			{Kind: "wait_result", Fields: map[string]string{"watch": "w-1"}},
			{Kind: "ack", Fields: map[string]string{"obligation": "o-1"}},
		},
	})
	r.Add(Scenario{
		Name: "dedupe-delivery",
		Seed: 0x4445445550455f30,
		Steps: []Command{
			{Kind: "webhook.deliver", Fields: map[string]string{"delivery": "d-1"}},
			{Kind: "webhook.redeliver", Fields: map[string]string{"delivery": "d-1"}},
		},
	})
	r.Add(Scenario{
		Name: "loop-guard",
		Seed: 0x4c4f4f505f475541,
		Steps: []Command{
			{Kind: "watch_pr", Fields: map[string]string{"repo": "refusedguy/beacon", "pr": "74"}},
			{Kind: "tick", Fields: map[string]string{"verdict": "ready"}},
			{Kind: "tick", Fields: map[string]string{"verdict": "ready"}},
		},
	})
	return r
}

// prng is a splitmix64 generator. It is pure integer arithmetic, so its output
// is identical on every Go version and never depends on the global rand state.
type prng struct{ state uint64 }

func newPRNG(seed uint64) *prng { return &prng{state: seed} }

func (p *prng) next() uint64 {
	p.state += 0x9e3779b97f4a7c15
	z := p.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func cloneFields(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneSteps(in []Command) []Command {
	out := make([]Command, len(in))
	for i, step := range in {
		out[i] = Command{Kind: step.Kind, Fields: cloneFields(step.Fields)}
	}
	return out
}

func encodeFields(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(fields[k])
	}
	return b.String()
}
