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

// WriteMarker creates or replaces the marker for worktreeDir. The file is
// written with 0600 in a 0700 directory so other local users cannot read the
// recorded agent identity.
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
	if err := os.WriteFile(path, append(data, '\n'), markerFilePerm); err != nil {
		return Marker{}, fmt.Errorf("worktree: marker: write: %w", err)
	}
	return m, nil
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
// created with the supplied agentID. An existing marker keeps its agentID.
func TouchMarker(storeDir, worktreeDir, agentID string, now time.Time) (Marker, error) {
	m, ok, err := ReadMarker(storeDir, worktreeDir)
	if err != nil {
		return Marker{}, err
	}
	if ok && strings.TrimSpace(m.AgentID) != "" {
		agentID = m.AgentID
	}
	return WriteMarker(storeDir, worktreeDir, agentID, now)
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
