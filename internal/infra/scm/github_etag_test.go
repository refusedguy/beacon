package scm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestGetPRUsesETagCache(t *testing.T) {
	requests := 0
	conditional := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional++
			rw.WriteHeader(http.StatusNotModified)
			return
		}
		rw.Header().Set("ETag", `"v1"`)
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"number":42,"head":{"sha":"abc"}}`))
	}))
	defer srv.Close()
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithEnterpriseURLs(srv.URL+"/", srv.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	g := NewGitHub(client, "o", "r", "main")
	for i := 0; i < 3; i++ {
		pr, err := g.getPR(context.Background(), 42)
		if err != nil {
			t.Fatal(err)
		}
		if pr.GetHead().GetSHA() != "abc" {
			t.Fatalf("cached PR lost head SHA: %+v", pr)
		}
	}
	if requests != 3 || conditional != 2 {
		t.Fatalf("requests=%d conditional=%d", requests, conditional)
	}
}
