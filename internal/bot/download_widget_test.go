package bot

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"xa4yy_vidsave/internal/config"
	"xa4yy_vidsave/internal/download"
	"xa4yy_vidsave/internal/link"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

type fakeChatAPI struct {
	mu      sync.Mutex
	sends   int
	deletes int
}

func (f *fakeChatAPI) Send(tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends++
	return tgbotapi.Message{MessageID: 77}, nil
}
func (f *fakeChatAPI) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	return &tgbotapi.APIResponse{Ok: true}, nil
}
func (f *fakeChatAPI) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends, f.deletes
}

type widgetCall struct {
	method string
	params url.Values
}
type widgetServer struct {
	mu    sync.Mutex
	calls []widgetCall
	fail  bool
}

func (f *widgetServer) endpoint(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		f.calls = append(f.calls, widgetCall{method: strings.TrimPrefix(r.URL.Path, "/"), params: r.PostForm})
		fail := f.fail
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("error"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "sendWidget") {
			_, _ = w.Write([]byte("{\"ok\":true,\"result\":{\"message_id\":42}}"))
		} else {
			_, _ = w.Write([]byte("{\"ok\":true,\"result\":true}"))
		}
	}))
}
func (f *widgetServer) callsFor(method string) []widgetCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []widgetCall{}
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}
func widgetTestBot(server *httptest.Server, f *fakeChatAPI, enabled bool) *Bot {
	return &Bot{
		cfg:               &config.Config{DownloadWidgetsEnabled: enabled},
		sender:            &Sender{api: f, log: zap.NewNop(), sleep: func(time.Duration) {}},
		log:               zap.NewNop(),
		downloadWidgetAPI: &downloadWidgetAPI{endpoint: server.URL, client: &http.Client{Timeout: time.Second}},
	}
}

