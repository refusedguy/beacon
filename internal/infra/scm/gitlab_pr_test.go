package scm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestGitLabOpenPR(t *testing.T) {
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/merge_requests") {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if !strings.Contains(r.URL.EscapedPath(), "o%2Fr") {
			t.Errorf("project id not escaped: %s", r.URL.EscapedPath())
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["source_branch"] != "beacon/T-CHILD" || body["target_branch"] != "beacon/T-EPIC" || body["title"] != "Child work" {
			t.Errorf("body = %v", body)
		}
		glWriteJSON(t, w, map[string]any{"iid": 55})
	}))

	iid, err := g.OpenPR(context.Background(), "beacon/T-CHILD", "beacon/T-EPIC", "Child work")
	if err != nil {
		t.Fatal(err)
	}
	if iid != 55 {
		t.Fatalf("iid = %d", iid)
	}
}

func TestGitLabOpenPRConflict(t *testing.T) {
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		glWriteJSON(t, w, map[string]any{"message": "branch already has an open merge request"})
	}))
	_, err := g.OpenPR(context.Background(), "head", "main", "title")
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("error = %v", err)
	}
}
