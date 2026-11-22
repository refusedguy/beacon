package taskcli

import (
	"strings"
	"testing"
)

// Три пути соответствуют каталогам поиска из карточки CD-04.
const (
	cd04ProjectHook = "/repo/.dmux-hooks/claimed.sh"
	cd04LocalHook   = "/repo/.dmux/hooks/claimed.sh"
	cd04GlobalHook  = "/home/user/.dmux/hooks/claimed.sh"
)

func TestResolveHookPrefersProjectWhenAllExecutable(t *testing.T) {
	candidates := []HookCandidate{
		{Path: cd04ProjectHook, Executable: true},
		{Path: cd04LocalHook, Executable: true},
		{Path: cd04GlobalHook, Executable: true},
	}

	got, warnings := ResolveHook(candidates)
	if got != cd04ProjectHook {
		t.Fatalf("выбран %q, нужно %q", got, cd04ProjectHook)
	}
	if len(warnings) != 0 {
		t.Fatalf("предупреждений %d, нужно 0: %v", len(warnings), warnings)
	}
}

func TestResolveHookWarnsAboutNonExecutableProjectAndContinues(t *testing.T) {
	candidates := []HookCandidate{
		{Path: cd04ProjectHook, Executable: false},
		{Path: cd04LocalHook, Executable: true},
		{Path: cd04GlobalHook, Executable: true},
	}

	got, warnings := ResolveHook(candidates)
	if got != cd04LocalHook {
		t.Fatalf("выбран %q, нужно %q", got, cd04LocalHook)
	}
	if len(warnings) != 1 {
		t.Fatalf("предупреждений %d, нужно 1: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], cd04ProjectHook) {
		t.Fatalf("предупреждение не называет путь проекта: %s", warnings[0])
	}
}

func TestResolveHookLocalOverrideAndGlobalFallback(t *testing.T) {
	withLocal := []HookCandidate{
		{Path: cd04LocalHook, Executable: true},
		{Path: cd04GlobalHook, Executable: true},
	}
	got, _ := ResolveHook(withLocal)
	if got != cd04LocalHook {
		t.Fatalf("при локальном скрипте выбран %q, нужно %q", got, cd04LocalHook)
	}

	// Локальный скрипт удалён, остался глобальный.
	withoutLocal := []HookCandidate{
		{Path: cd04GlobalHook, Executable: true},
	}
	got, _ = ResolveHook(withoutLocal)
	if got != cd04GlobalHook {
		t.Fatalf("после удаления локального выбран %q, нужно %q", got, cd04GlobalHook)
	}
}

func TestResolveHookNoExecutableCandidate(t *testing.T) {
	candidates := []HookCandidate{
		{Path: cd04ProjectHook, Executable: false},
		{Path: cd04LocalHook, Executable: false},
		{Path: cd04GlobalHook, Executable: false},
	}

	got, warnings := ResolveHook(candidates)
	if got != "" {
		t.Fatalf("выбран %q, нужно пустое имя", got)
	}
	if len(warnings) != len(candidates) {
		t.Fatalf("предупреждений %d, нужно %d: %v", len(warnings), len(candidates), warnings)
	}
}
