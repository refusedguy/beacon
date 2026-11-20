// Package projects resolves a project key or path to one known project.
//
// The resolver is a pure boundary: it never opens a file, walks a directory or
// runs git. A Source port supplies the projects, and the resolver only matches
// the caller's key against the keys and paths the port returns. Path matching
// uses lexical canonicalization, so two spellings of the same directory reach
// the same project. The adapter behind Source owns all I/O, including symlink
// and git-root discovery, and passes already-canonical paths to the resolver.
package projects

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

var (
	// ErrEmptyKey reports a blank key that cannot name a project.
	ErrEmptyKey = errors.New("projects: empty key")
	// ErrNotFound reports a key that matches no known project.
	ErrNotFound = errors.New("projects: not found")
	// ErrAmbiguous reports a key that matches more than one project.
	ErrAmbiguous = errors.New("projects: ambiguous key")
	// ErrMissingPort reports construction with no Source.
	ErrMissingPort = errors.New("projects: missing source port")
	// ErrPort reports a failure of the Source port.
	ErrPort = errors.New("projects: source port failure")
)

// Project is one project known to Beacon. ID is the stable project identity,
// Key is the short name callers use, and Path is the canonical path of the
// project directory as supplied by the Source.
type Project struct {
	ID   string
	Key  string
	Path string
}

// Source is the project-listing port. List returns the projects known to the
// host. The adapter behind it performs all file and git I/O; the resolver
// holds only this interface and never touches the filesystem.
type Source interface {
	List(ctx context.Context) ([]Project, error)
}

// Resolver maps a key or path to exactly one project through a Source. The
// zero value has no port and returns ErrMissingPort from Resolve; use New to
// build a usable Resolver.
type Resolver struct {
	source Source
}

// New builds a Resolver over source. A nil source is refused, because a
// resolver with no port can never answer a lookup.
func New(source Source) (*Resolver, error) {
	if source == nil {
		return nil, ErrMissingPort
	}
	return &Resolver{source: source}, nil
}

// Resolve returns the single project matching key. A key matches when it
// equals a project key or, after lexical canonicalization, a project path.
// Distinct matches are counted by project ID, so one project reached through
// both its key and its path is still a single match. A blank key is rejected
// before the port runs. A port error is wrapped in ErrPort; no match is
// ErrNotFound and several matches are ErrAmbiguous.
func (r *Resolver) Resolve(ctx context.Context, key string) (Project, error) {
	if r == nil || r.source == nil {
		return Project{}, ErrMissingPort
	}
	wanted := strings.TrimSpace(key)
	if wanted == "" {
		return Project{}, ErrEmptyKey
	}
	if ctx == nil {
		ctx = context.Background()
	}

	projects, err := r.source.List(ctx)
	if err != nil {
		return Project{}, fmt.Errorf("%w: %v", ErrPort, err)
	}

	wantedPath := canonicalPath(wanted)
	matches := make(map[string]Project)
	for _, p := range projects {
		if !matchesKey(p, wanted, wantedPath) {
			continue
		}
		if _, seen := matches[p.ID]; !seen {
			matches[p.ID] = p
		}
	}

	switch len(matches) {
	case 0:
		return Project{}, fmt.Errorf("%w: %q", ErrNotFound, wanted)
	case 1:
		for _, p := range matches {
			return p, nil
		}
	}

	ids := make([]string, 0, len(matches))
	for id := range matches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return Project{}, fmt.Errorf("%w: %q matches %s", ErrAmbiguous, wanted, strings.Join(ids, ", "))
}

// matchesKey reports whether key names project p, either by its exact key or
// by a canonically equal path. An empty project key or path never matches.
func matchesKey(p Project, key, canonicalKeyPath string) bool {
	if p.Key != "" && key == p.Key {
		return true
	}
	if p.Path == "" || canonicalKeyPath == "" {
		return false
	}
	return canonicalKeyPath == canonicalPath(p.Path)
}

// canonicalPath normalizes a path lexically without touching the filesystem.
// It trims surrounding space, cleans separators and redundant segments, and
// drops a trailing slash. Callers must pass paths the Source has already
// resolved through any symlinks.
func canonicalPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return ""
	}
	return cleaned
}
