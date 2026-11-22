package taskcli

import "fmt"

// HookCandidate описывает один путь поиска сценария события.
// Порядок кандидатов задаёт вызывающая сторона: проект, локальный каталог,
// глобальный каталог. Executable сообщает, есть ли у файла бит исполнения.
// ResolveHook не обращается к файловой системе.
type HookCandidate struct {
	Path       string
	Executable bool
}

// ResolveHook выбирает первый исполняемый кандидат в порядке списка.
// Неисполняемый кандидат даёт предупреждение с его путём и не прерывает поиск.
// Возвращаются выбранный путь и список предупреждений. Пустой путь означает,
// что исполняемых кандидатов нет.
func ResolveHook(candidates []HookCandidate) (string, []string) {
	var warnings []string
	for _, candidate := range candidates {
		if candidate.Executable {
			return candidate.Path, warnings
		}
		warnings = append(warnings, fmt.Sprintf(
			"предупреждение: сценарий %s не исполняемый", candidate.Path,
		))
	}
	return "", warnings
}
