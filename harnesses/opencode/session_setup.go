package opencode

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/refusedguy/beacon/pkg/core"
)

// applyModel sends the model chosen for a spawn. It stays on the critical path
// because callers observe the model switch as soon as Spawn returns to confirm
// a config reload took effect. The caller passes the resolved model so setup
// does not resolve it again.
func (h *Harness) applyModel(ctx context.Context, id string, ref core.ModelRef) error {
	if ref.IsZero() {
		return nil
	}
	_, status, err := h.do(ctx, http.MethodPost, "/api/session/"+url.PathEscape(id)+"/model", map[string]any{
		"model": ref,
	})
	if err != nil && status != http.StatusNotFound && status != http.StatusConflict {
		return err
	}
	return nil
}

// moveSession sends the workspace chosen for a spawn. A 404 or 409 is accepted
// because the create request already carried the directory.
func (h *Harness) moveSession(ctx context.Context, id string, req core.SpawnRequest) error {
	if req.Workspace.Path == "" {
		return nil
	}
	_, status, err := h.do(ctx, http.MethodPost, "/api/session/"+url.PathEscape(id)+"/move", map[string]any{
		"directory": req.Workspace.Path,
	})
	if err != nil && status != http.StatusNotFound && status != http.StatusConflict {
		return err
	}
	return nil
}

// deferSessionSetup queues the move follow-up after the spawn tool returns.
// The create request already carries the directory, so the move only repeats a
// value and must not hold up dispatch. The detached context keeps it running
// after the caller's request context is cancelled; do still bounds the request
// with requestTimeout. The 404 and 409 tolerance is preserved.
func (h *Harness) deferSessionSetup(ctx context.Context, id string, req core.SpawnRequest) {
	setupCtx := context.WithoutCancel(ctx)
	h.setupWG.Add(1)
	go func() {
		defer h.setupWG.Done()
		if err := h.moveSession(setupCtx, id, req); err != nil {
			slog.Warn("deferred session move failed", "session", id, "error", err)
		}
	}()
}

// waitForSetup blocks until every deferred session setup call finishes. Tests
// use it to observe the follow-up calls deterministically.
func (h *Harness) waitForSetup() {
	h.setupWG.Wait()
}
