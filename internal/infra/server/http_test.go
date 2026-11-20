package server

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// okHandler is the protected endpoint behind the bearer middleware.
func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func TestBearer(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{name: "no token", header: "", want: http.StatusUnauthorized},
		{name: "valid token", header: "Bearer secret", want: http.StatusOK},
		{name: "wrong token", header: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic secret", want: http.StatusUnauthorized},
		{name: "empty bearer", header: "Bearer ", want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := Bearer("secret")(http.HandlerFunc(okHandler))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusOK && rec.Body.String() != "ok" {
				t.Fatalf("body = %q, want ok", rec.Body.String())
			}
			if tt.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("401 response is missing WWW-Authenticate")
			}
		})
	}
}

func TestLoadOrCreateTokenFilePerm(t *testing.T) {
	dir := t.TempDir()

	token, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	path := TokenPath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("token %q is not a regular file", path)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("token mode = %o, want 600", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != token {
		t.Fatalf("file token = %q, want %q", strings.TrimSpace(string(raw)), token)
	}
}

func TestLoadOrCreateTokenIsStable(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("token changed across loads: %q then %q", first, second)
	}
}

func TestLoadOrCreateTokenRejectsLoosePerm(t *testing.T) {
	dir := t.TempDir()
	path := TokenPath(dir)
	if err := os.WriteFile(path, []byte("token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateToken(dir); !errors.Is(err, ErrTokenPerm) {
		t.Fatalf("error = %v, want ErrTokenPerm", err)
	}
}

func TestLoadOrCreateTokenRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := TokenPath(dir)
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateToken(dir); !errors.Is(err, ErrEmptyToken) {
		t.Fatalf("error = %v, want ErrEmptyToken", err)
	}
}

func TestListenIsLoopbackWithAssignedPort(t *testing.T) {
	listener, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr = %T, want *net.TCPAddr", listener.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("listen IP = %s, want loopback", addr.IP)
	}
	if addr.Port == 0 {
		t.Fatal("listener port = 0, want an OS-assigned port")
	}
}

func TestServeAuthenticatesEndToEnd(t *testing.T) {
	dir := t.TempDir()
	srv, err := Serve(dir, http.HandlerFunc(okHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()

	// Serve generates the token in the state dir; a fresh load must find it.
	stored, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored == "" {
		t.Fatal("Serve did not leave a token in the state dir")
	}

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{name: "no token", header: "", want: http.StatusUnauthorized},
		{name: "valid token", header: "Bearer " + stored, want: http.StatusOK},
		{name: "wrong token", header: "Bearer wrong", want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://"+srv.Addr()+"/api/v1/health", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestTokenPath(t *testing.T) {
	dir := filepath.Join("state", "beacon")
	if got, want := TokenPath(dir), filepath.Join(dir, TokenFileName); got != want {
		t.Fatalf("TokenPath = %q, want %q", got, want)
	}
}
