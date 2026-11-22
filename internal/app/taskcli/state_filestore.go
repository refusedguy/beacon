package taskcli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

const (
	// stateWriteAttempts counts the first try and four retries. The four waits
	// between them are 0.1, 0.2, 0.4 and 0.8 seconds.
	stateWriteAttempts  = 5
	stateWriteBaseDelay = 100 * time.Millisecond
)

// temporaryLockError reports whether err is a transient lock condition. Only
// these errors are retried, so a permanent error reaches the caller on the
// first attempt instead of being hidden by the wrapper.
func temporaryLockError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK) ||
		errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, syscall.EINTR) ||
		errors.Is(err, os.ErrPermission)
}

// retryTemporaryWrite calls op until it succeeds, reports a permanent error or
// runs out of attempts. The sleep argument lets a test observe the delays
// without waiting. Callers pass the same state on every attempt, so a retry
// keeps the result idempotent.
func retryTemporaryWrite(op func() error, sleep func(time.Duration)) error {
	var err error
	for attempt := 0; attempt < stateWriteAttempts; attempt++ {
		if attempt > 0 {
			sleep(stateWriteBaseDelay << (attempt - 1))
		}
		err = op()
		if err == nil || !temporaryLockError(err) {
			return err
		}
	}
	return err
}

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
	var f *os.File
	err := retryTemporaryWrite(func() error {
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		return err
	}, time.Sleep)
	if err != nil {
		return nil, taskstore.Startupf(r.lockOpenErr, err)
	}
	err = retryTemporaryWrite(func() error {
		return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}, time.Sleep)
	if err != nil {
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
	payload := append(data, '\n')
	return retryTemporaryWrite(func() error {
		return taskstore.AtomicWrite(r.path(), payload, 0644)
	}, time.Sleep)
}
