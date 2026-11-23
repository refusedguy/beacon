package taskcli

// hook_actions.go задаёт именованные команды проекта через каталог хуков.
// Это не события инструментов из CS-01: TOOL_LIFECYCLE_EVENTS остаётся в
// плагине OpenCode и здесь не расширяется.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/refusedguy/beacon/internal/infra/hookrun"
)

// HookAction — имя команды проекта, которую можно вызвать через каталог хуков.
type HookAction string

const (
	HookActionTest HookAction = "run_test"
	HookActionDev  HookAction = "run_dev"
)

// HookActionMode — режим запуска сценария действия.
type HookActionMode string

const (
	HookActionModeCheck HookActionMode = "check"
	HookActionModeDev   HookActionMode = "dev"
)

// HookActionSpec — описание сценария действия и режима его запуска.
type HookActionSpec struct {
	Name   HookAction     `json:"name"`
	Script string         `json:"script"`
	Mode   HookActionMode `json:"mode"`
}

// hookActions — единственный реестр разрешённых действий. Имя действия
// совпадает с базисом имени файла сценария до первой точки, как у событий
// жизненного цикла карточки.
var hookActions = map[HookAction]HookActionSpec{
	HookActionTest: {Name: HookActionTest, Script: "run_test.sh", Mode: HookActionModeCheck},
	HookActionDev:  {Name: HookActionDev, Script: "run_dev.sh", Mode: HookActionModeDev},
}

// HookActionNames возвращает отсортированный список зарегистрированных действий.
func HookActionNames() []HookAction {
	names := make([]HookAction, 0, len(hookActions))
	for name := range hookActions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// DescribeHookAction возвращает описание сценария и режим запуска по имени
// действия. Функция не трогает файловую систему и не запускает процесс.
// Неизвестное имя отвергается.
func DescribeHookAction(name HookAction) (HookActionSpec, error) {
	spec, ok := hookActions[name]
	if !ok {
		return HookActionSpec{}, fmt.Errorf("taskcli: неизвестное действие хука %q", name)
	}
	return spec, nil
}

// RunHookAction запускает сценарий действия из dir и возвращает его вывод.
// Неизвестное имя отвергается. Если сценарий не найден, результат пустой.
func RunHookAction(dir string, name HookAction, stdin string) ([]byte, error) {
	spec, err := DescribeHookAction(name)
	if err != nil {
		return nil, err
	}
	names, err := hookFiles(dir, HookEvent(spec.Name))
	if err != nil {
		if os.IsNotExist(err) {
			return []byte{}, nil
		}
		return nil, err
	}
	if len(names) == 0 {
		return []byte{}, nil
	}
	script := names[0]
	for _, candidate := range names {
		if candidate == spec.Script {
			script = candidate
			break
		}
	}
	return hookrun.Run(filepath.Join(dir, script), stdin)
}
