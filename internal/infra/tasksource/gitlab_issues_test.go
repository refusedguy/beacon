package tasksource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func glIssueWriteJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func glIssueServer(t *testing.T) *GitLabIssues {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/issues") && r.Method == http.MethodGet:
			if !strings.Contains(r.URL.EscapedPath(), "o%2Fr") {
				t.Errorf("project id not escaped: %s", r.URL.EscapedPath())
			}
			if r.URL.Query().Get("state") != "opened" || r.URL.Query().Get("per_page") != "100" {
				t.Errorf("query = %v", r.URL.Query())
			}
			glIssueWriteJSON(t, w, []map[string]any{
				{"iid": 8, "title": "Claimed", "labels": []string{"beacon/claimed"}, "description": "Scope: skip.go"},
				{"iid": 9, "title": "Field task", "description": "Scope: internal/a.go, internal/b.go\nDepends: 3\nPriority: 7\n", "web_url": "https://gitlab.example/o/r/-/issues/9"},
				{"iid": 10, "title": "YAML task", "description": "```yaml\nscope:\n  - internal/c/\n  - internal/*.go\ndepends:\n  - GL-9\npriority: 2\n```\n"},
			})
		case strings.HasSuffix(p, "/issues/9/labels"):
			if r.Method != http.MethodPost {
				t.Errorf("claim method = %s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["labels"] != "beacon/claimed" {
				t.Errorf("claim body = %v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"iid":9}`))
		case strings.HasSuffix(p, "/issues/9") && r.Method == http.MethodPut:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["state_event"] != "close" {
				t.Errorf("complete body = %v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"iid":9,"state":"closed"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return NewGitLabIssues(srv.URL+"/api/v4/", "tok", "o", "r")
}

func TestGitLabIssuesLifecycle(t *testing.T) {
	s := glIssueServer(t)
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].ID != "GL-9" || tasks[0].Title != "Field task" || tasks[0].Priority != 7 {
		t.Fatalf("first task = %+v", tasks[0])
	}
	if tasks[0].Status != core.TaskAvailable || tasks[0].SourceRef != "https://gitlab.example/o/r/-/issues/9" {
		t.Fatalf("first task status = %+v", tasks[0])
	}
	if len(tasks[0].Scope) != 2 || !tasks[0].Scope[0].CoversPath("internal/a.go") {
		t.Fatalf("scope = %v", tasks[0].Scope)
	}
	if len(tasks[0].DependsOn) != 1 || tasks[0].DependsOn[0] != "GL-3" {
		t.Fatalf("depends = %v", tasks[0].DependsOn)
	}
	if tasks[1].ID != "GL-10" || tasks[1].Priority != 2 {
		t.Fatalf("second task = %+v", tasks[1])
	}
	if len(tasks[1].Scope) != 2 || !tasks[1].Scope[0].CoversPath("internal/c/x.go") || !tasks[1].Scope[1].CoversPath("internal/z.go") {
		t.Fatalf("yaml scope = %v", tasks[1].Scope)
	}
	if len(tasks[1].DependsOn) != 1 || tasks[1].DependsOn[0] != "GL-9" {
		t.Fatalf("yaml depends = %v", tasks[1].DependsOn)
	}
	if err := s.Claim(context.Background(), "GL-9"); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(context.Background(), "GL-9"); err != nil {
		t.Fatal(err)
	}
}

func TestGitLabIssuesInvalidID(t *testing.T) {
	s := glIssueServer(t)
	if err := s.Claim(context.Background(), "bad"); core.CodeOf(err) != core.CodeInvalidInput {
		t.Fatalf("claim error = %v", err)
	}
	if err := s.Complete(context.Background(), "GH-9"); core.CodeOf(err) != core.CodeInvalidInput {
		t.Fatalf("complete error = %v", err)
	}
}
