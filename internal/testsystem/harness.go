// Package testsystem is a scenario harness for Beacon's eval and regression
// tests. It drives stub executors over a fixed command sequence and returns a
// fully deterministic event trace.
//
// Runner performs no external I/O. It never opens a network connection, starts
// a process, reads the clock or the environment. Every value in a Result comes
// from the Scenario (name, seed and steps) alone. That makes a trace safe to
// record as a golden fingerprint and to replay inside an eval: the same seed
// always yields the same events, on any Go version. Parsing an artery spec is
// pure too: ParseArterySpec takes the spec bytes from its caller and reads no
// file, so the package as a whole stays free of the FILE capability.
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

	"gopkg.in/yaml.v3"
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

// encodeFields returns the canonical, sorted key=value form of fields. Keys and
// values are escaped so every field set has one encoding. Without escaping,
// {"a": "b,c=d"} and {"a": "b", "c": "d"} both produce "a=b,c=d" and share a
// fingerprint.
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
		writeEscaped(&b, k)
		b.WriteByte('=')
		writeEscaped(&b, fields[k])
	}
	return b.String()
}

// writeEscaped writes s with the canonical form's own characters escaped:
// backslash first, then the comma and equals separators. Escaping the escape
// character keeps the encoding injective for arbitrary keys and values.
func writeEscaped(b *strings.Builder, s string) {
	for _, c := range s {
		switch c {
		case '\\', ',', '=':
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
}

// --- Seeded regression artery (BCX-54, ADR 0214) ---

// SeedIDs is the seed list of one artery run. The YAML spelling "none" means
// an empty list: a healthy pass injects no faults.
type SeedIDs []string

// UnmarshalYAML accepts both a list of seed ids and the scalar "none".
func (s *SeedIDs) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && (node.Value == "none" || node.Value == "") {
		*s = nil
		return nil
	}
	var ids []string
	if err := node.Decode(&ids); err != nil {
		return fmt.Errorf("testsystem: seed list must be a list of ids or none: %w", err)
	}
	*s = ids
	return nil
}

// SeedSpec is one injectable failure from docs/tasks/S7.011.md. Value is the
// numeric seed and Code is the fault code the controller puts in the trace.
type SeedSpec struct {
	ID     string `yaml:"id"`
	Fault  string `yaml:"fault"`
	Source string `yaml:"source"`
	Value  uint64 `yaml:"value"`
	Code   uint64 `yaml:"code"`
	Marker string `yaml:"marker"`
}

// RunSpec is one pass of the artery. Expect is "pass" or "fail_at_expect".
// ExpectMarker names the marker a failing pass must actually emit.
type RunSpec struct {
	Name         string  `yaml:"name"`
	Seed         SeedIDs `yaml:"seed"`
	Expect       string  `yaml:"expect"`
	ExpectMarker string  `yaml:"expect_marker,omitempty"`
}

// ArterySpec is the part of tests/scenarios/seeded.yaml the controller reads.
// The four faults and the three passes come from the file, not from Go
// literals, so editing the spec changes what CI executes.
type ArterySpec struct {
	Schema          string            `yaml:"schema"`
	Sources         map[string]string `yaml:"sources"`
	Seeds           []SeedSpec        `yaml:"seeds"`
	Runs            []RunSpec         `yaml:"runs"`
	NegativeControl RunSpec           `yaml:"negative_control"`
}

// ParseArterySpec parses and validates the seeded-regression spec bytes. It
// reads no file: the caller supplies the contents, which keeps the FILE
// capability out of this package and its gates.
func ParseArterySpec(data []byte) (ArterySpec, error) {
	var spec ArterySpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return ArterySpec{}, fmt.Errorf("testsystem: parse artery spec: %w", err)
	}
	if err := spec.validate(); err != nil {
		return ArterySpec{}, err
	}
	return spec, nil
}

