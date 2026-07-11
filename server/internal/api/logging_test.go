package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddlewareAllowsLocalAndNativeOrigins(t *testing.T) {
	tests := []string{
		"http://localhost:5173",
		"https://127.0.0.1:7331",
		"http://[::1]:7331",
		"capacitor://localhost",
		"ionic://localhost",
	}
	for _, origin := range tests {
		t.Run(origin, func(t *testing.T) {
			nextCalled := false
			handler := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusAccepted)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
			req.Header.Set("Origin", origin)
			resp := httptest.NewRecorder()

			handler.ServeHTTP(resp, req)

			if !nextCalled {
				t.Fatal("expected next handler to be called")
			}
			if got := resp.Header().Get("Access-Control-Allow-Origin"); got != origin {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, origin)
			}
			if got := resp.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
				t.Fatalf("Access-Control-Allow-Credentials = %q, want true", got)
			}
		})
	}
}

func TestCORSMiddlewareRejectsRemoteOrigins(t *testing.T) {
	handler := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/relay/bind/start", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusForbidden)
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

func TestAllowedCORSOriginRejectsInvalidOrigins(t *testing.T) {
	for _, origin := range []string{"", "null", "https://evil.example", "ftp://localhost", "https://localhost/path", "https://user@localhost"} {
		if got := allowedCORSOrigin(origin); got != "" {
			t.Fatalf("allowedCORSOrigin(%q) = %q, want empty", origin, got)
		}
	}
}
