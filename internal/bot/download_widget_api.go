package bot

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"xa4yy_vidsave/internal/link"
)

//go:embed download_widget.html
var downloadWidgetHTML string

// The BotAPI upload client is deliberately untouched: timeout for widget
// requests must not prematurely terminate a 50 MiB Telegram video upload.
type downloadWidgetAPI struct {
	endpoint string
	client   *http.Client
}

func newDownloadWidgetAPI(token string) *downloadWidgetAPI {
	return &downloadWidgetAPI{
		endpoint: "https://api.telegram.org/bot" + token,
		client:   &http.Client{Timeout: 8 * time.Second},
	}
}

func (a *downloadWidgetAPI) call(parent context.Context, method string, params url.Values) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/"+method, bytes.NewBufferString(params.Encode()))
	if err != nil {
		return nil, errors.New("invalid widget API request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		// Never propagate URLs: standard HTTP errors include bot tokens in the URL.
		return nil, fmt.Errorf("widget API %s: network failure", method)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("widget API %s: HTTP %d", method, resp.StatusCode)
	}
	var response map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&response); err != nil {
		return nil, fmt.Errorf("widget API %s: invalid response", method)
	}
	var ok bool
	if err := json.Unmarshal(response["ok"], &ok); err != nil || !ok {
		return nil, fmt.Errorf("widget API %s: rejected", method)
	}
	return response["result"], nil
}

type downloadWidgetData struct {
	Platform string
	Stage    string
	Percent  *int
	Revision int64
	Photo    bool
}

func widgetStateJSON(state downloadWidgetData) (string, error) {
	switch state.Platform {
	case string(link.TypeYouTube), string(link.TypeTikTok), string(link.TypeInstagram):
	default:
		return "", errors.New("unsupported widget platform")
	}
	switch state.Stage {
	case "validating", "downloading", "preparing", "sending", "success":
	default:
		return "", errors.New("unsupported widget stage")
	}
	if state.Percent != nil && (state.Stage != "downloading" && state.Stage != "success" || *state.Percent < 0 || *state.Percent > 100) {
		return "", errors.New("invalid widget percentage")
	}
	data := map[string]interface{}{"v": 1, "p": state.Platform, "s": state.Stage, "n": state.Percent, "r": state.Revision}
	if state.Photo {
		if state.Platform != string(link.TypeTikTok) || (state.Percent != nil && state.Stage != "success") {
			return "", errors.New("photo widget cannot carry measured byte percent")
		}
		data["ph"] = true
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	if len(raw) > 1024 {
		return "", errors.New("widget state exceeds Telegram limit")
	}
	return string(raw), nil
}

func (a *downloadWidgetAPI) send(ctx context.Context, chatID int64, replyID int, platform string) (int, error) {
	return a.sendWithMedia(ctx, chatID, replyID, platform, false)
}

func (a *downloadWidgetAPI) sendWithMedia(ctx context.Context, chatID int64, replyID int, platform string, photo bool) (int, error) {
	payload, err := widgetStateJSON(downloadWidgetData{Platform: platform, Stage: "validating", Revision: time.Now().UnixMicro(), Photo: photo})
	if err != nil {
		return 0, err
	}
	v := url.Values{
		"chat_id":   {strconv.FormatInt(chatID, 10)},
		"html":      {downloadWidgetHTML},
		"chat_data": {payload},
		"caption":   {"🎬 XA4Y · Загружаем видео"},
	}
	if photo {
		v["caption"] = []string{"📷 XA4Y · Загружаем фотографии"}
	}
	_ = url.Values{ // placeholder removed below

	}
	if replyID > 0 {
		reply, _ := json.Marshal(map[string]interface{}{"message_id": replyID, "allow_sending_without_reply": true})
		v.Set("reply_parameters", string(reply))
	}
	raw, err := a.call(ctx, "sendWidget", v)
	if err != nil {
		return 0, err
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, errors.New("sendWidget result invalid")
	}
	var messageID int
	if err := json.Unmarshal(result["message_id"], &messageID); err != nil || messageID <= 0 {
		return 0, errors.New("sendWidget returned no message ID")
	}
	return messageID, nil
}

func (a *downloadWidgetAPI) edit(ctx context.Context, chatID int64, messageID int, state downloadWidgetData) error {
	payload, err := widgetStateJSON(state)
	if err != nil {
		return err
	}
	params := url.Values{
		"chat_id":    {strconv.FormatInt(chatID, 10)},
		"message_id": {strconv.Itoa(messageID)},
		"chat_data":  {payload},
	}
	_, err = a.call(ctx, "editMessageWidgetData", params)
	return err
}
