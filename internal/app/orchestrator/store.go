package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// WatchReader supplies the active Watches projected onto the orchestrator's
// decision shape. The composition root performs the projection from the
// domain store, so this package needs no domain types (ADR 0005).
type WatchReader interface {
	ActiveWatches(ctx context.Context) ([]Watch, error)
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
	dependencyPRRef = regexp.MustCompile(`(?i)^(?:pr\s*#|pr/|pr-|#)(\d+)$`)
	// dependencyIDLike matches slash-free, letter-led strings that carry a
	// digit and use only card ID characters. A valid taskstore ID of this
	// shape is a card ref. An invalid one is rejected instead of becoming a
	// git path. Real paths carry a separator or have no numeric group.
	dependencyIDLike = regexp.MustCompile(`(?i)^[a-z][a-z0-9_.-]*[0-9][a-z0-9_.-]*$`)
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

// DependencyCardStatus is the tri-state result of resolving a task card ref.
// Waiting can change not_done into done, but it cannot create a missing card.
// Keeping the states apart lets Decide report a broken ref instead of waiting
// forever for a file that does not exist.
type DependencyCardStatus string

const (
	// DependencyCardDone means the card carries an explicit done status.
	DependencyCardDone DependencyCardStatus = "done"
	// DependencyCardNotDone means the card exists but is not done yet, or has
	// no status line. A later tick may satisfy this ref.
	DependencyCardNotDone DependencyCardStatus = "not_done"
	// DependencyCardNotFound means the card file does not exist in DocsDir.
	DependencyCardNotFound DependencyCardStatus = "not_found"
)

// DependencyCardNotFoundError is returned by Satisfied when a waitFor ref
// names a task card that does not exist. Callers can match it with errors.As
// and report a broken dependency instead of treating it as a normal wait.
type DependencyCardNotFoundError struct {
	Ref string
}

func (e *DependencyCardNotFoundError) Error() string {
	return fmt.Sprintf("orchestrator: dependency card %s not found", e.Ref)
}

// Satisfied resolves one dependency reference. An empty ref is unsatisfied,
// and any failure to verify a fact is an error rather than a false positive.
// A missing task card is an error of type *DependencyCardNotFoundError: no
// future tick can satisfy a ref to a card that is not in DocsDir.
func (r *FileDependencyResolver) Satisfied(ctx context.Context, ref string) (bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false, nil
	}
	if m := dependencyPRRef.FindStringSubmatch(ref); m != nil {
		return r.prMerged(ctx, m[1])
	}
	if taskstore.IsID(ref) {
		status, err := r.cardDone(ref)
		if err != nil {
			return false, err
		}
		if status == DependencyCardNotFound {
			return false, &DependencyCardNotFoundError{Ref: ref}
		}
		return status == DependencyCardDone, nil
	}
	if dependencyIDLike.MatchString(ref) {
		return false, fmt.Errorf("orchestrator: dependency %q looks like a card ID but is not a valid one", ref)
	}
	return r.existsOnMain(ctx, ref)
}

// cardDone reads a task card through an fs.FS so the app layer uses no
// os.ReadFile (ADR 0005). It separates not_found, not_done and done. A read
// failure is an error. The status line is accepted in Russian and English.
func (r *FileDependencyResolver) cardDone(ref string) (DependencyCardStatus, error) {
	dir := r.DocsDir
	if dir == "" {
		dir = "docs/tasks"
	}
	raw, err := fs.ReadFile(os.DirFS(dir), ref+".md")
	if errors.Is(err, fs.ErrNotExist) {
		return DependencyCardNotFound, nil
	}
	if err != nil {
		return "", fmt.Errorf("orchestrator: read card %s: %w", ref, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "статус:") && !strings.HasPrefix(lower, "status:") {
			continue
		}
		value := strings.TrimSpace(line[strings.Index(line, ":")+1:])
		if strings.EqualFold(strings.Trim(value, "*` "), "done") {
			return DependencyCardDone, nil
		}
		return DependencyCardNotDone, nil
	}
	return DependencyCardNotDone, nil
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
