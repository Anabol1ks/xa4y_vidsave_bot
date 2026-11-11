package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strconv"
	"sync"
	"testing"

	"xa4yy_vidsave/internal/config"
	"xa4yy_vidsave/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

// Uses the disposable CI database, never production, and a local fake Telegram API.
// This exercises sendWidget -> widget_query -> answerWidgetQuery -> editMessageWidgetData.
func TestAnalyticsWidgetTelegramWireIntegration(t *testing.T) {
	dsn := os.Getenv("VIDSAVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL test database")
	}
	store, err := storage.New(dsn, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var mu sync.Mutex
	methods := []string{}
	params := map[string][]map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := path.Base(r.URL.Path)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		args := map[string]string{}
		for k, v := range r.PostForm {
			if len(v) > 0 {
				args[k] = v[0]
			}
		}
		mu.Lock()
		methods = append(methods, method)
		params[method] = append(params[method], args)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":9999,"is_bot":true,"first_name":"TestBot","username":"testbot"}}`))
		case "sendWidget":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42,"chat":{"id":123,"type":"private"}}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer server.Close()
	api, err := tgbotapi.NewBotAPIWithAPIEndpoint("fake-token", server.URL+"/bot%s/%s")
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{api: api, cfg: &config.Config{AdminTelegramIDs: map[int64]struct{}{123: {}}}, log: zap.NewNop(), store: store}
	if err := b.sendAnalyticsWidget(123); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sent := params["sendWidget"][0]
	mu.Unlock()
	if sent["chat_id"] != "123" || sent["allow_queries"] != "true" || sent["html"] == "" || sent["chat_data"] == "" {
		t.Fatalf("incorrect sendWidget parameters: %v", sent)
	}
	if _, ok := sent["public_data"]; ok {
		t.Fatal("private stats sent in public state")
	}
	if _, ok := sent["is_shared"]; ok {
		t.Fatal("owner widget must not be shared")
	}
	var first storage.WidgetAnalytics
	if err := json.Unmarshal([]byte(sent["chat_data"]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Period != storage.Period24h {
		t.Fatalf("initial period %s", first.Period)
	}
	query := &widgetQuery{
		ID: "query-1", From: &tgbotapi.User{ID: 123},
		Chat:      &tgbotapi.Chat{ID: 123, Type: "private"},
		MessageID: 42, Data: "stats:all", Type: json.RawMessage(`{"type":"data"}`),
	}
	b.handleWidgetQuery(context.Background(), query)
	mu.Lock()
	answer := params["answerWidgetQuery"][0]
	edit := params["editMessageWidgetData"][0]
	mu.Unlock()
	if answer["widget_query_id"] != "query-1" || edit["message_id"] != strconv.Itoa(42) || edit["chat_id"] != "123" {
		t.Fatalf("query state update mismatch: answer=%v edit=%v", answer, edit)
	}
	var window storage.WidgetAnalytics
	if err := json.Unmarshal([]byte(answer["data"]), &window); err != nil {
		t.Fatal(err)
	}
	if window.Period != storage.PeriodAll || answer["data"] != edit["chat_data"] {
		t.Fatal("period was not updated persistently")
	}
	mu.Lock()
	before := len(params["editMessageWidgetData"])
	mu.Unlock()
	query.From = &tgbotapi.User{ID: 124}
	query.Chat = &tgbotapi.Chat{ID: 124, Type: "private"}
	query.ID = "unauthorized-query"
	b.handleWidgetQuery(context.Background(), query)
	mu.Lock()
	after := len(params["editMessageWidgetData"])
	mu.Unlock()
	if before != after {
		t.Fatal("unauthorized query wrote widget state")
	}
}
