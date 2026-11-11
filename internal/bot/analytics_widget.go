package bot

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xa4yy_vidsave/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

//go:embed analytics_widget.html
var analyticsWidgetHTML string

func statsWidgetAuthorized(q *widgetQuery, allow map[int64]struct{}) bool {
	if q == nil || q.From == nil || q.Chat == nil || q.MessageID <= 0 || q.ID == "" {
		return false
	}
	if !q.Chat.IsPrivate() || q.Chat.ID != q.From.ID || q.From.IsBot || q.From.ID <= 0 {
		return false
	}
	if len(q.Type) == 0 {
		return false
	}
	var widgetType struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(q.Type, &widgetType); err != nil || widgetType.Type != "data" {
		return false
	}
	_, ok := allow[q.From.ID]
	return ok
}

func statsPeriodFromQuery(data string) (storage.AnalyticsPeriod, bool) {
	if !strings.HasPrefix(data, "stats:") || len(data) > 64 {
		return "", false
	}
	return storage.ParseAnalyticsPeriod(strings.TrimPrefix(data, "stats:"))
}

func marshalWidgetAnalytics(stat storage.WidgetAnalytics) (string, error) {
	data, err := json.Marshal(stat)
	if err != nil {
		return "", err
	}
	if len(data) > 1024 {
		return "", fmt.Errorf("widget analytics payload exceeds 1024-byte Telegram limit: %d", len(data))
	}
	return string(data), nil
}

func sendWidgetParams(chatID int64, payload string) tgbotapi.Params {
	return tgbotapi.Params{
		"chat_id":       strconv.FormatInt(chatID, 10),
		"html":          analyticsWidgetHTML,
		"allow_queries": "true",
		"chat_data":     payload,
		"caption":       "📊 XA4Y · Analytics — закрытая статистика. Если виджет не поддерживается, отправь /stats_text.",
	}
}

func (b *Bot) sendAnalyticsWidget(chatID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	report, err := b.store.AnalyticsWindow(ctx, storage.Period24h)
	if err != nil {
		return err
	}
	payload, err := marshalWidgetAnalytics(report)
	if err != nil {
		return err
	}
	res, err := b.api.MakeRequest("sendWidget", sendWidgetParams(chatID, payload))
	if err != nil {
		return fmt.Errorf("sendWidget: %w", err)
	}
	var msg struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(res.Result, &msg); err != nil {
		return fmt.Errorf("decode sendWidget result: %w", err)
	}
	if msg.MessageID <= 0 {
		return fmt.Errorf("sendWidget returned no message ID")
	}
	return nil
}

func (b *Bot) sendTextStats(chatID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stats, err := b.store.Analytics(ctx)
	if err != nil {
		b.log.Error("analytics command failed", zap.Error(err))
		b.sender.Text(chatID, "не удалось получить статистику 😕")
		return
	}
	b.sender.Text(chatID, formatStats(stats))
}

// A widget never invokes a new download. It may only select one of the
// four pre-approved aggregate periods and refresh that specific message.
func (b *Bot) handleWidgetQuery(parent context.Context, q *widgetQuery) {
	if q == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			b.log.Error("panic in widget query", zap.Any("recover", recovered))
			b.answerWidgetQuery(q.ID, `{"error":"internal"}`)
		}
	}()
	if !statsWidgetAuthorized(q, b.cfg.AdminTelegramIDs) {
		b.answerWidgetQuery(q.ID, `{"error":"forbidden"}`)
		return
	}
	period, ok := statsPeriodFromQuery(q.Data)
	if !ok {
		b.answerWidgetQuery(q.ID, `{"error":"unknown_action"}`)
		return
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	report, err := b.store.AnalyticsWindow(ctx, period)
	if err != nil {
		b.log.Warn("widget analytics query failed", zap.Error(err))
		b.answerWidgetQuery(q.ID, `{"error":"unavailable"}`)
		return
	}
	payload, err := marshalWidgetAnalytics(report)
	if err != nil {
		b.log.Error("widget analytics too large", zap.Error(err))
		b.answerWidgetQuery(q.ID, `{"error":"unavailable"}`)
		return
	}
	// The answer is private to the querying widget and must arrive in <15s.
	// Do not wait for editMessageWidgetData before answering.
	if err := b.answerWidgetQuery(q.ID, payload); err != nil {
		b.log.Warn("answerWidgetQuery failed", zap.Error(err))
	}
	params := tgbotapi.Params{
		"chat_id":    strconv.FormatInt(q.Chat.ID, 10),
		"message_id": strconv.Itoa(q.MessageID),
		"chat_data":  payload,
	}
	if _, err := b.api.MakeRequest("editMessageWidgetData", params); err != nil {
		b.log.Warn("editMessageWidgetData failed", zap.Error(err))
	}
}

func (b *Bot) answerWidgetQuery(queryID, data string) error {
	if queryID == "" {
		return nil
	}
	p := tgbotapi.Params{"widget_query_id": queryID, "data": data}
	_, err := b.api.MakeRequest("answerWidgetQuery", p)
	return err
}
