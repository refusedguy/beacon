package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type w115Call struct {
	name string
	args []string
}

// w115CLI builds a CLI whose runner records calls and returns canned results.
func w115CLI(t *testing.T, respond func(w115Call) runResult) (*CLI, *bytes.Buffer, *bytes.Buffer, *[]w115Call) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	calls := new([]w115Call)
	c := &CLI{
		out:    out,
		errOut: errOut,
		root:   t.TempDir(),
		run: func(dir, name string, args, env []string) runResult {
			call := w115Call{name, append([]string(nil), args...)}
			*calls = append(*calls, call)
			if respond == nil {
				return runResult{}
			}
			return respond(call)
		},
	}
	return c, out, errOut, calls
}

func TestW115SplitDoctorWatch(t *testing.T) {
	flags, command := splitDoctorWatch([]string{"--watch", "--interval", "1s", "--", "/usr/bin/test", "-f", "/tmp/done"})
	if len(flags) != 3 || flags[0] != "--watch" || flags[1] != "--interval" || flags[2] != "1s" {
		t.Fatalf("флаги: %q", flags)
	}
	if len(command) != 3 || command[0] != "/usr/bin/test" || command[1] != "-f" || command[2] != "/tmp/done" {
		t.Fatalf("команда: %q", command)
	}

	flags, command = splitDoctorWatch([]string{"--watch"})
	if len(flags) != 1 || len(command) != 0 {
		t.Fatalf("без --: флаги %q команда %q", flags, command)
	}
}

