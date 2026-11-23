package app

import "context"

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
