package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type stubDrainer struct {
	fn func(context.Context) error
}

func (s stubDrainer) Shutdown(ctx context.Context) error { return s.fn(ctx) }

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestBoundedShutdownConfirmedDrainPassesDeadline(t *testing.T) {
	var seen context.Context
	drain := stubDrainer{fn: func(ctx context.Context) error {
		seen = ctx
		return nil
	}}

	if err := BoundedShutdown(nil, time.Second, drain); err != nil {
		t.Fatalf("подтверждённый дренаж вернул ошибку: %v", err)
	}
	if seen == nil {
		t.Fatal("дренаж не получил контекст")
	}
	if _, ok := seen.Deadline(); !ok {
		t.Fatal("дренаж не получил контекст с ограниченным временем")
	}
}

func TestBoundedShutdownDrainErrorFailsWithReason(t *testing.T) {
	want := errors.New("collector down")
	var buf bytes.Buffer
	drain := stubDrainer{fn: func(context.Context) error { return want }}

	err := BoundedShutdown(testLogger(&buf), time.Second, drain)
	if !errors.Is(err, want) {
		t.Fatalf("ошибка дренажа потеряна: %v", err)
	}
	if !strings.Contains(buf.String(), "shutdown drain failed") {
		t.Fatalf("причина не записана в лог: %q", buf.String())
	}
}

func TestBoundedShutdownTimeoutFailsWithReason(t *testing.T) {
	var buf bytes.Buffer
	drain := stubDrainer{fn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}

	start := time.Now()
	err := BoundedShutdown(testLogger(&buf), 30*time.Millisecond, drain)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("неподтверждённый дренаж вернул nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидался deadline, получено: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("дренаж не ограничен таймером: %s", elapsed)
	}
	if !strings.Contains(buf.String(), "shutdown drain timed out") {
		t.Fatalf("причина таймаута не записана в лог: %q", buf.String())
	}
}

func TestBoundedShutdownDefaultTimeoutWhenNonPositive(t *testing.T) {
	var seen context.Context
	drain := stubDrainer{fn: func(ctx context.Context) error {
		seen = ctx
		return nil
	}}

	if err := BoundedShutdown(nil, 0, drain); err != nil {
		t.Fatalf("дренаж с таймаутом по умолчанию вернул ошибку: %v", err)
	}
	if seen == nil {
		t.Fatal("дренаж не получил контекст")
	}
	deadline, ok := seen.Deadline()
	if !ok {
		t.Fatal("контекст дренажа не имеет дедлайна")
	}
	if got := time.Until(deadline); got <= 0 || got > DefaultShutdownTimeout {
		t.Fatalf("дедлайн %s вне предела %s", got, DefaultShutdownTimeout)
	}
}
