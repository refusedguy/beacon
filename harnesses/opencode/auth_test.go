package opencode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplyAuthUserPasswordUsesBasic(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/event", nil)
	ApplyAuth(req, "opencode:deadbeef")

	user, pass, ok := req.BasicAuth()
	if !ok || user != "opencode" || pass != "deadbeef" {
		t.Fatalf("BasicAuth = %q, %q, %v; want opencode, deadbeef, true", user, pass, ok)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("Authorization = %q, want Basic prefix", auth)
	}
	if strings.Contains(auth, "Bearer") {
		t.Fatalf("Authorization = %q, must not contain Bearer", auth)
	}
}

func TestApplyAuthPlainTokenUsesBearer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/event", nil)
	ApplyAuth(req, "plain-token")

	if got := req.Header.Get("Authorization"); got != "Bearer plain-token" {
		t.Fatalf("Authorization = %q, want Bearer plain-token", got)
	}
	if _, _, ok := req.BasicAuth(); ok {
		t.Fatal("plain token set Basic auth")
	}
}