func TestDownloadWidgetHTMLAndStates(t *testing.T) {
	raw := []byte(downloadWidgetHTML)
	if len(raw) == 0 || len(raw) > 32768 {
		t.Fatalf("HTML size %d", len(raw))
	}
	var compressed bytes.Buffer
	enc, err := zlib.NewWriterLevel(&compressed, 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() > 4096 {
		t.Fatalf("Telegram zlib9 limit exceeded: %d", compressed.Len())
	}
	for _, label := range []string{"YouTube Shorts", "TikTok", "Instagram Reels", "Этап 2 из 4", "chatDataChanged", "prefers-reduced-motion"} {
		if !strings.Contains(downloadWidgetHTML, label) {
			t.Errorf("missing %q", label)
		}
	}
	for _, forbidden := range []string{"TelegramWidget.request(", "fetch(", "WebSocket", "Попробовать снова", "retryDownload"} {
		if strings.Contains(downloadWidgetHTML, forbidden) {
			t.Errorf("forbidden action %q", forbidden)
		}
	}
	pct := 68
	for _, p := range []string{"youtube", "tiktok", "instagram"} {
		raw, err := widgetStateJSON(downloadWidgetData{Platform: p, Stage: "downloading", Percent: &pct, Revision: 19})
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 1024 || !strings.Contains(raw, "\"n\":68") {
			t.Fatal("bad state", raw)
		}
		var state map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
		if state["p"] != p {
			t.Fatal("platform mismatch")
		}
	}
	if _, err := widgetStateJSON(downloadWidgetData{Platform: "youtube", Stage: "sending", Percent: &pct}); err == nil {
		t.Fatal("fake upload percentages allowed")
	}
	if _, err := widgetStateJSON(downloadWidgetData{Platform: "tiktok", Stage: "retry"}); err == nil {
		t.Fatal("unsupported widget action allowed")
	}
}

func TestDownloadWidgetHTTPAndPrivateFallback(t *testing.T) {
	fake := &widgetServer{}
	server := fake.endpoint(t)
	defer server.Close()
	chat := &fakeChatAPI{}
	b := widgetTestBot(server, chat, true)
	for _, platform := range []link.Type{link.TypeInstagram, link.TypeTikTok, link.TypeYouTube} {
		status := b.beginDownloadStatus(context.Background(), "private", 123, 56, platform)
		if status.widget == nil {
			t.Fatalf("widget not sent for %s", platform)
		}
		status.stage("downloading")
		status.progress(download.ProgressEvent{Known: true, Percent: 68})
		// Stop without a real download; Stage 4 error cards are not enabled.
		status.finish(false)
	}
	time.Sleep(100 * time.Millisecond)
	sends := fake.callsFor("sendWidget")
	if len(sends) != 3 {
		t.Fatalf("expected three platforms, got %d", len(sends))
	}
	for _, c := range sends {
		if c.params.Get("allow_queries") != "" {
			t.Fatal("progress widget should accept no requests")
		}
		if c.params.Get("public_data") != "" {
			t.Fatal("public data should be empty")
		}
		if c.params.Get("chat_id") != "123" || c.params.Get("html") == "" {
			t.Fatal("bad widget request")
		}
		if !strings.Contains(c.params.Get("reply_parameters"), "56") {
			t.Fatal("missing reply reference")
		}
	}
	_, deleted := chat.counts()
	if deleted != 3 {
		t.Fatalf("expected 3 widget deletions after failures; got %d", deleted)
	}
	// Group must retain the old text-only status.
	group := b.beginDownloadStatus(context.Background(), "group", -123, 0, link.TypeTikTok)
	if group.widget != nil || group.textMessage == nil {
		t.Fatal("widget sent to a group")
	}
	group.finish(false)
	count, _ := chat.counts()
	if count != 1 {
		t.Fatal("group fallback text was not sent")
	}
}

func TestDownloadWidgetRejectedFallsBackToText(t *testing.T) {
	fake := &widgetServer{fail: true}
	server := fake.endpoint(t)
	defer server.Close()
	chat := &fakeChatAPI{}
	b := widgetTestBot(server, chat, true)
	status := b.beginDownloadStatus(context.Background(), "private", 123, 0, link.TypeInstagram)
	if status.widget != nil || status.textMessage == nil {
		t.Fatal("expected legacy status on widget failure")
	}
	status.finish(false)
	sent, deleted := chat.counts()
	if sent != 1 || deleted != 1 {
		t.Fatalf("fallback messages: sent=%d deleted=%d", sent, deleted)
	}
}

func TestDownloadWidgetEditThrottling(t *testing.T) {
	fake := &widgetServer{}
	server := fake.endpoint(t)
	defer server.Close()
	chat := &fakeChatAPI{}
	b := widgetTestBot(server, chat, true)
	status := b.beginDownloadStatus(context.Background(), "private", 123, 0, link.TypeYouTube)
	if status.widget == nil {
		t.Fatal("widget send failed")
	}
	for i := 0; i < 1000; i++ {
		status.progress(download.ProgressEvent{Known: true, Percent: i % 101})
	}
	time.Sleep(900 * time.Millisecond)
	edits := fake.callsFor("editMessageWidgetData")
	if len(edits) > 1 {
		t.Fatalf("widget edit flood: %d", len(edits))
	}
	status.finish(false)
}

func TestDownloadWidgetPilotGate(t *testing.T) {
	b := &Bot{cfg: &config.Config{
		DownloadWidgetsEnabled: true,
		DownloadWidgetPilotIDs: map[int64]struct{}{665731091: {}},
	}}
	if !b.downloadWidgetActive(665731091) {
		t.Fatal("pilot owner denied")
	}
	if b.downloadWidgetActive(999) {
		t.Fatal("non-pilot user must keep old behaviour")
	}
	b.cfg.DownloadWidgetsEnabled = false
	if b.downloadWidgetActive(665731091) {
		t.Fatal("feature flag off still enabled widget")
	}
	b.cfg.DownloadWidgetsEnabled = true
	b.cfg.DownloadWidgetPilotIDs = nil
	if !b.downloadWidgetActive(999) {
		t.Fatal("empty pilot allowlist means explicit full rollout")
	}
}
