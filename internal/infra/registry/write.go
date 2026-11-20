// Package registry writes the author-owned agent registry configuration.
// This file owns the atomic config write (T5.27, ADR 0086).
package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// defaultConfigPerm is the mode for a new file when the caller passes zero.
// The author config carries no secrets (ADR 0086), so a readable file is the
// right default.
const defaultConfigPerm os.FileMode = 0o644

// tempPrefix names the staging files. It differs from the taskstore prefix so
// a leftover from either writer can be attributed to one of them.
const tempPrefix = ".beacon-registry-"

// AtomicWriteConfig replaces path with data in one atomic step. It stages the
// bytes in a temporary file in the same directory, syncs and closes that file,
// then renames it over path. A concurrent reader therefore sees either the
// complete old file or the complete new file, never a half-written one.
//
// When path already exists, its permission bits are preserved across the
// replace; perm is used only for a new file. A zero perm for a new file selects
// defaultConfigPerm. On any error before the rename, the original file is left
// untouched and the staging file is removed. Writing the same data twice leaves
// exactly those bytes in the file.
func AtomicWriteConfig(path string, data []byte, perm os.FileMode) error {
	return writeConfig(path, data, perm, nil)
}

// writeConfig is the internal form of AtomicWriteConfig. beforePublish runs
// after the staging file is synced and closed but before the rename; an error
// from it aborts the write and leaves path unchanged. Tests use the hook to
// simulate an interruption and prove the original file survives it.
func writeConfig(path string, data []byte, perm os.FileMode, beforePublish func() error) error {
	mode, err := configMode(path, perm)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, tempPrefix)
	if err != nil {
		return fmt.Errorf("registry: write config: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("registry: write config: write temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("registry: write config: sync temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("registry: write config: close temp %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("registry: write config: chmod temp %s: %w", tmp, err)
	}
	if beforePublish != nil {
		if err := beforePublish(); err != nil {
			return fmt.Errorf("registry: write config: interrupted before publish: %w", err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("registry: write config: rename %s -> %s: %w", tmp, path, err)
	}
	committed = true
	return syncConfigDir(dir)
}

// configMode returns the mode to apply to the staging file. An existing target
// keeps its own permission bits so an atomic replace cannot silently widen or
// narrow access. A missing target uses perm, or defaultConfigPerm if perm is
// zero. A stat error other than "not found" is reported instead of guessed.
func configMode(path string, perm os.FileMode) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().Perm(), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("registry: write config: stat %s: %w", path, err)
	}
	if perm == 0 {
		return defaultConfigPerm, nil
	}
	return perm, nil
}

// syncConfigDir flushes the directory entry after the rename so a crash cannot
// lose the new name after the data has reached disk.
func syncConfigDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("registry: write config: open dir %s: %w", dir, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("registry: write config: sync dir %s: %w", dir, err)
	}
	return nil
}
