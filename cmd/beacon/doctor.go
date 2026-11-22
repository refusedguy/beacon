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

	"github.com/refusedguy/beacon/internal/infra/opencode"
)

// minGoVersion is the single Go threshold doctor checks. It carries the same
// value as go.mod and README.md, so a version bump changes one place. CU-05.
const minGoVersion = "go1.26.0"

var goVersionPattern = regexp.MustCompile(`go([0-9]+)\.([0-9]+)(?:\.([0-9]+))?`)

// parseGoVersion reads a version from a `go version` line or from
// minGoVersion. Both carry the `go` prefix, so one parser serves both.
func parseGoVersion(s string) (major, minor, patch int, ok bool) {
	m := goVersionPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		patch, _ = strconv.Atoi(m[3])
	}
	return major, minor, patch, true
}

// belowGoVersion compares a version against the threshold component by
// component, including the patch that the old numeric literal ignored.
func belowGoVersion(major, minor, patch, wantMajor, wantMinor, wantPatch int) bool {
	if major != wantMajor {
		return major < wantMajor
	}
	if minor != wantMinor {
		return minor < wantMinor
	}
	return patch < wantPatch
}

// Doctor statuses are the four verdicts of RULES-CHECK-CONTRACT.md:9. The
// human output and these constants use the same uppercase register as the
// rules report in commands.go. Per RULES-CHECK-CONTRACT.md:19, FAIL and BROKEN
// block while PASS and WARN do not. A blocking check makes doctor exit 1.
const (
	doctorPass   = "PASS"
	doctorWarn   = "WARN"
	doctorFail   = "FAIL"
	doctorBroken = "BROKEN"
)

type checkResult struct {
	name   string
	status string
	detail string
}

// doctorStatusBlocks is the one blocking rule for every doctor status.
func doctorStatusBlocks(status string) bool {
	return status == doctorFail || status == doctorBroken
}

// renderDoctorChecks prints every check and returns the doctor exit code.
func (c *CLI) renderDoctorChecks(checks []checkResult) int {
	code := 0
	for _, ch := range checks {
		fmt.Fprintf(c.out, "%s: %s %s\n", ch.name, ch.status, ch.detail)
		if doctorStatusBlocks(ch.status) {
			code = 1
		}
	}
	return code
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
	return c.renderDoctorChecks(checks)
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
		return checkResult{"go", doctorBroken, "не удалось выполнить go version: " + strings.TrimSpace(string(res.stderr))}
	}

	line := strings.TrimSpace(string(res.stdout))
	m := goVersionPattern.FindStringSubmatch(line)
	if m == nil {
		return checkResult{"go", doctorBroken, "не удалось разобрать вывод go version: " + line}
	}
	version := m[0]

	major, minor, patch, parsed := parseGoVersion(line)
	wantMajor, wantMinor, wantPatch, thresholdOK := parseGoVersion(minGoVersion)
	if !parsed || !thresholdOK {
		return checkResult{"go", doctorBroken, "не удалось разобрать порог Go: " + minGoVersion}
	}
	if belowGoVersion(major, minor, patch, wantMajor, wantMinor, wantPatch) {
		return checkResult{"go", doctorFail, fmt.Sprintf("%s: требуется >= %s (ADR 0015)", version, strings.TrimPrefix(minGoVersion, "go"))}
	}
	return checkResult{"go", doctorPass, version}
}

func (c *CLI) checkGates() checkResult {
	path, err := findGates(c.root)
	if err != nil {
		return checkResult{"beacon-gates", doctorBroken, err.Error()}
	}
	return checkResult{"beacon-gates", doctorPass, path}
}

func (c *CLI) checkStateDir() checkResult {
	path, err := stateDir(c.root)
	if err != nil {
		return checkResult{"state", doctorBroken, err.Error()}
	}
	info, err := os.Stat(path)
	if err != nil {
		return checkResult{"state", doctorBroken, fmt.Sprintf("%s: %v", path, err)}
	}
	if !info.IsDir() {
		return checkResult{"state", doctorFail, path + ": не каталог"}
	}
	return checkResult{"state", doctorPass, path}
}

// doctorGatesEnv is a developer-tool override for `beacon doctor` only. No
// daemon, MCP tool or CI path reads it, so it is not a production setting.
// Operators can rely on PATH or the built binary in tools/beacon-gates.
const doctorGatesEnv = "BEACON_GATES"

// findGates prefers an explicit developer override, then PATH, then the built
// binary in the module tree. T0.5 owns the tools/beacon-gates directory.
func findGates(root string) (string, error) {
	if path := os.Getenv(doctorGatesEnv); path != "" {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("%s=%s: файл не найден", doctorGatesEnv, path)
		}
		if info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("%s=%s: файл не исполняемый", doctorGatesEnv, path)
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
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s: файл не исполняемый", path)
	}
	return path, nil
}

// beaconStateDirEnv overrides the whole state directory. It is the explicit
// knob for callers that manage project scoping themselves, and for tests that
// need an isolated store without changing the working directory.
const beaconStateDirEnv = "BEACON_STATE_DIR"

// beaconProjectRootEnv carries the canonical project root from the OpenCode
// plugin to the MCP process. It is the explicit input that replaces the old
// os.Getwd() guess for a plugin instance loaded from a worktree (ADR 0069).
const beaconProjectRootEnv = "BEACON_PROJECT_ROOT"

// legacyProjectRootEnvs are the old names for the project root. They remain
// readable during migration, but each one warns on use. BEACON_PROJECT_ROOT is
// the one name every consumer documents.
var legacyProjectRootEnvs = []string{"BEACON_ROOT", "BEACON_REPO"}

// projectRootFromEnv resolves the project root environment variable for every
// consumer: doctor, task config and MCP config. The canonical name wins. A
// legacy name is used only when the canonical name is empty, and always prints
// a warning. An empty result means the caller falls back to its own detection.
func projectRootFromEnv() string {
	root := strings.TrimSpace(os.Getenv(beaconProjectRootEnv))
	for _, name := range legacyProjectRootEnvs {
		if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
			fmt.Fprintf(os.Stderr, "beacon: %s устарел, используйте %s\n", name, beaconProjectRootEnv)
			if root == "" {
				root = raw
			}
		}
	}
	return root
}

// stateDir returns the state directory of the project the caller runs in.
//
// An explicit project root passed by the caller wins over the root in
// BEACON_PROJECT_ROOT, which the OpenCode plugin sets for its MCP server. Both
// beat the legacy detection from the working directory. With neither input the
// old rules stay: a working directory containing .git gets a project-scoped
// store under <base>/beacon/projects/<name>-<hash>, anything else keeps the
// legacy <base>/beacon path (ADR 0011).
//
// The variadic form keeps the many existing no-argument call sites unchanged.
func stateDir(root ...string) (string, error) {
	if override := os.Getenv(beaconStateDirEnv); override != "" {
		return override, nil
	}
	base, err := stateBaseDir()
	if err != nil {
		return "", err
	}
	projectRoot := projectRootFromEnv()
	for _, candidate := range root {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			projectRoot = trimmed
			break
		}
	}
	if projectRoot != "" {
		key, err := projectStateKey(projectRoot)
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "beacon", "projects", key), nil
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
// The fallback lives in opencode.StateBaseDir so the XDG rule is written once
// and shared with the service discovery path (ADR 0092).
func stateBaseDir() (string, error) {
	return opencode.StateBaseDir()
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
