package bench

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// Fixture sizes are fixed and seeded so two runs on the same machine measure
// the same amount of work.
const (
	fixtureRuns      = 120
	fixtureTasks     = 90
	fixtureDecisions = 400
	fixtureCards     = 60
	fixtureWireRuns  = 30
	fixtureSeed      = 20261125
)

func seeded() *rand.Rand {
	return rand.New(rand.NewSource(fixtureSeed))
}

func hexString(r *rand.Rand, n int) string {
	const digits = "0123456789abcdef"
	var b strings.Builder
	b.Grow(n)
	for i := 0; i < n; i++ {
		b.WriteByte(digits[r.Intn(len(digits))])
	}
	return b.String()
}

// runFixtures builds runs with a realistic mix of states, scopes and checks.
// Snapshot queries read all of these fields, so the fixtures must be populated
// rather than zero valued.
func runFixtures(n int) []*core.Run {
	r := seeded()
	states := []core.RunState{
		core.RunRunning, core.RunWaitingCI, core.RunNeedsRebase,
		core.RunNeedsAgentFix, core.RunBlocked, core.RunMerged, core.RunClosed,
	}
	out := make([]*core.Run, 0, n)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		run := &core.Run{
			ID:            core.RunID(fmt.Sprintf("run-%04d", i)),
			TaskID:        core.TaskID(fmt.Sprintf("T-20261125-%04d", i)),
			State:         states[i%len(states)],
			PRNumber:      2000 + i,
			HeadSHA:       hexString(r, 40),
			ObservedSHA:   hexString(r, 40),
			SessionID:     "ses_" + hexString(r, 16),
			WorkspacePath: fmt.Sprintf("/mnt/projects/beacon/worktrees/T-20261125-%04d", i),
			Branch:        fmt.Sprintf("beacon/T-20261125-%04d", i),
			Scope:         []core.ScopePath{core.ScopeFile("pkg/core/run.go"), core.ScopeDir("internal/bench/")},
			MaxAttempts:   12,
			Reason:        "checks failed on main",
			CreatedAt:     now.Add(-time.Duration(i) * time.Minute),
			UpdatedAt:     now.Add(-time.Duration(i) * time.Second),
		}
		run.Checks = []core.CheckResult{
			{Name: "go test", State: core.CheckSuccess},
			{Name: "gates", State: core.CheckFailure, Output: "one advisory line"},
		}
		out = append(out, run)
	}
	return out
}

// taskFixtures builds cards with dependency chains and scope patterns so the
// recursive ready-task query and the queue join both do real work.
func taskFixtures(n int) []core.Task {
	out := make([]core.Task, 0, n)
	for i := 0; i < n; i++ {
		task := core.Task{
			ID:            core.TaskID(fmt.Sprintf("T-20261125-%04d", i)),
			Title:         fmt.Sprintf("Hot path %d", i),
			Body:          strings.Repeat("A benchmark fixture paragraph. ", 8),
			Priority:      50 + i%50,
			Status:        core.TaskAvailable,
			SourceRef:     fmt.Sprintf("docs/tasks/T-20261125-%04d.md", i),
			Kind:          core.TaskKindTask,
			BaseBranch:    "main",
			RequireReview: core.ReviewModeLLM,
			WorkerAgent:   "worker",
			WorkerPrompt:  "implement and open a PR",
		}
		task.Scope = []core.ScopePath{
			core.ScopeFile(fmt.Sprintf("internal/bench/path_%02d.go", i%20)),
			core.ScopePattern("docs/perf/**"),
		}
		if i > 0 && i%3 == 0 {
			task.DependsOn = []core.TaskID{
				core.TaskID(fmt.Sprintf("T-20261125-%04d", i-1)),
				core.TaskID(fmt.Sprintf("T-20261125-%04d", i/2)),
			}
		}
		out = append(out, task)
	}
	return out
}

// decisionFixtures builds structured decisions across runs and the orphan
// bucket that the bounded snapshot query keeps separate.
func decisionFixtures(n int, runIDs []string) []app.Decision {
	r := seeded()
	kinds := []string{"dispatch", "reconcile", "control"}
	verdicts := []string{"ready", "retry", "keep_working"}
	out := make([]app.Decision, 0, n)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		id := ""
		if i%4 != 0 {
			id = runIDs[r.Intn(len(runIDs))]
		}
		out = append(out, app.Decision{
			At:       now.Add(time.Duration(i) * time.Second),
			CycleID:  fmt.Sprintf("cycle-%04d", i%16),
			Kind:     kinds[i%len(kinds)],
			RunID:    id,
			TaskID:   fmt.Sprintf("T-20261125-%04d", i%fixtureTasks),
			PRNumber: 2000 + i%200,
			Verdict:  verdicts[i%len(verdicts)],
			Action:   "admit",
			Reason:   strings.Repeat("snapshot decision reason ", 3),
			Fields:   map[string]any{"attempt": i % 12, "sha": hexString(r, 8)},
		})
	}
	return out
}

// cardFixtures writes realistic markdown cards. Each card has a fenced YAML
// block plus flat Scope and Depends lines, which is the shape ParseTaskFields
// handles on every task source refresh.
func cardFixtures(n int) []string {
	r := seeded()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		dep := fmt.Sprintf("T-20261125-%04d", (i+n-1)%n)
		out = append(out, fmt.Sprintf(`# T-20261125-%04d. Benchmark card %d

Scope: internal/bench/**, docs/perf/**
Depends: %s
Priority: %d
Kind: task
BaseBranch: main
RequireReview: llm

`+"```yaml"+`
id: T-20261125-%04d
scope:
  - internal/bench/%02d.go
  - docs/perf/dossier.md
depends:
  - %s
priority: %d
kind: task
require_review: llm
`+"```"+`

Goal: make the hot path measurable with one command.

Context: fixture %s.

Steps: seed, measure, write the dossier.

Check: two runs stay within 15 percent.
`, i, i, dep, 50+i%50, i, i%20, dep, 50+i%50, hexString(r, 8)))
	}
	return out
}

// secretText marks whether a fixture is expected to change under redaction.
// The one plain text pays the full rule chain without a match, but must not be
// reported as an error.
type secretText struct {
	value    string
	redacted bool
}

// secretTexts builds text that exercises every redaction rule. Prefixes are
// assembled at runtime so the fixtures do not duplicate the secret patterns
// owned by pkg/core/redact.
func secretTexts() []secretText {
	r := seeded()
	secret := func(s string) secretText { return secretText{value: s, redacted: true} }
	return []secretText{
		secret("token " + "gh" + "p_" + hexString(r, 24) + " in log line"),
		secret("PAT " + "github" + "_pat_" + hexString(r, 24)),
		secret("slack " + "xox" + "b-" + hexString(r, 16)),
		secret("openai " + "sk-" + hexString(r, 24)),
		secret("google " + "AIza" + hexString(r, 35)),
		secret("telegram " + hexString(r, 10) + ":" + hexString(r, 35)),
		secret("clone https://ci:" + hexString(r, 12) + "@git.example.com/beacon.git"),
		secret("Authorization: Bearer " + hexString(r, 24)),
		secret(`config token="` + hexString(r, 24) + `" verbosity=2`),
		{value: "plain text with no secret and a long tail of ordinary words"},
	}
}

func runIDs(runs []*core.Run) []string {
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		out = append(out, string(run.ID))
	}
	return out
}
