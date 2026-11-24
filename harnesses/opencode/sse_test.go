package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestStreamIdleUsesConfiguredAuth(t *testing.T) {
	cases := []struct {
		name      string
		token     string
		wantUser  string
		wantPass  string
		wantPlain string
	}{
		{"user password token", "opencode:deadbeef", "opencode", "deadbeef", ""},
		{"plain token", "plain-token", "", "", "plain-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/event" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if tc.wantUser != "" {
					user, pass, ok := r.BasicAuth()
					if !ok || user != tc.wantUser || pass != tc.wantPass {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				} else if got := r.Header.Get("Authorization"); got != "Bearer "+tc.wantPlain {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				mu.Lock()
				gotAuth = r.Header.Get("Authorization")
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			h := New(Config{Endpoint: srv.URL, Model: "model", Agent: "agent", Token: tc.token})
			if err := h.streamIdle(context.Background()); err != nil {
				t.Fatalf("streamIdle: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if tc.wantUser != "" {
				if !strings.HasPrefix(gotAuth, "Basic ") || strings.Contains(gotAuth, "Bearer") {
					t.Fatalf("Authorization = %q, want Basic without Bearer", gotAuth)
				}
			} else if gotAuth != "Bearer "+tc.wantPlain {
				t.Fatalf("Authorization = %q, want Bearer %q", gotAuth, tc.wantPlain)
			}
		})
	}
}