func (a ArterySpec) validate() error {
	if len(a.Seeds) != 4 {
		return fmt.Errorf("testsystem: artery spec has %d seeds, want 4", len(a.Seeds))
	}
	byID := make(map[string]SeedSpec, len(a.Seeds))
	for _, s := range a.Seeds {
		if s.ID == "" || s.Marker == "" || s.Value == 0 || s.Code == 0 {
			return fmt.Errorf("testsystem: incomplete seed spec: %+v", s)
		}
		if _, exists := byID[s.ID]; exists {
			return fmt.Errorf("testsystem: duplicate seed id %q", s.ID)
		}
		byID[s.ID] = s
	}
	want := []string{"healthy", "seeded", "healthy_again"}
	if len(a.Runs) != len(want) {
		return fmt.Errorf("testsystem: artery spec has %d runs, want %d", len(a.Runs), len(want))
	}
	for i, name := range want {
		if a.Runs[i].Name != name {
			return fmt.Errorf("testsystem: run %d is %q, want %q", i, a.Runs[i].Name, name)
		}
	}
	if a.Runs[0].Expect != "pass" || len(a.Runs[0].Seed) != 0 {
		return fmt.Errorf("testsystem: healthy run must pass without seeds")
	}
	seeded := a.Runs[1]
	if seeded.Expect != "fail_at_expect" || seeded.ExpectMarker == "" {
		return fmt.Errorf("testsystem: seeded run must fail at an expected marker")
	}
	seen := make(map[string]bool, len(seeded.Seed))
	for _, id := range seeded.Seed {
		if _, ok := byID[id]; !ok {
			return fmt.Errorf("testsystem: seeded run names unknown seed %q", id)
		}
		seen[id] = true
	}
	if len(seen) != len(byID) {
		return fmt.Errorf("testsystem: seeded run injects %d of %d faults", len(seen), len(byID))
	}
	if a.Runs[2].Expect != "pass" || len(a.Runs[2].Seed) != 0 {
		return fmt.Errorf("testsystem: healthy_again run must pass without seeds")
	}
	if len(a.NegativeControl.Seed) != 0 || a.NegativeControl.Expect != "no_diagnostic_value" {
		return fmt.Errorf("testsystem: negative control must run without seeds and expect no diagnostic value")
	}
	return nil
}

// FaultController builds scenarios that inject the spec's faults. It holds no
// mutable state: the same ids always yield the same commands.
type FaultController struct {
	seeds map[string]SeedSpec
}

// NewFaultController validates spec and indexes its seeds by id.
func NewFaultController(spec ArterySpec) (*FaultController, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	c := &FaultController{seeds: make(map[string]SeedSpec, len(spec.Seeds))}
	for _, s := range spec.Seeds {
		c.seeds[s.ID] = s
	}
	return c, nil
}

// Inject returns a scenario whose trace carries one diagnostic event per seed.
// Runner emits the events. The controller only adds fault commands, so a pass
// can fail only through an observed diagnostic in the trace.
func (c *FaultController) Inject(ids []string) (Scenario, error) {
	steps := []Command{
		{Kind: "watch_pr", Fields: map[string]string{"repo": "refusedguy/beacon", "pr": "1145"}},
		{Kind: "wait_result", Fields: map[string]string{"watch": "artery"}},
	}
	seed := uint64(0x4152544552595f30) // "ARTERY_0"
	for _, id := range ids {
		s, ok := c.seeds[id]
		if !ok {
			return Scenario{}, fmt.Errorf("testsystem: unknown seed id %q", id)
		}
		seed = mixSeed(seed, s.Value^s.Code)
		steps = append(steps, Command{Kind: "fault." + id, Fields: map[string]string{
			"fault":      s.ID,
			"fault_code": strconv.FormatUint(s.Code, 10),
			"marker":     s.Marker,
			"diagnostic": "true",
		}})
	}
	steps = append(steps, Command{Kind: "ack", Fields: map[string]string{"obligation": "artery"}})
	return Scenario{Name: "artery", Seed: seed, Steps: steps}, nil
}

func mixSeed(acc, value uint64) uint64 {
	acc ^= value + 0x9e3779b97f4a7c15 + (acc << 6) + (acc >> 2)
	return acc
}

// RunOutcome is the observable result of one pass. Pass and Failed come from
// the events Runner emitted, never from a counter inside the controller.
type RunOutcome struct {
	Name        string
	Pass        bool
	Failed      bool
	Marker      string
	Faults      []string
	Fingerprint string
}

func (o RunOutcome) String() string {
	return fmt.Sprintf("artery run %s: pass=%t fail=%t marker=%s faults=%d fingerprint=%s",
		o.Name, o.Pass, o.Failed, o.Marker, len(o.Faults), o.Fingerprint)
}

