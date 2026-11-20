package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adapterDoer implements opencode.Doer with a canned response, so the cmd
// adapter can be tested without a real server. The net/http import stays in
// the test file: the capabilities gate skips _test.go (ADR 0005).
type adapterDoer struct {
	requests []*http.Request
	fn       func(*http.Request) (*http.Response, error)
}

func (a *adapterDoer) Do(req *http.Request) (*http.Response, error) {
	a.requests = append(a.requests, req)
	if a.fn == nil {
		return nil, errors.New("adapterDoer: fn не задан")
	}
	return a.fn(req)
}

func writeServiceFile(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := filepath.Join(dir, "opencode", "service.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewOpencodePortMissingService(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := opencodeHTTPDoer
	t.Cleanup(func() { opencodeHTTPDoer = old })
	opencodeHTTPDoer = &adapterDoer{}

	if port, err := newOpencodePort(); err == nil || port != nil {
		t.Fatalf("ожидалась ошибка без service.json, port=%v err=%v", port, err)
	}
}

func TestNewOpencodePortUsesPasswordFromServiceFile(t *testing.T) {
	writeServiceFile(t, `{"url":"http://127.0.0.1:49374/","pid":1,"version":"2.3.0","password":"secret"}`)

	doer := &adapterDoer{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
			Header:     http.Header{},
		}, nil
	}}
	old := opencodeHTTPDoer
	t.Cleanup(func() { opencodeHTTPDoer = old })
	opencodeHTTPDoer = doer

	port, err := newOpencodePort()
	if err != nil {
		t.Fatalf("newOpencodePort: %v", err)
	}
	if _, err := port.ListAgents(context.Background()); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(doer.requests) != 1 {
		t.Fatalf("ожидался 1 запрос, получено %d", len(doer.requests))
	}
	user, pass, ok := doer.requests[0].BasicAuth()
	if !ok || user != "opencode" || pass != "secret" {
		t.Fatalf("неверный basic auth: user=%q pass=%q ok=%v", user, pass, ok)
	}
	if got := doer.requests[0].URL.String(); got != "http://127.0.0.1:49374/api/agent" {
		t.Fatalf("неверный адрес запроса: %q", got)
	}
}
