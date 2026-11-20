package wake

import (
	"strings"
	"testing"
)

var wantMechanisms = []Mechanism{MechanismPi, MechanismKilo, MechanismCursor}
var wantAxes = []Axis{AxisTruth, AxisDedup, AxisBusy, AxisRestart, AxisDelivery}

func TestMatrixCoversEveryAxisForEveryMechanism(t *testing.T) {
	seen := make(map[Mechanism]map[Axis]int)
	for _, m := range wantMechanisms {
		seen[m] = make(map[Axis]int)
		for _, a := range wantAxes {
			seen[m][a] = 0
		}
	}
	for _, r := range Matrix {
		if _, ok := seen[r.Mechanism]; !ok {
			t.Fatalf("unknown mechanism %q in matrix", r.Mechanism)
		}
		if _, ok := seen[r.Mechanism][r.Axis]; !ok {
			t.Fatalf("unknown axis %q in matrix", r.Axis)
		}
		seen[r.Mechanism][r.Axis]++
	}
	for _, m := range wantMechanisms {
		for _, a := range wantAxes {
			if got := seen[m][a]; got != 1 {
				t.Fatalf("cell (%s, %s) occurs %d times, want 1", m, a, got)
			}
		}
	}
	if len(Matrix) != 15 {
		t.Fatalf("matrix has %d rows, want 15", len(Matrix))
	}
}

func TestMatrixRowsAreGrounded(t *testing.T) {
	known := map[string]bool{
		"wake-pi.md":        true,
		"wake-kilo-code.md": true,
		"wake-cursor.md":    true,
	}
	for _, r := range Matrix {
		if strings.TrimSpace(r.Fact) == "" {
			t.Fatalf("row (%s, %s) has no fact", r.Mechanism, r.Axis)
		}
		if !known[r.Source] {
			t.Fatalf("row (%s, %s) cites unknown source %q", r.Mechanism, r.Axis, r.Source)
		}
	}
}

func TestSourceAnchors(t *testing.T) {
	want := make(map[string]string)
	want["pi-original-url-404"] = "github.com/mariozechner/pi-coding-agent"
	want["pi-repository"] = "https://github.com/earendil-works/pi"
	want["kilo-commit"] = "bd8b98aeab348c08221a836ba091b855a62814e9"
	want["cursor-api-split"] = "v0/v1"

	if len(SourceAnchors) != len(want) {
		t.Fatalf("anchors = %d, want %d", len(SourceAnchors), len(want))
	}
	for _, a := range SourceAnchors {
		v, ok := want[a.ID]
		if !ok {
			t.Fatalf("unexpected anchor %q", a.ID)
		}
		if a.Value != v {
			t.Fatalf("anchor %q = %q, want %q", a.ID, a.Value, v)
		}
	}
}

func TestCommonPrimitives(t *testing.T) {
	want := make(map[Primitive]bool)
	want[PrimitiveDeliveryID] = true
	want[PrimitiveEventsHint] = true
	want[PrimitiveStateTruth] = true
	want[PrimitiveLeaseRelease] = true

	if len(Primitives) != len(want) {
		t.Fatalf("primitives = %d, want %d", len(Primitives), len(want))
	}
	known := map[string]bool{
		"wake-pi.md":        true,
		"wake-kilo-code.md": true,
		"wake-cursor.md":    true,
	}
	seen := make(map[Primitive]bool)
	for _, p := range Primitives {
		if seen[p.Primitive] {
			t.Fatalf("duplicate primitive %q", p.Primitive)
		}
		seen[p.Primitive] = true
		if !want[p.Primitive] {
			t.Fatalf("unexpected primitive %q", p.Primitive)
		}
		if strings.TrimSpace(p.Summary) == "" {
			t.Fatalf("primitive %q has no summary", p.Primitive)
		}
		if len(p.Reports) == 0 {
			t.Fatalf("primitive %q cites no report", p.Primitive)
		}
		for _, r := range p.Reports {
			if !known[r] {
				t.Fatalf("primitive %q cites unknown report %q", p.Primitive, r)
			}
		}
	}
}

func TestNonMechanismsAreTheRejectedOnes(t *testing.T) {
	want := []string{
		"a built-in sleep scheduler in the Pi coding-agent core",
		"automatic events.subscribe reconnect as a recovery path",
		"FINISHED, IDLE or agent_end as final readiness",
	}
	if len(NonMechanisms) != len(want) {
		t.Fatalf("non-mechanisms = %d, want %d", len(NonMechanisms), len(want))
	}
	set := make(map[string]bool)
	for _, n := range NonMechanisms {
		set[n] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Fatalf("missing rejected behaviour %q", w)
		}
	}
}

func TestOverviewContainsEveryCell(t *testing.T) {
	out := Overview()
	if !strings.Contains(out, "| Axis | Pi | Kilo Code | Cursor |") {
		t.Fatal("overview has no header")
	}
	for _, m := range wantMechanisms {
		for _, a := range wantAxes {
			fact := FactFor(m, a)
			if fact == "" {
				t.Fatalf("missing fact for (%s, %s)", m, a)
			}
			if !strings.Contains(out, fact) {
				t.Fatalf("overview omits fact for (%s, %s)", m, a)
			}
		}
	}
}
