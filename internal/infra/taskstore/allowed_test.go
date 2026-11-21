package taskstore

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// cardWithFields собирает карточку с заданными Типом, Статусом и Фазой.
// Остальные поля валидны, поэтому ParseCard отклоняет только подставленное поле.
func cardWithFields(typ, status, phase string) []byte {
	return []byte(fmt.Sprintf(`# T2.4. Пример карточки
Статус: %s
Фаза: %s
Тип: %s
Владелец файлов: internal/infra/mcp/server.go
Зависимости: T1.1
Ссылка: ADR 0003
Агент: нет
Время: нет
Цель: цель
Контекст: контекст
Шаги: шаги
Проверка: проверка
Приёмка: приёмка
`, status, phase, typ))
}

func TestValidSetsComplete(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"types", ValidTypes(), []string{"task", "epic", "adr", "doc", "eval"}},
		{"statuses", ValidStatuses(), []string{"todo", "in_progress", "done", "dropped"}},
		{"phases", ValidPhases(), []string{"P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.got, tc.want) {
				t.Errorf("множество = %v, ждали %v", tc.got, tc.want)
			}
		})
	}
}

// TestValidSetsMatchParseCard использует ParseCard как эталон. Каждое значение
// экспортированного множества парсер обязан принять, а значение вне множества
// отклонить с кодом валидации и именем поля.
func TestValidSetsMatchParseCard(t *testing.T) {
	cases := []struct {
		name, field string
		values      []string
	}{
		{"types", "Тип", ValidTypes()},
		{"statuses", "Статус", ValidStatuses()},
		{"phases", "Фаза", ValidPhases()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, v := range tc.values {
				typ, status, phase := "task", "todo", "P2"
				switch tc.field {
				case "Тип":
					typ = v
				case "Статус":
					status = v
				case "Фаза":
					phase = v
				}
				if _, err := ParseCard("T2.4.md", cardWithFields(typ, status, phase)); err != nil {
					t.Errorf("ParseCard отклонил допустимое значение %s=%q: %v", tc.field, v, err)
				}
			}

			typ, status, phase := "task", "todo", "P2"
			switch tc.field {
			case "Тип":
				typ = "bogus"
			case "Статус":
				status = "bogus"
			case "Фаза":
				phase = "bogus"
			}
			_, err := ParseCard("T2.4.md", cardWithFields(typ, status, phase))
			if err == nil {
				t.Fatalf("ParseCard принял значение вне множества %s", tc.field)
			}
			if Code(err) != ExitValidation || !strings.Contains(err.Error(), tc.field) {
				t.Errorf("ошибка %q не сообщает о поле %s с кодом 1", err, tc.field)
			}
		})
	}
}

func TestValidPredicates(t *testing.T) {
	checks := []struct {
		name string
		set  []string
		ok   func(string) bool
	}{
		{"type", ValidTypes(), IsValidType},
		{"status", ValidStatuses(), IsValidStatus},
		{"phase", ValidPhases(), IsValidPhase},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			for _, v := range tc.set {
				if !tc.ok(v) {
					t.Errorf("предикат отклонил значение %q из своего множества", v)
				}
			}
			if tc.ok("bogus") {
				t.Errorf("предикат принял значение вне множества")
			}
		})
	}
}

func TestValidSetsReturnCopies(t *testing.T) {
	for _, get := range []func() []string{ValidTypes, ValidStatuses, ValidPhases} {
		first := get()
		first[0] = "changed"
		if second := get(); second[0] == "changed" {
			t.Errorf("изменение результата затронуло каноническое множество: %v", second)
		}
	}
}
