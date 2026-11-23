package scm

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/refusedguy/beacon/pkg/core"
)

// OpenPR creates a merge request from head into base and returns its IID.
func (g *GitLab) OpenPR(ctx context.Context, head, base, title string) (int, error) {
	path := "/projects/" + g.project() + "/merge_requests"
	data, err := g.request(ctx, http.MethodPost, path, nil, map[string]string{
		"source_branch": head,
		"target_branch": base,
		"title":         title,
	})
	if err != nil {
		return 0, err
	}
	var mr glMR
	if err := json.Unmarshal(data, &mr); err != nil {
		return 0, err
	}
	if mr.IID == 0 {
		return 0, core.Errorf(core.CodeUnavailable, "open MR returned no iid")
	}
	return mr.IID, nil
}
