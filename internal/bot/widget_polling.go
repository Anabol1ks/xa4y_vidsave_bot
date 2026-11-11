package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

// The currently pinned Telegram library (v5.5.1) has no widget_query field.
// We intentionally replace, rather than duplicate, the old long-polling loop.
// This preserves messages and inline queries while decoding the new update type.
type telegramUpdate struct {
	tgbotapi.Update
	WidgetQuery *widgetQuery `json:"widget_query"`
}

type widgetQuery struct {
	ID        string          `json:"id"`
	From      *tgbotapi.User  `json:"from"`
	Chat      *tgbotapi.Chat  `json:"chat"`
	MessageID int             `json:"message_id"`
	Data      string          `json:"data"`
	Type      json.RawMessage `json:"type"`
}

type pollResult struct {
	OK          bool              `json:"ok"`
	Result      []json.RawMessage `json:"result"`
	Description string            `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func decodeTelegramUpdates(raw []byte) ([]telegramUpdate, error) {
	var reply pollResult
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("decode getUpdates: %w", err)
	}
	if !reply.OK {
		return nil, fmt.Errorf("getUpdates returned not ok")
	}
	updates := make([]telegramUpdate, 0, len(reply.Result))
	for _, item := range reply.Result {
		var update telegramUpdate
		if err := json.Unmarshal(item, &update); err != nil {
			// Malformed data must never silently advance the Telegram offset.
			return nil, fmt.Errorf("decode update: %w", err)
		}
		updates = append(updates, update)
	}
	return updates, nil
}

func getUpdates(ctx context.Context, client *http.Client, endpoint string, offset int) ([]telegramUpdate, error) {
	values := url.Values{}
	values.Set("offset", strconv.Itoa(offset))
	values.Set("timeout", "30")
	values.Set("limit", "100")
	values.Set("allowed_updates", `["message","inline_query","widget_query"]`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build getUpdates request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	if err != nil {
		// Do not propagate URL-bearing errors: it contains the Telegram token.
		return nil, errors.New("telegram polling network error")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram polling HTTP %d", res.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read getUpdates response: %w", err)
	}
	return decodeTelegramUpdates(payload)
}

func (b *Bot) pollUpdates(ctx context.Context) {
	client := &http.Client{Timeout: 38 * time.Second}
	endpoint := "https://api.telegram.org/bot" + b.cfg.BotToken + "/getUpdates"
	offset := 0
	backoff := time.Second
	b.log.Info("bot started, waiting for updates (widget-aware polling)")
	for ctx.Err() == nil {
		updates, err := getUpdates(ctx, client, endpoint, offset)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			b.log.Warn("getUpdates failed", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, update := range updates {
			if update.UpdateID < offset {
				continue
			}
			offset = update.UpdateID + 1
			if update.WidgetQuery != nil {
				go b.handleWidgetQuery(ctx, update.WidgetQuery)
			} else {
				go b.handleUpdate(ctx, update.Update)
			}
		}
	}
}
