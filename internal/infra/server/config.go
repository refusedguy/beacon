// Package server owns the loopback HTTP server and its machine-wide lease.
// This file resolves the server settings from built-in defaults and an optional
// flat key: value file. Parsing uses only the standard library.
package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// ConfigFileName is the file name callers pass to Load by convention.
	ConfigFileName = "config.yaml"

	// DefaultAddr binds a free port on the loopback interface. Port zero lets
	// the OS choose the port, the same value http.go uses.
	DefaultAddr = "127.0.0.1:0"

	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultReadTimeout       = 15 * time.Second
	DefaultWriteTimeout      = 30 * time.Second
	DefaultShutdownTimeout   = 3 * time.Second

	// stateDirEnv is the XDG variable named in ADR 0011.
	stateDirEnv = "XDG_STATE_HOME"

	beaconStateName = "beacon"
)

// Config holds the resolved server settings. Timeouts use time.Duration so a
// caller can pass them to http.Server and the shutdown context without parsing
// strings again.
type Config struct {
	Addr              string        `json:"addr" yaml:"addr"`
	StateDir          string        `json:"stateDir" yaml:"stateDir"`
	ReadHeaderTimeout time.Duration `json:"readHeaderTimeout" yaml:"readHeaderTimeout"`
	ReadTimeout       time.Duration `json:"readTimeout" yaml:"readTimeout"`
	WriteTimeout      time.Duration `json:"writeTimeout" yaml:"writeTimeout"`
	ShutdownTimeout   time.Duration `json:"shutdownTimeout" yaml:"shutdownTimeout"`
}

// DefaultStateDir returns $XDG_STATE_HOME/beacon, or ~/.local/state/beacon when
// XDG_STATE_HOME is unset. A relative XDG value stays relative.
func DefaultStateDir() (string, error) {
	if base := strings.TrimSpace(os.Getenv(stateDirEnv)); base != "" {
		return filepath.Join(base, beaconStateName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("server config: home dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", beaconStateName), nil
}

// DefaultConfig returns the built-in settings without reading a file.
func DefaultConfig() (Config, error) {
	dir, err := DefaultStateDir()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Addr:              DefaultAddr,
		StateDir:          dir,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		ShutdownTimeout:   DefaultShutdownTimeout,
	}, nil
}

// Load merges the built-in defaults with the optional flat key: value file at
// path. A missing file leaves the defaults in place and returns no error. An
// empty file does the same. An empty path skips the file layer. A file with an
// unknown key, a malformed line, a bad duration, an invalid address or an empty
// state dir is an error. Load validates the merged result before returning it.
func Load(path string) (Config, error) {
	cfg, err := DefaultConfig()
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(path) != "" {
		if err := applyFile(&cfg, path); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the effective settings. Load calls it, and a caller that
// builds a Config by hand can call it too.
func (c Config) Validate() error {
	if err := validateAddr(c.Addr); err != nil {
		return fmt.Errorf("server config: %w", err)
	}
	if strings.TrimSpace(c.StateDir) == "" {
		return errors.New("server config: stateDir is empty")
	}
	for _, item := range []struct {
		name  string
		value time.Duration
	}{
		{"readHeaderTimeout", c.ReadHeaderTimeout},
		{"readTimeout", c.ReadTimeout},
		{"writeTimeout", c.WriteTimeout},
		{"shutdownTimeout", c.ShutdownTimeout},
	} {
		if item.value <= 0 {
			return fmt.Errorf("server config: %s must be positive", item.name)
		}
	}
	return nil
}

func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("server config: %s: %w", path, err)
	}
	for i, raw := range strings.Split(string(data), "\n") {
		key, value, skip, err := parseLine(raw, path, i+1)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if err := applyKey(cfg, path, i+1, key, value); err != nil {
			return err
		}
	}
	return nil
}

// parseLine reads one key: value line. Blank lines and lines starting with #
// are skipped. A value can be quoted, and a plain value ends at " #".
func parseLine(raw, path string, line int) (key, value string, skip bool, err error) {
	text := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
	if text == "" || strings.HasPrefix(text, "#") {
		return "", "", true, nil
	}
	idx := strings.Index(text, ":")
	if idx < 0 {
		return "", "", false, fmt.Errorf("server config: %s:%d: expected key: value", path, line)
	}
	key = strings.TrimSpace(text[:idx])
	if key == "" {
		return "", "", false, fmt.Errorf("server config: %s:%d: empty key", path, line)
	}
	value, err = parseValue(strings.TrimSpace(text[idx+1:]))
	if err != nil {
		return "", "", false, fmt.Errorf("server config: %s:%d: %w", path, line, err)
	}
	return key, value, false, nil
}

func parseValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw[0] == '"' || raw[0] == '\'' {
		quote := raw[0]
		for i := 1; i < len(raw); i++ {
			if raw[i] == quote {
				return raw[1:i], nil
			}
		}
		return "", errors.New("unterminated quote")
	}
	if idx := strings.Index(raw, " #"); idx >= 0 {
		raw = strings.TrimSpace(raw[:idx])
	}
	return raw, nil
}

// normalizeKey lets addr, Addr and ADDR name the same setting, and lets
// readTimeout, read_timeout and read-timeout name the same setting.
func normalizeKey(key string) string {
	replacer := strings.NewReplacer("_", "", "-", "", " ", "")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(key)))
}

func applyKey(cfg *Config, path string, line int, key, value string) error {
	switch normalizeKey(key) {
	case "addr":
		if err := validateAddr(value); err != nil {
			return fmt.Errorf("server config: %s:%d: addr: %w", path, line, err)
		}
		cfg.Addr = value
	case "statedir":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("server config: %s:%d: stateDir is empty", path, line)
		}
		cfg.StateDir = value
	case "readheadertimeout":
		d, err := parseTimeout(value)
		if err != nil {
			return fmt.Errorf("server config: %s:%d: readHeaderTimeout: %w", path, line, err)
		}
		cfg.ReadHeaderTimeout = d
	case "readtimeout":
		d, err := parseTimeout(value)
		if err != nil {
			return fmt.Errorf("server config: %s:%d: readTimeout: %w", path, line, err)
		}
		cfg.ReadTimeout = d
	case "writetimeout":
		d, err := parseTimeout(value)
		if err != nil {
			return fmt.Errorf("server config: %s:%d: writeTimeout: %w", path, line, err)
		}
		cfg.WriteTimeout = d
	case "shutdowntimeout":
		d, err := parseTimeout(value)
		if err != nil {
			return fmt.Errorf("server config: %s:%d: shutdownTimeout: %w", path, line, err)
		}
		cfg.ShutdownTimeout = d
	default:
		return fmt.Errorf("server config: %s:%d: unknown key %q", path, line, key)
	}
	return nil
}

func parseTimeout(value string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", value, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", value)
	}
	return d, nil
}

// validateAddr checks the shape of host:port and the port range. The bind
// interface stays a decision of http.go, which binds loopback only.
func validateAddr(addr string) error {
	_, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("invalid address %q: bad port", addr)
	}
	return nil
}
