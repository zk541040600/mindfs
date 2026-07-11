package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddlewareAllowsLocalNativeAndSameHostOrigins(t *testing.T) {
	tests := []struct {
		name   string
		target string
		origin string
	}{
		{name: "localhost dev server", target: "http://mindfs.local/api/tree", origin: "http://localhost:5173"},
		{name: "IPv4 loopback", target: "http://mindfs.local/api/tree", origin: "https://127.0.0.1:7331"},
		{name: "IPv6 loopback", target: "http://mindfs.local/api/tree", origin: "http://[::1]:7331"},
		{name: "LAN same host", target: "http://10.23.50.137:7331/api/tree", origin: "http://10.23.50.137:7331"},
		{name: "relay same host", target: "https://relay.a9gent.com/api/tree", origin: "https://relay.a9gent.com"},
		{name: "custom proxy same host", target: "https://mindfs.example/api/tree", origin: "https://mindfs.example"},
		{name: "Capacitor", target: "http://mindfs.local/api/tree", origin: "capacitor://localhost"},
		{name: "Ionic", target: "http://mindfs.local/api/tree", origin: "ionic://localhost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			handler := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusAccepted)
			}))
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req.Header.Set("Origin", tt.origin)
			resp := httptest.NewRecorder()

			handler.ServeHTTP(resp, req)

			if !nextCalled {
				t.Fatal("expected next handler to be called")
			}
			if got := resp.Header().Get("Access-Control-Allow-Origin"); got != tt.origin {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, tt.origin)
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

func TestWebSocketUpgraderUsesCORSOriginRules(t *testing.T) {
	allowed := httptest.NewRequest(http.MethodGet, "https://relay.a9gent.com/ws", nil)
	allowed.Header.Set("Origin", "https://relay.a9gent.com")
	if !upgrader.CheckOrigin(allowed) {
		t.Fatal("expected same-host relay origin to be allowed for websocket upgrade")
	}

	local := httptest.NewRequest(http.MethodGet, "http://10.23.50.137:7331/ws", nil)
	local.Header.Set("Origin", "http://10.23.50.137:7331")
	if !upgrader.CheckOrigin(local) {
		t.Fatal("expected same-host LAN origin to be allowed for websocket upgrade")
	}

	rejected := httptest.NewRequest(http.MethodGet, "http://10.23.50.137:7331/ws", nil)
	rejected.Header.Set("Origin", "https://evil.example")
	if upgrader.CheckOrigin(rejected) {
		t.Fatal("expected mismatched remote origin to be rejected for websocket upgrade")
	}
}

func TestAllowedCORSOriginRejectsInvalidOrigins(t *testing.T) {
	for _, origin := range []string{"", "null", "https://evil.example", "https://8.8.8.8", "http://10.23.50.138:7331", "ftp://localhost", "https://localhost/path", "https://user@localhost"} {
		if got := allowedCORSOrigin(origin, "10.23.50.137:7331"); got != "" {
			t.Fatalf("allowedCORSOrigin(%q) = %q, want empty", origin, got)
		}
	}
}
