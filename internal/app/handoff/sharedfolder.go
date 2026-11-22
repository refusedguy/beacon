// Package handoff stores plans, notes, findings and handoff documents in a
// state directory outside agent worktrees. Agents on different worktrees read
// the same record without copying it through a pull request.
package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Kind narrows a SharedFolder key to one document class.
type Kind string

const (
	KindPlan        Kind = "plan"
	KindNote        Kind = "note"
	KindFinding     Kind = "finding"
	KindHandoff     Kind = "handoff"
	KindDeliverable Kind = "deliverable"
)

// Valid reports whether k belongs to the allowed set.
func (k Kind) Valid() bool {
	switch k {
	case KindPlan, KindNote, KindFinding, KindHandoff, KindDeliverable:
		return true
	default:
		return false
	}
}

// Key identifies one document in the shared folder.
type Key struct {
	WorkstreamID string
	Kind         Kind
	Name         string
}

// ErrInvalidKey is reported for keys that cannot be stored safely.
var ErrInvalidKey = errors.New("handoff: invalid key")

// ErrNotFound is reported when a document does not exist.
var ErrNotFound = errors.New("handoff: not found")

// InvalidKeyError names the field that failed validation.
type InvalidKeyError struct {
	Field  string
	Value  string
	Reason string
}

func (e *InvalidKeyError) Error() string {
	return fmt.Sprintf("handoff: %s %q: %s", e.Field, e.Value, e.Reason)
}

// Is makes errors.Is(err, ErrInvalidKey) work for every validation failure.
func (e *InvalidKeyError) Is(target error) bool { return target == ErrInvalidKey }

func invalidKey(field, value, reason string) error {
	return &InvalidKeyError{Field: field, Value: value, Reason: reason}
}

