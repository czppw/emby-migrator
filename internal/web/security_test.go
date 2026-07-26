package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	for _, name := range []string{
		"Content-Security-Policy",
		"Permissions-Policy",
		"Referrer-Policy",
		"Strict-Transport-Security",
		"X-Content-Type-Options",
		"X-Frame-Options",
	} {
		if response.Header().Get(name) == "" {
			t.Fatalf("security header %s is missing", name)
		}
	}
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	body := bytes.NewReader([]byte(`{"value":"` + strings.Repeat("x", 1<<20) + `"}`))
	req := httptest.NewRequest(http.MethodPost, "http://example.test/", body)
	response := httptest.NewRecorder()
	var value map[string]string
	if decodeJSON(response, req, &value) {
		t.Fatal("decodeJSON accepted an oversized request")
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestBoundedConcurrency(t *testing.T) {
	tests := []struct {
		value   int
		maximum int
		want    int
	}{
		{value: 4, maximum: 64, want: 4},
		{value: 500, maximum: 64, want: 64},
		{value: 500, maximum: 0, want: 500},
		{value: -1, maximum: 64, want: -1},
	}
	for _, tt := range tests {
		if got := boundedConcurrency(tt.value, tt.maximum); got != tt.want {
			t.Fatalf("boundedConcurrency(%d, %d) = %d, want %d", tt.value, tt.maximum, got, tt.want)
		}
	}
}

func TestRecoverJSONDoesNotExposePanicDetails(t *testing.T) {
	handler := recoverJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret database path")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), "secret database path") {
		t.Fatalf("panic details leaked in response: %s", response.Body.String())
	}
}
