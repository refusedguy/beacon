// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_task_close. An agent closes one named card with
// a terminal status and a short reason. The call is idempotent by
// (cardID, status, reason): a repeat with the same three values returns ok
// without another write, and a repeat with a different status or reason is
// refused. A card held in_progress by another agent can only be closed by
// that agent.
//
// The store is a port so the handler stays testable. FileTaskCloseStore is the
// stdlib-only adapter: it reads markdown cards, holds an exclusive flock over
// the check and the write, and swaps the status line with an atomic rename.
// The reason and the duplicate target live in a sidecar ledger under StateDir,
// so the card keeps the 13 fields the taskstore parser and doc gate accept.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
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

// TaskCloseToolName is the advertised name registered in server.go and
// installed by attachTaskClose.
const TaskCloseToolName = "beacon_task_close"

// TaskCloseInput is the argument object of beacon_task_close. CardID pins the
// one card to close; there is no automatic selection. Status is one of done,
// dropped, blocked or duplicate. Reason is the short audit reason and is part
// of the idempotency key. DuplicateOf is required only for duplicate. AgentID
// is required and checked when the card is in_progress and owned by an agent.
type TaskCloseInput struct {
	CardID      string `json:"cardID" jsonschema:"exact card id to close; required"`
	Status      string `json:"status" jsonschema:"done, dropped, blocked or duplicate; required"`
	Reason      string `json:"reason" jsonschema:"short reason recorded in the close ledger; required"`
	DuplicateOf string `json:"duplicateOf,omitempty" jsonschema:"card id this one duplicates; required when status is duplicate"`
	AgentID     string `json:"agentID" jsonschema:"agent closing the card; required and checked when the card is in_progress"`
}

// TaskCloseOutput confirms the terminal status written to the card. OK is true
// for both a fresh close and an idempotent repeat.
type TaskCloseOutput struct {
	OK     bool   `json:"ok"`
	CardID string `json:"cardID"`
	Status string `json:"status"`
}

func validTaskCloseStatus(v string) bool {
	switch v {
	case "done", "dropped", "blocked", "duplicate":
		return true
	default:
		return false
	}
}

func taskCloseTerminal(v string) bool {
	return v != "" && v != "todo" && v != "in_progress"
}

// TaskCloseStore is the store boundary of beacon_task_close. Close validates
// existence, ownership and the idempotency key under one lock and writes the
// status plus the ledger. closedAt is supplied by the caller so a test can pin
// the clock.
type TaskCloseStore interface {
	Close(ctx context.Context, in TaskCloseInput, closedAt string) (TaskCloseOutput, error)
}

// TaskCloseHandler validates the input and delegates the atomic step to the
// store. It holds no card state of its own.
type TaskCloseHandler struct {
	store TaskCloseStore
	now   func() time.Time
}

// NewTaskCloseHandler builds a handler over the given store.
func NewTaskCloseHandler(store TaskCloseStore) *TaskCloseHandler {
	return newTaskCloseHandler(store, time.Now)
}

func newTaskCloseHandler(store TaskCloseStore, now func() time.Time) *TaskCloseHandler {
	if now == nil {
		now = time.Now
	}
	return &TaskCloseHandler{store: store, now: now}
}

// Handle adapts Close to the go-sdk typed tool handler signature.
func (h *TaskCloseHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in TaskCloseInput) (*mcpsdk.CallToolResult, TaskCloseOutput, error) {
	out, err := h.Close(ctx, in)
	if err != nil {
		return nil, TaskCloseOutput{}, err
	}
	return nil, out, nil
}

// Close checks the input at the boundary, stamps the time and asks the store
// for the atomic close. Status is lowercased so callers can use either case.
func (h *TaskCloseHandler) Close(ctx context.Context, in TaskCloseInput) (TaskCloseOutput, error) {
	if h == nil || h.store == nil {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, "task_close: store is nil")
	}
	in.CardID = strings.TrimSuffix(strings.TrimSpace(in.CardID), ".md")
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	in.Reason = strings.TrimSpace(in.Reason)
	in.DuplicateOf = strings.TrimSuffix(strings.TrimSpace(in.DuplicateOf), ".md")
	in.AgentID = strings.TrimSpace(in.AgentID)
	if in.CardID == "" || in.Reason == "" {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, "task_close: cardID and reason are required")
	}
	if !validTaskCloseStatus(in.Status) {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("task_close: unknown status %q", in.Status))
	}
	if in.Status == "duplicate" {
		if in.DuplicateOf == "" {
			return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, "task_close: duplicateOf is required when status is duplicate")
		}
		if in.DuplicateOf == in.CardID {
			return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, "task_close: a card cannot duplicate itself")
		}
	}
	when := h.now().UTC().Format(time.RFC3339)
	return h.store.Close(ctx, in, when)
}

