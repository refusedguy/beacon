package reachability

import (
	"os"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

const adrReason = "ADR 0263: wake/claim/wait вне MCP; нужен отдельный ADR (BCX-49)"
const waveReason = "T21x-адаптеры подключаются в поток deliver в Wave 7"

func exceptionFixture(t *testing.T) (*analysis, string) {
	t.Helper()
	root, err := gate.Fixture(map[string]string{
		"internal/infra/demo/adapter.go": "package demo\n\n" +
			"type PlantedAdapter struct{}\n\ntype WaveAdapter struct{}\n",
		"internal/infra/demo/wired.go": "package demo\n\ntype WiredAdapter struct{}\n",
		"cmd/beacon/wire.go": "package main\n\n" +
			"import \"github.com/refusedguy/beacon/internal/infra/demo\"\n\n" +
			"func wire() *demo.WiredAdapter { return &demo.WiredAdapter{} }\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	a, err := parseAnalysis(root)
	if err != nil {
		t.Fatal(err)
	}
	return a, root
}

func TestReasonAligned(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{adrReason, true},
		{waveReason, false},
		{"ghci-провайдер подключается к composition root в Wave 6", false},
		{"ADR 0263: отложено до отдельного ADR", true},
		{"без ссылки на ADR", false},
	}
	for _, tc := range cases {
		if got := reasonAligned(tc.reason); got != tc.want {
			t.Errorf("reasonAligned(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

func TestEvaluateExceptionsNegativeControl(t *testing.T) {
	a, _ := exceptionFixture(t)
	ex := gate.Exceptions{Permanent: []gate.PermanentEntry{
		{Rule: gate.RuleReachUnwired, Path: "internal/infra/demo/GhostAdapter", Reason: adrReason},
		{Rule: gate.RuleReachUnwired, Path: "internal/infra/demo/WiredAdapter", Reason: waveReason},
	}}
	rows, exit := evaluateExceptions(ex, a)
	if exit != gate.Red {
		t.Fatalf("exit = %d, want Red", exit)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %v", len(rows), rows)
	}
	for i, row := range rows {
		if row.Verdict != verdictBlocking {
			t.Errorf("row %d verdict = %s, want blocking: %v", i, row.Verdict, row)
		}
		if !strings.Contains(row.String(), "verdict=blocking") {
			t.Errorf("row %d line missing verdict: %s", i, row.String())
		}
	}
	var lines []string
	for _, row := range rows {
		lines = append(lines, row.String())
	}
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "internal/infra/demo/GhostAdapter in_code=0") {
		t.Errorf("missing deleted-symbol row: %s", out)
	}
	if !strings.Contains(out, "internal/infra/demo/WiredAdapter in_code=1 from_cmd=1") {
		t.Errorf("missing cmd-reachable row: %s", out)
	}
}

func TestEvaluateExceptionsNonBlocking(t *testing.T) {
	a, _ := exceptionFixture(t)
	ex := gate.Exceptions{Permanent: []gate.PermanentEntry{
		{Rule: gate.RuleReachUnwired, Path: "internal/infra/demo/PlantedAdapter", Reason: adrReason},
		{Rule: gate.RuleReachUnwired, Path: "internal/infra/demo/WiredAdapter", Reason: adrReason},
		{Rule: gate.RuleReachUnwired, Path: "internal/infra/demo/WaveAdapter", Reason: waveReason},
	}}
	rows, exit := evaluateExceptions(ex, a)
	if exit != gate.Green {
		t.Fatalf("exit = %d, want Green: %v", exit, rows)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	bySymbol := map[string]exceptionRow{}
	for _, row := range rows {
		bySymbol[row.Symbol] = row
	}
	planted := bySymbol["internal/infra/demo/PlantedAdapter"]
	if planted.Verdict != verdictOK {
		t.Errorf("PlantedAdapter verdict = %s, want ok", planted.Verdict)
	}
	wired := bySymbol["internal/infra/demo/WiredAdapter"]
	if wired.Verdict != verdictWarning || !wired.FromCmd || !wired.ADRAligned {
		t.Errorf("WiredAdapter row = %v, want reachable warning under ADR", wired)
	}
	wave := bySymbol["internal/infra/demo/WaveAdapter"]
	if wave.Verdict != verdictWarning || wave.ADRAligned {
		t.Errorf("WaveAdapter row = %v, want non-aligned warning", wave)
	}
}

func TestCheckExceptionsFlag(t *testing.T) {
	root, err := gate.Fixture(map[string]string{
		"internal/infra/demo/adapter.go": plantedAdapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })

	if !hasCheckExceptions([]string{"-root", root, "--check-exceptions"}) {
		t.Fatal("--check-exceptions not detected")
	}
	if got := withoutCheckExceptions([]string{"--check-exceptions", "--root=" + root}); len(got) != 1 || got[0] != "--root="+root {
		t.Fatalf("withoutCheckExceptions = %v", got)
	}
	// A flag routing error sends --check-exceptions to ParseOptions, which
	// returns Broken. Accepting the flag yields Green or Red, never Broken.
	if got := Run([]string{"--check-exceptions", "-root", root}); got == gate.Broken {
		t.Fatalf("Run(--check-exceptions) = Broken, flag was not routed")
	}
}
