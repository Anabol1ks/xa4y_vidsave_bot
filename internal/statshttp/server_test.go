package statshttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xa4yy_vidsave/internal/storage"
)

func TestLoopbackAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:18873", "[::1]:18873"} {
		if !validLoopbackAddress(addr) {
			t.Errorf("expected valid address: %s", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:18873", ":18873", "localhost:18873", "1.2.3.4:18873", "[::]:18873", "127.0.0.1", ""} {
		if validLoopbackAddress(addr) {
			t.Errorf("must reject non-loopback or malformed address: %s", addr)
		}
	}
}

func TestHTTPStats(t *testing.T) {
	var hits int
	h := Handler(func(context.Context) (storage.AnalyticsSnapshot, error) {
		hits++
		return storage.AnalyticsSnapshot{Users: 7, Requests: 4, Success: 3}, nil
	})
	request := httptest.NewRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "\"users_since_tracking\":7") {
		t.Fatalf("status %d, body %q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "telegram_user_id") {
		t.Fatal("must not reveal Telegram user IDs")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	// No POST/DELETE or write action.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/stats", nil))
	if w.Code != http.StatusMethodNotAllowed || hits != 1 {
		t.Fatalf("unexpected write response: status=%d hits=%d", w.Code, hits)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 200 {
		t.Errorf("healthz status %d", w.Code)
	}
}

func TestHTTPStatsError(t *testing.T) {
	h := Handler(func(context.Context) (storage.AnalyticsSnapshot, error) {
		return storage.AnalyticsSnapshot{}, errors.New("database internal detail")
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "database internal detail") {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}
