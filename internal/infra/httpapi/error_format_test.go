package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteErrorResponseFormat pins the bytes of the HTTP API error response.
// The shared httproute writer was removed in CLEAN-02, so httpapi owns this
// format alone. Reusing another error type must keep these bytes unchanged.
func TestWriteErrorResponseFormat(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		code    string
		message string
		want    string
	}{
		{
			name:    "client error",
			status:  http.StatusBadRequest,
			code:    "INVALID_INPUT",
			message: "bad body",
			want:    "{\"ok\":false,\"code\":\"INVALID_INPUT\",\"message\":\"bad body\"}\n",
		},
		{
			name:    "auth error keeps explicit status",
			status:  http.StatusUnauthorized,
			code:    "UNAUTHORIZED",
			message: "no token",
			want:    "{\"ok\":false,\"code\":\"UNAUTHORIZED\",\"message\":\"no token\"}\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, tc.status, tc.code, tc.message)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q", ct)
			}
			if got := w.Body.String(); got != tc.want {
				t.Fatalf("body = %q, want %q", got, tc.want)
			}
		})
	}
}
