package httproute

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) Error {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body Error
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body
}

func TestRouteFound(t *testing.T) {
	r := New("/api/v1")
	r.GET("/health", func(w http.ResponseWriter, _ *http.Request) error {
		_, err := io.WriteString(w, "ok")
		return err
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want ok", rec.Body.String())
	}
}

func TestUnknownPathReturnsJSON404(t *testing.T) {
	r := New("/api/v1")
	r.GET("/health", func(w http.ResponseWriter, _ *http.Request) error {
		return nil
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	body := decodeError(t, rec)
	if body.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", body.Code, CodeNotFound)
	}
	if body.Message == "" {
		t.Fatal("error message is empty")
	}
}

func TestHandlerErrorMapsToStatusAndCode(t *testing.T) {
	r := New("/api/v1")
	r.GET("/bad", func(http.ResponseWriter, *http.Request) error {
		return fmt.Errorf("handler: %w", InvalidArgument("bad id %q", "x"))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/bad", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	body := decodeError(t, rec)
	if body.Code != CodeInvalidArgument {
		t.Fatalf("code = %q, want %q", body.Code, CodeInvalidArgument)
	}
	if body.Message != `bad id "x"` {
		t.Fatalf("message = %q, want %q", body.Message, `bad id "x"`)
	}
}

func TestUnknownMethodReturnsJSON405(t *testing.T) {
	r := New("/api/v1")
	r.GET("/health", func(http.ResponseWriter, *http.Request) error { return nil })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	body := decodeError(t, rec)
	if body.Code != CodeMethodNotAllowed {
		t.Fatalf("code = %q, want %q", body.Code, CodeMethodNotAllowed)
	}
}

func TestEmptyRouterReturns404(t *testing.T) {
	r := New("/api/v1")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	body := decodeError(t, rec)
	if body.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", body.Code, CodeNotFound)
	}
}

func TestWriteErrorPlainErrorIsInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, fmt.Errorf("db password is secret"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	body := decodeError(t, rec)
	if body.Code != CodeInternal {
		t.Fatalf("code = %q, want %q", body.Code, CodeInternal)
	}
	if body.Message != "internal error" {
		t.Fatalf("message = %q, want internal error", body.Message)
	}
}
