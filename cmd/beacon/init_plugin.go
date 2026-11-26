package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// pluginSource resolves the project plugin source directory.
func pluginSource(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if fromEnv := envString("BEACON_PLUGIN_DIR", ""); fromEnv != "" {
		return fromEnv
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "..", ".opencode", "plugins", "beacon-project"))
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate
	}
	return ""
}

// deployPlugin copies the project plugin into the repository.
func deployPlugin(root string, o initOptions) error {
	if o.skipPlugin {
		return nil
	}
	src := pluginSource(o.pluginDir)
	if src == "" {
		fmt.Fprintln(os.Stderr, "warning: no plugin source found; pass --plugin-dir to install the project plugin")
		return nil
	}
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "warning: plugin source is not a directory; pass --plugin-dir")
		return nil
	}
	dst := filepath.Join(root, ".opencode", "plugins", "beacon-project")
	if err := copyTree(src, dst); err != nil {
		return err
	}
	fmt.Printf("installed project plugin from %s\n", src)
	return nil
}

// copyTree recursively copies src into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
