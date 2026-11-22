package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"testing"
)

const helperEnv = "GO_WANT_TMUX_HELPER"

// TestTmuxHelperProcess emulates the tmux binary as a child process. It is
// inert unless GO_WANT_TMUX_HELPER is set, so it is a no-op during a normal
// test run of this package.
func TestTmuxHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	sep := -1
	for i, a := range os.Args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || len(os.Args) < sep+3 {
		os.Exit(2)
	}
	code, err := strconv.Atoi(os.Args[sep+2])
	if err != nil {
		os.Exit(2)
	}
	os.Stdout.WriteString(os.Args[sep+1])
	os.Exit(code)
}

func fakeCmd(stdout string, exitCode int) *exec.Cmd {
	args := []string{"-test.run=TestTmuxHelperProcess", "--", stdout, strconv.Itoa(exitCode)}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	return cmd
}

func TestRunnerRunPassesBinaryAndArgs(t *testing.T) {
	ctx := context.Background()
	wantArgs := []string{"capture-pane", "-t", "session:0.0", "-p"}
	var gotName string
	var gotArgs []string
	var gotCtx context.Context

	r := Runner{
		Command: func(c context.Context, name string, args ...string) *exec.Cmd {
			gotCtx = c
			gotName = name
			gotArgs = append([]string(nil), args...)
			return fakeCmd("pane content", 0)
		},
	}

	out, err := r.Run(ctx, wantArgs...)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if string(out) != "pane content" {
		t.Errorf("stdout = %q, want %q", out, "pane content")
	}
	if gotCtx != ctx {
		t.Error("Run did not forward the context to Command")
	}
	if gotName != "tmux" {
		t.Errorf("command name = %q, want the Binary constant value %q", gotName, "tmux")
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("args = %q, want %q", gotArgs, wantArgs)
	}
}

func TestRunnerRunReturnsCommandError(t *testing.T) {
	r := Runner{
		Command: func(c context.Context, name string, args ...string) *exec.Cmd {
			return fakeCmd("", 1)
		},
	}

	_, err := r.Run(context.Background(), "capture-pane")
	if err == nil {
		t.Fatal("Run returned nil error for a failing command, want *exec.ExitError")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %T (%v), want *exec.ExitError", err, err)
	}
}
