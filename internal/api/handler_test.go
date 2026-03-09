package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deusyu/rc_deusyu/internal/store"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s)
}

func TestCreateValidation(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"empty body", `{}`, 400},
		{"missing url", `{"target_url":""}`, 400},
		{"invalid url scheme", `{"target_url":"ftp://example.com"}`, 400},
		{"no host", `{"target_url":"http://"}`, 400},
		{"bad method", `{"target_url":"https://example.com","method":"HACK"}`, 400},
		{"negative retries", `{"target_url":"https://example.com","max_retries":-1}`, 400},
		{"too many retries", `{"target_url":"https://example.com","max_retries":99}`, 400},
		{"valid minimal", `{"target_url":"https://example.com"}`, 202},
		{"valid full", `{"target_url":"https://example.com","method":"PUT","max_retries":3}`, 202},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/notifications", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d, body = %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}
