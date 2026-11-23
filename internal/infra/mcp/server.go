// Package mcp exposes worker and orchestrator tools to agents over MCP.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// NewServer builds the MCP server and registers every tool in the catalog.
func NewServer(sv Services) *msdk.Server {
	s := msdk.NewServer(&msdk.Implementation{Name: "beacon", Version: "v1.0.0"}, nil)
	for _, register := range toolRegistrations {
		register(s, sv)
	}
	// The catalog keeps one task-create tool. Replace its basic registration
	// with one that also accepts the epic fields, so a single call expands a
	// plan into child cards.
	s.RemoveTools(taskCreateTool.Name)
	msdk.AddTool(s, taskCreateTool, func(ctx context.Context, _ *msdk.CallToolRequest, a epicCreateArgs) (*msdk.CallToolResult, any, error) {
		if sv.TaskCreator == nil {
			return nil, nil, core.Errorf(core.CodeUnavailable, "task creator is not configured")
		}
		plan := make([]core.ChildSpec, 0, len(a.Plan))
		for _, spec := range a.Plan {
			plan = append(plan, core.ChildSpec{
				Title: spec.Title, Scope: specScopes(spec.Scope),
				Depends: specDeps(spec.Depends), Priority: spec.Priority,
			})
		}
		created, err := sv.TaskCreator.Create(ctx, app.CreateTaskInput{
			Title: a.Title, Body: a.Body, Scope: a.Scope, Depends: a.Depends, Priority: a.Priority,
			ID: a.ID, Kind: core.TaskKind(a.Kind), BaseBranch: a.BaseBranch,
			RequireReview: core.ReviewMode(a.RequireReview), Plan: plan,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(created), nil, nil
	})
	return s
}

// epicCreateArgs is the input schema of beacon_task_create. It extends the
// basic create arguments with the fields an epic card needs.
type epicCreateArgs struct {
	Title         string         `json:"title"`
	Body          string         `json:"body,omitempty"`
	Scope         []string       `json:"scope,omitempty"`
	Depends       []string       `json:"depends,omitempty"`
	Priority      int            `json:"priority,omitempty"`
	ID            string         `json:"id,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	BaseBranch    string         `json:"base_branch,omitempty"`
	RequireReview string         `json:"require_review,omitempty"`
	Plan          []epicChildArg `json:"plan,omitempty"`
}

type epicChildArg struct {
	Title    string   `json:"title"`
	Scope    []string `json:"scope,omitempty"`
	Depends  []string `json:"depends,omitempty"`
	Priority int      `json:"priority,omitempty"`
}

func specScopes(values []string) []core.ScopePath {
	out := make([]core.ScopePath, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		switch {
		case strings.ContainsAny(value, "*?"):
			out = append(out, core.ScopePattern(value))
		case strings.HasSuffix(value, "/"):
			out = append(out, core.ScopeDir(value))
		default:
			out = append(out, core.ScopeFile(value))
		}
	}
	return out
}

func specDeps(values []string) []core.TaskID {
	out := make([]core.TaskID, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, core.TaskID(value))
		}
	}
	return out
}

func textResult(s string) *msdk.CallToolResult {
	return &msdk.CallToolResult{Content: []msdk.Content{&msdk.TextContent{Text: s}}}
}

func jsonResult(v any) *msdk.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return textResult(fmt.Sprintf("{\"error\":%q}", err.Error()))
	}
	return textResult(string(b))
}

// ServeStdio connects the server to a stdio transport and blocks until the session ends.
func ServeStdio(s *msdk.Server) error {
	sess, err := s.Connect(context.Background(), &msdk.StdioTransport{}, nil)
	if err != nil {
		return err
	}
	return sess.Wait()
}
