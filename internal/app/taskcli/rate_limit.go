package taskcli

import (
	"errors"
	"fmt"
	"strings"
)

// ---- ADR 0031 §4 / Q-report 2.3: цепочка фолбэка агентов при 429 ----
//
// Rule and chain live in the CLI manager. The MCP core never sees executor
// names or the 429 rule, so the ADR 0006 and ADR 0024 boundary holds. The
// external job id is the task card id and is never rewritten on a handover.

// Executors are the fixed rungs of the CLI-manager fallback chain.
const (
	ExecutorClaudeCode = "claude-code"
	ExecutorHermes     = "hermes"
	ExecutorGemini     = "gemini"
)

// FallbackChain is the order a job walks when an executor answers 429.
var FallbackChain = []string{ExecutorClaudeCode, ExecutorHermes, ExecutorGemini}

// RateLimited is the 429 signal from an executor. It is the only event that
// triggers routing. Status is 429 so a caller can raise it from its own
// transport error without importing the CLI package.
type RateLimited struct {
	Executor string
	Message  string
}

func (e *RateLimited) Error() string {
	if e == nil || e.Message == "" {
		return "executor answered 429"
	}
	return e.Message
}

// Status makes RateLimited legible to generic transport-error handling.
func (e *RateLimited) Status() int { return 429 }

// statusCoder is the narrow shape of an error that carries an HTTP status.
type statusCoder interface {
	Status() int
}

// IsRateLimit reports whether err is a 429 from an executor. Plain errors and
// 5xx responses are not routing signals and must not move a job.
func IsRateLimit(err error) bool {
	if err == nil {
		return false
	}
	var rl *RateLimited
	if errors.As(err, &rl) {
		return true
	}
	var sc statusCoder
	return errors.As(err, &sc) && sc.Status() == 429
}

// Job is one CLI-managed dispatch. ID is the external job id and never changes
// when the executor changes; keeping it stable is the point of the fallback.
type Job struct {
	ID       string
	Executor string
	Tried    []string
}

// NewJob starts a job on the first executor of the chain.
func NewJob(id string) (Job, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Job{}, Validationf("не задан внешний ID задачи")
	}
	return Job{ID: id, Executor: FallbackChain[0]}, nil
}

// NextOnRateLimit advances the job one step along the chain. The external id
// is returned unchanged. ok is false when the chain is exhausted; in that
// case the job itself is unchanged, so the executor is stable too.
func NextOnRateLimit(j Job) (Job, bool) {
	i := chainIndex(j.Executor)
	if i < 0 || i+1 >= len(FallbackChain) {
		return j, false
	}
	tried := append(append([]string{}, j.Tried...), j.Executor)
	return Job{ID: j.ID, Executor: FallbackChain[i+1], Tried: tried}, true
}

func chainIndex(agent string) int {
	for i, name := range FallbackChain {
		if name == agent {
			return i
		}
	}
	return -1
}

// RouteResult is the observable outcome of one routing step.
type RouteResult struct {
	JobID     string // external job id, unchanged by the handover
	From      string
	To        string
	Status    string // "in_progress <executor>", or the old status when exhausted
	Exhausted bool
}

// RouteRateLimit applies the fallback rule to a task card. On a 429 it moves
// the executor one step forward, keeps the external job id (the card ID), and
// writes the new executor into the card so status shows who runs it now. A
// card whose executor is outside the chain starts from the first rung.
func (s *Service) RouteRateLimit(id string, upstream error) (RouteResult, error) {
	if !IsRateLimit(upstream) {
		return RouteResult{}, Validationf("фолбэк вызывается только по 429, получено: %v", upstream)
	}
	id = strings.TrimSuffix(strings.TrimSpace(id), ".md")
	if !IsID(id) {
		return RouteResult{}, Validationf("неверный ID карточки %q", id)
	}

	repo := newRateLimitRepo(s.Cfg)
	unlock, err := repo.lock()
	if err != nil {
		return RouteResult{}, err
	}
	defer unlock()

	target, err := repo.load(id)
	if err != nil {
		return RouteResult{}, err
	}
	if target.Status != "todo" && target.Status != "in_progress" {
		return RouteResult{}, Validationf("карточка %s в статусе %s, фолбэк неприменим", id, target.Status)
	}

	from := target.Agent
	if chainIndex(from) < 0 {
		from = FallbackChain[0]
	}
	job := Job{ID: target.ID, Executor: from}
	next, ok := NextOnRateLimit(job)
	if !ok {
		return RouteResult{JobID: target.ID, From: from, To: from,
			Status: target.Status + " " + from, Exhausted: true}, nil
	}

	newText := setCardExecutor(target.Raw, next.Executor, from)
	if err := repo.save(*target, newText); err != nil {
		return RouteResult{}, err
	}
	return RouteResult{JobID: target.ID, From: from, To: next.Executor,
		Status: "in_progress " + next.Executor}, nil
}

// setCardExecutor writes the current executor into the status fields of a
// card. Агент stays the machine-readable field; Исполнитель and Фолбэк make
// the handover readable in status. Fields are updated in place and inserted
// once, so repeated fallbacks never duplicate them.
func setCardExecutor(raw, executor, from string) string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines)+2)
	hasExecutor, hasFallback := false, false
	for _, line := range lines {
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			line = strings.TrimSuffix(line, "\r")
		}
		switch {
		case strings.HasPrefix(line, "Агент:"):
			out = append(out, "Агент: "+executor+cr)
		case strings.HasPrefix(line, "Исполнитель:"):
			out = append(out, "Исполнитель: "+executor+cr)
			hasExecutor = true
		case strings.HasPrefix(line, "Фолбэк:"):
			out = append(out, fmt.Sprintf("Фолбэк: %s → %s (429)%s", from, executor, cr))
			hasFallback = true
		case strings.HasPrefix(line, "Статус:"):
			out = append(out, "Статус: in_progress"+cr)
		default:
			out = append(out, line+cr)
		}
	}
	if !hasExecutor {
		out = insertAfter(out, "Время:", "Исполнитель: "+executor)
	}
	if !hasFallback {
		out = insertAfter(out, "Исполнитель:", fmt.Sprintf("Фолбэк: %s → %s (429)", from, executor))
	}
	return strings.Join(out, "\n")
}

// insertAfter places line after the last field with the given prefix. If the
// prefix is absent the line is appended.
func insertAfter(lines []string, prefix, line string) []string {
	at := -1
	for i, existing := range lines {
		if strings.HasPrefix(existing, prefix) {
			at = i
		}
	}
	if at < 0 {
		return append(lines, line)
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at+1]...)
	out = append(out, line)
	return append(out, lines[at+1:]...)
}
