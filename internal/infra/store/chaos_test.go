package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// TestChaosMidTransactionFailureKeepsSeq checks the observed writer when a
// transaction inserts a row and then fails. The rollback must drop the row and
// leave persist_progress.seq where it was.
func TestChaosMidTransactionFailureKeepsSeq(t *testing.T) {
	d := openObservabilityDB(t)
	op := NewObservedPersist(d)

	err := op.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO watches (id, repo, pr, head_sha, status, revision, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"chaos-partial", "org/repo", 1, "sha", "watching", 1, []byte(`{}`)); err != nil {
			return err
		}
		return errors.New("injected failure after partial write")
	})
	appErr := persistFailed(t, err)
	if !strings.Contains(appErr.Message, "injected failure after partial write") {
		t.Fatalf("error message = %q, want the injected cause", appErr.Message)
	}

	state, err := d.PersistState()
	if err != nil {
		t.Fatalf("PersistState: %v", err)
	}
	if state.Seq != 0 {
		t.Fatalf("seq = %d after rolled-back write, want 0", state.Seq)
	}
	if !strings.Contains(state.LastError, "injected failure") || state.FailedSince.IsZero() {
		t.Fatalf("state = %+v, want a recorded error and timestamp", state)
	}

	var partial int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM watches WHERE id = ?`, "chaos-partial").Scan(&partial); err != nil {
		t.Fatalf("count partial row: %v", err)
	}
	if partial != 0 {
		t.Fatalf("partial row survived rollback: %d", partial)
	}

	// A later committed write advances seq by exactly one. The rolled-back row
	// stays absent.
	err = op.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO watches (id, repo, pr, head_sha, status, revision, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"chaos-committed", "org/repo", 2, "sha", "watching", 1, []byte(`{}`))
		return err
	})
	if err != nil {
		t.Fatalf("committed write: %v", err)
	}
	state, err = d.PersistState()
	if err != nil {
		t.Fatalf("PersistState after commit: %v", err)
	}
	if state.Seq != 1 || state.LastError != "" || !state.FailedSince.IsZero() {
		t.Fatalf("state after commit = %+v, want seq 1 and a cleared error", state)
	}
	if err := d.readDB.QueryRow(`SELECT count(*) FROM watches WHERE id = ?`, "chaos-partial").Scan(&partial); err != nil {
		t.Fatalf("count partial row: %v", err)
	}
	if partial != 0 {
		t.Fatalf("rolled-back row reappeared after a commit: %d", partial)
	}
}

// TestChaosCorruptEventIsReportedAndRestartSurvives reloads a journal that
// holds one damaged JSON row. The row surfaces in stats.loadErrors, the good
// rows still load, and the store keeps accepting writes after the restart.
func TestChaosCorruptEventIsReportedAndRestartSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	goodFirst := insertRawEvent(t, d, "watch:chaos:first", []byte(`{"seq":1,"kind":"watch"}`))
	badSeq := insertRawEvent(t, d, "watch:chaos:bad", []byte(`{"seq":2,"kind":`))
	goodLast := insertRawEvent(t, d, "watch:chaos:last", []byte(`{"seq":3,"kind":"watch"}`))
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restarted, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer restarted.Close()

	state, loadErrors, err := restarted.PersistStats()
	if err != nil {
		t.Fatalf("PersistStats after restart: %v", err)
	}
	if len(loadErrors) != 1 {
		t.Fatalf("loadErrors = %+v, want exactly one damaged row", loadErrors)
	}
	if loadErrors[0].Line != badSeq || loadErrors[0].Reason == "" {
		t.Fatalf("load error = %+v, want line %d with a decoder reason", loadErrors[0], badSeq)
	}
	if state.Seq != 0 || state.LastError != "" {
		t.Fatalf("persist state = %+v, want no writes recorded by a read", state)
	}

	events, errs, err := restarted.LoadEvents()
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(errs) != 1 || len(events) != 2 {
		t.Fatalf("load = %d events, %d errors, want 2 and 1", len(events), len(errs))
	}
	if events[0].Seq != goodFirst || events[1].Seq != goodLast {
		t.Fatalf("loaded seqs = %d/%d, want %d/%d", events[0].Seq, events[1].Seq, goodFirst, goodLast)
	}

	// Startup continues: the store still commits a write after the bad row.
	err = restarted.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO watches (id, repo, pr, head_sha, status, revision, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"chaos-after-corruption", "org/repo", 9, "sha", "watching", 1, []byte(`{}`))
		return err
	})
	if err != nil {
		t.Fatalf("write after restart: %v", err)
	}
}

// chaosAcquirePollerLock uses the same non-blocking flock election the daemon
// runs on <dir>/beacon-poller.lock. Two instances that call it concurrently
// cannot both own the lock.
func chaosAcquirePollerLock(path string) (func(), bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// TestChaosPollerLockElectsOneInstance races four instances for the poller
// lock. Exactly one wins, and a later instance can take over after release.
func TestChaosPollerLockElectsOneInstance(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "beacon-poller.lock")
	const instances = 4
	start := make(chan struct{})
	owned := make(chan bool, instances)
	releases := make(chan func(), instances)
	var wg sync.WaitGroup

	for i := 0; i < instances; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			release, got, err := chaosAcquirePollerLock(lockPath)
			if err != nil {
				t.Errorf("acquire poller lock: %v", err)
				owned <- false
				return
			}
			owned <- got
			if got {
				releases <- release
			}
		}()
	}
	close(start)
	wg.Wait()
	close(owned)

	winners := 0
	for got := range owned {
		if got {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("poller lock owners = %d, want 1", winners)
	}

	release := <-releases
	release()
	_, got, err := chaosAcquirePollerLock(lockPath)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if !got {
		t.Fatal("reacquire after release failed, want the next instance to own the lock")
	}
}
