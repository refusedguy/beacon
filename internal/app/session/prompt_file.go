// Package session holds Beacon's session services. This file implements the
// private prompt-file handoff. The store writes a prompt to a 0600 file, hands
// the caller a shell command that reads and removes that file, and cleans up
// files left behind by a failed launch. The prompt itself never enters a shell
// argument, so quotes, backticks, dollars and newlines need no escaping.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// PromptFilePerm is the mode of every prompt file. Only the owner can read it.
const PromptFilePerm = 0o600

// PromptFileShell names the shells whose read-and-delete command differs. Any
// other value uses the POSIX form.
const (
	PromptFileShellPOSIX = "posix"
	PromptFileShellFish  = "fish"
)

// ErrPromptFileWrite means the store could not create the private file. The
// caller uses this signal to fall back to the inline escaping path.
var ErrPromptFileWrite = errors.New("session: prompt file write failed")

// PromptFileFS is the disk boundary for prompt files. The cmd adapter supplies
// a real filesystem and tests supply an in-memory one. Every method maps to a
// single filesystem operation so the store holds no file handle of its own.
type PromptFileFS interface {
	// Create writes data to a new file named name in dir. It fails if the file
	// already exists and sets mode PromptFilePerm on success. It returns the
	// full path.
	Create(dir, name string, data []byte) (string, error)
	// Read returns the bytes stored at path.
	Read(path string) ([]byte, error)
	// Remove deletes the file at path.
	Remove(path string) error
	// Names lists the base names of the files in dir.
	Names(dir string) ([]string, error)
}

// PromptFileStore writes prompts to private files and builds the command that
// reads and removes them. The store owns no goroutine and is safe for one
// caller at a time. Use NewPromptFileStore to build one.
type PromptFileStore struct {
	fs     PromptFileFS
	dir    string
	now    func() time.Time
	suffix func() (string, error)
}

// NewPromptFileStore builds a store that keeps prompt files in dir. A nil fs
// is accepted but every operation then returns ErrPromptFileWrite.
func NewPromptFileStore(fs PromptFileFS, dir string) *PromptFileStore {
	return &PromptFileStore{fs: fs, dir: dir, now: time.Now, suffix: randomSuffix}
}

func randomSuffix() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// PromptDelivery is the result of preparing a prompt for a launch. When
// UsedFile is true, Command reads and removes Path and Inline is empty. When
// it is false, the write failed and Inline carries the escaped prompt for the
// caller's built-in path.
type PromptDelivery struct {
	Path     string
	Command  string
	Inline   string
	UsedFile bool
}

// sanitizeSlug turns a task slug into a safe file-name prefix. Only lowercase
// letters, digits, hyphens and underscores survive. An empty slug becomes
// "prompt".
func sanitizeSlug(slug string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(slug)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "prompt"
	}
	return out
}

func (s *PromptFileStore) prefix(slug string) string {
	return sanitizeSlug(slug) + "-"
}

func (s *PromptFileStore) fileName(slug string) (string, error) {
	suffix, err := s.suffix()
	if err != nil {
		return "", err
	}
	stamp := s.now().UTC().Format("20060102T150405.000000000")
	return s.prefix(slug) + stamp + "-" + suffix + ".prompt", nil
}

// Write stores prompt in a new private file and returns its path. The name
// combines a sanitized slug, a UTC timestamp and a random suffix, so two
// writes for the same slug never share a path.
func (s *PromptFileStore) Write(prompt, slug string) (string, error) {
	if s == nil || s.fs == nil {
		return "", ErrPromptFileWrite
	}
	name, err := s.fileName(slug)
	if err != nil {
		return "", fmt.Errorf("%w: suffix: %v", ErrPromptFileWrite, err)
	}
	path, err := s.fs.Create(s.dir, name, []byte(prompt))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPromptFileWrite, err)
	}
	return path, nil
}

// ReadAndDelete returns the exact bytes of the file and removes it. It removes
// the file even when the read fails, so a failed read leaves no trace on disk.
func (s *PromptFileStore) ReadAndDelete(path string) ([]byte, error) {
	if s == nil || s.fs == nil {
		return nil, ErrPromptFileWrite
	}
	data, readErr := s.fs.Read(path)
	removeErr := s.fs.Remove(path)
	if readErr != nil {
		return nil, readErr
	}
	if removeErr != nil {
		return nil, removeErr
	}
	return data, nil
}

// Cleanup removes every prompt file whose name starts with the slug prefix.
// It returns the number of files removed. Files for other slugs stay in place.
func (s *PromptFileStore) Cleanup(slug string) (int, error) {
	if s == nil || s.fs == nil {
		return 0, ErrPromptFileWrite
	}
	prefix := s.prefix(slug)
	names, err := s.fs.Names(s.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if err := s.fs.Remove(filepath.Join(s.dir, name)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// shellQuote wraps a value in single quotes for a POSIX shell. A single quote
// inside the value is escaped with a backslash, so the shell keeps the literal
// quote.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ReadDeleteCommand builds a command that loads the file into a variable,
// removes the file and prints the bytes. A trailing sentinel keeps a POSIX
// command substitution from stripping the prompt's final newlines. The fish
// branch uses string collect with -N for the same reason. The prompt is never
// copied into the command.
func (s *PromptFileStore) ReadDeleteCommand(path, shell string) string {
	q := shellQuote(path)
	if shell == PromptFileShellFish {
		return fmt.Sprintf("set -l __beacon_prompt (cat %s | string collect -N); rm -f %s; printf '%%s' \"$__beacon_prompt\"", q, q)
	}
	return fmt.Sprintf("__beacon_prompt=$(cat %s; printf x); rm -f %s; printf '%%s' \"${__beacon_prompt%%x}\"", q, q)
}

// Deliver writes the prompt and builds the read-and-delete command for the
// given shell. If the write fails, it returns an escaped inline prompt so the
// caller can use its built-in path without losing the launch.
func (s *PromptFileStore) Deliver(prompt, slug, shell string) PromptDelivery {
	path, err := s.Write(prompt, slug)
	if err != nil {
		return PromptDelivery{Inline: shellQuote(prompt)}
	}
	return PromptDelivery{Path: path, Command: s.ReadDeleteCommand(path, shell), UsedFile: true}
}
