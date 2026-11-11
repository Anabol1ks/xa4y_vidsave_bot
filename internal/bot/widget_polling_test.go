package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const mixedUpdates = `{
  "ok":true,
  "result":[
    {"update_id":1,"message":{"message_id":10,"from":{"id":100,"first_name":"Tester"},"chat":{"id":100,"type":"private"},"text":"/stats"}},
    {"update_id":2,"inline_query":{"id":"inline-1","from":{"id":100,"first_name":"Tester"},"query":"youtube:abc","offset":""}},
    {"update_id":3,"widget_query":{"id":"widget-q-1","from":{"id":100,"first_name":"Tester"},"chat":{"id":100,"type":"private"},"message_id":11,"data":"stats:7d","type":{"type":"data"}}}
  ]
}`

func TestDecodeTelegramUpdatesPreservesOldAndNewEvents(t *testing.T) {
	updates, err := decodeTelegramUpdates([]byte(mixedUpdates))
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 3 {
		t.Fatalf("got %d updates", len(updates))
	}
	if updates[0].UpdateID != 1 || updates[0].Message == nil || updates[0].Message.Text != "/stats" {
		t.Fatal("message update lost")
	}
	if updates[1].UpdateID != 2 || updates[1].InlineQuery == nil || updates[1].InlineQuery.ID != "inline-1" {
		t.Fatal("inline update lost")
	}
	q := updates[2].WidgetQuery
	if updates[2].UpdateID != 3 || q == nil || q.ID != "widget-q-1" || q.Chat.ID != 100 || q.From.ID != 100 || q.Data != "stats:7d" {
		t.Fatalf("widget update lost: %+v", updates[2])
	}
	for _, bad := range []string{"not json", `{"ok":false}`, `{"ok":true,"result":[{"update_id":"not a number"}]}`} {
		if _, err := decodeTelegramUpdates([]byte(bad)); err == nil {
			t.Errorf("malformed updates accepted: %q", bad)
		}
	}
}

func TestGetUpdatesUsesOnePollingSession(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Method != "POST" {
			t.Errorf("request method %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		v, err := url.ParseQuery(string(body))
		if err != nil {
			t.Error(err)
		}
		if v.Get("offset") != "4" || v.Get("timeout") != "30" || v.Get("limit") != "100" {
			t.Errorf("unexpected polling args %+v", v)
		}
		if !strings.Contains(v.Get("allowed_updates"), "widget_query") ||
			!strings.Contains(v.Get("allowed_updates"), "message") ||
			!strings.Contains(v.Get("allowed_updates"), "inline_query") {
			t.Errorf("allowed_updates missing: %+v", v)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mixedUpdates))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	updates, err := getUpdates(ctx, server.Client(), server.URL+"/getUpdates", 4)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || len(updates) != 3 {
		t.Fatalf("requests=%d updates=%d", requestCount, len(updates))
	}
}

func TestPollingFailureDoesNotRevealToken(t *testing.T) {
	token := "secret-token-never-log"
	client := &http.Client{Timeout: 150 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := getUpdates(ctx, client, "http://127.0.0.1:1/bot"+token+"/getUpdates", 0)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("sensitive polling error: %v", err)
	}
}
