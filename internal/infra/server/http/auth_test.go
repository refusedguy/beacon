package httproute

import (
	nethttp "net/http"
	"net/http/httptest"
	"testing"
)

func TestBearer(t *testing.T) {
	const token = "s3cret-token"

	tests := []struct {
		name       string
		auth       string
		wantStatus int
	}{
		{
			name:       "valid token",
			auth:       "Bearer " + token,
			wantStatus: nethttp.StatusOK,
		},
		{
			name:       "missing token",
			auth:       "",
			wantStatus: nethttp.StatusUnauthorized,
		},
		{
			name:       "wrong token",
			auth:       "Bearer wrong-token",
			wantStatus: nethttp.StatusUnauthorized,
		},
		{
			name:       "wrong scheme",
			auth:       "Basic " + token,
			wantStatus: nethttp.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			next := nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
				reached = true
				w.WriteHeader(nethttp.StatusOK)
			})
			handler := Bearer(token)(next)

			req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/resource", nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			wantReached := tt.wantStatus == nethttp.StatusOK
			if reached != wantReached {
				t.Fatalf("next reached = %t, want %t", reached, wantReached)
			}
		})
	}
}

func TestBearerRejectionSetsAuthenticateHeader(t *testing.T) {
	next := nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {
		t.Fatal("next must not be called on a rejected request")
	})
	handler := Bearer("s3cret-token")(next)

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/resource", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("WWW-Authenticate"); got != bearerScheme {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, bearerScheme)
	}
}
