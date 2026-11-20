package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// createMergeLedgerSQL owns the durable idempotency ledger. One row per
// (watchID, revision) is enough: a repeated orchestrate pass must not merge
// the same Watch revision twice. Like the other on-first-use tables in the
// state file, it is created on first use so this card adds no migration.
const createMergeLedgerSQL = `
CREATE TABLE IF NOT EXISTS orchestrator_merges (
  watch_id   TEXT NOT NULL,
  revision   INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (watch_id, revision)
)
`

// WatchReader supplies the active Watches projected onto the orchestrator's
// decision shape. The composition root performs the projection from the
// domain store, so this package needs no domain types (ADR 0005).
type WatchReader interface {
	ActiveWatches(ctx context.Context) ([]Watch, error)
}

// DBStore adapts Beacon state to the orchestrator's Store port. Active
// Watches come from the injected reader; the ledger and the cancellation use
// the same SQLite file through its single-writer connection.
type DBStore struct {
	reader WatchReader
	db     *store.DB
}

// NewDBStore builds the production Store. reader projects the domain Watches
// and db is the state file that holds the ledger and the cancel event log.
func NewDBStore(reader WatchReader, db *store.DB) *DBStore {
	return &DBStore{reader: reader, db: db}
}

// ActiveWatches delegates to the reader built at the composition root.
func (s *DBStore) ActiveWatches(ctx context.Context) ([]Watch, error) {
	if s == nil || s.reader == nil {
		return nil, fmt.Errorf("orchestrator: watch reader is nil")
	}
	return s.reader.ActiveWatches(ctx)
}

// WasMerged reports whether (watchID, revision) is already in the ledger.
// store.DB exposes writes but no generic read port, so the SELECT runs inside
// Write on the same single-writer connection; the statement is read-only.
func (s *DBStore) WasMerged(ctx context.Context, watchID string, revision uint64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("orchestrator: merge store is nil")
	}
	var found bool
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createMergeLedgerSQL); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(`
			SELECT count(*) FROM orchestrator_merges
			WHERE watch_id = ? AND revision = ?`, watchID, revision,
		).Scan(&n); err != nil {
			return err
		}
		found = n > 0
		return nil
	})
	return found, err
}

