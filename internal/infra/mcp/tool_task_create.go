package mcp

import (
	"context"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// CreateTaskArgs is the input schema for beacon_task_create.
type CreateTaskArgs struct {
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Scope    []string `json:"scope"`
	Depends  []string `json:"depends"`
	Priority int      `json:"priority"`
}

var taskCreateTool = &msdk.Tool{
	Name:        "beacon_task_create",
	Description: "Create and publish a task card. Orchestrator only.",
}

func registerTaskCreate(s *msdk.Server, sv Services) {
	msdk.AddTool(s, taskCreateTool, func(ctx context.Context, _ *msdk.CallToolRequest, a CreateTaskArgs) (*msdk.CallToolResult, any, error) {
		if sv.TaskCreator == nil {
			return nil, nil, core.Errorf(core.CodeUnavailable, "task creator is not configured")
		}
		created, err := sv.TaskCreator.Create(ctx, app.CreateTaskInput{
			Title: a.Title, Body: a.Body, Scope: a.Scope, Depends: a.Depends, Priority: a.Priority,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(created), nil, nil
	})
}
