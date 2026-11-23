package tasksource

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestGitHubIssuesParsesYAMLAndScopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/o/r/issues") {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		body := "```yaml\nscope:\n  - internal/pkg/\n  - internal/**/*.go\ndepends:\n  - 3\npriority: 5\n```\nScope: docs/**\nDepends: GH-4\n"
		_, _ = fmt.Fprintf(w, `[{"number":11,"title":"YAML task","html_url":"https://github.com/o/r/issues/11","body":%q}]`, body)
	}))
	defer srv.Close()
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewGitHubIssues(client, "o", "r")
	tasks, err := s.ListAvailable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	task := tasks[0]
	if task.ID != "GH-11" || task.Priority != 5 {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Scope) != 3 {
		t.Fatalf("scope = %v", task.Scope)
	}
	if !task.Scope[0].CoversPath("internal/pkg/x.go") || !task.Scope[1].CoversPath("internal/a/b.go") {
		t.Fatalf("scope kinds = %v", task.Scope)
	}
	if len(task.DependsOn) != 2 || task.DependsOn[0] != "GH-3" || task.DependsOn[1] != "GH-4" {
		t.Fatalf("depends = %v", task.DependsOn)
	}
}