// RecordMerge inserts (watchID, revision) into the ledger. A repeat is a
// no-op, so the ledger itself is idempotent under redelivery.
func (s *DBStore) RecordMerge(ctx context.Context, watchID string, revision uint64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("orchestrator: merge store is nil")
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(createMergeLedgerSQL); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO orchestrator_merges (watch_id, revision, created_at)
			VALUES (?, ?, ?)`, watchID, revision, time.Now().UTC().UnixNano())
		return err
	})
}

// RecordFixAttempt persists one FIX agent attempt for a blocked Watch. The
// domain method is the pure copy that owns the increment, so the counter lives
// on the block record and an unblock clears it with the rest of the record.
// A Watch that was unblocked between projection and this call is left alone.
func (s *DBStore) RecordFixAttempt(ctx context.Context, watchID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("orchestrator: fix attempt store is nil")
	}
	_ = ctx
	w, err := s.db.Watch(watchID)
	if err != nil {
		return fmt.Errorf("orchestrator: load watch %s for fix attempt: %w", watchID, err)
	}
	next := w.RecordFixAttempt()
	if next.Blocked == nil {
		return nil
	}
	return s.db.SaveWatch(next)
}

// CloseWatch marks a Watch closed as merged. The periodic pass must not route
// through mcp.NewCancelWatcher: that records closeReason="cancelled" for a PR
// the hosting service actually merged. Setting the three fields directly keeps
// the audit reason honest; a Watch that is already closed is left untouched.
func (s *DBStore) CloseWatch(ctx context.Context, watchID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("orchestrator: watch store is nil")
	}
	_ = ctx
	w, err := s.db.Watch(watchID)
	if err != nil {
		return fmt.Errorf("orchestrator: load watch %s for close: %w", watchID, err)
	}
	if w.Status == "closed" {
		return nil
	}
	w.Status = "closed"
	w.Verdict = "merged"
	w.CloseReason = "merged"
	return s.db.SaveWatch(w)
}

// MergedPR implements MergedPRReader from the Watch rows the process already
// has in SQLite. Both the active table and the archive are checked: a PR merged
// more than the retention window ago still satisfies a waitFor reference. No
// hosting-service call is made here.
func (s *DBStore) MergedPR(ctx context.Context, pr int) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("orchestrator: merge store is nil")
	}
	_ = ctx
	all, err := s.db.AllWatches()
	if err != nil {
		return false, fmt.Errorf("orchestrator: watches for PR %d: %w", pr, err)
	}
	for _, w := range all {
		if w.PR == pr && (string(w.Verdict) == "merged" || w.CloseReason == "merged") {
			return true, nil
		}
	}
	archived, err := s.db.ArchivedWatches()
	if err != nil {
		return false, fmt.Errorf("orchestrator: archived watches for PR %d: %w", pr, err)
	}
	for _, w := range archived {
		if w.PR == pr && (string(w.Verdict) == "merged" || w.CloseReason == "merged") {
			return true, nil
		}
	}
	return false, nil
}

// HoldWatch persists the hold reason on a Watch whose merge is waiting on a
// dependency. The verdict is visible to beacon_list_running and beacon inspect
// through the same store handle they already read, and no obligation is staged:
// an unsatisfied dependency is nobody's fault and wakes nobody (ADR 0158).
func (s *DBStore) HoldWatch(ctx context.Context, watchID, verdict string) error {
	if s == nil || s.db == nil {
		return errors.New("orchestrator: watch store is nil")
	}
	_ = ctx
	w, err := s.db.Watch(watchID)
	if err != nil {
		return fmt.Errorf("orchestrator: load watch %s for dependency hold: %w", watchID, err)
	}
	if w.Status == "closed" {
		return nil
	}
	setWatchVerdict(&w.Verdict, verdict)
	return s.db.SaveWatch(w)
}

// setWatchVerdict assigns a string to a named string field without naming the
// domain type. The app layer is not allowed to import internal/domain (ADR
// 0005), but the Store adapter still has to write the persisted verdict.
func setWatchVerdict[T ~string](dst *T, value string) { *dst = T(value) }

// mergeFields mirrors the domain MergePolicy persisted under "mergePolicy".
// The pointer on MergePolicy distinguishes an absent key (default policy) from
// an explicit autoMerge=false that must block the merge.
type mergeFields struct {
	AutoMerge       bool     `json:"autoMerge"`
	RequireApproval bool     `json:"requireApproval"`
	WaitFor         []string `json:"waitFor"`
	WaitForMain     []string `json:"waitForMain"`
}

type policyCarrier struct {
	MergePolicy *mergeFields `json:"mergePolicy"`
}

// PolicyFromPayload parses the autor-merge policy of a serialized Watch.
// An absent mergePolicy leaves the default "on" in place, matching
// domain.DefaultMergePolicy. Explicit hold-backs disable the merge.
func PolicyFromPayload(raw []byte) *AutoMergePolicy {
	var c policyCarrier
	if err := json.Unmarshal(raw, &c); err != nil {
		p := DefaultAutoMergePolicy()
		return &p
	}
	if c.MergePolicy == nil {
		p := DefaultAutoMergePolicy()
		return &p
	}
	m := c.MergePolicy
	autoMerge := m.AutoMerge
	return &AutoMergePolicy{
		AutoMerge:       &autoMerge,
		RequireApproval: m.RequireApproval,
		WaitFor:         m.WaitFor,
		WaitForMain:     m.WaitForMain,
	}
}

// CommandRunner is the seam the dependency resolver uses for local `git`
// probes. The app layer must not import os/exec (ADR 0005), so the composition
// root supplies the real runner and tests supply a fake. A runner error is
// never reported as a satisfied dependency. PR refs never reach this runner.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ErrRefMissing is the runner signal that `git show main:<ref>` found no such
// path. Other errors are propagation failures and must reach the caller.
var ErrRefMissing = errors.New("orchestrator: dependency ref missing on main")

var (
	dependencyPRRef   = regexp.MustCompile(`(?i)^(?:pr\s*#|pr/|pr-|#)(\d+)$`)
	dependencyCardRef = regexp.MustCompile(`(?i)^t\d+\.\d+$`)
)

// MergedPRReader answers whether a pull request already reached main. The
// composition root backs it with the Watch rows the process already loaded
// from SQLite (including the archive), so resolving a #12 reference costs a
// local read instead of a `gh pr view` child process. An unverifiable answer
// is an error, never a false positive.
type MergedPRReader interface {
	MergedPR(ctx context.Context, pr int) (bool, error)
}

// MergedPRReaderFunc adapts a function to the MergedPRReader port.
type MergedPRReaderFunc func(ctx context.Context, pr int) (bool, error)

// MergedPR calls f. A nil function reports "not merged" without an error, which
// keeps the dependency gate fail-closed.
func (f MergedPRReaderFunc) MergedPR(ctx context.Context, pr int) (bool, error) {
	if f == nil {
		return false, nil
	}
	return f(ctx, pr)
}

// FileDependencyResolver resolves free-form refs from facts the process
// already holds. Card refs (T3.123) are satisfied when the card file documents
// "Статус: done". PR refs (#12, PR#12, pr/12) are satisfied when MergedPR
// confirms the PR is merged from loaded Watch state: no gh process is started.
// Every other string is a path that must exist on main, checked locally with
// `git show main:<ref>`.
type FileDependencyResolver struct {
	// DocsDir holds task cards. The empty value means "docs/tasks".
	DocsDir string
	// Runner supplies git for arbitrary file refs. It is never used for PR
	// refs. A nil Runner yields an error, never a true.
	Runner CommandRunner
	// MergedPR supplies the already-loaded PR state. A nil reader is an error
	// for a PR ref, never a silent pass.
	MergedPR MergedPRReader
}

var _ DependencyResolver = (*FileDependencyResolver)(nil)

// NewDependencyResolver builds a resolver for card and file refs with the
// conventional docs dir. PR refs additionally need a MergedPRReader; callers
// that resolve them should use NewStoreDependencyResolver or set the field.
func NewDependencyResolver(runner CommandRunner) *FileDependencyResolver {
	return &FileDependencyResolver{DocsDir: "docs/tasks", Runner: runner}
}

// NewStoreDependencyResolver builds the production resolver. mergedPR is the
// already-loaded Watch state, so a #12 reference never starts a gh process.
func NewStoreDependencyResolver(docsDir string, runner CommandRunner, mergedPR MergedPRReader) *FileDependencyResolver {
	if docsDir == "" {
		docsDir = "docs/tasks"
	}
	return &FileDependencyResolver{DocsDir: docsDir, Runner: runner, MergedPR: mergedPR}
}

// Satisfied resolves one dependency reference. An empty ref is unsatisfied,
// and any failure to verify a fact is an error rather than a false positive.
func (r *FileDependencyResolver) Satisfied(ctx context.Context, ref string) (bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false, nil
	}
	if m := dependencyPRRef.FindStringSubmatch(ref); m != nil {
		return r.prMerged(ctx, m[1])
	}
	if dependencyCardRef.MatchString(ref) {
		return r.cardDone(ref)
	}
	return r.existsOnMain(ctx, ref)
}

