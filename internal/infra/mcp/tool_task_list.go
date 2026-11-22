// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_task_list, a read-only projection of the card
// store behind beacon_task_claim. The orchestrator calls it once to get the
// free cards it may spawn into, instead of walking markdown and GitHub pull
// requests itself. The tool makes no network call and writes no card state.
package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// TaskListToolName is the name AttachTaskList registers.
const TaskListToolName = "beacon_task_list"

// taskListDescription is shared by the server descriptor and the attach
// function, so tools/list cannot drift from the registered tool.
const taskListDescription = "List free task cards available for an agent to claim. Returns todo cards with no agent, ordered by phase then id, without changing card state."

// defaultTaskListLimit keeps one call small enough for a spawn wave.
const defaultTaskListLimit = 12

// taskListGoalRunes is the goal preview length. Goals are Russian text, so the
// cut counts runes rather than bytes to avoid splitting a UTF-8 sequence.
const taskListGoalRunes = 120

// TaskListInput is the argument object of beacon_task_list. Every field is
// optional. The caller passes excludeOwnerFiles from its own active watches
// and pull requests; the tool never queries GitHub.
type TaskListInput struct {
	Phase             string   `json:"phase,omitempty" jsonschema:"optional phase filter, P0 through P8"`
	Limit             int      `json:"limit,omitempty" jsonschema:"optional maximum cards to return; defaults to 12"`
	ExcludeOwnerFiles []string `json:"excludeOwnerFiles,omitempty" jsonschema:"optional owner file paths already held by active watches or pull requests"`
}

// TaskListCard is one spawnable card. Agent is empty for every returned card;
// the field stays in the contract so a caller can assert the filter.
type TaskListCard struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Phase      string   `json:"phase"`
	Status     string   `json:"status"`
	OwnerFiles []string `json:"ownerFiles"`
	Goal       string   `json:"goal"`
	Agent      string   `json:"agent"`
}

// TaskListOutput is the response of beacon_task_list. Truncated reports that
// more free cards matched than the caller's limit allowed.
type TaskListOutput struct {
	Cards     []TaskListCard `json:"cards"`
	Truncated bool           `json:"truncated,omitempty"`
}

// TaskListStore is the read side of the card store. Available returns every
// matching free card in deterministic order; the handler applies the limit.
type TaskListStore interface {
	Available(ctx context.Context, in TaskListInput) ([]TaskListCard, error)
}

// ClaimStoreTaskLister adapts the claim store to TaskListStore. It reads the
// same card directory under the same lock as beacon_task_claim and reuses its
// parser, so the two tools cannot disagree about what a free card is.
type ClaimStoreTaskLister struct {
	Claim *FileTaskClaimStore
}

// Available projects the claim store's cards into list rows. It applies the
// phase, agent and owner-file filters, then sorts by phase and id.
func (s *ClaimStoreTaskLister) Available(ctx context.Context, in TaskListInput) ([]TaskListCard, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.Claim == nil {
		return nil, domain.NewAppError(domain.CodePersistFailed, "task_list: store is nil")
	}
	cards, err := s.Claim.cards()
	if err != nil {
		return nil, err
	}

	phase := strings.TrimSpace(in.Phase)
	excluded := make(map[string]bool, len(in.ExcludeOwnerFiles))
	for _, path := range in.ExcludeOwnerFiles {
		if path = strings.TrimSpace(path); path != "" {
			excluded[path] = true
		}
	}

	out := make([]TaskListCard, 0, len(cards))
	for _, c := range cards {
		if !claimStatusAvailable(c.Status) {
			continue
		}
		if agent := strings.TrimSpace(c.Agent); agent != "" && !strings.EqualFold(agent, "нет") {
			continue
		}
		if phase != "" && strings.TrimSpace(c.Phase) != phase {
			continue
		}
		if ownerTaken(c.OwnerFiles, excluded) {
			continue
		}
		out = append(out, TaskListCard{
			ID:         c.ID,
			Title:      c.Title,
			Phase:      c.Phase,
			Status:     c.Status,
			OwnerFiles: append([]string(nil), c.OwnerFiles...),
			Goal:       shortTaskGoal(c.Goal),
			Agent:      "",
		})
	}

	sort.Slice(out, func(i, j int) bool {
		left, right := claimPhaseRank(out[i].Phase), claimPhaseRank(out[j].Phase)
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ownerTaken reports whether a card owns any path the caller already holds.
func ownerTaken(files []string, excluded map[string]bool) bool {
	for _, file := range files {
		if excluded[file] {
			return true
		}
	}
	return false
}

// shortTaskGoal cuts a goal to the preview length without splitting a rune.
func shortTaskGoal(goal string) string {
	runes := []rune(goal)
	if len(runes) <= taskListGoalRunes {
		return goal
	}
	return string(runes[:taskListGoalRunes])
}

// TaskListHandler validates the input, asks the store for free cards and
// applies the limit. It owns no card state.
type TaskListHandler struct {
	store TaskListStore
}

// NewTaskListHandler builds a handler over the given store. A nil store is
// kept so the tool stays registered and reports PERSIST_FAILED until a
// composition root supplies one.
func NewTaskListHandler(store TaskListStore) *TaskListHandler {
	return &TaskListHandler{store: store}
}

// Handle adapts Available to the go-sdk typed tool handler signature.
func (h *TaskListHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in TaskListInput) (*mcpsdk.CallToolResult, TaskListOutput, error) {
	out, err := h.List(ctx, in)
	if err != nil {
		return nil, TaskListOutput{}, err
	}
	return nil, out, nil
}

// List validates the phase and limit, then returns at most limit cards. A
// non-positive limit means the default, so an omitted field and an explicit
// zero behave alike.
func (h *TaskListHandler) List(ctx context.Context, in TaskListInput) (TaskListOutput, error) {
	if h == nil || h.store == nil {
		return TaskListOutput{}, domain.NewAppError(domain.CodePersistFailed, "task_list: store is nil")
	}
	in.Phase = strings.TrimSpace(in.Phase)
	if in.Phase != "" && !validClaimPhase(in.Phase) {
		return TaskListOutput{}, domain.NewAppError(domain.CodeInvalidInput, fmt.Sprintf("task_list: unknown phase %q", in.Phase))
	}
	if in.Limit <= 0 {
		in.Limit = defaultTaskListLimit
	}

	cards, err := h.store.Available(ctx, in)
	if err != nil {
		return TaskListOutput{}, err
	}
	out := TaskListOutput{Cards: cards}
	if len(cards) > in.Limit {
		out.Cards = cards[:in.Limit]
		out.Truncated = true
	}
	return out, nil
}

// AttachTaskList registers exactly one beacon_task_list tool. The output
// schema comes from AllOutputSchemas, so go-sdk never infers one from the
// handler type. A nil schema keeps the real handler registered.
func AttachTaskList(server *mcpsdk.Server, handler *TaskListHandler, outputSchema *jsonschema.Schema) {
	if server == nil || handler == nil {
		return
	}
	if outputSchema == nil {
		outputSchema = &jsonschema.Schema{Type: "object"}
	}
	annotations := toolAnnotations[TaskListToolName]
	server.RemoveTools(TaskListToolName)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         TaskListToolName,
		Description:  taskListDescription,
		OutputSchema: outputSchema,
		Annotations:  &annotations,
	}, handler.Handle)
}
