package taskcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// fileState owns the path, the lock and the JSON load/save of one state file.
// Decision rules stay in the service; this type only moves bytes.
//
// A state machine with lockFile set uses a private flock on a dedicated lock
// file. An empty lockFile means the shared card lock, which worker history and
// rate-limit routing used before the repositories were split out.
//
// The dedicated flock matters. The shared card lock removes its file after a
// stale heuristic read, and another worker can create a new lock file between
// that read and the removal. Two writers then enter the critical section at
// once. A flock lives on the open descriptor, and the kernel drops it when the
// process ends, so neither the stale heuristic nor the removal race applies.
type fileState[T any] struct {
	stateDir string
	root     string
	file     string
	lockFile string

	readErr     string
	parseErr    string
	writeErr    string
	lockOpenErr string
	lockTakeErr string

	empty     func() T
	normalize func(T) T
}

func (r fileState[T]) path() string {
	return filepath.Join(r.stateDir, r.file)
}

// lock returns a release function. The same closure shape works for the shared
// card lock and for the private flock, so callers do not know which one they
// hold.
func (r fileState[T]) lock() (func(), error) {
	if r.lockFile == "" {
		lock, err := taskstore.AcquireLock(taskstore.LockPath(r.stateDir, r.root), r.root)
		if err != nil {
			return nil, err
		}
		return func() { _ = lock.Release() }, nil
	}

	path := filepath.Join(r.stateDir, r.lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, taskstore.Startupf(r.lockOpenErr, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, taskstore.Startupf(r.lockTakeErr, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// missing returns the value used when the state file does not exist yet.
func (r fileState[T]) missing() T {
	if r.empty != nil {
		return r.empty()
	}
	var zero T
	return zero
}

func (r fileState[T]) load() (T, error) {
	data, err := os.ReadFile(r.path())
	if os.IsNotExist(err) {
		return r.missing(), nil
	}
	if err != nil {
		return r.missing(), taskstore.Startupf(r.readErr, err)
	}
	var st T
	if err := json.Unmarshal(data, &st); err != nil {
		return r.missing(), taskstore.Startupf(r.parseErr, err)
	}
	if r.normalize != nil {
		st = r.normalize(st)
	}
	return st, nil
}

func (r fileState[T]) save(st T) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return taskstore.Startupf(r.writeErr, err)
	}
	return taskstore.AtomicWrite(r.path(), append(data, '\n'), 0644)
}
