package taskstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyCloseFieldsWritesStatusAndReason(t *testing.T) {
	o := CloseCardOptions{
		Status:      "duplicate",
		Reason:      "duplicate",
		DuplicateOf: "HD-007",
		ClosedBy:    "agent-1",
		ClosedAt:    "2026-11-21T10:00:00Z",
	}
	got, err := ApplyCloseFields(validCard, o)
	if err != nil {
		t.Fatalf("ApplyCloseFields: %v", err)
	}
	for _, want := range []string{
		"Статус: duplicate",
		"Причина: duplicate",
		"Дубликат: HD-007",
		"Закрыл: agent-1",
		"Время закрытия: 2026-11-21T10:00:00Z",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет строки %q в:\n%s", want, got)
		}
	}
	card, err := ParseCard("T2.4.md", []byte(got))
	if err != nil {
		t.Fatalf("карточка после записи не разбирается: %v", err)
	}
	if card.Status != "duplicate" || card.Reason != "duplicate" ||
		card.DuplicateOf != "HD-007" || card.ClosedBy != "agent-1" ||
		card.ClosedAt != "2026-11-21T10:00:00Z" {
		t.Fatalf("поля закрытия не разобраны: %+v", card)
	}
	if card.Extra["Причина"] != "duplicate" {
		t.Fatal("Причина пропала из Extra: старое чтение сломано")
	}
}

func TestApplyCloseFieldsRejectsBadOptions(t *testing.T) {
	cases := []struct {
		name string
		opt  CloseCardOptions
	}{
		{"статус", CloseCardOptions{Status: "bogus"}},
		{"дубль без цели", CloseCardOptions{Status: "duplicate"}},
		{"время", CloseCardOptions{Status: "done", ClosedAt: "вчера"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ApplyCloseFields(validCard, tc.opt); Code(err) != ExitValidation {
				t.Fatalf("ожидали код 1, получили %v", err)
			}
		})
	}
}

func TestApplyCloseFieldsDefaultsCloseTime(t *testing.T) {
	got, err := ApplyCloseFields(validCard, CloseCardOptions{Status: "blocked"})
	if err != nil {
		t.Fatalf("ApplyCloseFields: %v", err)
	}
	card, err := ParseCard("T2.4.md", []byte(got))
	if err != nil {
		t.Fatalf("карточка не разбирается: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, card.ClosedAt); err != nil {
		t.Fatalf("время закрытия не RFC3339: %q, %v", card.ClosedAt, err)
	}
}

func TestApplyCloseFieldsIdempotent(t *testing.T) {
	o := CloseCardOptions{
		Status: "done", Reason: "completed", ClosedBy: "agent-1",
		ClosedAt: "2026-11-21T10:00:00Z",
	}
	first, err := ApplyCloseFields(validCard, o)
	if err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	second, err := ApplyCloseFields(first, o)
	if err != nil {
		t.Fatalf("вторая запись: %v", err)
	}
	if second != first {
		t.Fatal("повторная запись изменила карточку")
	}
}

func TestApplyCloseFieldsClearsStaleDuplicate(t *testing.T) {
	dup, err := ApplyCloseFields(validCard, CloseCardOptions{
		Status: "duplicate", DuplicateOf: "HD-007", ClosedAt: "2026-11-21T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("дубль: %v", err)
	}
	done, err := ApplyCloseFields(dup, CloseCardOptions{
		Status: "done", Reason: "already_landed", ClosedAt: "2026-11-22T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("перезапись: %v", err)
	}
	if strings.Contains(done, "Дубликат:") {
		t.Fatalf("старый Дубликат остался в:\n%s", done)
	}
}

func TestCloseCardWritesUnderLock(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	cardPath := filepath.Join(root, "T2.4.md")
	if err := os.WriteFile(cardPath, []byte(validCard), 0644); err != nil {
		t.Fatal(err)
	}
	o := CloseCardOptions{
		Status: "blocked", Reason: "ждём owner", Blocker: "watch-1",
		ClosedBy: "agent-1", ClosedAt: "2026-11-21T10:00:00Z",
	}
	if err := CloseCard(LockPath(state, root), root, cardPath, o); err != nil {
		t.Fatalf("CloseCard: %v", err)
	}
	if _, err := os.Stat(LockPath(state, root)); !os.IsNotExist(err) {
		t.Fatalf("замок не отпущен: %v", err)
	}
	data, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	card, err := ParseCard("T2.4.md", data)
	if err != nil {
		t.Fatalf("карточка после CloseCard не разбирается: %v", err)
	}
	if card.Status != "blocked" || card.Reason != "ждём owner" || card.Blocker != "watch-1" {
		t.Fatalf("поля блокера не записаны: %+v", card)
	}
}