func TestW115WakesOnlyOnRisingEdge(t *testing.T) {
	seq := []bool{false, true, true, false, true}
	detail := []string{"", "ci green", "ci green", "", "review done"}
	idx := 0
	slept := 0
	wakes := []string{}

	gotWakes, ticks, err := runDoctorWatcher(
		func() (bool, string, error) {
			i := idx
			idx++
			return seq[i], detail[i], nil
		},
		func(text string) error { wakes = append(wakes, text); return nil },
		len(seq),
		func() { slept++ },
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotWakes != 2 || ticks != 5 {
		t.Fatalf("wakes=%d ticks=%d, хотим 2 и 5", gotWakes, ticks)
	}
	if len(wakes) != 2 || wakes[0] != "ci green" || wakes[1] != "review done" {
		t.Fatalf("wake события: %q", wakes)
	}
	if slept != 4 {
		t.Fatalf("sleep вызовов %d, хотим 4 (между проверками)", slept)
	}
}

func TestW115AlreadyTrueWakesOnce(t *testing.T) {
	idx := 0
	wakes := 0
	gotWakes, ticks, err := runDoctorWatcher(
		func() (bool, string, error) { idx++; return true, "ready", nil },
		func(string) error { wakes++; return nil },
		3,
		func() {},
	)
	if err != nil || gotWakes != 1 || wakes != 1 || ticks != 3 {
		t.Fatalf("wakes=%d/%d ticks=%d err=%v", gotWakes, wakes, ticks, err)
	}
}

func TestW115NoWakeWhenConditionNeverMet(t *testing.T) {
	idx := 0
	wakes := 0
	gotWakes, ticks, err := runDoctorWatcher(
		func() (bool, string, error) { idx++; return false, "", nil },
		func(string) error { wakes++; return nil },
		3,
		func() {},
	)
	if err != nil || gotWakes != 0 || wakes != 0 || ticks != 3 {
		t.Fatalf("wakes=%d/%d ticks=%d err=%v", gotWakes, wakes, ticks, err)
	}
}

func TestW115CheckErrorStopsWithoutRetry(t *testing.T) {
	checkErr := errors.New("проверка упала")
	idx := 0
	wakes := 0
	gotWakes, ticks, err := runDoctorWatcher(
		func() (bool, string, error) {
			idx++
			if idx == 2 {
				return false, "", checkErr
			}
			return true, "ready", nil
		},
		func(string) error { wakes++; return nil },
		3,
		func() {},
	)
	if !errors.Is(err, checkErr) {
		t.Fatalf("ошибка %v, хотим %v", err, checkErr)
	}
	if gotWakes != 1 || wakes != 1 || ticks != 2 || idx != 2 {
		t.Fatalf("wakes=%d/%d ticks=%d проверок=%d", gotWakes, wakes, ticks, idx)
	}
}

func TestW115WakeErrorStops(t *testing.T) {
	wakeErr := errors.New("wake недоступен")
	wakes := 0
	gotWakes, ticks, err := runDoctorWatcher(
		func() (bool, string, error) { return true, "ready", nil },
		func(string) error { wakes++; return wakeErr },
		3,
		func() {},
	)
	if !errors.Is(err, wakeErr) {
		t.Fatalf("ошибка %v, хотим %v", err, wakeErr)
	}
	if gotWakes != 0 || wakes != 1 || ticks != 1 {
		t.Fatalf("wakes=%d/%d ticks=%d", gotWakes, wakes, ticks)
	}
}

func TestW115CLIEmitsOneEventAndNoModelCalls(t *testing.T) {
	responses := []runResult{
		{code: 1},
		{stdout: []byte("ci green\n")},
	}
	i := 0
	c, out, errOut, calls := w115CLI(t, func(w115Call) runResult {
		r := responses[i]
		i++
		return r
	})

	args := []string{"--watch", "--interval", "1ms", "--max-ticks", "2", "--session", "ses-1", "--", "/usr/bin/check", "--mode", "ci"}
	if code := c.Run(append([]string{"doctor"}, args...)); code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
	if out.String() != "wake session=ses-1 ci green\n" {
		t.Fatalf("событие: %q", out.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("вызовов %d, хотим 2", len(*calls))
	}
	for _, call := range *calls {
		if call.name != "/usr/bin/check" {
			t.Fatalf("внешний вызов %q, ожидался только check без модели", call.name)
		}
	}
	if (*calls)[0].args[0] != "--mode" || (*calls)[0].args[1] != "ci" {
		t.Fatalf("аргументы check: %q", (*calls)[0].args)
	}
}

func TestW115CLICheckFailureIsHonest(t *testing.T) {
	c, out, errOut, _ := w115CLI(t, func(w115Call) runResult {
		return runResult{code: 5, stderr: []byte("checker crashed\n")}
	})

	code := c.Run([]string{"doctor", "--watch", "--max-ticks", "1", "--", "/usr/bin/check"})
	if code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой при ошибке: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "checker crashed") {
		t.Fatalf("нет причины в stderr: %q", errOut.String())
	}
}

func TestW115CLIRequiresExternalCheck(t *testing.T) {
	c, out, errOut, _ := w115CLI(t, nil)

	if code := c.Run([]string{"doctor", "--watch"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "нужна внешняя проверка") {
		t.Fatalf("нет ошибки: stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestW115CLIBoundedNotMet(t *testing.T) {
	c, out, errOut, calls := w115CLI(t, func(w115Call) runResult { return runResult{code: 1} })

	if code := c.Run([]string{"doctor", "--watch", "--max-ticks", "1", "--", "/usr/bin/check"}); code != 1 {
		t.Fatalf("код выхода %d, ожидался 1", code)
	}
	if out.Len() != 0 || len(*calls) != 1 {
		t.Fatalf("stdout %q, вызовов %d", out.String(), len(*calls))
	}
	if !strings.Contains(errOut.String(), "не стало истинным") {
		t.Fatalf("нет причины в stderr: %q", errOut.String())
	}
}

func TestW115CLIRejectsBadInterval(t *testing.T) {
	c, _, errOut, calls := w115CLI(t, nil)

	if code := c.Run([]string{"doctor", "--watch", "--interval", "0", "--", "/usr/bin/check"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if len(*calls) != 0 || !strings.Contains(errOut.String(), "положительным") {
		t.Fatalf("проверка запущена при плохом интервале: calls %d stderr %q", len(*calls), errOut.String())
	}

	c2, _, errOut2, _ := w115CLI(t, nil)
	if code := c2.Run([]string{"doctor", "--watch", "--max-ticks", "-1", "--", "/usr/bin/check"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut2.String(), "отрицательным") {
		t.Fatalf("нет ошибки max-ticks: %q", errOut2.String())
	}
}
