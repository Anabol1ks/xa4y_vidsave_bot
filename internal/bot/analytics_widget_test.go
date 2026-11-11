package bot

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"strings"
	"testing"

	"xa4yy_vidsave/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestWidgetAnalyticsSecurity(t *testing.T) {
	allowed := map[int64]struct{}{123: {}}
	valid := &widgetQuery{
		ID: "q-1", From: &tgbotapi.User{ID: 123},
		Chat:      &tgbotapi.Chat{ID: 123, Type: "private"},
		MessageID: 42, Data: "stats:24h", Type: json.RawMessage(`{"type":"data"}`),
	}
	if !statsWidgetAuthorized(valid, allowed) {
		t.Fatal("owner in private chat denied")
	}
	tests := []struct {
		name   string
		change func(*widgetQuery)
	}{
		{"other user", func(q *widgetQuery) { q.From.ID = 124; q.Chat.ID = 124 }},
		{"owner in group", func(q *widgetQuery) { q.Chat.Type = "supergroup"; q.Chat.ID = -100 }},
		{"forged chat", func(q *widgetQuery) { q.Chat.ID = 999 }},
		{"bot sender", func(q *widgetQuery) { q.From.IsBot = true }},
		{"no message", func(q *widgetQuery) { q.MessageID = 0 }},
		{"no query id", func(q *widgetQuery) { q.ID = "" }},
		{"wallet type", func(q *widgetQuery) { q.Type = json.RawMessage(`{"type":"sign_data"}`) }},
		{"bad type", func(q *widgetQuery) { q.Type = json.RawMessage(`{"type":"garbage"}`) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := *valid
			f := *valid.From
			c := *valid.Chat
			q.From = &f
			q.Chat = &c
			tc.change(&q)
			if statsWidgetAuthorized(&q, allowed) {
				t.Fatal("incorrectly authorized")
			}
		})
	}
	if statsWidgetAuthorized(valid, nil) || statsWidgetAuthorized(nil, allowed) {
		t.Fatal("must fail closed")
	}
}

func TestStatsWidgetPeriods(t *testing.T) {
	for _, good := range []string{"stats:24h", "stats:7d", "stats:30d", "stats:all"} {
		if _, ok := statsPeriodFromQuery(good); !ok {
			t.Fatal("rejected", good)
		}
	}
	for _, bad := range []string{"stats", "stats:", "stats:ALL", "stats:all:extra", "retry", "download:https://example.com", "stats:24h ", "stats:100d", strings.Repeat("z", 70)} {
		if _, ok := statsPeriodFromQuery(bad); ok {
			t.Fatal("accepted", bad)
		}
	}
}

func TestAnalyticsWidgetPayload(t *testing.T) {
	rate := 87.5
	report := storage.WidgetAnalytics{
		Version: 1, Period: storage.Period7d, Revision: 1234, Users: 3, NewUsers: 2, Requests: 8, Success: 7, Errors: 1, CacheHits: 4, SuccessRate: &rate,
		Platforms:  []storage.WindowPlatform{{Name: "youtube", Requests: 5, Success: 5}},
		ErrorCodes: []storage.WindowError{{Code: "auth_required", Count: 1}}, Buckets: []int64{1, 1, 2, 3, 1, 0, 0},
	}
	s, err := marshalWidgetAnalytics(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) > 1024 {
		t.Fatal("payload too large")
	}
	var output storage.WidgetAnalytics
	if err := json.Unmarshal([]byte(s), &output); err != nil {
		t.Fatal(err)
	}
	if output.Users != 3 || output.Success != 7 || output.Period != "7d" {
		t.Fatal(output)
	}
	if strings.Contains(s, "telegram_user_id") {
		t.Fatal("private user ids must not be in widget state")
	}
	report.ErrorCodes = []storage.WindowError{{Code: strings.Repeat("x", 1100)}}
	if _, err := marshalWidgetAnalytics(report); err == nil {
		t.Fatal("oversized state must be rejected")
	}
}

func TestAnalyticsWidgetHTMLLimits(t *testing.T) {
	data := []byte(analyticsWidgetHTML)
	if len(data) == 0 || len(data) > 32768 {
		t.Fatalf("HTML length %d invalid", len(data))
	}
	var compressed bytes.Buffer
	zc, _ := zlib.NewWriterLevel(&compressed, 9)
	_, _ = zc.Write(data)
	_ = zc.Close()
	if compressed.Len() > 4096 {
		t.Fatalf("zlib level 9 length %d exceeds 4096 bytes", compressed.Len())
	}
	for _, needle := range []string{"W.request", "chatDataChanged", "data-p=\"24h\"", "data-p=\"7d\"", "data-p=\"30d\"", "data-p=\"all\"", "prefers-reduced-motion"} {
		if !strings.Contains(analyticsWidgetHTML, needle) {
			t.Errorf("missing %s", needle)
		}
	}
	for _, forbidden := range []string{"fetch(", "XMLHttpRequest", "WebSocket", "retryDownload", "Попробовать снова"} {
		if strings.Contains(analyticsWidgetHTML, forbidden) {
			t.Errorf("forbidden API/action %q", forbidden)
		}
	}
}

func TestSendWidgetParams(t *testing.T) {
	p := sendWidgetParams(123, "{}")
	if p["chat_id"] != "123" || p["chat_data"] != "{}" || p["allow_queries"] != "true" {
		t.Fatalf("unexpected params: %+v", p)
	}
	if _, ok := p["public_data"]; ok {
		t.Fatal("owner private data must not be public_data")
	}
	if _, ok := p["is_shared"]; ok {
		t.Fatal("owner widget must not be shared")
	}
}
