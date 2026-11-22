// Scenario harness tests. They check the two properties the harness promises
// its callers: a trace depends only on the scenario (determinism) and only on
// its seed for the random part (seed regression). No test here opens a socket,
// starts a process or touches the clock.
package testsystem

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func goldenScenario() Scenario {
	return Scenario{
		Name: "golden",
		Seed: 0x0102030405060708,
		Steps: []Command{
			{Kind: "watch_pr", Fields: map[string]string{"repo": "refusedguy/beacon", "pr": "73"}},
			{Kind: "ack", Fields: map[string]string{"obligation": "o-1"}},
		},
	}
}

// TestRunnerDeterministic runs the same scenario repeatedly and requires the
// full trace and its fingerprint to be identical. A hidden clock, env read or
// global PRNG would break this.
func TestRunnerDeterministic(t *testing.T) {
	s := goldenScenario()
	first := Run(s)
	for i := 2; i <= 5; i++ {
		got := Run(s)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs from run 1:\nfirst: %+v\ngot:   %+v", i, first, got)
		}
		if got.Fingerprint() != first.Fingerprint() {
			t.Fatalf("run %d fingerprint %s != %s", i, got.Fingerprint(), first.Fingerprint())
		}
	}
}

// TestGoldenFingerprint pins the canonical text, receipts and digest of a
// fixed scenario. This is the seed regression anchor: any drift in the stub
// executor or the serialization shows up here.
func TestGoldenFingerprint(t *testing.T) {
	got := Run(goldenScenario())

	wantText := "scenario=golden seed=0102030405060708\n" +
		"0|watch_pr.done|0acc9c175458d409|pr=73,repo=refusedguy/beacon,seed=0102030405060708,step=0\n" +
		"1|ack.done|c834f414dc076571|obligation=o-1,seed=0102030405060708,step=1\n"
	if got.String() != wantText {
		t.Fatalf("canonical trace changed:\ngot:\n%swant:\n%s", got.String(), wantText)
	}

	wantFingerprint := "9353f9291609a749398a98f525ca925014dc6b097b882472dd865e5ff2a32546"
	if got.Fingerprint() != wantFingerprint {
		t.Fatalf("fingerprint = %s, want %s", got.Fingerprint(), wantFingerprint)
	}

	wantReceipts := []string{"0acc9c175458d409", "c834f414dc076571"}
	for i, want := range wantReceipts {
		if got.Events[i].Receipt != want {
			t.Fatalf("event %d receipt = %s, want %s", i, got.Events[i].Receipt, want)
		}
	}
}

// TestCollidingFieldSetsHaveDistinctFingerprints guards the canonical text
// against ambiguity. Both scenarios have the same name, seed and steps; only
// the field layout differs. The old encoder gave both "a=b,c=d", so this test
// fails before the escaping fix.
func TestCollidingFieldSetsHaveDistinctFingerprints(t *testing.T) {
	base := Scenario{Name: "collision", Seed: 0x434f4c4c4953494f}
	a := base
	a.Steps = []Command{{Kind: "watch_pr", Fields: map[string]string{"a": "b,c=d"}}}
	b := base
	b.Steps = []Command{{Kind: "watch_pr", Fields: map[string]string{"a": "b", "c": "d"}}}

	ra, rb := Run(a), Run(b)
	if ra.Events[0].String() == rb.Events[0].String() {
		t.Fatalf("distinct field sets share canonical event text: %q", ra.Events[0].String())
	}
	if ra.Fingerprint() == rb.Fingerprint() {
		t.Fatalf("distinct field sets share fingerprint %s", ra.Fingerprint())
	}
}

