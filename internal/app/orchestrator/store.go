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

// CommandRunner is the seam the dependency resolver uses for `gh` and `git`.
// The app layer must not import os/exec (ADR 0005), so the composition root
// supplies the real runner and tests supply a fake. A runner error is never
// reported as a satisfied dependency.
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

// FileDependencyResolver resolves free-form refs from the task card files and
// from the hosting CLI / git. Card refs (T3.123) are satisfied when the card
// file documents "Статус: done". PR refs (#12, PR#12, pr/12) are satisfied
// when gh reports state MERGED. Every other string is a path that must exist
// on main, checked with `git show main:<ref>`.
type FileDependencyResolver struct {
	// DocsDir holds task cards. The empty value means "docs/tasks".
	DocsDir string
	// Runner supplies gh and git. A nil Runner yields an error, never a true.
	Runner CommandRunner
}

var _ DependencyResolver = (*FileDependencyResolver)(nil)

// NewDependencyResolver builds the resolver with the conventional docs dir.
func NewDependencyResolver(runner CommandRunner) *FileDependencyResolver {
	return &FileDependencyResolver{DocsDir: "docs/tasks", Runner: runner}
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

// prMerged asks gh for the PR state. Only MERGED satisfies the dependency;
// OPEN, CLOSED and an unverifiable state all hold the merge back.
func (r *FileDependencyResolver) prMerged(ctx context.Context, pr string) (bool, error) {
	if r == nil || r.Runner == nil {
		return false, errors.New("orchestrator: dependency runner is nil")
	}
	out, err := r.Runner.Run(ctx, "gh", "pr", "view", pr, "--json", "state")
	if err != nil {
		return false, fmt.Errorf("orchestrator: gh pr view %s: %w", pr, err)
	}
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return false, fmt.Errorf("orchestrator: gh pr view %s decode: %w", pr, err)
	}
	return strings.EqualFold(strings.TrimSpace(payload.State), "MERGED"), nil
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
