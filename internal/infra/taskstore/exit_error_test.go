package taskstore

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestExitErrorKeepsCause(t *testing.T) {
	base := errors.New("исходная причина")
	err := StartupfWithCause(base, "контекст: %v", base)

	if want := "контекст: исходная причина"; err.Error() != want {
		t.Fatalf("Error() = %q, ждали %q", err.Error(), want)
	}
	if got := Code(err); got != ExitStartup {
		t.Fatalf("Code = %d, ждали %d", got, ExitStartup)
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("errors.As на *ExitError не сработал: %v", err)
	}
	if ee.Unwrap() != base {
		t.Fatalf("Unwrap = %v, ждали исходную причину %v", ee.Unwrap(), base)
	}
	if !errors.Is(err, base) {
		t.Fatalf("errors.Is не видит исходную причину: %v", err)
	}
	if !errors.Is(fmt.Errorf("снаружи: %w", err), base) {
		t.Fatalf("errors.Is не видит причину через обёртку: %v", err)
	}
}

func TestExitErrorWithoutCauseUnwrapsToNil(t *testing.T) {
	base := errors.New("исходная причина")
	for _, err := range []error{
		Startupf("контекст"),
		Validationf("контекст"),
	} {
		var ee *ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("errors.As на *ExitError не сработал: %v", err)
		}
		if ee.Unwrap() != nil {
			t.Fatalf("Unwrap = %v, ждали nil", ee.Unwrap())
		}
		if errors.Is(err, base) {
			t.Fatalf("errors.Is нашёл причину там, где её нет: %v", err)
		}
	}
}

func TestReplaceCardsKeepsCause(t *testing.T) {
	idx := openTestIndex(t)
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	changed := []pendingCard{{card: Card{ID: "T3.7"}}}
	err := idx.replaceCards(changed, nil)
	if err == nil {
		t.Fatal("replaceCards на закрытом индексе вернул nil")
	}
	if !errors.Is(err, sql.ErrDBClosed) {
		t.Fatalf("errors.Is не видит sql.ErrDBClosed: %v", err)
	}
	if want := fmt.Sprintf("не начато обновление индекса: %v", sql.ErrDBClosed); err.Error() != want {
		t.Fatalf("Error() = %q, ждали %q", err.Error(), want)
	}
	if got := Code(err); got != ExitStartup {
		t.Fatalf("Code = %d, ждали %d", got, ExitStartup)
	}
}
