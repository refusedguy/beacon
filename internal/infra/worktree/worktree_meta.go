// Package worktree stores and checks active-agent markers for worktrees.
package worktree

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// WorktreeMetaDir is the subdirectory under the Beacon state dir that
	// holds worktree metadata. The file name is a short hash of the cleaned
	// worktree path, so paths and agent IDs never leak into file names.
	WorktreeMetaDir = "worktree-meta"
	metaDirPerm     = 0o700
	metaFilePerm    = 0o600
	maxMergeGoals   = 32
)

// Known agents. These are the Watch roles the orchestrator uses. The values
// live here because infra must not import app.
const (
	AgentExecutor     = "executor"
	AgentOrchestrator = "orchestrator"
	AgentFixer        = "fixer"
)

// Known permission modes for a resumed agent.
const (
	PermDefault           = "default"
	PermAcceptEdits       = "acceptEdits"
	PermPlan              = "plan"
	PermBypassPermissions = "bypassPermissions"
)

var (
	ErrMetaAgent    = errors.New("worktree: meta: unknown agent")
	ErrMetaPermMode = errors.New("worktree: meta: unknown permission mode")
	ErrMetaBranch   = errors.New("worktree: meta: invalid branch")
	ErrMetaGoals    = errors.New("worktree: meta: invalid merge goals")
	ErrMetaPath     = errors.New("worktree: meta: path mismatch")
	ErrMetaTime     = errors.New("worktree: meta: invalid timestamp")
)

// Meta is the durable record of the agent working in a worktree. Reopening a
// worktree uses it to select the agent and the permission mode without an
// external registry. MergeGoals is the ordered chain of merge targets.
type Meta struct {
	Path       string    `json:"path"`
	AgentID    string    `json:"agentID"`
	PermMode   string    `json:"permMode"`
	Branch     string    `json:"branch"`
	MergeGoals []string  `json:"mergeGoals"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// metaFile is the part of os.File that the writer needs.
type metaFile interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Close() error
}

// metaFS is the file system boundary for metadata I/O. Tests provide an
// in-memory implementation so they never touch disk.
type metaFS interface {
	ReadFile(string) ([]byte, error)
	MkdirAll(string, os.FileMode) error
	CreateTemp(string, string) (metaFile, error)
	Rename(string, string) error
	Remove(string) error
}

type osMetaFS struct{}

func (osMetaFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (osMetaFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func (osMetaFS) CreateTemp(dir, pattern string) (metaFile, error) {
	return os.CreateTemp(dir, pattern)
}

func (osMetaFS) Rename(old, new string) error { return os.Rename(old, new) }

func (osMetaFS) Remove(name string) error { return os.Remove(name) }

// MetaPath returns the metadata file for worktreeDir under storeDir.
func MetaPath(storeDir, worktreeDir string) string {
	sum := sha256.Sum256([]byte(cleanDir(worktreeDir)))
	return filepath.Join(storeDir, WorktreeMetaDir, fmt.Sprintf("%x.json", sum[:8]))
}

func knownAgent(agent string) bool {
	switch strings.TrimSpace(agent) {
	case AgentExecutor, AgentOrchestrator, AgentFixer:
		return true
	}
	return false
}

func knownPermMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case PermDefault, PermAcceptEdits, PermPlan, PermBypassPermissions:
		return true
	}
	return false
}

// validateMeta checks every field read from disk. A bad record is an error,
// not a silent miss, so reopening cannot trust a tampered file.
func validateMeta(m Meta, worktreeDir string) error {
	if cleanDir(m.Path) == "" || cleanDir(m.Path) != cleanDir(worktreeDir) {
		return fmt.Errorf("%w: got %q, want %q", ErrMetaPath, m.Path, cleanDir(worktreeDir))
	}
	if !knownAgent(m.AgentID) {
		return fmt.Errorf("%w: %q", ErrMetaAgent, m.AgentID)
	}
	if !knownPermMode(m.PermMode) {
		return fmt.Errorf("%w: %q", ErrMetaPermMode, m.PermMode)
	}
	branch := strings.TrimSpace(m.Branch)
	if branch == "" || branch != m.Branch || strings.Contains(branch, "..") ||
		strings.ContainsAny(branch, " \t\n") || strings.HasPrefix(branch, "-") ||
		strings.HasSuffix(branch, ".lock") {
		return fmt.Errorf("%w: %q", ErrMetaBranch, m.Branch)
	}
	if len(m.MergeGoals) > maxMergeGoals {
		return fmt.Errorf("%w: %d goals exceed %d", ErrMetaGoals, len(m.MergeGoals), maxMergeGoals)
	}
	seen := make(map[string]bool, len(m.MergeGoals))
	for _, goal := range m.MergeGoals {
		trimmed := strings.TrimSpace(goal)
		if trimmed == "" || trimmed != goal || seen[trimmed] {
			return fmt.Errorf("%w: %q", ErrMetaGoals, goal)
		}
		seen[trimmed] = true
	}
	if m.UpdatedAt.IsZero() {
		return ErrMetaTime
	}
	return nil
}

func writeMeta(fs metaFS, storeDir, worktreeDir string, m Meta) error {
	m.Path = cleanDir(worktreeDir)
	m.AgentID = strings.TrimSpace(m.AgentID)
	m.PermMode = strings.TrimSpace(m.PermMode)
	m.Branch = strings.TrimSpace(m.Branch)
	m.UpdatedAt = m.UpdatedAt.UTC()
	if err := validateMeta(m, worktreeDir); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	path := MetaPath(storeDir, worktreeDir)
	if err := fs.MkdirAll(filepath.Dir(path), metaDirPerm); err != nil {
		return fmt.Errorf("worktree: meta: mkdir: %w", err)
	}
	name, err := fs.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("worktree: meta: temp: %w", err)
	}
	defer fs.Remove(name.Name())
	if err := name.Chmod(metaFilePerm); err != nil {
		name.Close()
		return fmt.Errorf("worktree: meta: chmod: %w", err)
	}
	if _, err := name.Write(append(data, '\n')); err != nil {
		name.Close()
		return fmt.Errorf("worktree: meta: write temp: %w", err)
	}
	if err := name.Close(); err != nil {
		return fmt.Errorf("worktree: meta: close temp: %w", err)
	}
	if err := fs.Rename(name.Name(), path); err != nil {
		return fmt.Errorf("worktree: meta: replace: %w", err)
	}
	return nil
}

func readMeta(fs metaFS, storeDir, worktreeDir string) (Meta, bool, error) {
	path := MetaPath(storeDir, worktreeDir)
	data, err := fs.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, false, nil
	}
	if err != nil {
		return Meta{}, false, fmt.Errorf("worktree: meta: read: %w", err)
	}
	var m Meta
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Meta{}, false, fmt.Errorf("worktree: meta: %s: %w", path, err)
	}
	if err := validateMeta(m, worktreeDir); err != nil {
		return Meta{}, false, fmt.Errorf("worktree: meta: %s: %w", path, err)
	}
	return m, true, nil
}
