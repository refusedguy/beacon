package main

import (
	"context"
	"errors"
	"log"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// setupShutdownTest isolates the process-wide seams that setupObserve owns so
// a fake collector can decide whether the drain is confirmed.
func setupShutdownTest(t *testing.T) {
	t.Helper()
	t.Setenv("BEACON_STATE_DIR", t.TempDir())

	previousSink := observe.Default()
	previousOutput := log.Writer()
	previousDrain, previousTimeout, previousExit := shutdownDrain, shutdownTimeout, osExit
	t.Cleanup(func() {
		observe.SetDefault(previousSink)
		log.SetOutput(previousOutput)
		shutdownDrain, shutdownTimeout, osExit = previousDrain, previousTimeout, previousExit
	})
}

func TestSetupObserveConfirmedDrainKeepsExitCode(t *testing.T) {
	setupShutdownTest(t)

	var drained atomic.Int32
	shutdownDrain = func(context.Context) error {
		drained.Add(1)
		return nil
	}
	var codes []int
	osExit = func(code int) { codes = append(codes, code) }

	cleanup := setupObserve("list")
	cleanup()

	if drained.Load() != 1 {
		t.Fatalf("дренаж вызван %d раз, ожидался 1", drained.Load())
	}
	if len(codes) != 0 {
		t.Fatalf("подтверждённый дренаж завершил процесс: %v", codes)
	}
}

func TestSetupObserveFailedDrainExitsNonzero(t *testing.T) {
	setupShutdownTest(t)

	shutdownDrain = func(context.Context) error { return errors.New("collector down") }
	var codes []int
	osExit = func(code int) { codes = append(codes, code) }

	cleanup := setupObserve("list")
	cleanup()

	if len(codes) != 1 || codes[0] == 0 {
		t.Fatalf("ошибка дренажа дала коды %v, ожидался один ненулевой", codes)
	}
}

func TestSetupObserveDrainTimeoutExitsNonzeroAndIsBounded(t *testing.T) {
	setupShutdownTest(t)

	shutdownTimeout = 20 * time.Millisecond
	shutdownDrain = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	var codes []int
	osExit = func(code int) { codes = append(codes, code) }

	start := time.Now()
	cleanup := setupObserve("list")
	cleanup()
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("teardown не ограничен таймером: %s", elapsed)
	}
	if len(codes) != 1 || codes[0] == 0 {
		t.Fatalf("таймаут дренажа дал коды %v, ожидался один ненулевой", codes)
	}
}

func TestSetupObserveTeardownIsIdempotent(t *testing.T) {
	setupShutdownTest(t)

	var drained atomic.Int32
	shutdownDrain = func(context.Context) error {
		drained.Add(1)
		return errors.New("collector down")
	}
	var codes []int
	osExit = func(code int) { codes = append(codes, code) }

	cleanup := setupObserve("list")
	cleanup()
	cleanup()

	if drained.Load() != 1 {
		t.Fatalf("дренаж вызван %d раз, ожидался 1", drained.Load())
	}
	if len(codes) != 1 {
		t.Fatalf("теardown завершил процесс %d раз, ожидался 1", len(codes))
	}
}
