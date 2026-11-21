// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_task_claim. An agent passes its agentID and gets
// one todo card with the full hand-off context: id, goal, dependencies, owner
// files, plan mode and a five-field handoff snapshot. The card is marked
// in_progress in the same critical section that selects it, so two agents can
// never receive the same card.
//
// The store is a port so the handler stays testable. FileTaskClaimStore is the
// stdlib-only adapter: it reads markdown cards, holds an exclusive flock while
// it selects and writes, and swaps the three status fields with an atomic
// rename. Registering the tool in server.go and fixing its schema belong to
// the wiring card, not to this file.
package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// TaskClaimToolName is the name AttachTaskClaim registers. It is deliberately
// absent from the default ToolNames set: the contract card owns that registry.
const TaskClaimToolName = "beacon_task_claim"

// TaskClaimInput is the argument object of beacon_task_claim. AgentID is the
// only required field. CardID pins one card; without it the store selects the
// first todo card. Phase narrows automatic selection.
type TaskClaimInput struct {
	AgentID string `json:"agentID" jsonschema:"identifier of the agent receiving the task; required"`
	CardID  string `json:"cardID,omitempty" jsonschema:"optional exact card id; without it the next todo card is selected"`
	Phase   string `json:"phase,omitempty" jsonschema:"optional phase filter for automatic selection, for example P1"`
}

// TaskClaimOutput is the full context handed to the agent. Status is always
// in_progress on a successful claim. Handoff carries the five fields an agent
// needs to continue without reading the whole card history.
type TaskClaimOutput struct {
	CardID       string         `json:"cardID"`
	Title        string         `json:"title"`
	Status       string         `json:"status"`
	AgentID      string         `json:"agentID"`
	Goal         string         `json:"goal"`
	Dependencies []string       `json:"dependencies"`
	OwnerFiles   []string       `json:"ownerFiles"`
	PlanMode     string         `json:"planMode"`
	ClaimedAt    string         `json:"claimedAt"`
	Handoff      domain.Handoff `json:"handoff"`
}

// TaskClaimStore is the store boundary of beacon_task_claim. Claim selects one
// todo card, marks it in_progress and returns its context. Two concurrent
// claims must never return the same cardID. ClaimedAt is supplied by the caller
// so a test can pin the clock.
type TaskClaimStore interface {
	Claim(ctx context.Context, in TaskClaimInput, claimedAt string) (TaskClaimOutput, error)
}

// AgentCardLookup is the read side of a claim store: the card an agent
// currently holds. Claim records the agent id in the card's Агент field, so
// that field is the durable cardID -> agentID link; this interface exposes its
// lookup without widening the write-only TaskClaimStore port. The composition
// root uses it to bind a Watch that registered without an explicit cardID to
// the card its agent claimed, so a merged PR can close that card.
type AgentCardLookup interface {
	CardIDForAgent(agentID string) string
}

// TaskClaimHandler validates the input and delegates the atomic step to the
// store. It owns no card state of its own, so a fresh handler can be built for
// every server without losing a claim.
type TaskClaimHandler struct {
	store TaskClaimStore
	now   func() time.Time
}

// NewTaskClaimHandler builds a handler over the given store.
func NewTaskClaimHandler(store TaskClaimStore) *TaskClaimHandler {
	return newTaskClaimHandler(store, time.Now)
}

func newTaskClaimHandler(store TaskClaimStore, now func() time.Time) *TaskClaimHandler {
	if now == nil {
		now = time.Now
	}
	return &TaskClaimHandler{store: store, now: now}
}

// Handle adapts Claim to the go-sdk typed tool handler signature. Domain errors
// pass through unchanged so the MCP boundary can turn them into error shapes.
func (h *TaskClaimHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in TaskClaimInput) (*mcpsdk.CallToolResult, TaskClaimOutput, error) {
	out, err := h.Claim(ctx, in)
	if err != nil {
		return nil, TaskClaimOutput{}, err
	}
	return nil, out, nil
}

// Claim validates the agent id, stamps the time and asks the store for one
// card. An empty agent id is rejected before the store is touched.
func (h *TaskClaimHandler) Claim(ctx context.Context, in TaskClaimInput) (TaskClaimOutput, error) {
	if h == nil || h.store == nil {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodePersistFailed, "task_claim: store is nil")
	}
	if strings.TrimSpace(in.AgentID) == "" {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodeInvalidInput, "task_claim: agentID is required")
	}
	when := h.now().UTC().Format(time.RFC3339)
	return h.store.Claim(ctx, in, when)
}

// FileTaskClaimStore is a stdlib-only TaskClaimStore over markdown cards.
// TasksDir holds the cards; StateDir holds the lock file and defaults to
// TasksDir when empty.
type FileTaskClaimStore struct {
	TasksDir string
	StateDir string
}

