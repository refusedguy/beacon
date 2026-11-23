// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_task_close. An agent closes one named card with
// a terminal status and a short reason. The call is idempotent by the close
// decision (cardID, status, reason, duplicateOf, agentID): a repeat with the
// same values returns ok without another write, and a repeat that changes any
// of them is refused with a machine-readable conflict. A field missing from a
// partial close is filled in, so a retry after an interrupted write succeeds.
// A card held in_progress by another agent can only be closed by that agent.
//
// The store is a port so the handler stays testable. FileTaskCloseStore is the
// stdlib-only adapter: it reads markdown cards, holds the shared tasks flock
// over the check and the write, and writes the same six close fields that
// taskstore.CloseCard writes. AUD-05 removed the sidecar ledger so the card is
// the only source of truth: the idempotency key is read from the .md fields
// instead of a separate JSON file.
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
// for both a fresh close and an idempotent repeat. Created is true only for the
// first close: a repeat reports false even when it fills fields left missing by
// an interrupted write. Callers that ignore Created keep the previous contract.
type TaskCloseOutput struct {
	OK      bool   `json:"ok"`
	CardID  string `json:"cardID"`
	Status  string `json:"status"`
	Created bool   `json:"created"`
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
func (h *TaskCloseHandler) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in TaskCloseInput) (*mcpsdk.CallToolResult, TaskCloseOutput, error) {
	return typedTool(h.Close, false)(ctx, req, in)
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
// TasksDir holds the cards and the shared lock. StateDir is kept for wiring
// compatibility; close no longer keeps a sidecar there.
type FileTaskCloseStore struct {
	TasksDir string
	StateDir string
}

// closeCard is the subset of a card the tool reads. Raw is kept so the close
// write changes nothing else in the file. The set flags say which close fields
// the card actually has, so a partially written ledger can be completed on a
// repeat instead of being treated as a conflict.
type closeCard struct {
	ID             string
	Path           string
	Raw            string
	Status         string
	Agent          string // in_progress owner from Агент, distinct from Закрыл
	Reason         string
	ReasonSet      bool
	DuplicateOf    string
	DuplicateOfSet bool
	ClosedBy       string
	ClosedBySet    bool
	ClosedAt       string
	ClosedAtSet    bool
}

// closeRecord holds the close fields written to the card in the same order as
// taskstore.CloseCard: Статус, Блокировщик, Причина, Дубликат, Закрыл,
// Время закрытия. The idempotency key is Status, Reason, DuplicateOf and
// AgentID. ClosedAt is a timestamp stamped by the caller, not part of the key.
type closeRecord struct {
	Status      string
	Reason      string
	DuplicateOf string
	AgentID     string
	ClosedAt    string
}

var (
	closeCardIDRE    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(?:-[A-Za-z]+[0-9]+|[.-][0-9]+)$`)
	closeFieldLineRE = regexp.MustCompile(`^([\p{L}][\p{L}\p{N} ]{0,40}):[ \t]*(.*)$`)
)

// closeManagedFields is the close format of taskstore/close.go. The order and
// the names must stay identical so a card closed through MCP is
// indistinguishable from a card closed by taskstore.CloseCard.
var closeManagedFields = []string{
	"Статус", "Блокировщик", "Причина", "Дубликат", "Закрыл", "Время закрытия",
}

// Close takes the shared tasks flock first, then reads and writes the card.
// The lock makes the existence, ownership and idempotency checks authoritative
// against a concurrent claim, close or taskstore writer.
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
		return TaskCloseOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return TaskCloseOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return TaskCloseOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
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

	// The card is the only ledger: its close fields carry the idempotency key,
	// so a close made through taskstore and one made here share it.
	if taskCloseTerminal(chosen.Status) {
		if closeReplayDiffers(chosen, status, reason, duplicateOf, agentID) {
			return TaskCloseOutput{}, closeConflict(fmt.Sprintf(
				"task_close: card %s was already closed as %s with reason %q, duplicateOf %q, by %q",
				cardID, chosen.Status, chosen.Reason, chosen.DuplicateOf, chosen.ClosedBy))
		}
		// A matching replay stays ok=true. A close interrupted between writing
		// the fields and renaming the file can leave some of them missing, so
		// fill the gaps. Keep the recorded time when present: a retry must not
		// move it.
		rec := closeRecord{
			Status:      status,
			Reason:      reason,
			DuplicateOf: duplicateOf,
			AgentID:     agentID,
			ClosedAt:    strings.TrimSpace(closedAt),
		}
		if chosen.ClosedAtSet {
			rec.ClosedAt = chosen.ClosedAt
		}
		if closeReplayNeedsFill(chosen, rec) {
			if err := s.writeCloseFields(chosen, rec); err != nil {
				return TaskCloseOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
			}
		}
		// The card was already closed, even when this call filled missing
		// fields. Report a repeat so the caller cannot mistake a repair for a
		// new close.
		return TaskCloseOutput{OK: true, CardID: cardID, Status: status, Created: false}, nil
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

	rec := closeRecord{
		Status:      status,
		Reason:      reason,
		DuplicateOf: duplicateOf,
		AgentID:     agentID,
		ClosedAt:    strings.TrimSpace(closedAt),
	}
	if err := s.writeCloseFields(chosen, rec); err != nil {
		return TaskCloseOutput{}, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}
	return TaskCloseOutput{OK: true, CardID: cardID, Status: status, Created: true}, nil
}

// writeCloseFields writes all close fields in one atomic rename, under the same
// shared lock that claim and taskstore hold.
func (s *FileTaskCloseStore) writeCloseFields(card closeCard, rec closeRecord) error {
	return writeCloseFile(card.Path, []byte(applyCloseFields(card.Raw, rec)))
}

// applyCloseFields mirrors taskstore.ApplyCloseFields so both writers produce
// identical card fields. Existing lines are replaced in place, missing fields
// are inserted before the first "## " section, and empty values drop the line.
func applyCloseFields(raw string, rec closeRecord) string {
	values := map[string]string{
		"Статус":         rec.Status,
		"Блокировщик":    "",
		"Причина":        rec.Reason,
		"Дубликат":       rec.DuplicateOf,
		"Закрыл":         rec.AgentID,
		"Время закрытия": rec.ClosedAt,
	}

	cr := ""
	if strings.Contains(raw, "\r\n") {
		cr = "\r"
	}
	lines := strings.Split(raw, "\n")
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(lines)+len(closeManagedFields))
	insertAt := -1
	for _, line := range lines {
		if insertAt < 0 && strings.HasPrefix(line, "## ") {
			insertAt = len(out)
		}
		lineCR := ""
		body := line
		if strings.HasSuffix(line, "\r") {
			lineCR = "\r"
			body = strings.TrimSuffix(line, "\r")
		}
		key, known := closeManagedKey(body)
		if !known {
			out = append(out, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		value := values[key]
		if value == "" {
			continue
		}
		out = append(out, key+": "+value+lineCR)
	}

	missing := make([]string, 0, len(closeManagedFields))
	for _, key := range closeManagedFields {
		if !seen[key] && values[key] != "" {
			missing = append(missing, key+": "+values[key]+cr)
		}
	}
	if len(missing) > 0 {
		if insertAt < 0 {
			insertAt = len(out)
		}
		tail := append([]string(nil), out[insertAt:]...)
		out = append(out[:insertAt:insertAt], missing...)
		out = append(out, tail...)
	}
	return strings.Join(out, "\n")
}

// closeManagedKey reports whether a line is one of the managed close fields.
func closeManagedKey(line string) (string, bool) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return "", false
	}
	key := strings.TrimSpace(line[:colon])
	for _, managed := range closeManagedFields {
		if key == managed {
			return managed, true
		}
	}
	return "", false
}

// closeReplayDiffers reports whether a replay changes any field of the close
// decision. A field absent from a partial ledger is unset, so the replay may
// fill it rather than conflict.
func closeReplayDiffers(c closeCard, status, reason, duplicateOf, agentID string) bool {
	return c.Status != status ||
		(c.ReasonSet && c.Reason != reason) ||
		(c.DuplicateOfSet && c.DuplicateOf != duplicateOf) ||
		(c.ClosedBySet && c.ClosedBy != agentID)
}

// closeReplayNeedsFill reports whether the card lacks a field that the replay
// would write. A complete card is left untouched so its close time does not
// move on an idempotent repeat.
func closeReplayNeedsFill(c closeCard, rec closeRecord) bool {
	return (rec.Reason != "" && !c.ReasonSet) ||
		(rec.DuplicateOf != "" && !c.DuplicateOfSet) ||
		(rec.AgentID != "" && !c.ClosedBySet) ||
		(rec.ClosedAt != "" && !c.ClosedAtSet)
}

// cards reads and parses every card, skipping malformed files. The result is
// sorted by id so the existence checks are deterministic.
func (s *FileTaskCloseStore) cards() ([]closeCard, error) {
	entries, err := os.ReadDir(s.TasksDir)
	if err != nil {
		return nil, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
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

// lockPath is the same path claim and taskstore use; StateDir no longer holds
// a separate close lock.
func (s *FileTaskCloseStore) lockPath() string {
	return TasksLockPath(s.TasksDir)
}

// parseCloseCard reads the status, owner and close fields the check needs.
// A malformed card is skipped by the lister and reported as missing when
// pinned. Presence flags separate an absent field from an empty one.
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
		case "Причина":
			c.Reason = strings.TrimSpace(fm[2])
			c.ReasonSet = true
		case "Дубликат":
			c.DuplicateOf = strings.TrimSpace(fm[2])
			c.DuplicateOfSet = true
		case "Закрыл":
			c.ClosedBy = strings.TrimSpace(fm[2])
			c.ClosedBySet = true
		case "Время закрытия":
			c.ClosedAt = strings.TrimSpace(fm[2])
			c.ClosedAtSet = true
		}
	}
	if c.Status == "" {
		return closeCard{}, fmt.Errorf("task_close: card %s has no status", id)
	}
	return c, nil
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
