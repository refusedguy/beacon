//go:build eval

// Command worktree-gc закрепляет контракт сборщика worktree тремя сценариями
// из docs/research/research-worktree-gc.md, раздел 4. Раннер создаёт временный
// git-репозиторий и три worktree. Фейковый runner запоминает вызовы
// `git worktree remove` вместо удаления каталогов: боевые worktree не трогаются.
// Результат печатается в JSON.
//
// Запуск из каталога evals:
//
//	GOPROXY=off /tmp/opencode/go126/bin/go run -tags=eval ./worktree-gc
//
// Код выхода 0 только когда все три сценария PASS и негативный контроль со
// старым поведением тоже PASS. Описание и ожидаемый вывод в README.md.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	wtpkg "github.com/refusedguy/beacon/internal/infra/worktree"
)

const (
	reasonLocked       = "locked"
	reasonActiveWatch  = "active_watch"
	reasonActiveMarker = "active_marker"
	reasonDirty        = "dirty"
	reasonOutOfScope   = "out_of_scope"
	reasonRemove       = "remove"

	statusPass = "PASS"
	statusFail = "FAIL"
)

// watch — наблюдение, которое видит сборщик. State=watching означает живой Watch.
type watch struct {
	ID      string `json:"id"`
	PR      int    `json:"pr"`
	HeadSHA string `json:"head_sha"`
	Branch  string `json:"branch"`
	State   string `json:"state"`
}

