package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const minGoVersion = "1.26.0"

var goVersionPattern = regexp.MustCompile(`go([0-9]+)\.([0-9]+)(?:\.([0-9]+))?`)

type checkResult struct {
	name   string
	status string
	detail string
}

func (c *CLI) runDoctor(args []string) int {
	flags, command := splitDoctorWatch(args)
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	watch := fs.Bool("watch", false, "дешёвый watcher: внешняя проверка условия и wake по событию")
	interval := fs.Duration("interval", 2*time.Second, "интервал внешней проверки условия")
	session := fs.String("session", "", "сессия, для которой emit wake")
	maxTicks := fs.Int("max-ticks", 0, "число проверок до останова, 0 без ограничения")
	if !parseFlags("doctor", flags, c.errOut, fs) {
		return 2
	}
	if *watch {
		return c.runDoctorWatch(doctorWatchSpec{
			check:    command,
			session:  strings.TrimSpace(*session),
			interval: *interval,
			maxTicks: *maxTicks,
		})
	}

	checks := []checkResult{c.checkGo(), c.checkGates(), c.checkStateDir()}
	failed := false
	for _, ch := range checks {
		fmt.Fprintf(c.out, "%s: %s %s\n", ch.name, ch.status, ch.detail)
		if ch.status == "FAIL" {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// splitDoctorWatch separates a trailing `-- <command...>` from doctor's own
// flags. The command is the external, model-free condition check of the
// watcher (ADR 0049 p1). Without `--` there is no command.
func splitDoctorWatch(args []string) (flags, command []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// doctorWatchSpec is one external watcher. check is an argv executed through
// the CLI runner: exit 0 means the condition holds, exit 1 means it does not,
// any other exit is an error. wake emits one event when the condition becomes
// true; delivery of a follow-up stays with ADR 0026.
type doctorWatchSpec struct {
	check    []string
	session  string
	interval time.Duration
	maxTicks int
}

type doctorCheck func() (met bool, detail string, err error)
type doctorWake func(detail string) error

// runDoctorWatcher checks an external condition without calling a model and
// emits a wake only on a false-to-true edge. It has no retries, timers per
// delivery or escalation, so it adds no step to the ADR 0017 ladder. sleep is
// injected to keep tests off real time.
func runDoctorWatcher(check doctorCheck, wake doctorWake, maxTicks int, sleep func()) (wakes, ticks int, err error) {
	met := false
	for maxTicks == 0 || ticks < maxTicks {
		ticks++
		now, detail, checkErr := check()
		if checkErr != nil {
			return wakes, ticks, checkErr
		}
		if now && !met {
			if wakeErr := wake(detail); wakeErr != nil {
				return wakes, ticks, wakeErr
			}
			wakes++
		}
		met = now
		if maxTicks != 0 && ticks >= maxTicks {
			break
		}
		sleep()
	}
	return wakes, ticks, nil
}

// runDoctorWatch wires the external check and the wake event to the CLI. The
// event goes to stdout; the watcher never sends a prompt for the agent.
func (c *CLI) runDoctorWatch(spec doctorWatchSpec) int {
	if len(spec.check) == 0 {
		fmt.Fprintln(c.errOut, "beacon doctor: для --watch нужна внешняя проверка: beacon doctor --watch -- <команда> [аргументы]")
		writeUsage(c.errOut)
		return 2
	}
	if spec.interval <= 0 {
		fmt.Fprintf(c.errOut, "beacon doctor: интервал проверки должен быть положительным: %s\n", spec.interval)
		return 2
	}
	if spec.maxTicks < 0 {
		fmt.Fprintln(c.errOut, "beacon doctor: max-ticks не может быть отрицательным")
		return 2
	}

	check := func() (bool, string, error) {
		args := append([]string(nil), spec.check[1:]...)
		res := c.run(c.root, spec.check[0], args, nil)
		switch res.code {
		case 0:
			return true, strings.TrimSpace(string(res.stdout)), nil
		case 1:
			return false, "", nil
		default:
			reason := strings.TrimSpace(string(res.stderr))
			if reason == "" {
				reason = strings.TrimSpace(string(res.stdout))
			}
			if reason == "" {
				reason = fmt.Sprintf("код выхода %d", res.code)
			}
			return false, "", fmt.Errorf("%s: %s", strings.Join(spec.check, " "), reason)
		}
	}
	wake := func(detail string) error {
		detail = strings.TrimSpace(detail)
		if spec.session == "" {
			fmt.Fprintf(c.out, "wake %s\n", detail)
			return nil
		}
		fmt.Fprintf(c.out, "wake session=%s %s\n", spec.session, detail)
		return nil
	}
	sleep := func() { time.Sleep(spec.interval) }

	wakes, ticks, err := runDoctorWatcher(check, wake, spec.maxTicks, sleep)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon doctor watch: %v\n", err)
		return 2
	}
	if spec.maxTicks > 0 && wakes == 0 {
		fmt.Fprintf(c.errOut, "beacon doctor watch: условие не стало истинным за %d проверок\n", ticks)
		return 1
	}
	return 0
}

func (c *CLI) checkGo() checkResult {
	res := c.run(c.root, "go", []string{"version"}, nil)
	if res.code != 0 {
		return checkResult{"go", "FAIL", "не удалось выполнить go version: " + strings.TrimSpace(string(res.stderr))}
	}

	line := strings.TrimSpace(string(res.stdout))
	m := goVersionPattern.FindStringSubmatch(line)
	if m == nil {
		return checkResult{"go", "FAIL", "не удалось разобрать вывод go version: " + line}
	}

	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	version := m[0]

	if major < 1 || (major == 1 && minor < 26) {
		return checkResult{"go", "FAIL", fmt.Sprintf("%s: требуется >= %s (ADR 0015)", version, minGoVersion)}
	}
	return checkResult{"go", "OK", version}
}

func (c *CLI) checkGates() checkResult {
	path, err := findGates(c.root)
	if err != nil {
		return checkResult{"beacon-gates", "FAIL", err.Error()}
	}
	return checkResult{"beacon-gates", "OK", path}
}

func (c *CLI) checkStateDir() checkResult {
	path, err := stateDir()
	if err != nil {
		return checkResult{"state", "FAIL", err.Error()}
	}
	info, err := os.Stat(path)
	if err != nil {
		return checkResult{"state", "FAIL", fmt.Sprintf("%s: %v", path, err)}
	}
	if !info.IsDir() {
		return checkResult{"state", "FAIL", path + ": не каталог"}
	}
	return checkResult{"state", "OK", path}
}

// findGates prefers an explicit path, then PATH, then the built binary in the
// module tree. T0.5 owns the tools/beacon-gates directory.
func findGates(root string) (string, error) {
	if path := os.Getenv("BEACON_GATES"); path != "" {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("BEACON_GATES=%s: файл не найден", path)
		}
		return path, nil
	}

	if path, err := exec.LookPath("beacon-gates"); err == nil {
		return path, nil
	}

	path := filepath.Join(root, "tools", "beacon-gates", "beacon-gates")
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", errors.New("beacon-gates не найден")
	}
	return path, nil
}

// beaconStateDirEnv overrides the whole state directory. It is the explicit
// knob for callers that manage project scoping themselves, and for tests that
// need an isolated store without changing the working directory.
const beaconStateDirEnv = "BEACON_STATE_DIR"

// stateDir returns the state directory of the project the caller runs in.
//
// When the working directory is a project root (it contains .git, as a file in
// a worktree or a directory in the main checkout) the state is scoped to that
// project under <base>/beacon/projects/<name>-<hash>. Different projects then
// keep separate watches, obligations and open tasks. Outside a project root the
// legacy <base>/beacon path is kept, so tools run from a subdirectory or a
// temporary directory still share one store.
func stateDir() (string, error) {
	if override := os.Getenv(beaconStateDirEnv); override != "" {
		return override, nil
	}
	base, err := stateBaseDir()
	if err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Join(base, "beacon"), nil
	}
	if _, statErr := os.Stat(filepath.Join(cwd, ".git")); statErr != nil {
		return filepath.Join(base, "beacon"), nil
	}
	key, err := projectStateKey(cwd)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "beacon", "projects", key), nil
}

// stateBaseDir resolves XDG_STATE_HOME or the user's home state directory.
func stateBaseDir() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return base, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("не удалось определить домашний каталог: %w", err)
	}
	return filepath.Join(home, ".local", "state"), nil
}

// projectStateKey builds a stable, readable directory key for a project root.
// The hash of the absolute path separates projects with the same folder name;
// the name keeps the state directory recognizable on disk.
func projectStateKey(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("не удалось получить абсолютный путь проекта: %w", err)
	}
	sum := sha256.Sum256([]byte(abs))
	name := filepath.Base(abs)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "project"
	}
	return fmt.Sprintf("%s-%s", name, hex.EncodeToString(sum[:8])), nil
}
