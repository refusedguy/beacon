package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/app/taskcli"
	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// TaskCommand точка входа подкоманды task. Первый аргумент может быть «task»
// или сразу имя команды, чтобы dispetch из main.go не требовал особого случая.
func TaskCommand(args []string, stdout, stderr io.Writer) int {
	cfg, err := defaultTaskConfig()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return taskstore.Code(err)
	}
	return TaskCommandWithConfig(args, stdout, stderr, cfg)
}

// TaskCommandWithConfig выполняет подкоманду task с заданными каталогами.
func TaskCommandWithConfig(args []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	if len(args) > 0 && args[0] == "task" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, taskUsage)
		return taskstore.ExitValidation
	}
	switch args[0] {
	case "--self-test", "self-test":
		if err := taskcli.SelfTest(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return taskstore.ExitValidation
		}
		return taskstore.ExitOK
	case "list":
		return runTaskList(args[1:], stdout, stderr, cfg)
	case "new":
		return runTaskNew(args[1:], stdout, stderr, cfg)
	case "take":
		return runTaskTake(args[1:], stdout, stderr, cfg)
	default:
		fmt.Fprintf(stderr, "неизвестная подкоманда task %q\n%s", args[0], taskUsage)
		return taskstore.ExitValidation
	}
}

const taskUsage = "использование: beacon task list|new|take|--self-test [flags]"

type parsedArgs struct {
	vals  map[string][]string
	bools map[string]bool
	pos   []string
}

func (p *parsedArgs) value(name string) string {
	v := p.vals[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func parseTaskArgs(argv []string, values, bools map[string]bool) (*parsedArgs, error) {
	p := &parsedArgs{vals: map[string][]string{}, bools: map[string]bool{}}
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "--") {
			p.pos = append(p.pos, tok)
			continue
		}
		name, val, hasEq := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
		if bools[name] {
			if hasEq {
				b, err := strconv.ParseBool(val)
				if err != nil {
					return nil, taskstore.Validationf("неверное значение флага --%s", name)
				}
				p.bools[name] = b
			} else {
				p.bools[name] = true
			}
			continue
		}
		if !values[name] {
			return nil, taskstore.Validationf("неизвестный флаг --%s", name)
		}
		if !hasEq {
			if i+1 >= len(argv) {
				return nil, taskstore.Validationf("флаг --%s требует значение", name)
			}
			i++
			val = argv[i]
		}
		p.vals[name] = append(p.vals[name], val)
	}
	return p, nil
}

func failTask(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, err.Error())
	return taskstore.Code(err)
}

func runTaskList(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{
		"status": true, "phase": true, "agent": true, "file": true, "format": true,
	}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) > 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	out, err := taskcli.NewService(cfg).List(taskcli.ListOptions{
		Statuses: p.vals["status"], Phases: p.vals["phase"],
		Agents: p.vals["agent"], Files: p.vals["file"],
		Format: p.value("format"),
	})
	if err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprint(stdout, out)
	return taskstore.ExitOK
}

func runTaskNew(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{
		"type": true, "phase": true, "title": true, "reference": true,
		"owner-files": true, "dependencies": true, "goal": true, "context": true,
		"steps": true, "check": true, "acceptance": true,
	}, map[string]bool{"dry-run": true})
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) > 0 {
		return failTask(stderr, taskstore.Validationf("неожиданный аргумент %q", p.pos[0]))
	}
	id, filePath, err := taskcli.NewService(cfg).New(taskcli.NewOptions{
		Type: p.value("type"), Phase: p.value("phase"), Title: p.value("title"),
		Reference: p.value("reference"), OwnerFiles: p.value("owner-files"),
		Dependencies: p.value("dependencies"), Goal: p.value("goal"),
		Context: p.value("context"), Steps: p.value("steps"), Check: p.value("check"),
		Acceptance: p.value("acceptance"), DryRun: p.bools["dry-run"],
	})
	if err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintf(stdout, "%s %s\n", id, relativePath(cfg.Root, filePath))
	return taskstore.ExitOK
}

func runTaskTake(argv []string, stdout, stderr io.Writer, cfg taskcli.Config) int {
	p, err := parseTaskArgs(argv, map[string]bool{"agent": true, "time": true}, nil)
	if err != nil {
		return failTask(stderr, err)
	}
	if len(p.pos) != 1 {
		return failTask(stderr, taskstore.Validationf("нужен ровно один ID карточки"))
	}
	agent := p.value("agent")
	if agent == "" {
		agent = strings.TrimSpace(os.Getenv("BEACON_AGENT"))
	}
	out, err := taskcli.NewService(cfg).Take(taskcli.TakeOptions{
		ID: p.pos[0], Agent: agent, Time: p.value("time"),
	})
	if err != nil {
		return failTask(stderr, err)
	}
	fmt.Fprintln(stdout, out)
	return taskstore.ExitOK
}

func relativePath(root, filePath string) string {
	if root == "" {
		return filePath
	}
	rel, err := filepath.Rel(root, filePath)
	if err != nil {
		return filePath
	}
	return filepath.ToSlash(rel)
}

func defaultTaskConfig() (taskcli.Config, error) {
	root := strings.TrimSpace(os.Getenv("BEACON_ROOT"))
	if root == "" {
		root = findProjectRoot()
	}
	stateDir, err := taskstore.StateDir()
	if err != nil {
		return taskcli.Config{}, err
	}
	return taskcli.Config{
		TasksDir: filepath.Join(root, "docs", "tasks"),
		ADRDir:   filepath.Join(root, "docs", "adrs"),
		StateDir: stateDir,
		Root:     root,
	}, nil
}

// findProjectRoot поднимается от рабочего каталога к каталогу с go.mod и docs/tasks.
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		_, modErr := os.Stat(filepath.Join(dir, "go.mod"))
		_, tasksErr := os.Stat(filepath.Join(dir, "docs", "tasks"))
		if modErr == nil && tasksErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	cwd, _ := os.Getwd()
	return cwd
}
