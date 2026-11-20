// Package scheduler содержит проверки карточки перед запуском по расписанию.
package scheduler

// Dependency — одна зависимость карточки с заранее известным состоянием.
// Closed true означает, что зависимость закрыта и не удерживает запуск.
type Dependency struct {
	Ref    string
	Closed bool
}

// Card — снимок карточки для проверки перед запуском по расписанию. Status,
// OwnerFiles и состояние зависимостей уже загружены вызывающей стороной,
// поэтому CanSchedule не обращается к файлам, сети или другим источникам.
type Card struct {
	ID           string
	Status       string
	OwnerFiles   []string
	Dependencies []Dependency
}

// CanSchedule решает, можно ли поставить карточку в расписание. Карточка
// годна, когда она в статусе todo, у неё заполнены OwnerFiles и все
// зависимости закрыты. Второй результат — причина отказа; при успехе он пуст.
func CanSchedule(card Card) (bool, string) {
	if card.Status != "todo" {
		return false, "статус " + card.Status + " не todo"
	}
	if len(card.OwnerFiles) == 0 {
		return false, "не заполнены OwnerFiles"
	}
	for _, dep := range card.Dependencies {
		if !dep.Closed {
			return false, "зависимость " + dep.Ref + " не закрыта"
		}
	}
	return true, ""
}
