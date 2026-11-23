package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// PathCommitter is the part of the workspace port that publishes a new card.
type PathCommitter interface {
	CommitPath(ctx context.Context, ws core.Workspace, pathspec, message string) error
}

type CreateTaskInput struct {
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Scope    []string `json:"scope"`
	Depends  []string `json:"depends"`
	Priority int      `json:"priority"`
}

type CreatedTask struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Pushed bool   `json:"pushed"`
}

// TaskCreator writes a task card through the source, then commits and pushes
// docs/tasks so the next dispatch sees the card.
type TaskCreator struct {
	Source    TaskSource
	Workspace PathCommitter
	Root      core.Workspace
}

func (c *TaskCreator) Create(ctx context.Context, in CreateTaskInput) (CreatedTask, error) {
	var out CreatedTask
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return out, core.Errorf(core.CodeInvalidInput, "task title is required")
	}
	id, err := newTaskID()
	if err != nil {
		return out, err
	}
	task := core.Task{
		ID:        id,
		Title:     title,
		Body:      strings.TrimSpace(in.Body),
		Scope:     taskScopes(in.Scope),
		DependsOn: taskDependencies(in.Depends),
		Priority:  in.Priority,
		Status:    core.TaskAvailable,
	}
	created, err := c.Source.Create(ctx, task)
	if err != nil {
		return out, err
	}
	if err := c.Workspace.CommitPath(ctx, c.Root, "docs/tasks", "beacon: create task "+string(created.ID)); err != nil {
		return out, err
	}
	return CreatedTask{ID: string(created.ID), Path: created.SourceRef, Pushed: true}, nil
}

func taskScopes(values []string) []core.ScopePath {
	var out []core.ScopePath
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
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

func taskDependencies(values []string) []core.TaskID {
	var out []core.TaskID
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, core.TaskID(value))
		}
	}
	return out
}

func newTaskID() (core.TaskID, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	return core.TaskID("T-" + time.Now().UTC().Format("20060102") + "-" + hex.EncodeToString(b[:])), nil
}
