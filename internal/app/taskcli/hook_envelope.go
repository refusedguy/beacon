// Контракт переменных окружения для сценариев жизненного цикла карточки.
// Модуль не знает о процессе и не читает родительское окружение: он строит
// контракт из типизированного контекста события.
package taskcli

import (
	"fmt"
	"sort"
	"strings"
)

// Имена переменных окружения контракта. Первые три сохраняют поля текущего
// контракта stdin (event, card_id, time) для совместимости с #1551.
const (
	HookEnvEvent        = "BEACON_EVENT"
	HookEnvCardID       = "BEACON_CARD_ID"
	HookEnvTime         = "BEACON_TIME"
	HookEnvWatchID      = "BEACON_WATCH_ID"
	HookEnvPRNumber     = "BEACON_PR_NUMBER"
	HookEnvBranch       = "BEACON_BRANCH"
	HookEnvWorktreePath = "BEACON_WORKTREE_PATH"
	HookEnvTargetBranch = "BEACON_TARGET_BRANCH"
)

// MandatoryHookEnvCount фиксирует число обязательных полей контракта. Тест
// сверяет его с фактическим набором, чтобы новое поле не попало в контракт
// молча. Необязательное поле TargetBranch в счёт не входит.
const MandatoryHookEnvCount = 7

// HookEnvelope хранит контекст события жизненного цикла карточки. Обязательные
// поля описывают карточку, Watch, PR, ветку и worktree. TargetBranch
// заполняется только для события мержа.
type HookEnvelope struct {
	Event        string
	CardID       string
	Time         string
	WatchID      string
	PRNumber     string
	Branch       string
	WorktreePath string

	// TargetBranch отсутствует в контракте для всех событий, кроме мержа.
	TargetBranch string
}

// hookEnvField связывает поле контекста с именем переменной окружения.
type hookEnvField struct {
	name string
	key  string
	get  func(HookEnvelope) string
}

// mandatoryHookEnv задаёт обязательные поля и порядок их вывода.
var mandatoryHookEnv = [...]hookEnvField{
	{"Event", HookEnvEvent, func(e HookEnvelope) string { return e.Event }},
	{"CardID", HookEnvCardID, func(e HookEnvelope) string { return e.CardID }},
	{"Time", HookEnvTime, func(e HookEnvelope) string { return e.Time }},
	{"WatchID", HookEnvWatchID, func(e HookEnvelope) string { return e.WatchID }},
	{"PRNumber", HookEnvPRNumber, func(e HookEnvelope) string { return e.PRNumber }},
	{"Branch", HookEnvBranch, func(e HookEnvelope) string { return e.Branch }},
	{"WorktreePath", HookEnvWorktreePath, func(e HookEnvelope) string { return e.WorktreePath }},
}

// ValidateHookEnvelope проверяет полноту обязательных полей. Пустой
// TargetBranch ошибкой не считается: для закрытия карточки он не нужен.
func ValidateHookEnvelope(env HookEnvelope) error {
	var missing []string
	for _, field := range mandatoryHookEnv {
		if strings.TrimSpace(field.get(env)) == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("hook envelope: empty mandatory fields: %s", strings.Join(missing, ", "))
}

// BuildHookEnv собирает переменные окружения контракта в виде строк
// «КЛЮЧ=ЗНАЧЕНИЕ». Возвращаются только перечисленные выше ключи. Родительское
// окружение не читается, поэтому секреты процесса не попадают в контракт
// сценария. Вызов не проверяет полноту полей: за это отвечает
// ValidateHookEnvelope.
func BuildHookEnv(env HookEnvelope) []string {
	out := make([]string, 0, len(mandatoryHookEnv)+1)
	for _, field := range mandatoryHookEnv {
		out = append(out, field.key+"="+field.get(env))
	}
	if strings.TrimSpace(env.TargetBranch) != "" {
		out = append(out, HookEnvTargetBranch+"="+env.TargetBranch)
	}
	sort.Strings(out)
	return out
}