func outcomeFrom(result Result) RunOutcome {
	out := RunOutcome{Name: result.Scenario}
	for _, ev := range result.Events {
		if ev.Fields["diagnostic"] != "true" {
			continue
		}
		out.Failed = true
		out.Faults = append(out.Faults, ev.Fields["fault"])
		if out.Marker == "" {
			out.Marker = ev.Fields["marker"]
		}
	}
	out.Pass = !out.Failed
	out.Fingerprint = result.Fingerprint()
	return out
}

// ArteryResult holds the three passes and the negative control.
type ArteryResult struct {
	Runs            []RunOutcome
	NegativeControl RunOutcome
}

// String renders every outcome so a CI log shows the actual runs.
func (r ArteryResult) String() string {
	var b strings.Builder
	for _, out := range r.Runs {
		b.WriteString(out.String())
		b.WriteByte('\n')
	}
	b.WriteString(r.NegativeControl.String())
	b.WriteByte('\n')
	return b.String()
}

// RunArtery executes healthy -> seeded -> healthy_again as three real passes
// through Runner. It returns an error when any observed outcome breaks the
// spec's expectation. grep over the YAML is not part of this check.
func RunArtery(spec ArterySpec) (ArteryResult, error) {
	controller, err := NewFaultController(spec)
	if err != nil {
		return ArteryResult{}, err
	}
	var result ArteryResult
	for _, run := range spec.Runs {
		scenario, err := controller.Inject(run.Seed)
		if err != nil {
			return result, err
		}
		out := outcomeFrom(Run(scenario))
		out.Name = run.Name
		result.Runs = append(result.Runs, out)
		switch run.Expect {
		case "pass":
			if !out.Pass || out.Marker != "" {
				return result, fmt.Errorf("testsystem: artery %s expected pass, got fail marker %q", run.Name, out.Marker)
			}
		case "fail_at_expect":
			if !out.Failed {
				return result, fmt.Errorf("testsystem: artery %s did not fail on an injected fault", run.Name)
			}
			if out.Marker != run.ExpectMarker {
				return result, fmt.Errorf("testsystem: artery %s failed at %q, want %q", run.Name, out.Marker, run.ExpectMarker)
			}
			if len(out.Faults) != len(spec.Seeds) {
				return result, fmt.Errorf("testsystem: artery %s observed %d faults, want %d", run.Name, len(out.Faults), len(spec.Seeds))
			}
		default:
			return result, fmt.Errorf("testsystem: artery %s has unknown expect %q", run.Name, run.Expect)
		}
	}
	controlScenario, err := controller.Inject(spec.NegativeControl.Seed)
	if err != nil {
		return result, err
	}
	control := outcomeFrom(Run(controlScenario))
	control.Name = "negative_control"
	result.NegativeControl = control
	if !control.Pass || control.Marker != "" {
		return result, fmt.Errorf("testsystem: negative control produced diagnostic value %q", control.Marker)
	}
	if result.Runs[0].Fingerprint != control.Fingerprint {
		return result, fmt.Errorf("testsystem: negative control trace differs from healthy trace")
	}
	if result.Runs[0].Fingerprint != result.Runs[2].Fingerprint {
		return result, fmt.Errorf("testsystem: healthy and healthy_again traces differ")
	}
	return result, nil
}

// --- Self-contained replay trajectory (SB-04) ---

// TrajectorySchema is the version tag written into every snapshot. Replay
// refuses a snapshot captured by a different schema, so a change in step
// semantics cannot silently produce a different trace.
const TrajectorySchema = "beacon-trajectory/v1"

// RunConfig is the configuration that produced a run. It travels inside the
// snapshot so replay can check compatibility without a live worktree, agent or
// environment. Replay holds the settings a replay run uses; this mirrors a
// ReplayModelConfig that replaces the live provider during replay.
type RunConfig struct {
	Schema      string            `yaml:"schema"`
	Environment string            `yaml:"environment"`
	Replay      map[string]string `yaml:"replay,omitempty"`
}

// WatchCheck is one check recorded with a completed watch. It carries only the
// observable fields; the verdict and revision keep their existing format.
type WatchCheck struct {
	Name       string `yaml:"name"`
	Conclusion string `yaml:"conclusion"`
	Required   bool   `yaml:"required,omitempty"`
}

