// Package worktree stores and checks active-agent markers for worktrees.
// A marker is a small JSON file in a Beacon-owned directory, keyed by the
// cleaned path of the worktree. The collector reads it to keep a worktree
// whose agent is still active. Marker I/O never shells out to git.
package worktree

import (
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
	// MarkerDir is the subdirectory under the Beacon state dir that holds
	// active-agent markers. The collector uses the same constant to find them.
	MarkerDir = "worktree-markers"
	// DefaultMarkerTTL is how long a marker stays valid without a touch.
	DefaultMarkerTTL = 30 * time.Minute
	markerDirPerm    = 0o700
	markerFilePerm   = 0o600
)

// ErrMarkerOwnerConflict reports that a different agent already owns the
// marker for a worktree. Callers use errors.Is to separate a refused bind from
// other write errors.
var ErrMarkerOwnerConflict = errors.New("worktree: marker: owned by another agent")

// OwnerConflictError carries the owner of an existing marker and the agent
// whose bind was refused. It satisfies errors.Is against ErrMarkerOwnerConflict.
type OwnerConflictError struct {
	Path      string
	Owner     string
	Requested string
}

func (e *OwnerConflictError) Error() string {
	return fmt.Sprintf("worktree: marker: %s owned by %q; bind by %q refused", e.Path, e.Owner, e.Requested)
}

func (e *OwnerConflictError) Is(target error) bool {
	return target == ErrMarkerOwnerConflict
}

// errMarkerExists is the internal signal that the atomic publish lost the race
// to another writer.
var errMarkerExists = errors.New("worktree: marker: path already exists")

// Marker is the durable record that an agent is working in a worktree.
// Path is the worktree directory the marker belongs to, AgentID identifies
// the agent, and UpdatedAt is the last touch time in UTC.
type Marker struct {
	Path      string    `json:"path"`
	AgentID   string    `json:"agentID"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MarkerPath returns the marker file for worktreeDir under storeDir. The file
// name is a short hash of the cleaned worktree path, so paths and agent IDs
// never leak into file names.
func MarkerPath(storeDir, worktreeDir string) string {
	sum := sha256.Sum256([]byte(cleanDir(worktreeDir)))
	return filepath.Join(storeDir, MarkerDir, fmt.Sprintf("%x.json", sum[:8]))
}

// WriteMarker binds worktreeDir to agentID. The first bind publishes the
// marker with an atomic no-clobber operation. A bind by the same agent is a
// no-op and leaves the existing record untouched. A bind by a different agent
// returns ErrMarkerOwnerConflict instead of replacing the owner.
func WriteMarker(storeDir, worktreeDir, agentID string, now time.Time) (Marker, error) {
	if strings.TrimSpace(worktreeDir) == "" {
		return Marker{}, fmt.Errorf("worktree: marker: empty worktree dir")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return Marker{}, fmt.Errorf("worktree: marker: empty agentID")
	}
	m := Marker{Path: cleanDir(worktreeDir), AgentID: agentID, UpdatedAt: now.UTC()}
	data, err := json.Marshal(m)
	if err != nil {
		return Marker{}, err
	}
	path := MarkerPath(storeDir, worktreeDir)
	if err := os.MkdirAll(filepath.Dir(path), markerDirPerm); err != nil {
		return Marker{}, fmt.Errorf("worktree: marker: mkdir: %w", err)
	}
	switch err := writeMarkerNoClobber(path, append(data, '\n')); {
	case err == nil:
		return m, nil
	case errors.Is(err, errMarkerExists):
		existing, ok, readErr := ReadMarker(storeDir, worktreeDir)
		if readErr != nil {
			return Marker{}, readErr
		}
		if ok && existing.AgentID == agentID {
			return existing, nil
		}
		return Marker{}, &OwnerConflictError{Path: path, Owner: existing.AgentID, Requested: agentID}
	default:
		return Marker{}, err
	}
}

// createMarkerTemp writes data to a temp file in the marker directory and
// returns its path. os.CreateTemp uses O_CREATE|O_EXCL, so two writers never
// share a temp file. Callers remove the returned path when done.
func createMarkerTemp(dir, prefix string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, prefix)
	if err != nil {
		return "", fmt.Errorf("worktree: marker: temp: %w", err)
	}
	name := tmp.Name()
	if err := tmp.Chmod(markerFilePerm); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", fmt.Errorf("worktree: marker: chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", fmt.Errorf("worktree: marker: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("worktree: marker: close temp: %w", err)
	}
	return name, nil
}

// writeMarkerNoClobber publishes data at path only when path does not exist.
// os.Link creates the final name atomically and fails with EEXIST when another
// bind won the race, which is what gives the worktree a single owner.
func writeMarkerNoClobber(path string, data []byte) error {
	name, err := createMarkerTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-", data)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	if err := os.Link(name, path); err != nil {
		if os.IsExist(err) {
			return errMarkerExists
		}
		return fmt.Errorf("worktree: marker: publish: %w", err)
	}
	return nil
}

// replaceMarker atomically replaces an existing marker. The temp file lives in
// the same directory, so the rename cannot cross filesystems.
func replaceMarker(path string, data []byte) error {
	name, err := createMarkerTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-", data)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("worktree: marker: replace: %w", err)
	}
	return nil
}

// ReadMarker loads the marker for worktreeDir. A missing marker returns
// ok=false and no error. A corrupt marker is an error, not a silent miss, so
// the collector cannot treat broken state as an unlocked worktree.
func ReadMarker(storeDir, worktreeDir string) (Marker, bool, error) {
	path := MarkerPath(storeDir, worktreeDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, fmt.Errorf("worktree: marker: read: %w", err)
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, false, fmt.Errorf("worktree: marker: %s: %w", path, err)
	}
	return m, true, nil
}

// TouchMarker refreshes UpdatedAt for an existing marker. A missing marker is
// bound through WriteMarker, so its creation follows the no-clobber path. An
// existing marker keeps its agentID: a touch never replaces the owner.
func TouchMarker(storeDir, worktreeDir, agentID string, now time.Time) (Marker, error) {
	m, ok, err := ReadMarker(storeDir, worktreeDir)
	if err != nil {
		return Marker{}, err
	}
	if !ok {
		return WriteMarker(storeDir, worktreeDir, agentID, now)
	}
	if strings.TrimSpace(m.AgentID) != "" {
		agentID = m.AgentID
	}
	m = Marker{Path: cleanDir(worktreeDir), AgentID: agentID, UpdatedAt: now.UTC()}
	data, err := json.Marshal(m)
	if err != nil {
		return Marker{}, err
	}
	path := MarkerPath(storeDir, worktreeDir)
	if err := replaceMarker(path, append(data, '\n')); err != nil {
		return Marker{}, err
	}
	return m, nil
}

// MarkerValid reports whether a marker still protects its worktree. A marker
// with no agent, for another path, without a timestamp, from the future, or
// older than ttl is not valid. A non-positive ttl disables expiry.
func MarkerValid(m Marker, worktreeDir string, now time.Time, ttl time.Duration) bool {
	if strings.TrimSpace(m.AgentID) == "" || m.Path == "" {
		return false
	}
	if cleanDir(m.Path) != cleanDir(worktreeDir) {
		return false
	}
	if m.UpdatedAt.IsZero() || now.Before(m.UpdatedAt) {
		return false
	}
	return ttl <= 0 || now.Sub(m.UpdatedAt) < ttl
}