// claimCard is the subset of a card the tool needs. Raw is kept so the status
// swap changes nothing else in the file.
type claimCard struct {
	ID           string
	Path         string
	Raw          string
	Title        string
	Status       string
	Phase        string
	Agent        string
	Time         string
	OwnerFiles   []string
	Dependencies []string
	Goal         string
	Context      string
	Steps        string
	Check        string
	Acceptance   string
	PlanMode     string
}

var (
	claimCardIDRE     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*[.-][0-9]+$`)
	claimFieldLineRE  = regexp.MustCompile(`^([\p{L}][\p{L}\p{N} ]{0,40}):[ \t]*(.*)$`)
	claimStatusLineRE = regexp.MustCompile(`^(Статус|Агент|Время):[ \t]*(.*?)[ \t]*$`)
)

// Claim takes the flock first, then reads and writes the cards. Re-reading
// under the lock is what makes the status check authoritative: a claim that
// started earlier has already written in_progress by the time this one looks.
func (s *FileTaskClaimStore) Claim(ctx context.Context, in TaskClaimInput, claimedAt string) (TaskClaimOutput, error) {
	if err := ctx.Err(); err != nil {
		return TaskClaimOutput{}, err
	}
	if s == nil || strings.TrimSpace(s.TasksDir) == "" {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodePersistFailed, "task_claim: tasks dir is empty")
	}
	agentID := strings.TrimSpace(in.AgentID)
	if agentID == "" {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodeInvalidInput, "task_claim: agentID is required")
	}
	when := strings.TrimSpace(claimedAt)
	if when == "" {
		when = time.Now().UTC().Format(time.RFC3339)
	}
	phase := strings.TrimSpace(in.Phase)
	if phase != "" && !validClaimPhase(phase) {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("task_claim: unknown phase %q", phase))
	}
	cardID := strings.TrimSuffix(strings.TrimSpace(in.CardID), ".md")

	lockPath := s.lockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return TaskClaimOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()

	cards, err := s.cards()
	if err != nil {
		return TaskClaimOutput{}, err
	}

	var chosen claimCard
	if cardID != "" {
		found := false
		for _, c := range cards {
			if c.ID == cardID {
				chosen, found = c, true
				break
			}
		}
		if !found {
			return TaskClaimOutput{}, domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("task_claim: card %s not found", cardID))
		}
	} else {
		for _, c := range cards {
			if c.Status != "todo" || (phase != "" && c.Phase != phase) {
				continue
			}
			if err := handoffOf(c).Validate(); err != nil {
				continue
			}
			chosen = c
			break
		}
		if chosen.ID == "" {
			return TaskClaimOutput{}, domain.NewAppError(domain.CodeGone, "task_claim: no todo card available")
		}
	}

	if chosen.Status != "todo" {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodeGone, fmt.Sprintf(
			"task_claim: card %s already in_progress by %s at %s", chosen.ID, chosen.Agent, chosen.Time))
	}

	out := TaskClaimOutput{
		CardID:       chosen.ID,
		Title:        chosen.Title,
		Status:       "in_progress",
		AgentID:      agentID,
		Goal:         chosen.Goal,
		Dependencies: append([]string(nil), chosen.Dependencies...),
		OwnerFiles:   append([]string(nil), chosen.OwnerFiles...),
		PlanMode:     planModeOf(chosen.PlanMode),
		ClaimedAt:    when,
		Handoff:      handoffOf(chosen),
	}
	if err := out.Handoff.Validate(); err != nil {
		return TaskClaimOutput{}, err
	}
	if err := writeClaimFile(chosen.Path, []byte(replaceClaimFields(chosen.Raw, agentID, when))); err != nil {
		return TaskClaimOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	return out, nil
}

// cards reads and parses every card, skipping malformed files. The result is
// sorted by id so selection is deterministic across processes.
func (s *FileTaskClaimStore) cards() ([]claimCard, error) {
	entries, err := os.ReadDir(s.TasksDir)
	if err != nil {
		return nil, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	cards := make([]claimCard, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(s.TasksDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		c, err := parseClaimCard(path, data)
		if err != nil {
			continue
		}
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	return cards, nil
}

// CardIDForAgent returns the card the agent currently holds, or "" when it
// holds none. Claim writes the agent id into the card's Агент field under the
// same flock as the status swap, so that field is a durable cardID -> agentID
// link and no second store is needed. When an agent holds several cards the
// most recently claimed one wins, which is the card its next Watch is for.
// A bad or unreadable tasks dir yields no link; the caller keeps the Watch it
// already registered.
func (s *FileTaskClaimStore) CardIDForAgent(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if s == nil || agentID == "" || strings.TrimSpace(s.TasksDir) == "" {
		return ""
	}
	cards, err := s.cards()
	if err != nil {
		return ""
	}
	cardID, latest := "", ""
	for _, c := range cards {
		if c.Status != "in_progress" || c.Agent != agentID {
			continue
		}
		if cardID == "" || c.Time > latest {
			cardID, latest = c.ID, c.Time
		}
	}
	return cardID
}

func (s *FileTaskClaimStore) lockPath() string {
	dir := strings.TrimSpace(s.StateDir)
	if dir == "" {
		dir = s.TasksDir
	}
	return filepath.Join(dir, ".beacon-task-claim.lock")
}

// parseClaimCard reads the top-level fields of one card. A malformed card is
// skipped by the lister and rejected when pinned by CardID.
func parseClaimCard(filePath string, data []byte) (claimCard, error) {
	id := strings.TrimSuffix(filepath.Base(filePath), ".md")
	if !claimCardIDRE.MatchString(id) {
		return claimCard{}, fmt.Errorf("task_claim: unsupported card id %q", id)
	}
	c := claimCard{ID: id, Path: filePath, Raw: string(data)}
	for _, line := range strings.Split(c.Raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "# ") && c.Title == "" {
			title := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[2:]), id+"."))
			c.Title = strings.TrimSpace(strings.TrimPrefix(title, "."))
			continue
		}
		if line != strings.TrimLeft(line, " \t") {
			continue
		}
		fm := claimFieldLineRE.FindStringSubmatch(line)
		if fm == nil {
			continue
		}
		switch fm[1] {
		case "Статус":
			c.Status = fm[2]
		case "Фаза":
			c.Phase = fm[2]
		case "Агент":
			c.Agent = fm[2]
		case "Время":
			c.Time = fm[2]
		case "Владелец файлов":
			c.OwnerFiles = claimList(fm[2])
		case "Зависимости":
			c.Dependencies = claimList(fm[2])
		case "Цель":
			c.Goal = fm[2]
		case "Контекст":
			c.Context = fm[2]
		case "Шаги":
			c.Steps = fm[2]
		case "Проверка":
			c.Check = fm[2]
		case "Приёмка":
			c.Acceptance = fm[2]
		case "Режим плана":
			c.PlanMode = fm[2]
		}
	}
	if c.Status == "" || c.Goal == "" {
		return claimCard{}, fmt.Errorf("task_claim: card %s has no status or goal", id)
	}
	return c, nil
}

func claimList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || v == "нет" {
		return []string{}
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validClaimPhase(v string) bool {
	switch v {
	case "P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8":
		return true
	default:
		return false
	}
}

// planModeOf follows the card template: an empty field means fast, and only
// the literal "full" opts into the full plan mode.
func planModeOf(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "full") {
		return "full"
	}
	return "fast"
}

func handoffOf(c claimCard) domain.Handoff {
	return domain.Handoff{
		Intent:           c.Goal,
		Evidence:         c.Context,
		PatchPlan:        c.Steps,
		VerificationPlan: c.Check,
		RiskNotes:        c.Acceptance,
	}
}

// replaceClaimFields rewrites only the three lease fields and keeps the rest of
// the card byte for byte, including CRLF endings.
func replaceClaimFields(raw, agent, when string) string {
	cr := ""
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			line = strings.TrimSuffix(line, "\r")
		} else {
			cr = ""
		}
		fm := claimStatusLineRE.FindStringSubmatch(line)
		if fm == nil {
			continue
		}
		switch fm[1] {
		case "Статус":
			lines[i] = "Статус: in_progress" + cr
		case "Агент":
			lines[i] = "Агент: " + agent + cr
		case "Время":
			lines[i] = "Время: " + when + cr
		}
	}
	return strings.Join(lines, "\n")
}

// writeClaimFile replaces the card in one rename, so a reader sees either the
// old or the new version and never a half-written one.
func writeClaimFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".task-claim-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// AttachTaskClaim registers exactly one beacon_task_claim tool. It is opt-in:
// wiring it into NewServer is the contract card's job, so this helper is not
// called automatically. A claim changes card state and is not idempotent.
func AttachTaskClaim(server *mcpsdk.Server, handler *TaskClaimHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools(TaskClaimToolName)
	no := false
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        TaskClaimToolName,
		Description: "Atomically claim one todo task card for an agent. Returns the card id, goal, dependencies, owner files, plan mode and a handoff snapshot, and marks the card in_progress.",
		Annotations: &mcpsdk.ToolAnnotations{
			DestructiveHint: &no,
			IdempotentHint:  false,
		},
	}, handler.Handle)
}