// WatchOutcome is the verdict side of a completed watch copied into a
// trajectory. Adding a snapshot does not change how verdict or revision are
// stored in the journal; it only makes them available to a replay in a clean
// environment.
type WatchOutcome struct {
	Verdict  string       `yaml:"verdict"`
	Revision int          `yaml:"revision"`
	Checks   []WatchCheck `yaml:"checks,omitempty"`
}

// Trajectory is a self-contained snapshot of one completed run. It holds the
// config, the ordered actions, the recorded result and the watch outcome in one
// value. A journal persists only the bytes from Marshal; a replay reads those
// bytes back with no worktree, agent or external artifact. Scenario keeps the
// name, seed and actions; Result is the outcome recorded at capture time and is
// used only by Verify.
type Trajectory struct {
	Config   RunConfig    `yaml:"config"`
	Scenario Scenario     `yaml:"scenario"`
	Result   Result       `yaml:"result"`
	Watch    WatchOutcome `yaml:"watch"`
}

// Marshal serializes t to YAML bytes for journal storage. This package never
// opens a file: the caller persists the bytes and later replays them when the
// run must be replayed.
func (t Trajectory) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("testsystem: marshal trajectory: %w", err)
	}
	return data, nil
}

// compatible reports whether this build can replay t. It checks the schema tag
// and the presence of recorded actions. It deliberately ignores Result, so a
// snapshot whose result artifact was lost is still replayable.
func (t Trajectory) compatible() error {
	if t.Config.Schema != TrajectorySchema {
		return fmt.Errorf("testsystem: trajectory schema %q incompatible with %q", t.Config.Schema, TrajectorySchema)
	}
	if len(t.Scenario.Steps) == 0 {
		return fmt.Errorf("testsystem: trajectory %q has no recorded actions", t.Scenario.Name)
	}
	return nil
}

// ReplayResult runs only the recorded actions in a fresh Runner. It reads
// nothing but t: the runner is stateless and performs no external I/O, so this
// is a replay in a clean environment. An incompatible snapshot is rejected
// before any action runs.
func (t Trajectory) ReplayResult() (Result, error) {
	if err := t.compatible(); err != nil {
		return Result{}, err
	}
	return Run(t.Scenario), nil
}

// Verify replays the snapshot and checks the fresh trace against the result
// recorded at capture time. It returns an error when the recorded result is
// missing or no longer matches the replayed trace.
func (t Trajectory) Verify() error {
	if t.Result.Scenario != t.Scenario.Name || t.Result.Seed != t.Scenario.Seed {
		return fmt.Errorf("testsystem: trajectory %q: recorded result identity drifted", t.Scenario.Name)
	}
	got, err := t.ReplayResult()
	if err != nil {
		return err
	}
	if want := t.Result.Fingerprint(); got.Fingerprint() != want {
		return fmt.Errorf("testsystem: trajectory %q: replay fingerprint %s, recorded %s", t.Scenario.Name, got.Fingerprint(), want)
	}
	return nil
}

// Submission is the artifact a completed run produces: the canonical trace and
// its fingerprint. It is the beacon counterpart of an extracted prediction.
type Submission struct {
	Trace       string `yaml:"trace"`
	Fingerprint string `yaml:"fingerprint"`
}

func cloneRunConfig(in RunConfig) RunConfig {
	out := in
	if in.Replay != nil {
		out.Replay = make(map[string]string, len(in.Replay))
		for k, v := range in.Replay {
			out.Replay[k] = v
		}
	}
	return out
}

func cloneResult(in Result) Result {
	out := Result{Scenario: in.Scenario, Seed: in.Seed, Events: make([]Event, len(in.Events))}
	for i, ev := range in.Events {
		out.Events[i] = Event{Seq: ev.Seq, Kind: ev.Kind, Receipt: ev.Receipt, Fields: cloneFields(ev.Fields)}
	}
	return out
}

func cloneWatchOutcome(in WatchOutcome) WatchOutcome {
	out := WatchOutcome{Verdict: in.Verdict, Revision: in.Revision}
	if in.Checks != nil {
		out.Checks = make([]WatchCheck, len(in.Checks))
		copy(out.Checks, in.Checks)
	}
	return out
}
