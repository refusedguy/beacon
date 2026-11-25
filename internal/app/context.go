package app

import (
	"context"

	"github.com/refusedguy/beacon/pkg/core"
)

type parentSessionKey struct{}

// WithParentSession carries the orchestrator session ID through the cycle.
func WithParentSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, parentSessionKey{}, id)
}

func parentSession(ctx context.Context) string {
	if v, ok := ctx.Value(parentSessionKey{}).(string); ok {
		return v
	}
	return ""
}

type launchKey struct{}

type launchInfo struct {
	source core.LaunchSource
	agent  string
}

// WithLaunch carries the caller path and agent name from a command or tool to
// the dispatcher, so the first Save persists them instead of a post-hoc update.
func WithLaunch(ctx context.Context, source core.LaunchSource, agent string) context.Context {
	return context.WithValue(ctx, launchKey{}, launchInfo{source: source, agent: agent})
}

func launchFrom(ctx context.Context) (core.LaunchSource, string) {
	if v, ok := ctx.Value(launchKey{}).(launchInfo); ok {
		return v.source, v.agent
	}
	return core.LaunchDaemon, ""
}

type cycleIDKey struct{}

// WithCycleID attaches the run ID that correlates decision logs in one cycle.
func WithCycleID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, cycleIDKey{}, id)
}

func cycleID(ctx context.Context) string {
	if v, ok := ctx.Value(cycleIDKey{}).(string); ok {
		return v
	}
	return ""
}
