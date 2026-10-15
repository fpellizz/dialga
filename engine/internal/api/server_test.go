package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testToken is built, not written as a literal, so secret scanners do not
// mistake this throwaway test value for a real API key.
var testToken = strings.Repeat("0123456789abcdef", 2)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	s, err := New(Options{Token: testToken, AllowedOrigins: DefaultOrigins, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func TestGuard(t *testing.T) {
	h := newTestServer(t)
	tests := []struct {
		name       string
		method     string
		host       string
		origin     string
		auth       string
		wantStatus int
		wantCORS   string
	}{
		{"health with token", http.MethodGet, "127.0.0.1:5000", "", "Bearer " + testToken, http.StatusOK, ""},
		{"localhost host", http.MethodGet, "localhost:5000", "", "Bearer " + testToken, http.StatusOK, ""},
		{"tauri origin", http.MethodGet, "127.0.0.1:5000", "tauri://localhost", "Bearer " + testToken, http.StatusOK, "tauri://localhost"},
		{"missing token", http.MethodGet, "127.0.0.1:5000", "", "", http.StatusUnauthorized, ""},
		{"wrong token", http.MethodGet, "127.0.0.1:5000", "", "Bearer " + strings.Repeat("x", 32), http.StatusUnauthorized, ""},
		{"rebinding host", http.MethodGet, "evil.example:5000", "", "Bearer " + testToken, http.StatusForbidden, ""},
		{"foreign origin", http.MethodGet, "127.0.0.1:5000", "https://evil.example", "Bearer " + testToken, http.StatusForbidden, ""},
		{"preflight allowed", http.MethodOptions, "127.0.0.1:5000", "tauri://localhost", "", http.StatusNoContent, "tauri://localhost"},
		{"preflight foreign", http.MethodOptions, "127.0.0.1:5000", "https://evil.example", "", http.StatusForbidden, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/v1/health", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantCORS {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, tc.wantCORS)
			}
		})
	}
}

func TestCheckRejectsBadRequest(t *testing.T) {
	h := newTestServer(t)
	for name, body := range map[string]string{
		"not json":      "{",
		"unknown field": `{"profile":"sales","password":"x"}`,
		"invalid value": `{"profile":"sales","region":"us-east-1","instance":"1bad","localPort":11433,"sqlUser":"god"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/checks", strings.NewReader(body))
			req.Host = "127.0.0.1:5000"
			req.Header.Set("Authorization", "Bearer "+testToken)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code < 400 || rec.Code > 499 {
				t.Fatalf("status = %d, want 4xx", rec.Code)
			}
		})
	}
}

func TestNewRejectsShortToken(t *testing.T) {
	if _, err := New(Options{Token: "short"}); err == nil {
		t.Fatal("New accepted a short token")
	}
}
