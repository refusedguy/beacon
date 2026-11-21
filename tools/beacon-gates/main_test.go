package main

import (
	"os"
	"sort"
	"testing"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func TestRunBrokenCalls(t *testing.T) {
	cases := [][]string{
		{},
		{"nope"},
		{"capabilities", "-min-findings=abc"},
		{"capabilities", "--bogus"},
		{"layers-list", "-root"},
	}
	for _, args := range cases {
		if got := run(args); got != gate.Broken {
			t.Errorf("run(%q) = %d, want Broken", args, got)
		}
	}
}

func TestRunHelpAndList(t *testing.T) {
	if got := run([]string{"--help"}); got != gate.Green {
		t.Errorf("--help = %d, want Green", got)
	}
	if got := run([]string{"-h"}); got != gate.Green {
		t.Errorf("-h = %d, want Green", got)
	}
	if got := run([]string{"--list"}); got != gate.Green {
		t.Errorf("--list = %d, want Green", got)
	}
}

func TestRegistry(t *testing.T) {
	names := make([]string, 0, len(registry))
	for _, c := range registry {
		names = append(names, c.name)
	}
	if len(names) != 6 {
		t.Fatalf("registry has %d commands, want 6: %v", len(names), names)
	}
	want := append([]string(nil), names...)
	sort.Strings(want)
	for i := range names {
		if names[i] != want[i] {
			t.Fatalf("registry not sorted: %v", names)
		}
		if i > 0 && names[i] == names[i-1] {
			t.Fatalf("duplicate command %q", names[i])
		}
	}
}

func TestRunSelfTests(t *testing.T) {
	for _, c := range registry {
		if got := run([]string{c.name, "--self-test"}); got != gate.Green {
			t.Errorf("%s --self-test = %d, want Green", c.name, got)
		}
	}
}

func TestRunCapabilitiesRedOnPlantedTree(t *testing.T) {
	root, err := gate.Fixture(map[string]string{
		"internal/domain/plant.go": "package domain\n\nimport \"os/exec\"\n\n" +
			"func Run() error { _, err := exec.LookPath(\"gh\"); return err }\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	if got := run([]string{"capabilities", "-root", root}); got != gate.Red {
		t.Fatalf("planted tree = %d, want Red", got)
	}
}
