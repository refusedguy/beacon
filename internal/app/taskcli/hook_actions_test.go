package taskcli

import (
	"reflect"
	"strings"
	"testing"
)

func TestHookActionsRegistryHasExactlyTwo(t *testing.T) {
	want := []HookAction{HookActionDev, HookActionTest}
	got := HookActionNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("список действий: получили %v, ожидали %v", got, want)
	}
}

func TestDescribeHookActionReturnsScriptAndMode(t *testing.T) {
	cases := []struct {
		name HookAction
		want HookActionSpec
	}{
		{HookActionTest, HookActionSpec{Name: HookActionTest, Script: "run_test.sh", Mode: HookActionModeCheck}},
		{HookActionDev, HookActionSpec{Name: HookActionDev, Script: "run_dev.sh", Mode: HookActionModeDev}},
	}
	for _, tc := range cases {
		got, err := DescribeHookAction(tc.name)
		if err != nil {
			t.Fatalf("DescribeHookAction(%q): %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("DescribeHookAction(%q): получили %+v, ожидали %+v", tc.name, got, tc.want)
		}
	}
}

func TestDescribeHookActionRejectsUnknown(t *testing.T) {
	if _, err := DescribeHookAction(HookAction("run_unknown")); err == nil {
		t.Fatal("неизвестное действие принято")
	}
}

func TestRunHookActionWithScript(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "run_test.sh", "#!/bin/sh\nprintf 'tests passed\\n'\n")

	out, err := RunHookAction(dir, HookActionTest, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "tests passed" {
		t.Fatalf("вывод действия: %q", got)
	}
}

func TestRunHookActionWithoutScriptReturnsEmpty(t *testing.T) {
	out, err := RunHookAction(t.TempDir(), HookActionDev, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("результат без сценария не пуст: %q", out)
	}
}

func TestRunHookActionRejectsUnknown(t *testing.T) {
	if _, err := RunHookAction(t.TempDir(), HookAction("run_unknown"), ""); err == nil {
		t.Fatal("неизвестное действие запущено")
	}
}
