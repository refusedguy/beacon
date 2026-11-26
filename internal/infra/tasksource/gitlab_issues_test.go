package tasksource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
			if r.URL.Query().Get("page") != "1" {
				glIssueWriteJSON(t, w, []map[string]any{})
				return
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

func TestGitLabIssuesPrefersBodyID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			glIssueWriteJSON(t, w, []map[string]any{})
			return
		}
		glIssueWriteJSON(t, w, []map[string]any{
			{"iid": 9, "title": "Body id", "description": sharedIssueBody, "web_url": "https://gitlab.example/o/r/-/issues/9"},
		})
	}))
	defer srv.Close()
	s := NewGitLabIssues(srv.URL+"/api/v4/", "tok", "o", "r")
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	assertSharedIssueFields(t, tasks[0], "GL-7")
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

func glPagedIssues(t *testing.T, page, total int) []map[string]any {
	t.Helper()
	start := (page - 1) * gitlabIssuesPerPage
	items := make([]map[string]any, 0, gitlabIssuesPerPage)
	for i := start; i < start+gitlabIssuesPerPage && i < total; i++ {
		items = append(items, map[string]any{
			"iid":   i + 1,
			"title": strconv.Itoa(i + 1),
		})
	}
	return items
}

func TestGitLabIssuesPaginates(t *testing.T) {
	const total = 250
	var gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.URL.Query().Get("per_page"); got != strconv.Itoa(gitlabIssuesPerPage) {
			t.Errorf("per_page = %q", got)
		}
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page < 1 {
			t.Errorf("page = %q", r.URL.Query().Get("page"))
			page = 1
		}
		glIssueWriteJSON(t, w, glPagedIssues(t, page, total))
	}))
	defer srv.Close()
	s := NewGitLabIssues(srv.URL+"/api/v4/", "tok", "o", "r")
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != total {
		t.Fatalf("tasks = %d, want %d", len(tasks), total)
	}
	seen := make(map[core.TaskID]bool, total)
	for _, task := range tasks {
		seen[task.ID] = true
	}
	for i := 1; i <= total; i++ {
		if !seen[core.TaskID("GL-"+strconv.Itoa(i))] {
			t.Fatalf("missing GL-%d", i)
		}
	}
	// Three full pages plus the empty page that ends the walk.
	if gets != 4 {
		t.Fatalf("GETs = %d, want 4", gets)
	}
}

func TestGitLabIssuesPaginatesKeepsParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			glIssueWriteJSON(t, w, []map[string]any{
				{"iid": 1, "title": "First"},
			})
		case "2":
			glIssueWriteJSON(t, w, []map[string]any{
				{"iid": 101, "title": "Claimed", "labels": []string{"beacon/claimed"}},
				{"iid": 102, "title": "Body id", "description": sharedIssueBody},
			})
		default:
			glIssueWriteJSON(t, w, []map[string]any{})
		}
	}))
	defer srv.Close()
	s := NewGitLabIssues(srv.URL+"/api/v4/", "tok", "o", "r")
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	// sharedIssueBody carries priority 3, so it sorts first.
	assertSharedIssueFields(t, tasks[0], "GL-7")
	if tasks[1].ID != "GL-1" {
		t.Fatalf("second task = %+v", tasks[1])
	}
}

func TestGitLabIssuesStopsAtPageCap(t *testing.T) {
	var gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page < 1 {
			t.Errorf("page = %q", r.URL.Query().Get("page"))
			page = 1
		}
		glIssueWriteJSON(t, w, glPagedIssues(t, page, maxIssuePages*gitlabIssuesPerPage))
	}))
	defer srv.Close()
	s := NewGitLabIssues(srv.URL+"/api/v4/", "tok", "o", "r")
	if _, err := s.ListAvailable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gets != maxIssuePages {
		t.Fatalf("GETs = %d, want %d", gets, maxIssuePages)
	}
}
