package hookrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
)

const (
	hookrunHelperEnv     = "BEACON_HOOKRUN_HELPER"
	hookrunHelperModeEnv = "BEACON_HOOKRUN_MODE"
)

// TestMain doubles as the helper-process entry point. Run starts this test
// binary as a child with the helper env set, so no external shell or utility is
// needed. Without the env flag the package runs its tests as usual.
func TestMain(m *testing.M) {
	if os.Getenv(hookrunHelperEnv) == "1" {
		runHookrunHelper()
		return
	}
	os.Exit(m.Run())
}

func runHookrunHelper() {
	switch os.Getenv(hookrunHelperModeEnv) {
	case "echo":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stdout, "stdin:[%s]", data)
	case "streams":
		fmt.Fprint(os.Stdout, "out-fragment")
		fmt.Fprint(os.Stderr, "err-fragment")
	case "fail":
		fmt.Fprint(os.Stdout, "out-before-fail")
		fmt.Fprint(os.Stderr, "err-before-fail")
		os.Exit(3)
	default:
		os.Exit(2)
	}
}

// helperRun runs the package's own test binary as a hook script in the
// requested helper mode. The env is inherited by the child through t.Setenv.
func helperRun(t *testing.T, mode, stdin string) ([]byte, error) {
	t.Helper()
	t.Setenv(hookrunHelperEnv, "1")
	t.Setenv(hookrunHelperModeEnv, mode)
	return Run(os.Args[0], stdin)
}

func TestRunPassesStdin(t *testing.T) {
	out, err := helperRun(t, "echo", "payload-from-parent")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []byte("stdin:[payload-from-parent]"); !bytes.Contains(out, want) {
		t.Fatalf("output %q does not contain stdin echo %q", out, want)
	}
}

func TestRunEmptyStdin(t *testing.T) {
	out, err := helperRun(t, "echo", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []byte("stdin:[]"); !bytes.Contains(out, want) {
		t.Fatalf("output %q does not contain empty stdin marker %q", out, want)
	}
}

func TestRunReturnsStdoutAndStderr(t *testing.T) {
	out, err := helperRun(t, "streams", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"out-fragment", "err-fragment"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
}

func TestRunSurfacesExitCodeAndOutput(t *testing.T) {
	out, err := helperRun(t, "fail", "")
	if err == nil {
		t.Fatal("Run returned nil error for a non-zero exit")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %T (%v) is not *exec.ExitError", err, err)
	}
	if got := exitErr.ExitCode(); got != 3 {
		t.Fatalf("ExitCode = %d, want 3", got)
	}
	for _, want := range []string{"out-before-fail", "err-before-fail"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
}

func TestRunContextPassesStdin(t *testing.T) {
	t.Setenv(hookrunHelperEnv, "1")
	t.Setenv(hookrunHelperModeEnv, "echo")
	out, err := RunContext(context.Background(), os.Args[0], "ctx-payload")
	if err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	if want := []byte("stdin:[ctx-payload]"); !bytes.Contains(out, want) {
		t.Fatalf("output %q does not contain stdin echo %q", out, want)
	}
}