// FileTaskCloseStore is a stdlib-only TaskCloseStore over markdown cards.
// TasksDir holds the cards. StateDir holds the lock and the close ledger and
// defaults to TasksDir when empty.
type FileTaskCloseStore struct {
	TasksDir string
	StateDir string
}

// closeCard is the subset of a card the tool reads. Raw is kept so the status
// swap changes nothing else in the file.
type closeCard struct {
	ID     string
	Path   string
	Raw    string
	Status string
	Agent  string
}

// closeRecord is one close in the sidecar ledger. The idempotency key is the
// pair (Status, Reason) stored per cardID; the other fields are audit data.
type closeRecord struct {
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	DuplicateOf string `json:"duplicateOf,omitempty"`
	AgentID     string `json:"agentID"`
	ClosedAt    string `json:"closedAt"`
}

// closeLedger is the on-disk map of cardID to its close record.
type closeLedger struct {
	Cards map[string]closeRecord `json:"cards"`
}

var (
	closeCardIDRE    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*[.-][0-9]+$`)
	closeFieldLineRE = regexp.MustCompile(`^([\p{L}][\p{L}\p{N} ]{0,40}):[ \t]*(.*)$`)
	closeStatusRE    = regexp.MustCompile(`^Статус:[ \t]*(.*?)[ \t]*$`)
)

// Close takes the flock first, then reads and writes the cards and the ledger.
// The lock makes the existence, ownership and idempotency checks authoritative
// against a concurrent claim or close.
func (s *FileTaskCloseStore) Close(ctx context.Context, in TaskCloseInput, closedAt string) (TaskCloseOutput, error) {
	if err := ctx.Err(); err != nil {
		return TaskCloseOutput{}, err
	}
	if s == nil || strings.TrimSpace(s.TasksDir) == "" {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, "task_close: tasks dir is empty")
	}

	cardID := strings.TrimSuffix(strings.TrimSpace(in.CardID), ".md")
	status := strings.ToLower(strings.TrimSpace(in.Status))
	reason := strings.TrimSpace(in.Reason)
	duplicateOf := strings.TrimSuffix(strings.TrimSpace(in.DuplicateOf), ".md")
	agentID := strings.TrimSpace(in.AgentID)
	if cardID == "" || reason == "" || !validTaskCloseStatus(status) {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, "task_close: cardID, a valid status and reason are required")
	}
	if status == "duplicate" && (duplicateOf == "" || duplicateOf == cardID) {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, "task_close: duplicateOf must name a different existing card")
	}

	lockPath := s.lockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()

	cards, err := s.cards()
	if err != nil {
		return TaskCloseOutput{}, err
	}
	var chosen closeCard
	found := false
	for _, c := range cards {
		if c.ID == cardID {
			chosen, found = c, true
			break
		}
	}
	if !found {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("task_close: card %s not found", cardID))
	}

	ledger, err := readCloseLedger(s.ledgerPath())
	if err != nil {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	if rec, ok := ledger.Cards[cardID]; ok {
		if rec.Status != status || rec.Reason != reason {
			return TaskCloseOutput{}, closeConflict(fmt.Sprintf(
				"task_close: card %s was already closed as %s with reason %q", cardID, rec.Status, rec.Reason))
		}
		// A matching replay is ok. Re-apply the status when an earlier call
		// wrote the ledger but failed before the card rename.
		if chosen.Status != status {
			if err := s.writeStatus(chosen, status); err != nil {
				return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
			}
		}
		return TaskCloseOutput{OK: true, CardID: cardID, Status: status}, nil
	}

	if status == "duplicate" {
		targetExists := false
		for _, c := range cards {
			if c.ID == duplicateOf {
				targetExists = true
				break
			}
		}
		if !targetExists {
			return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("task_close: duplicateOf card %s not found", duplicateOf))
		}
	}
	if chosen.Status == "in_progress" && chosen.Agent != "" && chosen.Agent != agentID {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf(
			"task_close: card %s is in_progress by %s, not %s", cardID, chosen.Agent, agentID))
	}
	if taskCloseTerminal(chosen.Status) {
		return TaskCloseOutput{}, closeConflict(fmt.Sprintf(
			"task_close: card %s is already %s", cardID, chosen.Status))
	}

	rec := closeRecord{
		Status:      status,
		Reason:      reason,
		DuplicateOf: duplicateOf,
		AgentID:     agentID,
		ClosedAt:    strings.TrimSpace(closedAt),
	}
	ledger.Cards[cardID] = rec
	if err := writeCloseLedger(s.ledgerPath(), ledger); err != nil {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	if err := s.writeStatus(chosen, status); err != nil {
		return TaskCloseOutput{}, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	return TaskCloseOutput{OK: true, CardID: cardID, Status: status}, nil
}

// writeStatus rewrites only the Status line and keeps the rest byte for byte,
// including CRLF endings, then replaces the card in one atomic rename.
func (s *FileTaskCloseStore) writeStatus(card closeCard, status string) error {
	return writeCloseFile(card.Path, []byte(replaceCloseStatus(card.Raw, status)))
}

// cards reads and parses every card, skipping malformed files. The result is
// sorted by id so the existence checks are deterministic.
func (s *FileTaskCloseStore) cards() ([]closeCard, error) {
	entries, err := os.ReadDir(s.TasksDir)
	if err != nil {
		return nil, domain.NewAppError(domain.CodePersistFailed, err.Error())
	}
	cards := make([]closeCard, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(s.TasksDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		c, err := parseCloseCard(path, data)
		if err != nil {
			continue
		}
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	return cards, nil
}

func (s *FileTaskCloseStore) lockPath() string {
	return filepath.Join(s.stateDir(), ".beacon-task-close.lock")
}

func (s *FileTaskCloseStore) ledgerPath() string {
	return filepath.Join(s.stateDir(), ".beacon-task-close.json")
}

func (s *FileTaskCloseStore) stateDir() string {
	dir := strings.TrimSpace(s.StateDir)
	if dir == "" {
		return s.TasksDir
	}
	return dir
}

// parseCloseCard reads the two fields the ownership check needs. A malformed
// card is skipped by the lister and reported as missing when pinned.
func parseCloseCard(filePath string, data []byte) (closeCard, error) {
	id := strings.TrimSuffix(filepath.Base(filePath), ".md")
	if !closeCardIDRE.MatchString(id) {
		return closeCard{}, fmt.Errorf("task_close: unsupported card id %q", id)
	}
	c := closeCard{ID: id, Path: filePath, Raw: string(data)}
	for _, line := range strings.Split(c.Raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != strings.TrimLeft(line, " \t") {
			continue
		}
		fm := closeFieldLineRE.FindStringSubmatch(line)
		if fm == nil {
			continue
		}
		switch fm[1] {
		case "Статус":
			c.Status = strings.TrimSpace(fm[2])
		case "Агент":
			c.Agent = strings.TrimSpace(fm[2])
		}
	}
	if c.Status == "" {
		return closeCard{}, fmt.Errorf("task_close: card %s has no status", id)
	}
	return c, nil
}

// replaceCloseStatus rewrites only the Status line of a card.
func replaceCloseStatus(raw, status string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			line = strings.TrimSuffix(line, "\r")
		}
		if closeStatusRE.MatchString(line) {
			lines[i] = "Статус: " + status + cr
		}
	}
	return strings.Join(lines, "\n")
}

// readCloseLedger loads the sidecar ledger. A missing file is an empty ledger.
func readCloseLedger(path string) (closeLedger, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return closeLedger{Cards: map[string]closeRecord{}}, nil
	}
	if err != nil {
		return closeLedger{}, err
	}
	var ledger closeLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return closeLedger{}, err
	}
	if ledger.Cards == nil {
		ledger.Cards = map[string]closeRecord{}
	}
	return ledger, nil
}

func writeCloseLedger(path string, ledger closeLedger) error {
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return writeCloseFile(path, append(data, '\n'))
}

// closeConflict has the 409 status of other conflicts. It uses the duplicate
// code so callers can branch on domain.IsRejectDuplicate.
func closeConflict(message string) error {
	return &domain.AppError{Code: domain.CodeRejectDuplicate, Message: message, Status: 409}
}

// writeCloseFile replaces a file in one rename so a reader sees either the old
// or the new version and never a half-written one.
func writeCloseFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".task-close-*")
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
