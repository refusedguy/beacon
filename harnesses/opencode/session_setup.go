package opencode

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/refusedguy/beacon/pkg/core"
)

// configureSession applies the workspace and model chosen for a spawn. The
// caller passes the resolved model so session setup does not resolve it again.
func (h *Harness) configureSession(ctx context.Context, id string, req core.SpawnRequest, ref core.ModelRef) error {
	base := "/api/session/" + url.PathEscape(id)
	if req.Workspace.Path != "" {
		_, status, err := h.do(ctx, http.MethodPost, base+"/move", map[string]any{
			"directory": req.Workspace.Path,
		})
		if err != nil && status != http.StatusNotFound && status != http.StatusConflict {
			return err
		}
	}
	if !ref.IsZero() {
		_, status, err := h.do(ctx, http.MethodPost, base+"/model", map[string]any{
			"model": ref,
		})
		if err != nil && status != http.StatusNotFound && status != http.StatusConflict {
			return err
		}
	}
	return nil
}

// deferSessionSetup queues the move and model follow-ups after the spawn tool
// returns. The session create already carries the directory and model, so
// these calls only repeat values and must not hold up dispatch. The detached
// context keeps them running after the caller's request context is cancelled;
// do still bounds each request with requestTimeout. The 404 and 409 tolerance
// from configureSession is preserved.
func (h *Harness) deferSessionSetup(ctx context.Context, id string, req core.SpawnRequest, ref core.ModelRef) {
	setupCtx := context.WithoutCancel(ctx)
	h.setupWG.Add(1)
	go func() {
		defer h.setupWG.Done()
		if err := h.configureSession(setupCtx, id, req, ref); err != nil {
			slog.Warn("deferred session setup failed", "session", id, "error", err)
		}
	}()
}

// waitForSetup blocks until every deferred session setup call finishes. Tests
// use it to observe the follow-up calls deterministically.
func (h *Harness) waitForSetup() {
	h.setupWG.Wait()
}
