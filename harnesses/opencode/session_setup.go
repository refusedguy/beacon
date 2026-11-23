package opencode

import (
	"context"
	"net/http"
	"net/url"

	"github.com/refusedguy/beacon/pkg/core"
)

func (h *Harness) configureSession(ctx context.Context, id string, req core.SpawnRequest) error {
	base := "/api/session/" + url.PathEscape(id)
	if req.Workspace.Path != "" {
		_, status, err := h.do(ctx, http.MethodPost, base+"/move", map[string]any{
			"directory": req.Workspace.Path,
		})
		if err != nil && status != http.StatusNotFound && status != http.StatusConflict {
			return err
		}
	}
	if len(h.ModelRef) > 0 {
		_, status, err := h.do(ctx, http.MethodPost, base+"/model", map[string]any{
			"model": h.ModelRef,
		})
		if err != nil && status != http.StatusNotFound && status != http.StatusConflict {
			return err
		}
	}
	return nil
}
