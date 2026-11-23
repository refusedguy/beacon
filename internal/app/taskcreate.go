package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// PathCommitter is the part of the workspace port that publishes a new card.
type PathCommitter interface {
	CommitPath(ctx context.Context, ws core.Workspace, pathspec, message string) error
}

// BaseRebaser is an optional workspace capability. GitWorktree implements it
// so publishing a card syncs a stale local base with origin before pushing.
// Fakes that implement it let tests observe the rebase and the push retry.
type BaseRebaser interface {
	RebaseBase(ctx context.Context, ws core.Workspace, base string) error
}

// NonFastForwardError marks a push rejected because the remote ref advanced.
// A caller retries the publish once after rebasing on the latest origin base.
type NonFastForwardError struct {
	Err error
}

func (e *NonFastForwardError) Error() string {
	if e.Err == nil {
		return "push rejected as non-fast-forward"
	}
	return "push rejected as non-fast-forward: " + e.Err.Error()
}

func (e *NonFastForwardError) Unwrap() error { return e.Err }

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
	base := c.Root.Base
	if base == "" {
		base = c.Root.Branch
	}
	if base == "" {
		base = "main"
	}
	publish := func() error {
		return c.Workspace.CommitPath(ctx, c.Root, "docs/tasks", "beacon: create task "+string(created.ID))
	}
	// Bring the local base up to date before committing. The rebase is a no-op
	// when local already matches origin, so repeated calls stay harmless.
	if err := c.rebaseBase(ctx, base); err != nil {
		return out, err
	}
	if err := publish(); err != nil {
		var nff *NonFastForwardError
		if !errors.As(err, &nff) {
			return out, err
		}
		// Another card reached origin between the rebase and the push. Sync
		// once more and retry. CommitPath does not recommit a card that is
		// already committed, so the retry does not create a second commit.
		if err := c.rebaseBase(ctx, base); err != nil {
			return out, err
		}
		if err := publish(); err != nil {
			return out, err
		}
	}
	return CreatedTask{ID: string(created.ID), Path: created.SourceRef, Pushed: true}, nil
}

func (c *TaskCreator) rebaseBase(ctx context.Context, base string) error {
	rebaser, ok := c.Workspace.(BaseRebaser)
	if !ok {
		return nil
	}
	return rebaser.RebaseBase(ctx, c.Root, base)
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
