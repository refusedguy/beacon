// Package hookrun runs card lifecycle hook scripts on behalf of the app layer.
// It owns the process execution that the capability gate keeps out of app.
package hookrun

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Mode selects how a hook script is launched and awaited. A caller picks a
// mode per event, so adding a new event never changes this package.
type Mode string

const (
	// Notify fires the script and returns before it finishes. No output is read
	// and no deadline applies.
	Notify Mode = "notify"
	// Sync runs the script under a deadline and returns its combined output.
	Sync Mode = "sync"
	// Gate streams every output line to Spec.OnLine and terminates the script
	// when the deadline expires.
	Gate Mode = "gate"
)

// Valid reports whether m names a supported mode.
func (m Mode) Valid() bool {
	switch m {
	case Notify, Sync, Gate:
		return true
	default:
		return false
	}
}

// Spec is one hook invocation. Timeout bounds Sync and Gate and is ignored by
// Notify. OnLine is called once per output line in Gate mode.
type Spec struct {
	Path    string
	Stdin   string
	Timeout time.Duration
	OnLine  func(string)
}

// Result is the outcome shared by all three modes. Output is nil in Notify
// mode. Terminated is set when Execute sent SIGTERM after a deadline.
type Result struct {
	Mode       Mode
	Success    bool
	Output     []byte
	Err        error
	TimedOut   bool
	Terminated bool
	Detached   bool
}

// Runner launches a script and returns its Process. Tests inject a fake runner
// so no OS process is created.
type Runner interface {
	Start(path, stdin string) (Process, error)
}

// Process is a launched script driven by one mode. Await and Watch are
// alternate paths: a mode calls exactly one of them.
type Process interface {
	// Await blocks until the script exits or ctx expires. It returns combined
	// output and, on expiry, context.DeadlineExceeded.
	Await(ctx context.Context) ([]byte, error)
	// Watch calls onLine for every output line until exit or ctx expiry. It
	// returns the lines collected so far.
	Watch(ctx context.Context, onLine func(string)) ([]byte, error)
	// Terminate sends SIGTERM to the script. Repeated calls are harmless.
	Terminate() error
	// Forget detaches from the script so it can outlive the caller.
	Forget()
}

// Execute launches spec under mode with r and returns the mode's result
// contract. Invalid input is reported in Result.Err instead of panicking.
func Execute(r Runner, mode Mode, spec Spec) Result {
	res := Result{Mode: mode}
	if !mode.Valid() {
		res.Err = fmt.Errorf("hookrun: unknown mode %q", mode)
		return res
	}
	if mode == Notify {
		return executeNotify(r, spec)
	}
	if spec.Timeout <= 0 {
		res.Err = fmt.Errorf("hookrun: mode %q requires a positive timeout", mode)
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), spec.Timeout)
	defer cancel()

	p, err := r.Start(spec.Path, spec.Stdin)
	if err != nil {
		res.Err = err
		return res
	}

	var out []byte
	if mode == Sync {
		out, err = p.Await(ctx)
	} else {
		out, err = p.Watch(ctx, spec.OnLine)
	}
	res.Output = out
	res.Err = err
	res.Success = err == nil
	if errors.Is(err, context.DeadlineExceeded) {
		res.TimedOut = true
		res.Terminated = true
		_ = p.Terminate()
	}
	return res
}

func executeNotify(r Runner, spec Spec) Result {
	res := Result{Mode: Notify, Detached: true}
	p, err := r.Start(spec.Path, spec.Stdin)
	if err != nil {
		res.Err = err
		return res
	}
	p.Forget()
	res.Success = true
	return res
}

// OSRunner launches real scripts. Production wires this runner; tests inject a
// fake instead.
type OSRunner struct{}

func (OSRunner) Start(path, stdin string) (Process, error) {
	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader(stdin)

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("hookrun: pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, fmt.Errorf("hookrun: start %s: %w", path, err)
	}
	_ = pw.Close()

	p := &osProcess{cmd: cmd, reader: pr, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

type osProcess struct {
	cmd     *exec.Cmd
	reader  *os.File
	done    chan struct{}
	waitErr error
	termMu  sync.Once
	termErr error
}

func (p *osProcess) deadline(ctx context.Context) {
	if dl, ok := ctx.Deadline(); ok {
		_ = p.reader.SetReadDeadline(dl)
	}
}

func (p *osProcess) Await(ctx context.Context) ([]byte, error) {
	p.deadline(ctx)
	out, readErr := io.ReadAll(p.reader)
	if ctx.Err() != nil || errors.Is(readErr, os.ErrDeadlineExceeded) {
		return out, context.DeadlineExceeded
	}
	<-p.done
	if p.waitErr != nil {
		return out, p.waitErr
	}
	return out, nil
}

func (p *osProcess) Watch(ctx context.Context, onLine func(string)) ([]byte, error) {
	p.deadline(ctx)
	sc := bufio.NewScanner(p.reader)
	var out []byte
	for sc.Scan() {
		line := sc.Text()
		out = append(out, line...)
		out = append(out, '\n')
		if onLine != nil {
			onLine(line)
		}
		if ctx.Err() != nil {
			return out, context.DeadlineExceeded
		}
	}
	if ctx.Err() != nil || errors.Is(sc.Err(), os.ErrDeadlineExceeded) {
		return out, context.DeadlineExceeded
	}
	if sc.Err() != nil {
		return out, sc.Err()
	}
	<-p.done
	if p.waitErr != nil {
		return out, p.waitErr
	}
	return out, nil
}

func (p *osProcess) Terminate() error {
	p.termMu.Do(func() {
		if p.cmd.Process != nil {
			p.termErr = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	})
	return p.termErr
}

func (p *osProcess) Forget() {
	go func() {
		_, _ = io.Copy(io.Discard, p.reader)
		_ = p.reader.Close()
	}()
}
