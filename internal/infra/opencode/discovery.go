package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readFile is replaceable in tests. It is the only way this package touches
// the OpenCode registration file (ADR 0060, ADR 0092).
var readFile = os.ReadFile

// Config is the part of $XDG_STATE_HOME/opencode/service.json that Beacon
// needs. PID and Version are read here as well so callers that only need one
// field still share the single Discover() reader.
type Config struct {
	URL      string `json:"url"`
	Password string `json:"password"`
	PID      int    `json:"pid"`
	Version  string `json:"version"`
}

// StateBaseDir resolves XDG_STATE_HOME or the user's home state directory.
// It is shared with the CLI's own state-path helpers so the fallback is
// written once.
func StateBaseDir() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return base, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("не удалось определить домашний каталог: %w", err)
	}
	return filepath.Join(home, ".local", "state"), nil
}

// ServiceRegistrationPath returns the path of the registration file. It does
// not read the file: all reads go through Discover().
func ServiceRegistrationPath() (string, error) {
	base, err := StateBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "opencode", "service.json"), nil
}

// Discover reads and validates the service registration file once. Every path
// that needs the address, password, pid or version of the running OpenCode
// service calls this function; no caller opens the file again (ADR 0092).
func Discover() (Config, error) {
	return discover(readFile)
}

// DiscoverWith is the injectable form of Discover. Tests that replace a file
// reader in their own package use this seam instead of reaching into infra.
func DiscoverWith(read func(string) ([]byte, error)) (Config, error) {
	return discover(read)
}

func discover(read func(string) ([]byte, error)) (Config, error) {
	path, err := ServiceRegistrationPath()
	if err != nil {
		return Config{}, err
	}
	data, err := read(path)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("%s: неверный registration-файл: %w", path, err)
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.URL == "" {
		return Config{}, fmt.Errorf("%s: пустой url в registration-файле", path)
	}
	return cfg, nil
}