func validSegment(field, value string) error {
	if value == "" {
		return invalidKey(field, value, "must not be empty")
	}
	if value == "." || value == ".." {
		return invalidKey(field, value, "must not be a parent directory")
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return invalidKey(field, value, "must not be an absolute path")
	}
	if strings.ContainsAny(value, `/\`) {
		return invalidKey(field, value, "must not contain a path separator")
	}
	if strings.ContainsRune(value, 0) {
		return invalidKey(field, value, "must not contain NUL")
	}
	return nil
}

func (k Key) validate() error {
	if err := validSegment("workstreamID", k.WorkstreamID); err != nil {
		return err
	}
	if !k.Kind.Valid() {
		return invalidKey("kind", string(k.Kind), "must be plan, note, finding, handoff or deliverable")
	}
	return validSegment("name", k.Name)
}

// Document is a stored record together with its manifest. Version grows on
// every Put. WrittenAt is the UTC time of the last successful Put, so a reader
// can tell that another agent updated the document.
type Document struct {
	WorkstreamID string    `json:"workstream_id"`
	Kind         Kind      `json:"kind"`
	Name         string    `json:"name"`
	Body         []byte    `json:"body"`
	Version      int64     `json:"version"`
	WrittenAt    time.Time `json:"written_at"`
}

// Key returns the document key.
func (d Document) Key() Key {
	return Key{WorkstreamID: d.WorkstreamID, Kind: d.Kind, Name: d.Name}
}

// SharedFolder is the port agents and the orchestrator use to exchange
// documents. Implementations must be safe for concurrent use by many processes.
type SharedFolder interface {
	Put(ctx context.Context, key Key, body []byte) (Document, error)
	Get(ctx context.Context, key Key) (Document, error)
	List(ctx context.Context, workstreamID string, kind Kind) ([]Document, error)
}

// FileSharedFolder stores one JSON file per document under root/shared. The
// root must be absolute so no agent worktree can become the storage location.
type FileSharedFolder struct {
	root string
	now  func() time.Time
}

var _ SharedFolder = (*FileSharedFolder)(nil)

// NewFileSharedFolder creates a store rooted at an absolute state directory.
func NewFileSharedFolder(root string) (*FileSharedFolder, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("handoff: state dir %q must be absolute", root)
	}
	return &FileSharedFolder{root: filepath.Clean(root), now: time.Now}, nil
}

func (f *FileSharedFolder) documentPath(key Key) (string, error) {
	if err := key.validate(); err != nil {
		return "", err
	}
	return filepath.Join(f.root, "shared", key.WorkstreamID, string(key.Kind), key.Name+".json"), nil
}

// lock serialises writers from all processes that share the state directory.
// The flock lives on the open descriptor, so a crashed writer releases it.
func (f *FileSharedFolder) lock() (func(), error) {
	if err := os.MkdirAll(f.root, 0o700); err != nil {
		return nil, fmt.Errorf("handoff: state dir: %w", err)
	}
	path := filepath.Join(f.root, "sharedfolder.lock")
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("handoff: lock file: %w", err)
	}
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX); err != nil {
		_ = fh.Close()
		return nil, fmt.Errorf("handoff: lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(fh.Fd()), syscall.LOCK_UN)
		_ = fh.Close()
	}, nil
}

func readDocument(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Document{}, ErrNotFound
	}
	if err != nil {
		return Document{}, fmt.Errorf("handoff: read %s: %w", path, err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Document{}, fmt.Errorf("handoff: decode %s: %w", path, err)
	}
	if doc.Version < 1 || doc.WrittenAt.IsZero() {
		return Document{}, fmt.Errorf("handoff: decode %s: invalid manifest", path)
	}
	return doc, nil
}

// writeAtomic publishes data only after the whole temp file is written. A
// crash before the rename leaves the previous document untouched.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("handoff: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".shared-*.tmp")
	if err != nil {
		return fmt.Errorf("handoff: temp in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("handoff: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("handoff: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("handoff: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("handoff: close temp: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("handoff: publish %s: %w", path, err)
	}
	return nil
}

// Put stores body at key and returns the stored document with a fresh version
// and timestamp. Repeating Put increments the version by one.
func (f *FileSharedFolder) Put(ctx context.Context, key Key, body []byte) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	path, err := f.documentPath(key)
	if err != nil {
		return Document{}, err
	}
	unlock, err := f.lock()
	if err != nil {
		return Document{}, err
	}
	defer unlock()

	version := int64(1)
	if previous, err := readDocument(path); err == nil {
		version = previous.Version + 1
	} else if !errors.Is(err, ErrNotFound) {
		return Document{}, err
	}

	doc := Document{
		WorkstreamID: key.WorkstreamID,
		Kind:         key.Kind,
		Name:         key.Name,
		Body:         append([]byte(nil), body...),
		Version:      version,
		WrittenAt:    f.now().UTC(),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Document{}, fmt.Errorf("handoff: encode: %w", err)
	}
	if err := writeAtomic(path, append(data, '\n')); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// Get returns the document stored at key.
func (f *FileSharedFolder) Get(ctx context.Context, key Key) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	path, err := f.documentPath(key)
	if err != nil {
		return Document{}, err
	}
	unlock, err := f.lock()
	if err != nil {
		return Document{}, err
	}
	defer unlock()
	return readDocument(path)
}

// List returns all documents of one kind in a workstream, sorted by name.
// An empty result is not an error.
func (f *FileSharedFolder) List(ctx context.Context, workstreamID string, kind Kind) ([]Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validSegment("workstreamID", workstreamID); err != nil {
		return nil, err
	}
	if !kind.Valid() {
		return nil, invalidKey("kind", string(kind), "must be plan, note, finding, handoff or deliverable")
	}
	unlock, err := f.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	dir := filepath.Join(f.root, "shared", workstreamID, string(kind))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Document{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("handoff: list %s: %w", dir, err)
	}
	docs := make([]Document, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		doc, err := readDocument(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	return docs, nil
}
