package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// registryConfigDir is the directory, relative to a project root, that holds
// the author-owned agent registry config (ADR 0086).
const registryConfigDir = ".beacon"

// registryConfigFile is the name of the author config file.
const registryConfigFile = "agents.yaml"

// registryConfig describes where the agent registry config of a project lives.
//
// Path is the absolute location of agents.yaml. Key is the stable project key
// from projectStateKey; it is empty for the global config, which has no
// project identity. Root is the project root, also empty for the global config.
// Global reports whether the global config was selected.
type registryConfig struct {
	Path   string
	Key    string
	Root   string
	Global bool
}

// findRegistryConfig locates the author config of the current project.
//
// An explicit project root (the --project flag, passed in project) is used
// as-is. Otherwise the working directory is walked upwards: the first
// <dir>/.beacon/agents.yaml seen before the directory containing .git wins,
// and that directory becomes the key source. A working directory outside any
// git project falls back to the global config, whose location follows
// BEACON_STATE_DIR through stateDir (ADR 0011, ADR 0086).
func findRegistryConfig(project string) (registryConfig, error) {
	if root := strings.TrimSpace(project); root != "" {
		return registryConfigAtRoot(root)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return globalRegistryConfig()
	}

	dir := cwd
	for {
		candidate := filepath.Join(dir, registryConfigDir, registryConfigFile)
		if isRegularFile(candidate) {
			return registryConfigFor(dir, candidate)
		}
		if pathExists(filepath.Join(dir, ".git")) {
			return registryConfigAtRoot(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return globalRegistryConfig()
}

// pathExists and isRegularFile use the read-only fs.FS view rooted at the
// parent directory. Direct os.Stat in cmd is reserved for internal/infra by
// the capabilities gate (ADR 0005), so the lookup stays inside that boundary.
func pathExists(path string) bool {
	_, err := fs.Stat(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	return err == nil
}

func isRegularFile(path string) bool {
	info, err := fs.Stat(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	return err == nil && !info.IsDir()
}

// registryConfigAtRoot returns the config location for an explicit or detected
// project root. The config file may be absent; callers decide how to handle
// that. The key comes from the absolute root, so two projects sharing a folder
// name still get different keys (ADR 0086).
func registryConfigAtRoot(root string) (registryConfig, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return registryConfig{}, err
	}
	return registryConfigFor(abs, filepath.Join(abs, registryConfigDir, registryConfigFile))
}

// registryConfigFor binds a config path to the key of its project root.
func registryConfigFor(root, path string) (registryConfig, error) {
	key, err := projectStateKey(root)
	if err != nil {
		return registryConfig{}, err
	}
	return registryConfig{Path: path, Key: key, Root: root}, nil
}

// globalRegistryConfig returns the config used outside a git project. Its base
// is the state directory, so BEACON_STATE_DIR moves the global config with it.
func globalRegistryConfig() (registryConfig, error) {
	base, err := stateDir()
	if err != nil {
		return registryConfig{}, err
	}
	return registryConfig{Path: filepath.Join(base, registryConfigFile), Global: true}, nil
}
