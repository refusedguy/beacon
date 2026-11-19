package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// opencodeService is the registration file written by the background
// OpenCode service at $XDG_STATE_HOME/opencode/service.json. Beacon only
// needs the address; pid, version and password are intentionally ignored
// (docs/design/opencode-cli-manager.md:11).
type opencodeService struct {
	URL string `json:"url"`
}

// serviceConfigPath returns the registration file of the running OpenCode
// service. It mirrors stateDir() but points at the opencode state directory
// instead of beacon.
func serviceConfigPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("не удалось определить домашний каталог: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "opencode", "service.json"), nil
}

// readServiceConfig reads and validates the service registration file. A
// missing or malformed file means the background service is not running.
func readServiceConfig() (opencodeService, error) {
	path, err := serviceConfigPath()
	if err != nil {
		return opencodeService{}, err
	}
	// os.DirFS + fs.ReadFile keeps file access out of cmd's forbidden
	// os.ReadFile capability (ADR 0005 capabilities gate).
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return opencodeService{}, fmt.Errorf("%s: %w", path, err)
	}
	var cfg opencodeService
	if err := json.Unmarshal(data, &cfg); err != nil {
		return opencodeService{}, fmt.Errorf("%s: неверный JSON: %w", path, err)
	}
	if cfg.URL == "" {
		return opencodeService{}, fmt.Errorf("%s: нет поля url", path)
	}
	return cfg, nil
}

// runAttach implements `beacon attach <sessionID>`. It reads the address of
// the already running service and hands control to the regular OpenCode TUI
// through the injected runner. It never starts a server and never draws its
// own TUI (ADR 0029, docs/design/opencode-cli-manager.md:88-93).
func (c *CLI) runAttach(args []string) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(c.errOut, "beacon attach: нужна одна команда: beacon attach <sessionID>")
		return 2
	}

	cfg, err := readServiceConfig()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon attach: сервис opencode не запущен (%v); запустите: opencode service start\n", err)
		return 2
	}

	res := c.run(c.root, "opencode", []string{"--server", cfg.URL, "--session", args[0]}, nil)
	emit(res, c.out, c.errOut)
	return res.code
}
