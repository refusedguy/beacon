package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// adr0016MandatoryCommands are the subcommands ADR 0016 requires in the
// machine-readable catalog. A dispatcher that runs a command but leaves it out
// of `beacon list --format json` breaks the contract: agents read the catalog
// and cannot discover the command (BCX-68).
var adr0016MandatoryCommands = []string{"build", "test", "gates", "list", "doctor", "what"}

func TestADR0016MandatoryCommandsInCatalogAndDispatch(t *testing.T) {
	catalogNames := make(map[string]command, len(catalog))
	for _, c := range catalog {
		catalogNames[c.Name] = c
	}

	for _, name := range adr0016MandatoryCommands {
		entry, ok := catalogNames[name]
		if !ok {
			t.Errorf("команда %s обязательна по ADR 0016, но отсутствует в каталоге", name)
			continue
		}
		if entry.Summary == "" || entry.Usage == "" {
			t.Errorf("команда %s в каталоге без summary или usage: %+v", name, entry)
		}
		if runnerFor[name] == nil {
			t.Errorf("команда %s есть в каталоге, но отсутствует в диспетчере", name)
		}
	}
}

// runnerSource locates the runner of a catalog command. Runners build their
// flag sets locally, so the only way for a catalog test to see the flags a
// command actually accepts is to read the runner's declaration.
type runnerSource struct {
	file      string
	signature string
}

var flagContract = map[string]runnerSource{
	"doctor": {file: "doctor.go", signature: "func (c *CLI) runDoctor("},
	"agents": {file: "agents.go", signature: "func (c *CLI) runAgents("},
	"attach": {file: "attach.go", signature: "func parseAttachArgs("},
	"watch":  {file: "watch.go", signature: "func runWatch("},
}

var (
	usageFlagRE   = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
	flagDeclRE    = regexp.MustCompile(`fs\.[A-Za-z]+\("([a-z][a-z0-9-]*)",`)
	literalFlagRE = regexp.MustCompile(`"--([a-z][a-z0-9-]*)"`)
)

func readRunnerFile(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("прочитать %s: %v", file, err)
	}
	return string(data)
}

// functionBody cuts one function out of a source file so flags declared by a
// neighbouring command in the same file do not count as accepted flags.
func functionBody(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("в исходнике нет объявления %q", signature)
	}
	rest := src[start:]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

func namesFromMatches(matches [][]string) map[string]bool {
	out := make(map[string]bool)
	for _, m := range matches {
		out[m[1]] = true
	}
	return out
}

// TestCatalogFlagsMatchRunners keeps Usage an accurate description of the
// flags the runner accepts. The catalog is what `beacon list --format json`
// and `beacon what <command>` print, so an outdated flag list misleads agents
// that discover commands through it (CU-01).
func TestCatalogFlagsMatchRunners(t *testing.T) {
	entries := make(map[string]command, len(catalog))
	for _, c := range catalog {
		entries[c.Name] = c
	}

	for name, source := range flagContract {
		entry, ok := entries[name]
		if !ok {
			t.Errorf("команда %s отсутствует в каталоге", name)
			continue
		}

		src := readRunnerFile(t, source.file)
		body := functionBody(t, src, source.signature)
		advertised := namesFromMatches(usageFlagRE.FindAllStringSubmatch(entry.Usage, -1))
		declared := namesFromMatches(flagDeclRE.FindAllStringSubmatch(body, -1))
		for flag := range namesFromMatches(literalFlagRE.FindAllStringSubmatch(body, -1)) {
			declared[flag] = true
		}

		for flag := range advertised {
			if !declared[flag] {
				t.Errorf("каталог обещает --%s для %s, но раннер его не объявляет: %s", flag, name, entry.Usage)
			}
		}
		for flag := range declared {
			if !advertised[flag] {
				t.Errorf("раннер %s принимает --%s, но каталог его не описывает: %s", name, flag, entry.Usage)
			}
		}
	}

	doctor, ok := entries["doctor"]
	if !ok {
		return
	}
	if !strings.Contains(doctor.Usage, "-- <") {
		t.Errorf("usage doctor должен описывать форму -- <команда>: %s", doctor.Usage)
	}
	if !strings.Contains(readRunnerFile(t, "doctor.go"), "func splitDoctorWatch(") {
		t.Error("раннер doctor не отделяет команду после -- от своих флагов")
	}
}