// TestEncodeFieldsEscapesSeparators checks that keys and values cannot trade
// separators with each other, including the escape character itself.
func TestEncodeFieldsEscapesSeparators(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"plain", map[string]string{"a": "b"}, "a=b"},
		{"value separators", map[string]string{"a": "b,c=d"}, `a=b\,c\=d`},
		{"key separator", map[string]string{"a,b": "c"}, `a\,b=c`},
		{"backslash", map[string]string{"a": `b\c`}, `a=b\\c`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodeFields(tc.fields); got != tc.want {
				t.Fatalf("encodeFields = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSeedDrivesTrace checks that changing only the seed changes the random
// part of the trace (receipts) while leaving command kinds and echoed fields
// untouched.
func TestSeedDrivesTrace(t *testing.T) {
	a := goldenScenario()
	b := goldenScenario()
	b.Seed = 0x0102030405060709

	ra, rb := Run(a), Run(b)
	if ra.Fingerprint() == rb.Fingerprint() {
		t.Fatal("different seeds produced the same fingerprint")
	}
	if len(ra.Events) != len(rb.Events) {
		t.Fatalf("event counts differ: %d vs %d", len(ra.Events), len(rb.Events))
	}
	for i := range ra.Events {
		ea, eb := ra.Events[i], rb.Events[i]
		if ea.Kind != eb.Kind {
			t.Errorf("event %d kind changed with seed: %s vs %s", i, ea.Kind, eb.Kind)
		}
		if ea.Receipt == eb.Receipt {
			t.Errorf("event %d receipt did not change with seed", i)
		}
		if ea.Fields["repo"] != eb.Fields["repo"] || ea.Fields["obligation"] != eb.Fields["obligation"] {
			t.Errorf("event %d echoed fields changed with seed", i)
		}
	}
}

// TestSeedRoundTrip requires the seed to survive a full record/replay cycle
// and rejects a different seed on the same steps.
func TestSeedRoundTrip(t *testing.T) {
	g := Record(goldenScenario())
	if g.Seed != goldenScenario().Seed {
		t.Fatalf("recorded seed = %016x, want %016x", g.Seed, goldenScenario().Seed)
	}
	if err := g.Check(); err != nil {
		t.Fatalf("check after record: %v", err)
	}

	changed := g
	changed.Seed++
	if err := changed.Check(); err == nil {
		t.Fatal("check with a different seed unexpectedly passed")
	}
}

// TestRunnerEmptyScenario covers the boundary case: no steps, no events, but
// still a stable fingerprint that includes the scenario name and seed.
func TestRunnerEmptyScenario(t *testing.T) {
	got := Run(Scenario{Name: "empty", Seed: 42})
	if len(got.Events) != 0 {
		t.Fatalf("expected no events, got %d", len(got.Events))
	}
	if got.Fingerprint() != Run(Scenario{Name: "empty", Seed: 42}).Fingerprint() {
		t.Fatal("empty scenario fingerprint is not stable")
	}
	if got.Fingerprint() == Run(Scenario{Name: "empty", Seed: 43}).Fingerprint() {
		t.Fatal("empty scenario fingerprint ignores the seed")
	}
}

// TestRunnerNoExternalIO demonstrates that steps named after real external
// actions are stubbed. The runner returns ordinary done events with
// seed-derived receipts; it does not error on them or invoke any integration.
func TestRunnerNoExternalIO(t *testing.T) {
	s := Scenario{
		Name: "no-external-io",
		Seed: 0x4e4f5f494f5f3031,
		Steps: []Command{
			{Kind: "github.create_pr"},
			{Kind: "provider.call"},
			{Kind: "net.request"},
		},
	}
	got := Run(s)
	if len(got.Events) != len(s.Steps) {
		t.Fatalf("got %d events, want %d", len(got.Events), len(s.Steps))
	}
	for i, ev := range got.Events {
		if ev.Kind == "" || !strings.HasSuffix(ev.Kind, ".done") {
			t.Errorf("event %d kind %q is not a stub done event", i, ev.Kind)
		}
		if ev.Receipt == "" {
			t.Errorf("event %d has no deterministic receipt", i)
		}
		if ev.Fields["step"] != strconv.Itoa(i) {
			t.Errorf("event %d step field = %q", i, ev.Fields["step"])
		}
	}
	// An external call would make a second run differ. It does not.
	if !reflect.DeepEqual(got, Run(s)) {
		t.Fatal("stubbed external actions are not deterministic")
	}
}

// TestRunnerCopiesFields verifies that the runner does not alias the
// scenario's maps: mutating the input after Record must not break a Check.
func TestRunnerCopiesFields(t *testing.T) {
	s := goldenScenario()
	g := Record(s)
	s.Steps[0].Fields["repo"] = "someone/else"
	if err := g.Check(); err != nil {
		t.Fatalf("mutating the source scenario changed the regression: %v", err)
	}
}

// TestRegressionDriftDetection checks the negative path: editing a step makes
// a pinned regression fail while Replay exposes the new trace for inspection.
func TestRegressionDriftDetection(t *testing.T) {
	g := Record(goldenScenario())
	broken := g
	broken.Steps = append([]Command(nil), g.Steps...)
	broken.Steps[0] = Command{Kind: "watch_pr", Fields: map[string]string{"pr": "999"}}
	if err := broken.Check(); err == nil {
		t.Fatal("check with an edited step unexpectedly passed")
	}
	if len(broken.Replay().Events) != len(g.Steps) {
		t.Fatal("replay lost events")
	}
}

// TestRegistryAndLibrary covers reuse of ready scenarios from an eval: named
// lookup, insertion-order replay, a zero-value registry and library contents.
func TestRegistryAndLibrary(t *testing.T) {
	lib := NewLibrary()
	want := []string{"watch-lifecycle", "dedupe-delivery", "loop-guard"}
	if got := lib.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("library names = %v, want %v", got, want)
	}

	s, ok := lib.Get("watch-lifecycle")
	if !ok {
		t.Fatal("watch-lifecycle missing from library")
	}
	got, ok := lib.Run("watch-lifecycle")
	if !ok || len(got.Events) != len(s.Steps) {
		t.Fatalf("library run returned %d events from %d steps", len(got.Events), len(s.Steps))
	}
	if got.Fingerprint() != Run(s).Fingerprint() {
		t.Fatal("registry run diverged from a direct run")
	}
	if _, ok := lib.Run("missing"); ok {
		t.Fatal("missing scenario unexpectedly found")
	}

	all := lib.RunAll()
	if len(all) != len(want) {
		t.Fatalf("RunAll returned %d results, want %d", len(all), len(want))
	}
	for i := range all {
		if all[i].Scenario != want[i] {
			t.Errorf("RunAll[%d] scenario = %q, want %q", i, all[i].Scenario, want[i])
		}
	}

	var zero Registry
	if _, ok := zero.Get("anything"); ok {
		t.Fatal("empty registry returned a scenario")
	}
	zero.Add(s)
	if r, ok := zero.Get(s.Name); !ok || Run(r).Fingerprint() != got.Fingerprint() {
		t.Fatal("zero-value registry did not accept a scenario")
	}
}

// TestSeededRegressionArtery runs the three passes from
// tests/scenarios/seeded.yaml through the fault controller. The outcomes are
// read from the traces Runner emitted, so a grep over the spec cannot replace
// this test. The -v flag puts all four outcomes into the CI job log.
func TestSeededRegressionArtery(t *testing.T) {
	const path = "../../tests/scenarios/seeded.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read artery spec %s: %v", path, err)
	}
	spec, err := ParseArterySpec(data)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunArtery(spec)
	t.Logf("%s", result.String())
	if err != nil {
		t.Fatal(err)
	}
}
