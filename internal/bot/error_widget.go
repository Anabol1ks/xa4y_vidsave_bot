package bot

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"
)

//go:embed error_widget.html
var errorWidgetHTML string

// No secrets, source links, usernames, retry options or private metadata
// are included in the widget state. The HTML is static and needs no queries.
type errorWidgetState struct {
	Version  int    `json:"v"`
	Platform string `json:"p"`
	Code     string `json:"e"`
}

var allowedErrorWidgetCodes = map[string]struct{}{
	"invalid_url": {}, "unsupported_url": {}, "photo_post": {}, "auth_required": {},
	"private_video": {}, "source_restricted": {},
	"source_rate_limited": {}, "download_error": {}, "network_error": {},
	"too_large": {}, "too_long": {}, "send_error": {},
	"busy": {}, "limit_reached": {}, "internal_error": {},
}

func marshalErrorWidgetState(platform, code string) (string, error) {
	switch platform {
	case "youtube", "tiktok", "instagram", "generic":
	default:
		return "", errors.New("unsupported error widget platform")
	}
	if _, ok := allowedErrorWidgetCodes[code]; !ok {
		return "", errors.New("unsupported error widget code")
	}
	payload, err := json.Marshal(errorWidgetState{Version: 1, Platform: platform, Code: code})
	if err != nil || len(payload) > 1024 {
		return "", errors.New("invalid error widget payload")
	}
	return string(payload), nil
}

func (b *Bot) errorWidgetsActive(chatID int64) bool {
	return b != nil && b.cfg != nil && b.cfg.ErrorWidgetsEnabled &&
		b.downloadWidgetActive(chatID) && b.downloadWidgetAPI != nil
}

// sendErrorWidget creates an informational widget in a private bot chat.
// It NEVER calls yt-dlp, creates a download task or allows widget queries.
func (b *Bot) sendErrorWidget(chatID int64, replyID int, platform, code string) bool {
	if !b.errorWidgetsActive(chatID) {
		return false
	}
	payload, err := marshalErrorWidgetState(platform, code)
	if err != nil {
		return false
	}
	params := url.Values{
		"chat_id":   {strconv.FormatInt(chatID, 10)},
		"html":      {errorWidgetHTML},
		"chat_data": {payload},
		"caption":   {"⚠️ XA4Y · Подробности и поддерживаемые форматы"},
	}
	if replyID > 0 {
		reply, _ := json.Marshal(map[string]interface{}{
			"message_id":                  replyID,
			"allow_sending_without_reply": true,
		})
		params.Set("reply_parameters", string(reply))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	raw, err := b.downloadWidgetAPI.call(ctx, "sendWidget", params)
	if err != nil {
		b.log.Warn("error widget unavailable; falling back to text")
		return false
	}
	var result struct {
		MessageID int `json:"message_id"`
	}
	if json.Unmarshal(raw, &result) != nil || result.MessageID <= 0 {
		return false
	}
	return true
}

func (b *Bot) errorWidgetOrText(chatID int64, replyID int, platform, code, fallback string, private bool) {
	if private && b.sendErrorWidget(chatID, replyID, platform, code) {
		return
	}
	b.sender.TextReply(chatID, replyID, fallback)
}