// cardDone reads a task card through an fs.FS so the app layer uses no
// os.ReadFile (ADR 0005). A missing card is simply not done; a read failure
// is an error. The status line is accepted in Russian and English.
func (r *FileDependencyResolver) cardDone(ref string) (bool, error) {
	dir := r.DocsDir
	if dir == "" {
		dir = "docs/tasks"
	}
	raw, err := fs.ReadFile(os.DirFS(dir), ref+".md")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("orchestrator: read card %s: %w", ref, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "статус:") && !strings.HasPrefix(lower, "status:") {
			continue
		}
		value := strings.TrimSpace(line[strings.Index(line, ":")+1:])
		return strings.EqualFold(strings.Trim(value, "*` "), "done"), nil
	}
	return false, nil
}

// prMerged reads the PR state from the already-loaded Watch data. There is no
// `gh pr view` fallback: an extra CLI process per dependency is exactly what
// this resolver exists to remove. Only a merged PR satisfies the dependency;
// an absent row, an open PR and an unverifiable answer all hold the merge back.
func (r *FileDependencyResolver) prMerged(ctx context.Context, pr string) (bool, error) {
	if r == nil || r.MergedPR == nil {
		return false, errors.New("orchestrator: merged PR reader is nil")
	}
	number, err := strconv.Atoi(strings.TrimSpace(pr))
	if err != nil {
		return false, fmt.Errorf("orchestrator: bad PR dependency %q: %w", pr, err)
	}
	if number <= 0 {
		return false, fmt.Errorf("orchestrator: bad PR dependency %q", pr)
	}
	merged, err := r.MergedPR.MergedPR(ctx, number)
	if err != nil {
		return false, fmt.Errorf("orchestrator: PR %d state: %w", number, err)
	}
	return merged, nil
}

// existsOnMain probes the ref with `git show main:<ref>`. A path that is not
// on main is unsatisfied, not an error. A runner failure that is not the
// explicit ErrRefMissing signal is propagated so it cannot look like success.
func (r *FileDependencyResolver) existsOnMain(ctx context.Context, ref string) (bool, error) {
	if r == nil || r.Runner == nil {
		return false, errors.New("orchestrator: dependency runner is nil")
	}
	path := strings.TrimPrefix(strings.TrimPrefix(ref, "./"), "/")
	if path == "" {
		return false, nil
	}
	_, err := r.Runner.Run(ctx, "git", "show", "main:"+path)
	if errors.Is(err, ErrRefMissing) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("orchestrator: git show %s: %w", ref, err)
	}
	return true, nil
}
