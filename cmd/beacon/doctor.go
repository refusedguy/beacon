package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const minGoVersion = "1.26.0"

var goVersionPattern = regexp.MustCompile(`go([0-9]+)\.([0-9]+)(?:\.([0-9]+))?`)

type checkResult struct {
	name   string
	status string
	detail string
}

func (c *CLI) runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if !parseFlags("doctor", args, c.errOut, fs) {
		return 2
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

func stateDir() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "beacon"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("не удалось определить домашний каталог: %w", err)
	}
	return filepath.Join(home, ".local", "state", "beacon"), nil
}