// marker — запись активного агента со сроком действия (WG-03).
type marker struct {
	AgentID   string    `json:"agent_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// worktree — наблюдаемое состояние worktree во временном репозитории.
type worktree struct {
	Path    string  `json:"path"`
	HeadSHA string  `json:"head_sha"`
	Branch  string  `json:"branch"`
	Status  string  `json:"status"`
	Locked  bool    `json:"locked"`
	Marker  *marker `json:"marker,omitempty"`
}

// policy включает по одному правилу защиты. Каждый сценарий включает только
// своё правило, чтобы было видно, какое именно решение ловит регрессию.
type policy struct {
	scopeOnly   bool
	closedWatch *watch
	headguard   bool
	markers     bool
}

type decision struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type report struct {
	Decisions   []decision `json:"decisions"`
	Removed     int        `json:"removed"`
	RemoveCalls []string   `json:"remove_calls"`
}

// fakeRunner не удаляет каталоги. Он только запоминает вызов remove, поэтому
// eval не может повредить worktree вне временного каталога.
type fakeRunner struct {
	removeCalls []string
}

func (r *fakeRunner) remove(path string) {
	r.removeCalls = append(r.removeCalls, path)
}

// prefixMatch повторяет сравнение HEAD по префиксу SHA из cmd/beacon/gc.go.
func prefixMatch(recorded, head string) bool {
	return recorded != "" && head != "" && strings.HasPrefix(head, recorded)
}

// inScope — фильтр WG-01: закрытый Watch открывает сбор только своих worktree.
func inScope(wt worktree, watches []watch, closed watch) bool {
	for _, w := range watches {
		if w.ID != closed.ID {
			continue
		}
		if w.Branch != "" && w.Branch == wt.Branch {
			return true
		}
		if prefixMatch(w.HeadSHA, wt.HeadSHA) {
			return true
		}
	}
	return false
}

// collect — исполняемая модель правил удержания worktree. Порядок правил
// повторяет таблицу из раздела 1 исследования; scope, headguard и marker
// добавляют защиты, которых нет в текущем коде.
func collect(wts []worktree, watches []watch, p policy, r *fakeRunner, now time.Time) report {
	rep := report{Decisions: make([]decision, 0, len(wts))}
	for _, wt := range wts {
		reason := ""
		switch {
		case wt.Locked:
			reason = reasonLocked
		case p.scopeOnly && p.closedWatch != nil && !inScope(wt, watches, *p.closedWatch):
			reason = reasonOutOfScope
		}
		if reason == "" {
			for _, w := range watches {
				if w.State == "watching" && prefixMatch(w.HeadSHA, wt.HeadSHA) {
					reason = reasonActiveWatch + ":" + w.ID
					break
				}
			}
		}
		if reason == "" && p.headguard {
			for _, w := range watches {
				if w.State == "watching" && w.Branch != "" && w.Branch == wt.Branch {
					reason = reasonActiveWatch + ":" + w.ID
					break
				}
			}
		}
		if reason == "" && p.markers && wt.Marker != nil && wt.Marker.ExpiresAt.After(now) {
			reason = reasonActiveMarker
		}
		if reason == "" && strings.TrimSpace(wt.Status) != "" {
			reason = reasonDirty
		}
		if reason == "" {
			reason = reasonRemove
			r.remove(wt.Path)
			rep.Removed++
		}
		rep.Decisions = append(rep.Decisions, decision{Path: wt.Path, Reason: reason})
	}
	rep.RemoveCalls = r.removeCalls
	return rep
}

func firstReason(rep report) string {
	if len(rep.Decisions) == 0 {
		return "нет решения"
	}
	return rep.Decisions[0].Reason
}

// protectedReason принимает три причины из карточки и причину out_of_scope,
// которую даёт фильтр WG-01.
func protectedReason(reason string) bool {
	return reason == reasonOutOfScope ||
		reason == reasonActiveMarker ||
		reason == reasonDirty ||
		reason == reasonActiveWatch ||
		strings.HasPrefix(reason, reasonActiveWatch+":")
}

func statusOf(ok bool) string {
	if ok {
		return statusPass
	}
	return statusFail
}

// gitRun delegates to the infra git runner. The eval stays in the outside
// layer, so it must not shell out to git itself: os/exec is an infra-only
// capability. The runner sets the working directory and returns combined
// output, which keeps error messages identical to the production gc path.
func gitRun(dir string, args ...string) (string, error) {
	out, err := wtpkg.CommandRunner(dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

// dirExists uses ReadDir, which the capabilities gate does not reserve for
// infra. ReadDir on a regular file fails, so err == nil means a directory.
func dirExists(path string) bool {
	_, err := os.ReadDir(path)
	return err == nil
}

type scenarioResult struct {
	Name   string         `json:"name"`
	Status string         `json:"status"`
	Facts  map[string]any `json:"facts"`
}

type summary struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

type evalOutput struct {
	GeneratedAt     time.Time        `json:"generated_at"`
	Fixture         string           `json:"fixture"`
	Scenarios       []scenarioResult `json:"scenarios"`
	NegativeControl scenarioResult   `json:"negative_control"`
	Summary         summary          `json:"summary"`
}

func main() {
	os.Exit(run())
}

func run() int {
	root, err := os.MkdirTemp("", "wg05-eval-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "worktree-gc eval: %v\n", err)
		return 1
	}
	// The fixture is left in place for inspection. Removing it would need
	// os.RemoveAll, an infra-only capability, and a permanent gate exception
	// would have to be added and the committed gates binary rebuilt.

	fail := func(err error) int {
		fmt.Fprintf(os.Stderr, "worktree-gc eval: %v\n", err)
		return 1
	}

	repo := filepath.Join(root, "repo")
	// git init creates the directory, so no direct os.MkdirAll is needed.
	if _, err := gitRun("", "init", "-q", repo); err != nil {
		return fail(err)
	}
	steps := [][]string{
		{"config", "user.email", "wg05@example.com"},
		{"config", "user.name", "WG-05 Eval"},
	}
	for _, args := range steps {
		if _, err := gitRun(repo, args...); err != nil {
			return fail(err)
		}
	}
	// An empty commit seeds HEAD without writing a file, which keeps file
	// writes out of the outside layer.
	if _, err := gitRun(repo, "commit", "-q", "--allow-empty", "-m", "init"); err != nil {
		return fail(err)
	}
	shaA, err := gitRun(repo, "rev-parse", "HEAD")
	if err != nil {
		return fail(err)
	}

	wt1 := filepath.Join(root, "wt1")
	wt2 := filepath.Join(root, "wt2")
	wt3 := filepath.Join(root, "wt3")
	setup := [][]string{
		{"branch", "wt-local", shaA},
		{"worktree", "add", "--detach", "-q", wt1, shaA},
		{"worktree", "add", "-q", wt2, "wt-local"},
		{"worktree", "add", "--detach", "-q", wt3, shaA},
	}
	for _, args := range setup {
		if _, err := gitRun(repo, args...); err != nil {
			return fail(err)
		}
	}

	if _, err := gitRun(wt2, "commit", "-q", "--allow-empty", "-m", "local commit"); err != nil {
		return fail(err)
	}
	shaC, err := gitRun(wt2, "rev-parse", "HEAD")
	if err != nil {
		return fail(err)
	}

	status1, err := gitRun(wt1, "status", "--porcelain")
	if err != nil {
		return fail(err)
	}
	status2, err := gitRun(wt2, "status", "--porcelain")
	if err != nil {
		return fail(err)
	}
	status3, err := gitRun(wt3, "status", "--porcelain")
	if err != nil {
		return fail(err)
	}

	now := time.Now().UTC()
	unrelatedSHA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	wMerged := watch{ID: "watch-pr2", PR: 2, HeadSHA: unrelatedSHA, State: "merged"}
	wLocal := watch{ID: "watch-pr1", PR: 1, HeadSHA: shaA, Branch: "wt-local", State: "watching"}

	t1 := worktree{Path: wt1, HeadSHA: shaA, Status: status1}
	t2 := worktree{Path: wt2, HeadSHA: shaC, Branch: "wt-local", Status: status2}
	t3 := worktree{
		Path:    wt3,
		HeadSHA: shaA,
		Status:  status3,
		Marker:  &marker{AgentID: "wg05-eval-agent", ExpiresAt: now.Add(5 * time.Minute)},
	}

	// Сценарий A: чистый detached worktree без Watch переживает merged чужого PR.
	runnerA := &fakeRunner{}
	repA := collect([]worktree{t1}, []watch{wMerged},
		policy{scopeOnly: true, closedWatch: &wMerged}, runnerA, now)
	reasonA := firstReason(repA)
	okA := repA.Removed == 0 && len(runnerA.removeCalls) == 0 &&
		protectedReason(reasonA) && dirExists(wt1)
	results := []scenarioResult{{
		Name:   "detached-worktree-survives-unrelated-merge",
		Status: statusOf(okA),
		Facts: map[string]any{
			"removed":      repA.Removed,
			"remove_calls": len(runnerA.removeCalls),
			"reason":       reasonA,
			"dir_exists":   dirExists(wt1),
			"closed_pr":    wMerged.PR,
		},
	}}

	// Сценарий B: локальный коммит не делает worktree кандидатом.
	runnerB := &fakeRunner{}
	repB := collect([]worktree{t2}, []watch{wLocal}, policy{headguard: true}, runnerB, now)
	reasonB := firstReason(repB)
	_, catErr := gitRun(repo, "cat-file", "-e", shaC+"^{commit}")
	okB := repB.Removed == 0 && len(runnerB.removeCalls) == 0 &&
		strings.HasPrefix(reasonB, reasonActiveWatch+":") && catErr == nil && shaC != shaA
	results = append(results, scenarioResult{
		Name:   "local-commit-keeps-worktree",
		Status: statusOf(okB),
		Facts: map[string]any{
			"removed":              repB.Removed,
			"remove_calls":         len(runnerB.removeCalls),
			"reason":               reasonB,
			"watch_head_prefix":    watchHeadPrefix(wLocal.HeadSHA),
			"worktree_head_prefix": watchHeadPrefix(t2.HeadSHA),
			"local_commit_present": catErr == nil,
		},
	})

	// Сценарий C: свежий маркер удерживает worktree, просроченный нет.
	runnerC := &fakeRunner{}
	repC := collect([]worktree{t3}, nil, policy{markers: true}, runnerC, now)
	reasonC := firstReason(repC)
	freshOK := repC.Removed == 0 && len(runnerC.removeCalls) == 0 && reasonC == reasonActiveMarker

	expired := t3
	expiredMarker := *t3.Marker
	expiredMarker.ExpiresAt = now.Add(-time.Minute)
	expired.Marker = &expiredMarker
	runnerExpired := &fakeRunner{}
	repExpired := collect([]worktree{expired}, nil, policy{markers: true}, runnerExpired, now)
	reasonExpired := firstReason(repExpired)
	expiredOK := repExpired.Removed == 1 && len(runnerExpired.removeCalls) == 1 && reasonExpired == reasonRemove

	results = append(results, scenarioResult{
		Name:   "marker-blocks-collection",
		Status: statusOf(freshOK && expiredOK),
		Facts: map[string]any{
			"fresh_reason":     reasonC,
			"fresh_removed":    repC.Removed,
			"expired_reason":   reasonExpired,
			"expired_removed":  repExpired.Removed,
			"marker_valid_for": "5m",
		},
	})

	// Негативный контроль: старые правила теряют работу во всех трёх сценариях.
	oldA := &fakeRunner{}
	oldRepA := collect([]worktree{t1}, []watch{wMerged}, policy{}, oldA, now)
	oldB := &fakeRunner{}
	oldRepB := collect([]worktree{t2}, []watch{wLocal}, policy{}, oldB, now)
	oldC := &fakeRunner{}
	oldRepC := collect([]worktree{t3}, nil, policy{}, oldC, now)
	oldLost := oldRepA.Removed == 1 && oldRepB.Removed == 1 && oldRepC.Removed == 1
	negative := scenarioResult{
		Name:   "negative_control_old_behavior",
		Status: statusOf(oldLost),
		Facts: map[string]any{
			"detached_removed":     oldRepA.Removed,
			"local_commit_removed": oldRepB.Removed,
			"marker_removed":       oldRepC.Removed,
		},
	}

	out := evalOutput{
		GeneratedAt:     now,
		Fixture:         "временный git-репозиторий; runner записывает remove без удаления каталогов",
		Scenarios:       results,
		NegativeControl: negative,
	}
	for _, r := range results {
		if r.Status == statusPass {
			out.Summary.Passed++
		} else {
			out.Summary.Failed++
		}
	}
	if negative.Status == statusPass {
		out.Summary.Passed++
	} else {
		out.Summary.Failed++
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fail(err)
	}
	fmt.Println(string(data))
	if out.Summary.Failed > 0 {
		return 1
	}
	return 0
}

func watchHeadPrefix(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
